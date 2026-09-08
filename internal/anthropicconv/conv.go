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

	// Translate the OpenAI messages array into Anthropic Messages
	// format, preserving the full message structure — every role
	// (system/user/assistant/tool/function) and every content block
	// type (text, image_url, tool_use, tool_result). The previous
	// flatten-everything-to-text approach silently dropped images,
	// tool_use/tool_result blocks and whole tool/function messages,
	// which is why long/structured contexts (RAG documents, agent
	// tool results) arrived at the upstream incomplete.
	anthMsgs, systemText := translateMessages(in["messages"])

	out := map[string]any{
		"model":    model,
		"messages": anthMsgs,
		"stream":   streaming,
	}
	if len(systemText) > 0 {
		out["system"] = strings.Join(systemText, "\n")
	}

	// max_tokens — prefer max_completion_tokens, fall back to max_tokens.
	// Anthropic rejects requests without the field. When the client
	// omits it entirely we default to a large safe value (8192) rather
	// than the old 1024, so the gateway never hard-crops a long output
	// to ~1024 tokens — output-length limits are the client's concern,
	// not the proxy's. A client-supplied value is always used verbatim.
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
		maxTokens = 8192
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

	// Pass through any top-level parameter the gateway doesn't itself
	// manage, so clients' settings (tools, response_format, logit_bias,
	// user, ...) are not silently dropped. We only skip keys the gateway
	// derives/consumes and the OpenAI-only stream_options the handler
	// injects for usage accounting (Anthropic rejects unknown fields).
	managed := map[string]bool{
		"model": true, "messages": true, "stream": true, "system": true,
		"max_tokens": true, "max_completion_tokens": true, "temperature": true,
		"top_p": true, "stop": true, "stream_options": true,
	}
	for k, v := range in {
		if managed[k] {
			continue
		}
		out[k] = v
	}

	return json.Marshal(out)
}

// translateMessages converts an OpenAI Chat messages array into
// Anthropic Messages format. It returns the Anthropic messages and any
// system texts extracted from system-role messages.
func translateMessages(raw any) ([]map[string]any, []string) {
	var anthMsgs []map[string]any
	var systemText []string
	rawMsgs, ok := raw.([]any)
	if !ok {
		return anthMsgs, systemText
	}
	for _, r := range rawMsgs {
		m, ok := r.(map[string]any)
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
			content := translateContent(m["content"])
			if role == "assistant" {
				content = appendToolUse(content, toolCallsToBlocks(m["tool_calls"]))
			}
			if content == nil {
				continue
			}
			anthMsgs = append(anthMsgs, map[string]any{
				"role":    role,
				"content": content,
			})
		case "tool", "function":
			anthMsgs = append(anthMsgs, toolResultMessage(m))
		}
	}
	return anthMsgs, systemText
}

// translateContent converts an OpenAI message content value into the
// equivalent Anthropic content representation. Plain text (string or
// text-only array) stays a string; any image_url block forces a
// content-blocks array so the binary image data is preserved.
func translateContent(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return nil
		}
		return t
	case []any:
		var blocks []map[string]any
		for _, part := range t {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text":
				if s, ok := pm["text"].(string); ok && s != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": s})
				}
			case "image_url":
				if blk := imageURLToBlock(pm); blk != nil {
					blocks = append(blocks, blk)
				}
			}
		}
		if len(blocks) == 0 {
			return nil
		}
		// Pure-text array: join into a single string to keep the
		// prior behaviour and stay compatible with both providers.
		if allTextBlocks(blocks) {
			var sb strings.Builder
			for i, b := range blocks {
				if i > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b["text"].(string))
			}
			return sb.String()
		}
		return blocks
	default:
		return nil
	}
}

// allTextBlocks reports whether every block is a plain text block.
func allTextBlocks(blocks []map[string]any) bool {
	for _, b := range blocks {
		if b["type"] != "text" {
			return false
		}
	}
	return true
}

// appendToolUse merges assistant tool_use blocks into existing content.
func appendToolUse(content any, toolBlocks []map[string]any) any {
	if len(toolBlocks) == 0 {
		return content
	}
	switch c := content.(type) {
	case nil:
		return toolBlocks
	case string:
		blocks := []map[string]any{{"type": "text", "text": c}}
		return append(blocks, toolBlocks...)
	case []map[string]any:
		return append(c, toolBlocks...)
	default:
		return content
	}
}

// imageURLToBlock converts an OpenAI image_url content part into an
// Anthropic image content block. Only data: URLs are supported; a
// remote URL cannot be inlined into the Anthropic request.
func imageURLToBlock(pm map[string]any) map[string]any {
	urlMap, ok := pm["image_url"].(map[string]any)
	if !ok {
		return nil
	}
	rawURL, ok := urlMap["url"].(string)
	if !ok || rawURL == "" {
		return nil
	}
	if !strings.HasPrefix(rawURL, "data:") {
		return nil
	}
	mediaType, data := parseDataURL(rawURL)
	if data == "" {
		return nil
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": mediaType,
			"data":       data,
		},
	}
}

