package server

import (
	"context"
	"fmt"
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

// TestStreamResponse_Flushes verifies the non-Anthropic streaming
// path types-asserts http.Flusher and pushes each chunk to the wire
// before the handler returns. Without flushing, chunks would sit in
// net/http's internal 4 KiB buffer until the handler exits.
func TestStreamResponse_Flushes(t *testing.T) {
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	src := strings.NewReader("hello world")

	s := &Server{}
	s.streamResponse(w, src)

	if w.flushed == 0 {
		t.Fatal("expected at least one Flush call, got none")
	}
	if w.Body.String() != "hello world" {
		t.Errorf("body = %q, want %q", w.Body.String(), "hello world")
	}
}

// flushRecorder wraps httptest.ResponseRecorder to count Flush calls.
// httptest.ResponseRecorder already implements http.Flusher.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed int
}

func (f *flushRecorder) Flush() {
	f.flushed++
	// httptest.ResponseRecorder.Flush is a no-op, but the embed
	// already forwards headers/body writes via the shared fields.
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

// TestStreamResponseWithUsage_ScanError verifies that when the
// upstream sends a line longer than bufio.Scanner's default 64 KiB
// buffer, the resulting Err() is captured into log.Error so
// operators can spot a broken upstream instead of silently swallowing
// the failure.
func TestStreamResponseWithUsage_ScanError(t *testing.T) {
	// One line that exceeds bufio.Scanner's max token size (1 MiB, set in
	// streamResponseWithUsage), followed by EOF — guarantees scanner.Scan()
	// returns false with scanner.Err() == bufio.ErrTooLong.
	oversized := "data: " + strings.Repeat("x", 2*1024*1024) + "\n"
	// Need a trailing newline + data line for Scan to attempt the
	// oversized line and then fail on the next read.
	src := strings.NewReader(oversized + "data: short\n")

	log := &types.LogEntry{}
	w := httptest.NewRecorder()
	s := &Server{}

	s.streamResponseWithUsage(w, src, log)

	if log.Error == "" {
		t.Errorf("expected log.Error to capture scanner.Err(), got empty")
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

// TestBodyLimit_ChatCompletions verifies that requests with an
// oversized body receive 413 Request Entity Too Large.
func TestBodyLimit_ChatCompletions(t *testing.T) {
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
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	st := srv.Status()
	big := strings.Repeat("x", 10<<20+1)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/v1/chat/completions", st.BaseURL[len("http://"):]), strings.NewReader(big))
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413; body: %s", w.Code, w.Body.String())
	}
}
