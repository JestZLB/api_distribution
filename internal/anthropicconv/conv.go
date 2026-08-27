// Package anthropicconv translates between the OpenAI Chat Completions
// wire format (what this proxy exposes to its clients) and the
// Anthropic Messages API (what anthropic.com serves). The proxy keeps
// its public contract OpenAI-shaped; when the upstream is Anthropic we
// build a Messages request, send it, and then convert the response
// (JSON or SSE) back to the OpenAI format the caller expects.
//
// The package only depends on the standard library.
package anthropicconv

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AnthropicUsage reports the token counts returned by an Anthropic
// Messages response. InputTokens / OutputTokens are zero if the
// upstream didn't include them. They are int64 to safely hold values
// up to 2^53 (JSON's lossless range) without overflow: a malicious or
// malformed upstream returning {"prompt_tokens": 1e20} used to wrap
// to a negative int32 and corrupt DailyAgg totals.
type AnthropicUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// BuildRequest converts an OpenAI Chat Completions request body to an
// Anthropic Messages request body. The caller is responsible for
// replacing the public model id with the upstream id before invoking
// this function.
func BuildRequest(body []byte, streaming bool) ([]byte, error) {
	var in map[string]any
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("anthropicconv: parse OpenAI request: %w", err)
	}
	model, _ := in["model"].(string)
	if model == "" {
		return nil, errors.New("anthropicconv: request is missing 'model'")
	}

	// Split system content out of the messages array.
	var (
		anthMsgs   []map[string]any
		systemText []string
	)
	if rawMsgs, ok := in["messages"].([]any); ok {
		for _, raw := range rawMsgs {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			switch role {
			case "system":
				if txt := extractText(m["content"]); txt != "" {
					systemText = append(systemText, txt)
				}
			case "user", "assistant":
				anthMsgs = append(anthMsgs, map[string]any{
					"role":    role,
					"content": extractText(m["content"]),
				})
			}
		}
	}

	out := map[string]any{
		"model":    model,
		"messages": anthMsgs,
		"stream":   streaming,
	}
	if len(systemText) > 0 {
		out["system"] = strings.Join(systemText, "\n")
	}

	// max_tokens — prefer max_completion_tokens, fall back to max_tokens,
	// default to 1024 (Anthropic rejects requests without max_tokens).
	maxTokens := 0
	if v, ok := in["max_tokens"]; ok {
		maxTokens = toInt(v)
	}
	if v, ok := in["max_completion_tokens"]; ok {
		if mt := toInt(v); mt > 0 {
			maxTokens = mt
		}
	}
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	out["max_tokens"] = maxTokens

	if v, ok := in["temperature"].(float64); ok {
		out["temperature"] = v
	}
	if v, ok := in["top_p"].(float64); ok {
		out["top_p"] = v
	}

	// stop / stop_sequences
	if stop, ok := in["stop"]; ok {
		switch v := stop.(type) {
		case string:
			if v != "" {
				out["stop_sequences"] = []string{v}
			}
		case []any:
			var out2 []string
			for _, e := range v {
				if s, ok := e.(string); ok && s != "" {
					out2 = append(out2, s)
				}
			}
			if len(out2) > 0 {
				out["stop_sequences"] = out2
			}
		}
	}

	return json.Marshal(out)
}

// TransformResponse reads an Anthropic Messages JSON response and writes
// the equivalent OpenAI Chat Completions JSON to w. It returns the
// token usage reported by the upstream so the caller can update logs.
func TransformResponse(src io.Reader, w io.Writer) (AnthropicUsage, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return AnthropicUsage{}, err
	}
	var ant struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage AnthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(data, &ant); err != nil {
		return AnthropicUsage{}, fmt.Errorf("anthropicconv: parse upstream response: %w", err)
	}

	var content strings.Builder
	for _, c := range ant.Content {
		if c.Type == "text" {
			if content.Len() > 0 {
				content.WriteString("\n")
			}
			content.WriteString(c.Text)
		}
	}

	out := map[string]any{
		"id":      ant.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   ant.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content.String(),
				},
				"finish_reason": mapStopReason(ant.StopReason),
			},
		},
	}
	if ant.Usage.InputTokens != 0 || ant.Usage.OutputTokens != 0 {
		out["usage"] = map[string]any{
			"prompt_tokens":     ant.Usage.InputTokens,
			"completion_tokens": ant.Usage.OutputTokens,
			"total_tokens":      ant.Usage.InputTokens + ant.Usage.OutputTokens,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return ant.Usage, err
	}
	return ant.Usage, nil
}

