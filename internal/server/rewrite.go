package server

import (
	"bytes"
	"encoding/json"
	"strings"
)

// extractModel returns (alias, modelField, ok) where modelField is the
// JSON-pointer-style byte offset of the "model" key in body. We only
// need the field name string for the rewrite; offsets aren't required
// because we operate on the original bytes.
func extractModel(body []byte) (alias string, modelField string, ok bool) {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", "", false
	}
	if probe.Model == "" {
		return "", "", false
	}
	return probe.Model, "model", true
}

// rewriteModel replaces the value of the JSON "model" field. The
// rewrite is intentionally minimal: we walk the JSON byte stream and
// only touch the first occurrence of `"model": "..."`. This avoids
// re-marshalling which would re-order fields and confuse some
// upstream servers.
func rewriteModel(body []byte, field, newValue string) []byte {
	if field != "model" {
		// Fallback: marshal/unmarshal round-trip.
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			m["model"] = newValue
			if out, err := json.Marshal(m); err == nil {
				return out
			}
		}
		return body
	}

	key := []byte(`"model"`)
	idx := bytes.Index(body, key)
	if idx < 0 {
		return body
	}
	// Skip past the key.
	i := idx + len(key)
	// Skip whitespace and colon.
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == ':' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if i >= len(body) || body[i] != '"' {
		return body
	}
	// Find end of string value.
	j := i + 1
	for j < len(body) && body[j] != '"' {
		if body[j] == '\\' && j+1 < len(body) {
			j += 2
			continue
		}
		j++
	}
	if j >= len(body) {
		return body
	}

	out := make([]byte, 0, len(body))
	out = append(out, body[:i+1]...)
	out = append(out, []byte(escapeJSONString(newValue))...)
	out = append(out, body[j:]...)
	return out
}

func escapeJSONString(s string) string {
	b, _ := json.Marshal(s)
	// b is wrapped in quotes; drop them.
	return strings.TrimSuffix(strings.TrimPrefix(string(b), `"`), `"`)
}

// isStreaming inspects the body for the OpenAI "stream": true flag.
func isStreaming(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return false
	}
	return probe.Stream
}

// parseUsage extracts token counts from a non-streaming OpenAI-style
// response. Returns (0,0) if the field is missing or unparsable.
//
// The probe fields are int64 (matching Stats / DailyAgg) so a hostile
// upstream returning values beyond int32 cannot wrap into negative
// numbers and corrupt totals.
func parseUsage(body []byte) (input, output int64) {
	var probe struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return 0, 0
	}
	return probe.Usage.PromptTokens, probe.Usage.CompletionTokens
}

// ensureStreamUsage guarantees the OpenAI request body carries
// `stream_options.include_usage=true`. OpenAI only emits `usage` in the
// final SSE chunk of a streaming chat completion when the caller sets
// this flag, so we inject it on the forwarding path to keep streamed
// token accounting accurate.
//
// If `stream_options` already exists we merge into it, preserving any
// other fields the client sent (e.g. parallel_tool_calls). On parse
// failure the original bytes are returned untouched.
func ensureStreamUsage(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}

	so, _ := m["stream_options"].(map[string]any)
	if so == nil {
		so = make(map[string]any)
	}
	so["include_usage"] = true
	m["stream_options"] = so

	out, err := jsonMarshal(m)
	if err != nil {
		return body
	}
	return out
}

// jsonMarshal is split out so tests can stub it if needed.
var jsonMarshal = json.Marshal
