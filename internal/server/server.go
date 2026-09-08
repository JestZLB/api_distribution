// Package server implements the OpenAI-compatible HTTP proxy that
// receives client requests, resolves the requested model alias to a
// concrete provider, forwards the request, and streams the response
// back to the client.
package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"api_distribution/internal/anthropicconv"
	"api_distribution/internal/config"
	"api_distribution/internal/store"
	"api_distribution/internal/system"
	"api_distribution/internal/types"
	"api_distribution/internal/upstream"
)

const (
	// maxRequestBodyMB is the default memory-safety net for request
	// and buffered non-streaming response bodies. The gateway does NOT
	// enforce IDE-level context limits (that is the client's job), so
	// this only guards against genuinely oversized / hostile payloads
	// and is overridable via Config.MaxRequestBodyMB.
	maxRequestBodyMB = 256
)

type authKeyType struct{}

// authRequiredKey is set in the request context by authMiddleware so
// that handlers know whether the route requires gateway authentication.
var authRequiredKey = authKeyType{}

// Server is the OpenAI-compatible proxy.
type Server struct {
	cfgMgr *config.Manager
	store  *store.Store

	httpSrv *http.Server
	ln      net.Listener

	mu      sync.RWMutex
	status  types.ServerStatus
	started time.Time
}

// New creates a new proxy server.
func New(cfgMgr *config.Manager, st *store.Store) *Server {
	return &Server{
		cfgMgr: cfgMgr,
		store:  st,
		status: types.ServerStatus{},
	}
}

// Start binds the configured host:port and starts serving traffic.
// Returns an error if the port is already in use.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.httpSrv != nil {
		return fmt.Errorf("server already running")
	}

	cfg := s.cfgMgr.Get()
	host := cfg.ServerHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.ServerPort
	if port == 0 {
		port = 8080
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/models", s.handleListModels)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/", s.handleNotFound)

	handler := s.authMiddleware(mux)

	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 0, // streaming responses
		IdleTimeout:  60 * time.Second,
	}
	s.httpSrv = srv
	s.ln = ln
	s.started = time.Now()

	system.SafeGo("http.serve", func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Serve returned unexpectedly (port stole, etc.).
			// Clear our references so a subsequent Start can succeed
			// without leaking a stale *http.Server.
			s.mu.Lock()
			if s.httpSrv == srv {
				s.httpSrv = nil
				s.ln = nil
			}
			s.status.LastError = err.Error()
			s.status.Running = false
			s.mu.Unlock()
		}
	})

	s.status = types.ServerStatus{
		Running:   true,
		Host:      host,
		Port:      port,
		StartedAt: s.started.Unix(),
		BaseURL:   fmt.Sprintf("http://%s:%d/v1", host, port),
	}
	return nil
}

// Stop gracefully shuts down the proxy server. Shutdown can block on
// long-running handlers, so we capture the *http.Server under the
// lock and call Shutdown after releasing it. This keeps Status() and
// a concurrent Start() from being starved while we drain.
//
// The shutdown deadline is read from the current config so an SSE
// stream longer than the previous 5s default can finish draining;
// zero (or negative) falls back to the legacy 5s grace period.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.httpSrv == nil {
		s.mu.Unlock()
		return nil
	}
	srv := s.httpSrv
	s.httpSrv = nil
	s.ln = nil
	s.status.Running = false
	s.mu.Unlock()

	timeoutSec := 5
	if cfg := s.cfgMgr.Get(); cfg.ShutdownTimeoutSec > 0 {
		timeoutSec = cfg.ShutdownTimeoutSec
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// Status returns a snapshot of the current server status.
func (s *Server) Status() types.ServerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	st := s.status
	if s.httpSrv != nil {
		st.Running = true
	}
	st.RequestCount = atomic.LoadInt64(&s.status.RequestCount)
	st.ErrorCount = atomic.LoadInt64(&s.status.ErrorCount)
	return st
}

