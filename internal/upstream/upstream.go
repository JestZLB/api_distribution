// Package upstream contains a thin HTTP client that forwards a
// proxied request to the upstream LLM provider and returns the
// response (streamed or buffered).
package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"api_distribution/internal/ioextra"
	"api_distribution/internal/types"
)

// registry caches upstream clients so that multiple goroutines can
// reuse the same underlying TCP connection pool per provider.
var (
	clientsMu sync.RWMutex
	clients   = map[cacheKey]*Client{}
)

// cacheKey uniquely identifies a client configuration. When any of
// these fields change the cached transport is discarded.
type cacheKey struct {
	ID                 string
	Name               string
	Type               string
	BaseURL            string
	APIKey             string
	InsecureSkipVerify bool
}

func buildCacheKey(p types.Provider) cacheKey {
	return cacheKey{
		ID:                 p.ID,
		Name:               p.Name,
		Type:               p.Type,
		BaseURL:            p.BaseURL,
		APIKey:             p.APIKey,
		InsecureSkipVerify: false,
	}
}

// Client forwards requests to a single upstream provider.
type Client struct {
	provider types.Provider
	http     *http.Client

	// lastUsed records the most recent time the client finished a request
	// (or was created), in unix nanoseconds. It is read by ReapIdle to
	// identify entries that have been idle long enough to evict.
	lastUsed atomic.Int64
	// inFlight counts requests currently using this client. ReapIdle
	// never evicts an entry with inFlight > 0, so an active streaming
	// request cannot be reaped even if lastUsed is stale.
	inFlight atomic.Int64
}

// touch refreshes the last-used timestamp to now.
func (c *Client) touch() {
	c.lastUsed.Store(time.Now().UnixNano())
}

// acquire records that a request has begun using this client.
func (c *Client) acquire() {
	c.inFlight.Add(1)
}

// release records that a request has finished using this client and
// refreshes the last-used timestamp so idle accounting stays accurate.
func (c *Client) release() {
	c.inFlight.Add(-1)
	c.touch()
}

// GetOrCreate returns a cached Client for the given provider,
// reusing the same HTTP transport when the provider configuration has
// not changed. This dramatically reduces TLS handshakes under high
// concurrency while still picking up configuration changes.
func GetOrCreate(p types.Provider) *Client {
	key := buildCacheKey(p)
	clientsMu.RLock()
	if c := clients[key]; c != nil {
		c.touch()
		clientsMu.RUnlock()
		return c
	}
	clientsMu.RUnlock()

	clientsMu.Lock()
	defer clientsMu.Unlock()

	if c := clients[key]; c != nil {
		c.touch()
		return c
	}
	c := newClient(p)
	clients[key] = c
	return c
}

// Evict removes the cached client for the given provider and closes its
// idle connections. Callers should invoke this when a provider is
// deleted or when its connection-affecting fields (ID, Name, Type,
// BaseURL, APIKey) change, so the global cache does not accumulate
// entries — and their underlying http.Transport connection pools — for
// providers that no longer exist.
func Evict(p types.Provider) {
	key := buildCacheKey(p)
	clientsMu.Lock()
	if c := clients[key]; c != nil {
		log.Printf("[upstream] evict (explicit) id=%q name=%q type=%q baseUrl=%q lastUsed=%s",
			c.provider.ID, c.provider.Name, c.provider.Type, c.provider.BaseURL,
			time.Unix(0, c.lastUsed.Load()).Format(time.RFC3339))
		c.http.CloseIdleConnections()
		delete(clients, key)
	}
	clientsMu.Unlock()
}