// parseDataURL extracts the media type and base64 payload from a
// data: URL such as "data:image/png;base64,AAAA". It returns ("", "")
// when the URL is malformed.
func parseDataURL(s string) (mediaType, data string) {
	rest := strings.TrimPrefix(s, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", ""
	}
	meta := rest[:comma]
	data = rest[comma+1:]
	if i := strings.Index(meta, ";"); i >= 0 {
		mediaType = meta[:i]
	} else {
		mediaType = meta
	}
	return mediaType, data
}

// toolCallsToBlocks converts OpenAI assistant tool_calls into
// Anthropic tool_use content blocks. OpenAI serialises the arguments
// as a JSON string, which we parse back into a structured value.
func toolCallsToBlocks(v any) []map[string]any {
	calls, ok := v.([]any)
	if !ok {
		return nil
	}
	var blocks []map[string]any
	for _, raw := range calls {
		call, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := call["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		argsJSON, _ := fn["arguments"].(string)
		var input any
		if argsJSON != "" {
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				input = map[string]any{}
			}
		}
		if input == nil {
			input = map[string]any{}
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    call["id"],
			"name":  name,
			"input": input,
		})
	}
	return blocks
}

// toolResultMessage converts an OpenAI tool/function role message into
// an Anthropic user message carrying a tool_result content block.
func toolResultMessage(m map[string]any) map[string]any {
	toolUseID, _ := m["tool_call_id"].(string)
	content := translateContent(m["content"])
	if content == nil {
		content = ""
	}
	block := map[string]any{
		"type":        "tool_result",
		"tool_use_id": toolUseID,
		"content":     content,
	}
	if isErr, ok := m["is_error"].(bool); ok && isErr {
		block["is_error"] = true
	}
	return map[string]any{
		"role":    "user",
		"content": []map[string]any{block},
	}
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
//
// The implementation uses bufio.Reader.ReadBytes('\n') (not Scanner) so
// each line is a []byte slice into the reader's internal buffer — no
// per-line string copy like scanner.Text() would do. bytes.IndexByte
// locates the SSE prefix separator in a single pass without allocating
// a substring.
func TransformStream(src io.Reader, w io.Writer) (AnthropicUsage, error) {
	reader := bufio.NewReaderSize(src, 64*1024)
	// Anthropic event payloads can be a few KB each; allow generous buffers.

	flush, _ := w.(http.Flusher)

	var (
		eventType string
		msgID     string
		model     string
		created   = time.Now().Unix()
		stopSent  bool
		usage     AnthropicUsage
	)

	for {
		raw, err := reader.ReadBytes('\n')
		line := bytes.TrimRight(raw, "\n")

		if len(line) > 0 {
			// Single-pass prefix split: locate ':' and slice both halves
			// directly off `line`. Avoids the strings.TrimPrefix +
			// substring allocation that the previous code did per event.
			colon := bytes.IndexByte(line, ':')
			if colon >= 0 {
				prefix := line[:colon]
				value := bytes.TrimSpace(line[colon+1:])

				switch string(prefix) {
				case "event":
					eventType = string(value)
				case "data":
					payload := value
					switch eventType {
					case "message_start":
						var m struct {
							Message struct {
								ID    string         `json:"id"`
								Model string         `json:"model"`
								Usage AnthropicUsage `json:"usage"`
							} `json:"message"`
						}
						if err := json.Unmarshal(payload, &m); err == nil {
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
						if err := json.Unmarshal(payload, &d); err == nil && d.Delta.Type == "text_delta" {
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
						if err := json.Unmarshal(payload, &d); err == nil {
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
		} else {
			// Empty line — end-of-event separator. Reset the event type
			// so the next "data: ..." payload routes through the new
			// event's handler.
			eventType = ""
		}

		if err != nil {
			// ReadBytes returns io.EOF (no error) on clean EOF — match
			// the previous scanner.Err() == nil branch.
			if err != io.EOF {
				return usage, err
			}
			break
		}
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
// SSE event: "data: <json>\n\n". The previous bytes.Buffer-based
// implementation allocated two buffers per chunk (Marshal + buf); the
// streaming version pipes json.NewEncoder(w).Encode's JSON+newline
// directly into w and adds the closing "\n" so the on-the-wire bytes
// stay byte-identical to the legacy output.
func writeChunk(w io.Writer, chunk map[string]any) {
	_, _ = io.WriteString(w, "data: ")
	enc := json.NewEncoder(w)
	if err := enc.Encode(chunk); err != nil {
		return
	}
	// enc.Encode appends "\n"; SSE events use "\n\n" as the
	// event separator, so write one more newline to match.
	_, _ = io.WriteString(w, "\n")
}

// flushNow flushes when f is non-nil; otherwise it is a no-op.
func flushNow(f http.Flusher) {
	if f == nil {
		return
	}
	f.Flush()
}