// ---------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	cfg := s.cfgMgr.Get()
	total := len(cfg.Providers)
	enabled := 0
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabled++
		}
	}
	aliasTotal := len(cfg.ModelAliases)
	aliasEnabled := 0
	for _, a := range cfg.ModelAliases {
		if a.Enabled {
			aliasEnabled++
		}
	}
	uptime := int64(time.Since(s.started).Seconds())
	_, _ = fmt.Fprintf(w,
		`{"status":"ok","providers_total":%d,"providers_enabled":%d,"aliases_total":%d,"aliases_enabled":%d,"uptime_seconds":%d}`,
		total, enabled, aliasTotal, aliasEnabled, uptime,
	)
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	http.Error(w, fmt.Sprintf("not found: %s", r.URL.Path), http.StatusNotFound)
}

// handleListModels returns the model aliases the gateway exposes.
func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgMgr.Snapshot()
	if authRequired, _ := r.Context().Value(authRequiredKey).(bool); authRequired {
		if !s.authorize(r, cfg.ClientKeys) {
			http.Error(w, `{"error":{"message":"unauthorized","type":"auth_error"}}`, http.StatusUnauthorized)
			atomic.AddInt64(&s.status.ErrorCount, 1)
			return
		}
	}
	atomic.AddInt64(&s.status.RequestCount, 1)

	type modelObj struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	type listResp struct {
		Object string     `json:"object"`
		Data   []modelObj `json:"data"`
	}

	data := make([]modelObj, 0, len(cfg.ModelAliases))
	for _, a := range cfg.ModelAliases {
		if !a.Enabled {
			continue
		}
		owner := "gateway"
		for _, p := range cfg.Providers {
			if p.ID == a.ProviderID {
				owner = strings.ToLower(p.Name)
				break
			}
		}
		data = append(data, modelObj{
			ID:      a.Alias,
			Object:  "model",
			OwnedBy: owner,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, mustJSON(listResp{Object: "list", Data: data}))
}

// handleChatCompletions is the core forwarding endpoint. It rewrites
// the model's alias to the provider's actual model id, picks an
// upstream client, and streams the response back.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&s.status.RequestCount, 1)
	cfg := s.cfgMgr.Snapshot()

	// Authn: client must present a known client key (Authorization: Bearer ...).
	var matchedKey types.ClientKey
	if authRequired, _ := r.Context().Value(authRequiredKey).(bool); authRequired {
		if !s.authorize(r, cfg.ClientKeys) {
			http.Error(w, `{"error":{"message":"unauthorized","type":"auth_error"}}`,
				http.StatusUnauthorized)
			atomic.AddInt64(&s.status.ErrorCount, 1)
			return
		}
		matchedKey = matchClientKey(r, cfg.ClientKeys)
	}

	// maxBody is the memory-safety net for request / buffered response
	// bodies, derived from the configurable MaxRequestBodyMB (which
	// defaults to maxRequestBodyMB const). It is deliberately far above
	// any realistic IDE/client context so the gateway never acts as a
	// context-length enforcer. A zero config value (pre-normalize
	// configs) falls back to the default rather than collapsing the cap.
	maxMB := cfg.MaxRequestBodyMB
	if maxMB <= 0 {
		maxMB = maxRequestBodyMB
	}
	maxBody := int64(maxMB) << 20

	body, ok := readRequestBody(w, r, maxBody)
	if !ok {
		atomic.AddInt64(&s.status.ErrorCount, 1)
		return
	}

	alias, modelField, ok := extractModel(body)
	if !ok {
		http.Error(w, `{"error":{"message":"missing 'model' field","type":"invalid_request"}}`,
			http.StatusBadRequest)
		return
	}

	resolved, err := resolveAlias(cfg, alias)
	if err != nil {
		http.Error(w, errBody(err), http.StatusBadRequest)
		atomic.AddInt64(&s.status.ErrorCount, 1)
		return
	}

	streaming := isStreaming(body)
	log := types.LogEntry{
		ID:             newID(),
		Timestamp:      time.Now().UnixNano(),
		Method:         r.Method,
		Path:           r.URL.Path,
		Alias:          alias,
		ProviderID:     resolved.Provider.ID,
		ProviderName:   resolved.Provider.Name,
		ClientIP:       clientIP(r),
		APIKeySuffix:   keySuffix(matchedKey.Key),
		ClientKeyLabel: matchedKey.Label,
		Streaming:      streaming,
	}
	defer func() {
		s.store.Append(log)
	}()

	body = rewriteModel(body, modelField, resolved.ProviderModel)

	if strings.EqualFold(resolved.Provider.Type, types.ProviderAnthropic) {
		s.handleAnthropicChat(w, r, body, streaming, resolved.Provider, &log)
		return
	}

	// Build an upstream client and forward.
	cli := upstream.GetOrCreate(resolved.Provider)
	ctx := r.Context()

	// For streaming OpenAI requests, ensure the upstream returns `usage`
	// in the final SSE chunk by injecting stream_options.include_usage.
	if streaming {
		body = ensureStreamUsage(body)
	}

	start := time.Now()
	var resp *http.Response
	var doErr error
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, doErr = cli.Do(ctx, r.Method, "/chat/completions", body, streaming)
		if doErr == nil && resp.StatusCode < 500 {
			break
		}
		// Prepare for retry if possible.
		if attempt < maxAttempts-1 {
			retryable := doErr != nil || (resp != nil && resp.StatusCode >= 500)
			if resp != nil {
				resp.Body.Close()
				resp = nil
			}
			if retryable {
				// Retry noise: surface to stdout but do not pollute the request
				// log ring buffer. The final log entry below already records the
				// resolved status and error from the last attempt.
				stdlog.Printf("[retry] %s %s provider=%s model=%s attempt=%d: %v",
					r.Method, r.URL.Path, resolved.Provider.Name, alias, attempt+1, doErr)
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
	}

	if doErr != nil {
		log.Error = doErr.Error()
		log.StatusCode = http.StatusBadGateway
		atomic.AddInt64(&s.status.ErrorCount, 1)
		http.Error(w, errBody(doErr), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	log.StatusCode = resp.StatusCode

	// Copy upstream headers, then relay the body. Content-Length is
	// dropped so Go recomputes it from the bytes actually written — the
	// response body may be transformed (Anthropic path) or relayed
	// differently than upstream, and a stale Content-Length could hang
	// or truncate the client.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Del("Content-Length")

	if streaming {
		w.WriteHeader(resp.StatusCode)
		s.streamResponseWithUsage(w, resp.Body, &log)
	} else {
		// Buffer the full non-streaming response so usage can be parsed;
		// only the generous configurable safety net applies, never a
		// tight context-style cutoff. Check the limit BEFORE writing the
		// status so an oversized upstream response yields a clean 502.
		respBody, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		if rerr != nil {
			log.Error = rerr.Error()
			atomic.AddInt64(&s.status.ErrorCount, 1)
			http.Error(w, errBody(rerr), http.StatusBadGateway)
			return
		}
		if int64(len(respBody)) > maxBody {
			log.Error = "upstream response too large"
			atomic.AddInt64(&s.status.ErrorCount, 1)
			http.Error(w, `{"error":{"message":"upstream response too large","type":"upstream_error"}}`, http.StatusBadGateway)
			return
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		s.collectNonStream(respBody, &log)
	}
	log.LatencyMs = time.Since(start).Milliseconds()
}

// handleAnthropicChat forwards a request to Anthropic's Messages API,
// translating between the OpenAI Chat Completions wire format that
// this proxy exposes to clients and the Messages format that
// Anthropic expects. It reuses the same upstream client / log entry
// conventions as handleChatCompletions.
func (s *Server) handleAnthropicChat(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	streaming bool,
	p types.Provider,
	log *types.LogEntry,
) {
	ctx := r.Context()
	start := time.Now()

	anthBody, err := anthropicconv.BuildRequest(body, streaming)
	if err != nil {
		log.Error = err.Error()
		log.StatusCode = http.StatusBadRequest
		atomic.AddInt64(&s.status.ErrorCount, 1)
		http.Error(w, errBody(err), http.StatusBadRequest)
		return
	}

	cli := upstream.GetOrCreate(p)
	var resp *http.Response
	var doErr error
	const maxAttempts = 2
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, doErr = cli.Do(ctx, http.MethodPost, "/messages", anthBody, streaming)
		if doErr == nil && resp.StatusCode < 500 {
			break
		}
		if attempt < maxAttempts-1 {
			retryable := doErr != nil || (resp != nil && resp.StatusCode >= 500)
			if resp != nil {
				resp.Body.Close()
				resp = nil
			}
			if retryable {
				// Retry noise: surface to stdout but do not pollute the request
				// log ring buffer. The final log entry below already records the
				// resolved status and error from the last attempt.
				stdlog.Printf("[retry] %s %s provider=%s model=%s attempt=%d: %v",
					r.Method, r.URL.Path, p.Name, log.Alias, attempt+1, doErr)
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
	}

	if doErr != nil {
		log.Error = doErr.Error()
		log.StatusCode = http.StatusBadGateway
		atomic.AddInt64(&s.status.ErrorCount, 1)
		http.Error(w, errBody(doErr), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy upstream headers, then make sure the response shape matches
	// what an OpenAI client expects. We MUST drop Content-Length: the
	// body is transformed to the OpenAI shape below, whose byte length
	// differs from the upstream Anthropic body — a stale Content-Length
	// would make the client hang waiting for bytes or truncate early.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Del("Content-Length")
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	log.StatusCode = resp.StatusCode

	if streaming {
		usage, err := anthropicconv.TransformStream(resp.Body, w)
		if err != nil && !isClientDisconnect(err) {
			log.Error = err.Error()
			atomic.AddInt64(&s.status.ErrorCount, 1)
		} else if err == nil {
			// Clamp to int32 to avoid int wrap-around on hostile input,
			// mirroring the non-streaming path's usage backfill.
			log.InputTokens = clampToInt32(usage.InputTokens)
			log.OutputTokens = clampToInt32(usage.OutputTokens)
		}
	} else {
		usage, err := anthropicconv.TransformResponse(resp.Body, w)
		if err != nil && !isClientDisconnect(err) {
			log.Error = err.Error()
			atomic.AddInt64(&s.status.ErrorCount, 1)
		} else if err == nil {
			// Clamp to int32 to avoid int wrap-around on hostile input.
			log.InputTokens = clampToInt32(usage.InputTokens)
			log.OutputTokens = clampToInt32(usage.OutputTokens)
		}
	}

	log.LatencyMs = time.Since(start).Milliseconds()
}

// sseReaderPool caches *bufio.Reader instances for streamResponseWithUsage
// so each SSE stream doesn't allocate a fresh 64 KiB buffer. The reader
// is Reset(src) on Get and Put back when the stream ends, which discards
// any leftover bytes via Reset's documented behaviour.
//
// We cache *bufio.Reader (not bufio.Scanner) because Scanner.Text() copies
// each line into a fresh string; ReadBytes('\n') returns a []byte that
// already aliases the reader's internal buffer, so a 200 KiB Anthropic
// event no longer allocates a 200 KiB string per chunk.
var sseReaderPool = sync.Pool{
	New: func() any {
		return bufio.NewReaderSize(nil, 64*1024)
	},
}

// streamResponseWithUsage reads an SSE stream line-by-line, parses
// each "data: ..." chunk for a usage field (prompt_tokens /
// completion_tokens), and records it in the log entry. Every non-empty
// data line is forwarded to w. The stream ends when "data: [DONE]" is
// received.
//
// There is intentionally no per-line length ceiling: bufio.Reader
// grows its internal buffer to accommodate arbitrarily long lines, so
// a huge single SSE frame is forwarded whole instead of truncating the
// stream. A real read error or client disconnect still terminates the
// loop.
func (s *Server) streamResponseWithUsage(w http.ResponseWriter, src io.Reader, log *types.LogEntry) {
	reader := sseReaderPool.Get().(*bufio.Reader)
	reader.Reset(src)
	defer sseReaderPool.Put(reader)

	flusher, _ := w.(http.Flusher)

	for {
		// ReadBytes('\n') appends the line (with the trailing \n) into
		// the reader's internal buffer — no per-line string copy like
		// scanner.Text() would do.
		raw, err := reader.ReadBytes('\n')

		// ReadBytes includes the trailing \n in raw; trim it so the
		// rest of the logic sees the same shape scanner.Text() used to.
		line := bytes.TrimSuffix(raw, []byte("\n"))

		if len(line) > 0 && bytes.HasPrefix(line, []byte("data: ")) {
			data := line[len("data: "):]

			if bytes.Equal(data, []byte("[DONE]")) {
				fmt.Fprintf(w, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				return
			}

			// Try to parse usage from the chunk.
			var chunk struct {
				Usage *struct {
					PromptTokens     int64 `json:"prompt_tokens"`
					CompletionTokens int64 `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(data, &chunk); err == nil && chunk.Usage != nil {
				// Clamp to int32 before storing into LogEntry's int32 fields
				// so a hostile upstream returning 1e20 cannot wrap into a
				// negative int and corrupt daily totals.
				log.InputTokens = clampToInt32(chunk.Usage.PromptTokens)
				log.OutputTokens = clampToInt32(chunk.Usage.CompletionTokens)
			}

			// Write the original line to the client.
			fmt.Fprintf(w, "data: %s\n\n", data)
			if flusher != nil {
				flusher.Flush()
			}
		}

		if err != nil {
			// ReadBytes returns io.EOF (no error) on clean EOF — match
			// the old scanner.Err() == nil branch: don't flag log.Error.
			// Anything else is a real read error or a client disconnect.
			if err != io.EOF && !isClientDisconnect(err) {
				log.Error = fmt.Sprintf("upstream sse scan: %v", err)
			}
			return
		}
	}
}

func (s *Server) collectNonStream(body []byte, log *types.LogEntry) {
	// Best-effort: pull prompt/completion token counts from OpenAI-style response.
	// Not provider-agnostic — left as a future improvement.
	// Clamp to int32 range so a hostile upstream returning 1e20 cannot
	// wrap into a negative int and corrupt the daily totals.
	in, out := parseUsage(body)
	log.InputTokens = clampToInt32(in)
	log.OutputTokens = clampToInt32(out)
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// clampToInt32 clamps a token count into the int32 range that
// LogEntry.InputTokens / OutputTokens can hold. Values above
// math.MaxInt32 are saturated to MaxInt32; negative values (which
// token counts must never legitimately be) are clamped to 0 so a
// hostile upstream cannot inject a negative daily total via the
// int32 wrap-around.
func clampToInt32(v int64) int {
	if v < 0 {
		return 0
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(v)
}

// readRequestBody reads the request body with a hard size limit to
// prevent memory exhaustion from malicious or misconfigured clients.
func readRequestBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, bool) {
	defer r.Body.Close()
	limited := io.LimitReader(r.Body, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		http.Error(w, errBody(err), http.StatusBadRequest)
		return nil, false
	}
	if int64(len(body)) > maxBytes {
		http.Error(w, `{"error":{"message":"request body too large","type":"invalid_request"}}`, http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

// authMiddleware annotates /v1/* requests with an auth requirement so
// handlers can enforce gateway authentication without route-specific
// wiring. /healthz remains publicly accessible.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		required := strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/v1"
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authRequiredKey, required)))
	})
}

func (s *Server) authorize(r *http.Request, keys []types.ClientKey) bool {
	if len(keys) == 0 {
		// No keys configured -> open mode. Useful for local dev.
		return true
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	got := strings.TrimPrefix(h, "Bearer ")
	if len(got) < 8 {
		return false
	}
	// Walk enabled keys in shortest-first order so a length mismatch
	// doesn't leak key lengths via timing. Constant-time compare per
	// key via crypto/subtle. With <100 keys a linear scan is fine
	// and avoids the constant-time guarantees that a map lookup
	// would break anyway.
	ordered := make([]types.ClientKey, 0, len(keys))
	for _, k := range keys {
		if k.Enabled && k.Key != "" {
			ordered = append(ordered, k)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		return len(ordered[i].Key) < len(ordered[j].Key)
	})
	for _, k := range ordered {
		want := k.Key
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

// matchClientKey returns the ClientKey that satisfied the authorize
// check (or the zero value if none did / auth wasn't required). It is
// used purely for logging the matched key's suffix and label and so
// tolerates extra cost — we still keep the linear scan + crypto/subtle
// path so we don't accidentally compare against disabled keys.
func matchClientKey(r *http.Request, keys []types.ClientKey) types.ClientKey {
	if len(keys) == 0 {
		return types.ClientKey{}
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return types.ClientKey{}
	}
	got := strings.TrimPrefix(h, "Bearer ")
	if len(got) < 8 {
		return types.ClientKey{}
	}
	for _, k := range keys {
		if !k.Enabled || k.Key == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(k.Key)) == 1 {
			return k
		}
	}
	return types.ClientKey{}
}

type resolvedAlias struct {
	Alias         string
	Provider      types.Provider
	ProviderModel string
}

func resolveAlias(cfg types.Config, alias string) (resolvedAlias, error) {
	var match *types.ModelAlias
	for i := range cfg.ModelAliases {
		if cfg.ModelAliases[i].Alias == alias && cfg.ModelAliases[i].Enabled {
			match = &cfg.ModelAliases[i]
			break
		}
	}
	if match == nil {
		return resolvedAlias{}, fmt.Errorf("unknown model alias %q", alias)
	}

	for i := range cfg.Providers {
		p := cfg.Providers[i]
		if p.ID == match.ProviderID && p.Enabled {
			return resolvedAlias{
				Alias:         alias,
				Provider:      p,
				ProviderModel: match.ProviderModel,
			}, nil
		}
	}
	return resolvedAlias{}, fmt.Errorf("provider %q disabled or removed", match.ProviderID)
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.Index(v, ","); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func keySuffix(k string) string {
	if len(k) < 4 {
		return k
	}
	return k[len(k)-4:]
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// isClientDisconnect reports whether err looks like the client hung
// up the HTTP connection mid-stream rather than a real upstream
// failure. These errors (`broken pipe`, `connection reset by peer`,
// `use of closed network connection`) typically surface after the
// upstream already finished writing a successful response and so
// must NOT be recorded against the request — the dashboard's error
// rate would otherwise climb every time a user cancels an in-flight
// generation.
func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	for _, needle := range []string{
		"broken pipe",
		"connection reset",
		"use of closed network connection",
		"client disconnected",
		"stream closed",
	} {
		if strings.Contains(strings.ToLower(msg), needle) {
			return true
		}
	}
	return false
}

// mustJSON serializes v or returns an empty object on error.
// Used only for small static payloads.
func mustJSON(v any) string {
	b, err := jsonMarshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func errBody(err error) string {
	return fmt.Sprintf(`{"error":{"message":%q,"type":"upstream_error"}}`, err.Error())
}