// ReapIdle evicts cached clients whose most recent use is more than
// maxIdle ago and closes their idle connections. It returns the number
// of entries removed. This is intended to run on a timer so the global
// registry cannot grow without bound when many distinct providers are
// added, edited, and abandoned over the app's lifetime.
//
// Entries with an in-flight request (inFlight > 0) are never evicted,
// so an active streaming request is safe even if its lastUsed timestamp
// is stale. CloseIdleConnections only closes idle pooled connections,
// and callers that already hold a *Client keep their reference after
// the map entry is deleted.
func ReapIdle(maxIdle time.Duration) int {
	cutoff := time.Now().Add(-maxIdle).UnixNano()
	clientsMu.Lock()
	defer clientsMu.Unlock()

	log.Printf("[upstream] reap scan start: maxIdle=%s candidates=%d",
		maxIdle, len(clients))

	removed := 0
	skipped := 0
	kept := 0
	var minKeptIdle time.Duration
	now := time.Now().UnixNano()
	for key, c := range clients {
		if n := c.inFlight.Load(); n > 0 {
			// Active request(s) in progress — never evict, even if
			// lastUsed is stale (a long-running stream).
			log.Printf("[upstream] reap skip active id=%q name=%q type=%q baseUrl=%q inFlight=%d",
				c.provider.ID, c.provider.Name, c.provider.Type, c.provider.BaseURL, n)
			skipped++
			continue
		}
		last := c.lastUsed.Load()
		idle := time.Duration(now - last)
		if last < cutoff {
			// Never log the API key — provider identity is enough to
			// correlate with config.
			log.Printf("[upstream] reap evict id=%q name=%q type=%q baseUrl=%q idle=%s lastUsed=%s",
				c.provider.ID, c.provider.Name, c.provider.Type, c.provider.BaseURL,
				roundDuration(idle), time.Unix(0, last).Format(time.RFC3339))
			c.http.CloseIdleConnections()
			delete(clients, key)
			removed++
		} else {
			kept++
			if kept == 1 || idle < minKeptIdle {
				minKeptIdle = idle
			}
		}
	}

	log.Printf("[upstream] reap scan end: removed=%d skipped=%d remaining=%d minKeptIdle=%s",
		removed, skipped, len(clients), roundDuration(minKeptIdle))
	return removed
}

// roundDuration is a small helper that trims sub-second noise from a
// duration so log lines stay readable.
func roundDuration(d time.Duration) time.Duration {
	if d < 0 {
		return d
	}
	return d.Round(time.Second)
}

func newClient(p types.Provider) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		// TLS verification is enabled by default. Set
		// InsecureSkipVerify only when the provider explicitly
		// requires it (e.g. self-signed certs in dev).
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
	}
	c := &Client{
		provider: p,
		http: &http.Client{
			Transport: transport,
			// Streaming responses can take much longer than the
			// default; cancel per-request via the caller's ctx.
			Timeout: 0,
		},
	}
	c.touch()
	return c
}

// Do forwards method/path with body to the upstream and returns the
// raw *http.Response so the caller can stream or buffer it.
func (c *Client) Do(ctx context.Context, method, path string, body []byte, streaming bool) (*http.Response, error) {
	c.touch()
	base := strings.TrimRight(c.provider.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("provider %q has no base URL", c.provider.Name)
	}
	url := base + path
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	c.applyAuth(req)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	// Track an in-flight reference so ReapIdle cannot evict this client
	// while the caller is still streaming/reading the response body. The
	// reference is released exactly once when the body is closed.
	c.acquire()
	resp.Body = ioextra.WrapCloser(resp.Body, c.release)
	return resp, nil
}

// applyAuth sets the right authentication scheme for the configured
// provider type. Empty API keys are treated as "no auth" so local
// providers (e.g. Ollama) work without ceremony.
func (c *Client) applyAuth(req *http.Request) {
	key := c.provider.APIKey
	if key == "" {
		return
	}
	switch strings.ToLower(c.provider.Type) {
	case types.ProviderAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case types.ProviderAzure:
		// Azure OpenAI uses the legacy api-key header for deployment
		// URLs of the form https://{resource}.openai.azure.com/...
		req.Header.Set("api-key", key)
	default:
		// openai, gemini, ollama, custom — all serve
		// /chat/completions with Bearer auth.
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
