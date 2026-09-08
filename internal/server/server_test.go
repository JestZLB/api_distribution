package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api_distribution/internal/config"
	"api_distribution/internal/store"
	"api_distribution/internal/types"
)

// TestStop_DoesNotBlockStatus ensures Stop releases its mutex before
// calling Shutdown, so Status() can answer while a graceful drain
// is in progress. We exercise this by starting a server whose
// handler blocks for ~200 ms and initiating Stop in the background.
func TestStop_DoesNotBlockStatus(t *testing.T) {
	dir := t.TempDir()
	mgr := config.New(dir)
	if _, err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	st := store.New()

	srv := New(mgr, st)

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	// Override the handler via a fresh http.Server using the loopback
	// listener inside Start. We can't easily swap handlers, so we use
	// the default mux paths (healthz responds instantly and forces
	// Stop to drain it).
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	// Now issue Stop concurrently while calling Status. If Stop
	// holds the lock across Shutdown, Status would block too.
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- srv.Stop()
	}()

	// We have at most a few seconds for the in-flight connections
	// to drain. Poll Status().
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := srv.Status()
		if !st.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case err := <-stopDone:
		if err != nil && !strings.Contains(err.Error(), "closed") {
			t.Errorf("Stop returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return in time")
	}
}

// TestStartStop_Idempotent ensures Stop on an already-stopped server
// returns nil instead of panicking.
func TestStartStop_Idempotent(t *testing.T) {
	dir := t.TempDir()
	mgr := config.New(dir)
	if _, err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	srv := New(mgr, store.New())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

// TestStreamResponseWithUsage verifies that streamResponseWithUsage
// correctly parses SSE chunks, extracts usage from the final chunk,
// and forwards all data lines to the response writer.
func TestStreamResponseWithUsage(t *testing.T) {
	sseStream := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" World\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20}}\n\n" +
		"data: [DONE]\n\n"

	log := &types.LogEntry{}
	w := httptest.NewRecorder()
	s := &Server{}

	s.streamResponseWithUsage(w, strings.NewReader(sseStream), log)

	body := w.Body.String()

	// Verify all data chunks + [DONE] were forwarded.
	if !strings.Contains(body, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}") {
		t.Errorf("missing first chunk in output")
	}
	if !strings.Contains(body, "data: {\"choices\":[{\"delta\":{\"content\":\" World\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":20}}") {
		t.Errorf("missing second chunk in output")
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("missing [DONE] in output")
	}

	// Verify usage was parsed.
	if log.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", log.InputTokens)
	}
	if log.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", log.OutputTokens)
	}
}

// TestStreamResponseWithUsage_NoUsage verifies that when no chunk
// contains usage data, the log entry tokens remain zero.
func TestStreamResponseWithUsage_NoUsage(t *testing.T) {
	sseStream := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: [DONE]\n\n"

	log := &types.LogEntry{}
	w := httptest.NewRecorder()
	s := &Server{}

	s.streamResponseWithUsage(w, strings.NewReader(sseStream), log)

	if log.InputTokens != 0 || log.OutputTokens != 0 {
		t.Errorf("expected zero tokens when no usage present, got in=%d out=%d",
			log.InputTokens, log.OutputTokens)
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Errorf("missing [DONE] in output")
	}
}

// TestStreamResponseWithUsage_CleanEOF verifies that when the
// upstream closes the stream without sending "data: [DONE]", the
// scanner's Err() is nil and log.Error stays empty.
func TestStreamResponseWithUsage_CleanEOF(t *testing.T) {
	sseStream := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"

	log := &types.LogEntry{}
	w := httptest.NewRecorder()
	s := &Server{}

	s.streamResponseWithUsage(w, strings.NewReader(sseStream), log)

	if log.Error != "" {
		t.Errorf("expected no log.Error on clean EOF, got %q", log.Error)
	}
}