// TransformStream reads an Anthropic Messages SSE stream from src and
// writes the equivalent OpenAI Chat Completions SSE stream to w. The
// conversion emits a final `data: [DONE]` frame and flushes after each
// chunk when the writer supports it. It returns the token usage reported
// by the upstream so the caller can backfill request logs.
func TransformStream(src io.Reader, w io.Writer) (AnthropicUsage, error) {
	scanner := bufio.NewScanner(src)
	// Anthropic event payloads can be a few KB each; allow generous buffers.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	flush, _ := w.(http.Flusher)

	var (
		eventType string
		msgID     string
		model     string
		created   = time.Now().Unix()
		stopSent  bool
		usage     AnthropicUsage
	)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			eventType = ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			switch eventType {
			case "message_start":
				var m struct {
					Message struct {
						ID    string         `json:"id"`
						Model string         `json:"model"`
						Usage AnthropicUsage `json:"usage"`
					} `json:"message"`
				}
				if err := json.Unmarshal([]byte(payload), &m); err == nil {
					msgID = m.Message.ID
					model = m.Message.Model
					// The initial prompt token count is authoritative
					// here; message_delta only reports output tokens.
					usage.InputTokens = m.Message.Usage.InputTokens
				}
				writeChunk(w, map[string]any{
					"id":      msgID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []map[string]any{
						{
							"index":         0,
							"delta":         map[string]any{"role": "assistant"},
							"finish_reason": nil,
						},
					},
				})
				flushNow(flush)
			case "content_block_delta":
				var d struct {
					Delta struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"delta"`
				}
				if err := json.Unmarshal([]byte(payload), &d); err == nil && d.Delta.Type == "text_delta" {
					writeChunk(w, map[string]any{
						"id":      msgID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   model,
						"choices": []map[string]any{
							{
								"index":         0,
								"delta":         map[string]any{"content": d.Delta.Text},
								"finish_reason": nil,
							},
						},
					})
					flushNow(flush)
				}
			case "message_delta":
				var d struct {
					Delta struct {
						StopReason string `json:"stop_reason"`
					} `json:"delta"`
					Usage AnthropicUsage `json:"usage"`
				}
				if err := json.Unmarshal([]byte(payload), &d); err == nil {
					// The final output token count arrives here once the
					// model has finished generating the whole message.
					usage.OutputTokens = d.Usage.OutputTokens
					writeChunk(w, map[string]any{
						"id":      msgID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   model,
						"choices": []map[string]any{
							{
								"index":         0,
								"delta":         map[string]any{},
								"finish_reason": mapStopReason(d.Delta.StopReason),
							},
						},
					})
					flushNow(flush)
				}
			case "message_stop":
				if !stopSent {
					if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
						return usage, err
					}
					flushNow(flush)
					stopSent = true
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, err
	}
	// Some upstreams may close the connection before sending
	// message_stop; still emit [DONE] so OpenAI clients terminate.
	if !stopSent {
		if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
			return usage, err
		}
		flushNow(flush)
	}
	return usage, nil
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// extractText reduces an OpenAI message content (which may be a string,
// nil, or an array of typed parts) to a plain string.
func extractText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		var sb strings.Builder
		for _, part := range t {
			if pm, ok := part.(map[string]any); ok {
				if txt, ok := pm["text"].(string); ok {
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(txt)
				}
			}
		}
		return sb.String()
	default:
		return fmt.Sprint(t)
	}
}

// toInt converts a JSON-decoded numeric value to an int with sensible
// fallbacks.
func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		if i, err := strconv.Atoi(t); err == nil {
			return i
		}
	}
	return 0
}

// mapStopReason converts an Anthropic stop_reason to the closest
// OpenAI finish_reason string. Unknown values map to nil so the
// OpenAI client can decide what to do.
func mapStopReason(s string) any {
	switch s {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return nil
	}
}

// writeChunk serialises chunk as JSON and writes it as an OpenAI
// SSE event: "data: <json>\n\n".
func writeChunk(w io.Writer, chunk map[string]any) {
	data, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	_, _ = w.Write(buf.Bytes())
}

// flushNow flushes when f is non-nil; otherwise it is a no-op.
func flushNow(f http.Flusher) {
	if f == nil {
		return
	}
	f.Flush()
}
