package anthropicconv

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildRequest_Basic(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet-latest",
		"messages": [
			{"role":"system","content":"You are a helpful assistant."},
			{"role":"user","content":"Hello!"}
		],
		"max_tokens": 100,
		"stream": false
	}`)
	out, err := BuildRequest(in, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if got["model"] != "claude-3-5-sonnet-latest" {
		t.Errorf("model = %v", got["model"])
	}
	if got["system"] != "You are a helpful assistant." {
		t.Errorf("system = %v", got["system"])
	}
	if got["stream"] != false {
		t.Errorf("stream = %v", got["stream"])
	}
	if got["max_tokens"].(float64) != 100 {
		t.Errorf("max_tokens = %v", got["max_tokens"])
	}
	msgs, ok := got["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v", got["messages"])
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "Hello!" {
		t.Errorf("first message = %v", first)
	}
}

func TestBuildRequest_DefaultsAndFallbacks(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet-latest",
		"messages": [{"role":"user","content":"hi"}],
		"max_completion_tokens": 256
	}`)
	out, err := BuildRequest(in, true)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)

	if got["max_tokens"].(float64) != 256 {
		t.Errorf("max_tokens fallback = %v", got["max_tokens"])
	}
	if got["stream"] != true {
		t.Errorf("stream flag = %v", got["stream"])
	}

	// Default max_tokens when neither field is set: a large safe value
	// so a client that omits the field never gets its output hard-cropped
	// to a tiny default.
	in2 := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	out, err = BuildRequest(in2, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	_ = json.Unmarshal(out, &got)
	if got["max_tokens"].(float64) != 8192 {
		t.Errorf("default max_tokens = %v, want 8192", got["max_tokens"])
	}
}

func TestBuildRequest_StopAndContentArray(t *testing.T) {
	in := []byte(`{
		"model":"m",
		"messages":[
			{"role":"user","content":[
				{"type":"text","text":"Hello "},
				{"type":"text","text":"world!"}
			]}
		],
		"stop":["END","STOP"]
	}`)
	out, err := BuildRequest(in, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)

	msgs := got["messages"].([]any)
	m := msgs[0].(map[string]any)
	if m["content"] != "Hello \nworld!" {
		t.Errorf("merged content = %q", m["content"])
	}

	seqs, ok := got["stop_sequences"].([]any)
	if !ok || len(seqs) != 2 {
		t.Fatalf("stop_sequences = %v", got["stop_sequences"])
	}
	if seqs[0] != "END" || seqs[1] != "STOP" {
		t.Errorf("stop_sequences = %v", seqs)
	}
}

func TestBuildRequest_PreservesImagesAndToolBlocks(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet-latest",
		"messages": [
			{"role":"user","content":[
				{"type":"text","text":"What is in this image?"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}
			]}
		],
		"max_tokens": 100
	}`)
	out, err := BuildRequest(in, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	msgs := got["messages"].([]any)
	m := msgs[0].(map[string]any)
	blocks, ok := m["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %v", m["content"])
	}
	textBlk := blocks[0].(map[string]any)
	imgBlk := blocks[1].(map[string]any)
	if textBlk["type"] != "text" {
		t.Errorf("block[0].type = %v", textBlk["type"])
	}
	if imgBlk["type"] != "image" {
		t.Errorf("block[1].type = %v (image dropped!)", imgBlk["type"])
	}
	src := imgBlk["source"].(map[string]any)
	if src["media_type"] != "image/png" || src["data"] != "AAAA" {
		t.Errorf("image source = %v", src)
	}
}

func TestBuildRequest_PreservesToolRoleAndToolCalls(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet-latest",
		"messages": [
			{"role":"assistant","content":"Let me check.","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"foo\"}"}}
			]},
			{"role":"tool","tool_call_id":"call_1","content":"result text"}
		],
		"max_tokens": 100
	}`)
	out, err := BuildRequest(in, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// assistant: text + tool_use block
	asst := msgs[0].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("msg[0].role = %v", asst["role"])
	}
	asstContent, ok := asst["content"].([]any)
	if !ok || len(asstContent) != 2 {
		t.Fatalf("assistant content should be 2 blocks, got %v", asst["content"])
	}
	toolUse := asstContent[1].(map[string]any)
	if toolUse["type"] != "tool_use" {
		t.Errorf("block[1].type = %v (tool_use dropped!)", toolUse["type"])
	}
	if toolUse["name"] != "search" {
		t.Errorf("tool_use.name = %v", toolUse["name"])
	}
	input := toolUse["input"].(map[string]any)
	if input["q"] != "foo" {
		t.Errorf("tool_use.input = %v", input)
	}

	// tool role -> user message with tool_result block
	toolMsg := msgs[1].(map[string]any)
	if toolMsg["role"] != "user" {
		t.Errorf("msg[1].role = %v (tool role dropped!)", toolMsg["role"])
	}
	tr := toolMsg["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" {
		t.Errorf("tool_result block type = %v", tr["type"])
	}
	if tr["tool_use_id"] != "call_1" {
		t.Errorf("tool_use_id = %v", tr["tool_use_id"])
	}
	if tr["content"] != "result text" {
		t.Errorf("tool_result content = %v", tr["content"])
	}
}

