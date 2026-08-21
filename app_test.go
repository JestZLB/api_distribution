package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api_distribution/internal/types"
)

func TestTestProvider_EmptyBaseURL(t *testing.T) {
	a := &App{}
	_, err := a.TestProvider(types.Provider{BaseURL: "", APIKey: "key"})
	if err == nil || !strings.Contains(err.Error(), "base URL is empty") {
		t.Fatalf("expected 'base URL is empty' error, got: %v", err)
	}
}

func TestTestProvider_MissingAPIKey(t *testing.T) {
	a := &App{}
	msg, err := a.TestProvider(types.Provider{BaseURL: "http://localhost", APIKey: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "missing API key") {
		t.Fatalf("expected 'missing API key', got: %q", msg)
	}
}

func TestTestProvider_NetworkProbe_Success(t *testing.T) {
	// Start a local HTTP server that returns 200 for /models
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" && r.Header.Get("Authorization") == "Bearer test-key-1234" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[]}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer ts.Close()

	a := &App{}
	p := types.Provider{
		Name:    "test-provider",
		BaseURL: ts.URL,
		APIKey:  "test-key-1234",
	}
	msg, err := a.TestProvider(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "ok") || !strings.Contains(msg, "[status 200]") {
		t.Fatalf("expected 'ok ... [status 200]', got: %q", msg)
	}
	if strings.Contains(msg, "test-provider") || strings.Contains(msg, "test-key") {
		t.Fatalf("expected response to omit provider name / api key suffix, got: %q", msg)
	}
}

func TestTestProvider_NetworkProbe_ConnectionFailed(t *testing.T) {
	// Use a port that nothing listens on - guaranteed connection refused.
	a := &App{}
	p := types.Provider{
		Name:    "bad-provider",
		BaseURL: "http://127.0.0.1:1",
		APIKey:  "test-key",
	}
	msg, err := a.TestProvider(p)
	if err == nil {
		t.Fatalf("expected error for connection refused, got nil (msg=%q)", msg)
	}
	if !strings.Contains(err.Error(), "connection failed") {
		t.Fatalf("expected 'connection failed', got: %q", err)
	}
}

func TestTestProvider_NetworkProbe_StatusNon200(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	a := &App{}
	p := types.Provider{
		Name:    "unauth-provider",
		BaseURL: ts.URL,
		APIKey:  "bad-key",
	}
	msg, err := a.TestProvider(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Even 401 is still "connection succeeded" - the method reports status
	if !strings.Contains(msg, "ok") {
		t.Fatalf("expected 'ok' (connection succeeded even with 401), got: %q", msg)
	}
	if !strings.Contains(msg, "[status 401]") {
		t.Fatalf("expected [status 401], got: %q", msg)
	}
}

func TestTestProvider_NetworkProbe_TrailingSlash(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	a := &App{}
	p := types.Provider{
		Name:    "slash-provider",
		BaseURL: ts.URL + "/", // trailing slash
		APIKey:  "key12345",
	}
	msg, err := a.TestProvider(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(msg, "[status 200]") {
		t.Fatalf("expected [status 200], got: %q", msg)
	}
}