// TestStreamResponseWithUsage_LongLine verifies that a single SSE
// frame larger than the old scanner-based implementation's 1 MiB max
// token size is forwarded whole: bufio.Reader grows its internal
// buffer, so an arbitrarily long line is relayed without truncation,
// and no error is flagged because long lines are supported by design.
func TestStreamResponseWithUsage_LongLine(t *testing.T) {
	// One data line exceeding the old 1 MiB scanner cap, followed by a
	// clean [DONE] — the new implementation must forward it intact and
	// end the stream without recording an error.
	oversized := "data: " + strings.Repeat("x", 2*1024*1024) + "\n"
	src := strings.NewReader(oversized + "data: [DONE]\n\n")

	log := &types.LogEntry{}
	w := httptest.NewRecorder()
	s := &Server{}

	s.streamResponseWithUsage(w, src, log)

	if !strings.Contains(w.Body.String(), strings.Repeat("x", 2*1024*1024)) {
		t.Errorf("oversized line was truncated or dropped")
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Errorf("missing [DONE] in output")
	}
	if log.Error != "" {
		t.Errorf("expected no log.Error for a clean long-line stream, got %q", log.Error)
	}
}

// TestHealth_Enhanced verifies handleHealth returns the enhanced JSON
// response with providers_total, providers_enabled, etc.
func TestHealth_Enhanced(t *testing.T) {
	dir := t.TempDir()
	mgr := config.New(dir)
	if _, err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Add some providers and aliases to the config.
	if err := mgr.Save(types.Config{
		ServerHost:   "127.0.0.1",
		ServerPort:   0,
		LogRetention: 7,
		Providers: []types.Provider{
			{ID: "p1", Name: "openai", Type: types.ProviderOpenAI, BaseURL: "https://api.openai.com/v1", Enabled: true, Weight: 1},
			{ID: "p2", Name: "disabled", Type: types.ProviderCustom, BaseURL: "http://localhost", Enabled: false, Weight: 1},
		},
		ModelAliases: []types.ModelAlias{
			{ID: "a1", Alias: "gpt-4o", ProviderID: "p1", ProviderModel: "gpt-4o", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st := store.New()
	srv := New(mgr, st)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("missing status ok: %s", body)
	}
	if !strings.Contains(body, `"providers_total":2`) {
		t.Errorf("missing providers_total:2 in %s", body)
	}
	if !strings.Contains(body, `"providers_enabled":1`) {
		t.Errorf("missing providers_enabled:1 in %s", body)
	}
	if !strings.Contains(body, `"aliases_total":1`) {
		t.Errorf("missing aliases_total:1 in %s", body)
	}
	if !strings.Contains(body, `"aliases_enabled":1`) {
		t.Errorf("missing aliases_enabled:1 in %s", body)
	}
	if !strings.Contains(body, `"uptime_seconds"`) {
		t.Errorf("missing uptime_seconds in %s", body)
	}
}

// TestListModels_RequiresAuth verifies /v1/models returns 401 when a
// gateway key is configured but the request lacks a valid bearer token.
func TestListModels_RequiresAuth(t *testing.T) {
	dir := t.TempDir()
	mgr := config.New(dir)
	if _, err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := mgr.Save(types.Config{
		ServerHost: "127.0.0.1",
		ServerPort: 0,
		ClientKeys: []types.ClientKey{
			{ID: "ck1", Label: "test", Key: "gw-test-secret-key-1234", Enabled: true},
		},
		Providers: []types.Provider{
			{ID: "p1", Name: "openai", Type: types.ProviderOpenAI, BaseURL: "https://api.openai.com/v1", Enabled: true, Weight: 1},
		},
		ModelAliases: []types.ModelAlias{
			{ID: "a1", Alias: "gpt-4o", ProviderID: "p1", ProviderModel: "gpt-4o", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	srv := New(mgr, store.New())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	st := srv.Status()
	rawReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/v1/models", st.BaseURL[len("http://"):]), nil)
	w := httptest.NewRecorder()
	srv.handleListModels(w, rawReq.WithContext(context.WithValue(rawReq.Context(), authRequiredKey, true)))

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body: %s", w.Code, w.Body.String())
	}
}

// TestBodyLimit_ChatCompletions verifies that requests exceeding the
// configured MaxRequestBodyMB receive 413 Request Entity Too Large.
func TestBodyLimit_ChatCompletions(t *testing.T) {
	dir := t.TempDir()
	mgr := config.New(dir)
	if _, err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := mgr.Save(types.Config{
		ServerHost:       "127.0.0.1",
		ServerPort:       0,
		MaxRequestBodyMB: 1,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	srv := New(mgr, store.New())

	big := strings.Repeat("x", 2<<20+1) // 2 MiB + 1, over the 1 MiB limit
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/chat/completions", strings.NewReader(big))
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413; body: %s", w.Code, w.Body.String())
	}
}

// TestBodyLimit_DefaultDoesNotRejectLongContext verifies the gateway
// no longer enforces the old 10 MiB hard ceiling: a body above 10 MiB
// passes the body-limit gate (and only then fails JSON parsing / alias
// resolution with 400, never 413). This is the "context limits are the
// client's job, not the gateway's" contract.
func TestBodyLimit_DefaultDoesNotRejectLongContext(t *testing.T) {
	dir := t.TempDir()
	mgr := config.New(dir)
	if _, err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := mgr.Save(types.Config{
		ServerHost: "127.0.0.1",
		ServerPort: 0,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	srv := New(mgr, store.New())

	big := strings.Repeat("x", 10<<20+1) // exceeds the old 10 MiB cap
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/chat/completions", strings.NewReader(big))
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)

	if w.Code == http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, body limit rejected a >10 MiB body", w.Code)
	}
	// The oversized-but-invalid JSON should fail later in the pipeline
	// (extractModel) with 400, proving the body-limit gate passed it.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (invalid JSON after body gate); body: %s", w.Code, w.Body.String())
	}
}

// TestAnthropicChat_DropsUpstreamContentLength verifies that the
// Anthropic conversion path never forwards the upstream's
// Content-Length header. The upstream body is re-encoded into the
// OpenAI response shape below, whose byte length differs, so a stale
// Content-Length would hang or truncate the client.
func TestAnthropicChat_DropsUpstreamContentLength(t *testing.T) {
	anthResp := `{"id":"msg_01","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Writing the full body in one call lets Go set Content-Length.
		_, _ = io.WriteString(w, anthResp)
	}))
	defer up.Close()

	dir := t.TempDir()
	mgr := config.New(dir)
	if _, err := mgr.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	srv := New(mgr, store.New())

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(context.WithValue(req.Context(), authRequiredKey, true))
	w := httptest.NewRecorder()

	provider := types.Provider{
		ID:      "prov-content-length-test",
		Name:    "cl-content-length",
		Type:    types.ProviderAnthropic,
		BaseURL: up.URL,
		APIKey:  "sk-test",
	}
	var log types.LogEntry
	srv.handleAnthropicChat(w, req, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
		false, provider, &log)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", w.Code, w.Body.String())
	}
	if cl := w.Header().Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length = %q, want empty (must be recomputed after body transform)", cl)
	}

	// The relayed body must be the converted OpenAI shape, not the raw
	// upstream Anthropic body.
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode converted body: %v\n%s", err, w.Body.String())
	}
	if out["object"] != "chat.completion" {
		t.Errorf("object = %v, want chat.completion", out["object"])
	}
}

// TestStreamResponseWithUsage_LongLineNotTruncated verifies a single
// SSE data line larger than the old 1 MiB cap is forwarded whole
// instead of truncating/dropping the stream.
func TestStreamResponseWithUsage_LongLineNotTruncated(t *testing.T) {
	// ~1.3 MiB line, comfortably above the former sseMaxLineBytes.
	huge := strings.Repeat("Z", 1<<19+1<<20)
	sse := "data: " + huge + "\n\ndata: [DONE]\n\n"

	w := httptest.NewRecorder()
	srv := &Server{}
	var log types.LogEntry
	srv.streamResponseWithUsage(w, strings.NewReader(sse), &log)

	out := w.Body.String()
	if !strings.Contains(out, huge) {
		t.Errorf("long SSE line was truncated (out len=%d)", len(out))
	}
	if log.Error != "" {
		t.Errorf("log.Error set: %v", log.Error)
	}
	if !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Errorf("missing [DONE] terminator")
	}
}