func TestBuildRequest_PassesThroughTopLevelParams(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet-latest",
		"messages": [{"role":"user","content":"hi"}],
		"max_tokens": 100,
		"tools": [{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],
		"response_format": {"type":"json_object"},
		"user": "client-1"
	}`)
	out, err := BuildRequest(in, false)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := got["tools"]; !ok {
		t.Errorf("tools top-level param was dropped")
	}
	if _, ok := got["response_format"]; !ok {
		t.Errorf("response_format top-level param was dropped")
	}
	if got["user"] != "client-1" {
		t.Errorf("user top-level param was dropped: %v", got["user"])
	}
}

func TestBuildRequest_DropsGatewayInjectedStreamOptions(t *testing.T) {
	in := []byte(`{
		"model": "claude-3-5-sonnet-latest",
		"messages": [{"role":"user","content":"hi"}],
		"max_tokens": 100,
		"stream_options": {"include_usage": true}
	}`)
	out, err := BuildRequest(in, true)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["stream_options"]; ok {
		t.Errorf("stream_options should not be forwarded to Anthropic: %v", got["stream_options"])
	}
}

func TestTransformResponse(t *testing.T) {
	src := strings.NewReader(`{
		"id": "msg_01",
		"type": "message",
		"role": "assistant",
		"model": "claude-3-5-sonnet-latest",
		"stop_reason": "end_turn",
		"content": [
			{"type":"text","text":"Hello "},
			{"type":"text","text":"world!"}
		],
		"usage": {"input_tokens": 12, "output_tokens": 7}
	}`)
	var buf bytes.Buffer
	usage, err := TransformResponse(src, &buf)
	if err != nil {
		t.Fatalf("TransformResponse: %v", err)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", usage)
	}

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, buf.String())
	}
	if out["id"] != "msg_01" {
		t.Errorf("id = %v", out["id"])
	}
	choices := out["choices"].([]any)
	c := choices[0].(map[string]any)
	msg := c["message"].(map[string]any)
	if msg["content"] != "Hello \nworld!" {
		t.Errorf("content = %v", msg["content"])
	}
	if c["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v", c["finish_reason"])
	}
	u, ok := out["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing: %v", out)
	}
	if u["prompt_tokens"].(float64) != 12 || u["completion_tokens"].(float64) != 7 || u["total_tokens"].(float64) != 19 {
		t.Errorf("usage = %v", u)
	}
}

func TestTransformStream(t *testing.T) {
	input := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_02","model":"claude-3-5-sonnet-latest","role":"assistant","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world!"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	srcR := strings.NewReader(input)

	var buf bytes.Buffer
	usage, err := TransformStream(srcR, &buf)
	if err != nil {
		t.Fatalf("TransformStream: %v", err)
	}

	// Usage is collected from message_start (input_tokens) and
	// message_delta (output_tokens) and should reflect the upstream.
	if usage.InputTokens != 5 || usage.OutputTokens != 2 {
		t.Errorf("usage = %+v", usage)
	}

	frames := readFrames(t, buf.String())
	if len(frames) < 4 {
		t.Fatalf("expected at least 4 frames, got %d: %q", len(frames), buf.String())
	}

	if frames[0].ID != "msg_02" {
		t.Errorf("frame[0].id = %q", frames[0].ID)
	}
	if got := frames[0].Choices[0].Delta.Role; got != "assistant" {
		t.Errorf("frame[0].role = %q", got)
	}

	if frames[1].Choices[0].Delta.Content != "Hello " {
		t.Errorf("frame[1].content = %q", frames[1].Choices[0].Delta.Content)
	}
	if frames[2].Choices[0].Delta.Content != "world!" {
		t.Errorf("frame[2].content = %q", frames[2].Choices[0].Delta.Content)
	}

	if frames[3].Choices[0].FinishReason != "stop" {
		t.Errorf("frame[3].finish_reason = %q", frames[3].Choices[0].FinishReason)
	}

	tail := buf.String()[strings.LastIndex(buf.String(), "data: "):]
	if tail != "data: [DONE]\n\n" {
		t.Errorf("missing [DONE] terminator in:\n%s", buf.String())
	}
}

// readFrames splits SSE-style output into individual JSON chunks
// for inspection.
func readFrames(t *testing.T, s string) []sseFrame {
	t.Helper()
	var out []sseFrame
	for _, blk := range strings.Split(s, "\n\n") {
		blk = strings.TrimSpace(blk)
		if blk == "" {
			continue
		}
		if !strings.HasPrefix(blk, "data: ") {
			continue
		}
		body := strings.TrimPrefix(blk, "data: ")
		if body == "[DONE]" {
			continue
		}
		var f sseFrame
		if err := json.Unmarshal([]byte(body), &f); err != nil {
			t.Fatalf("decode frame %q: %v", body, err)
		}
		out = append(out, f)
	}
	return out
}

type sseFrame struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}
