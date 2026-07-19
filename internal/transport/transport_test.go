package transport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- clientauth ---

func TestApplyClientAuth(t *testing.T) {
	t.Run("post default", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://example.invalid/token", nil)
		form := url.Values{}
		ApplyClientAuth(req, form, ClientAuthConfig{ClientID: "id", ClientSecret: "sec"})
		if form.Get("client_id") != "id" || form.Get("client_secret") != "sec" {
			t.Errorf("form = %v", form)
		}
		if req.Header.Get("Authorization") != "" {
			t.Error("unexpected Authorization header")
		}
	})
	t.Run("basic", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://example.invalid/token", nil)
		form := url.Values{}
		ApplyClientAuth(req, form, ClientAuthConfig{Method: "basic", ClientID: "id", ClientSecret: "sec"})
		user, pass, ok := req.BasicAuth()
		if !ok || user != "id" || pass != "sec" {
			t.Errorf("basic auth = %q %q %v", user, pass, ok)
		}
		if len(form) != 0 {
			t.Errorf("credentials duplicated in form: %v", form)
		}
	})
	t.Run("none", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "https://example.invalid/token", nil)
		form := url.Values{}
		ApplyClientAuth(req, form, ClientAuthConfig{Method: "none", ClientID: "id", ClientSecret: "should-not-appear"})
		if form.Get("client_id") != "id" || form.Get("client_secret") != "" {
			t.Errorf("form = %v", form)
		}
	})
}

// --- tokens ---

func TestSaveLoadTokensRoundtrip(t *testing.T) {
	dir := t.TempDir()
	in := &StoredTokens{AccessToken: "EXAMPLE_TOKEN", RefreshToken: "r", TokenType: "Bearer", ExpiresAt: 42}
	if err := SaveTokens(dir, in); err != nil {
		t.Fatalf("SaveTokens: %v", err)
	}

	fi, err := os.Stat(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("tokens.json mode = %o, want 600", perm)
	}

	out, err := LoadTokens(dir)
	if err != nil {
		t.Fatalf("LoadTokens: %v", err)
	}
	if *out != *in {
		t.Errorf("roundtrip mismatch: %+v vs %+v", out, in)
	}
}

func TestParseTokenResponse(t *testing.T) {
	t.Run("expires_in honoured", func(t *testing.T) {
		tokens, err := ParseTokenResponse([]byte(`{"access_token":"a","refresh_token":"r","expires_in":3600}`))
		if err != nil {
			t.Fatal(err)
		}
		if tokens.ExpiresAt <= time.Now().Unix() {
			t.Errorf("ExpiresAt = %d, want future", tokens.ExpiresAt)
		}
	})
	t.Run("refresh-less non-expiring stores zero", func(t *testing.T) {
		tokens, err := ParseTokenResponse([]byte(`{"access_token":"a"}`))
		if err != nil {
			t.Fatal(err)
		}
		if tokens.ExpiresAt != 0 {
			t.Errorf("ExpiresAt = %d, want 0 (no known expiry)", tokens.ExpiresAt)
		}
	})
	t.Run("slack ok:false envelope", func(t *testing.T) {
		_, err := ParseTokenResponse([]byte(`{"ok":false,"error":"invalid_code"}`))
		if err == nil || !strings.Contains(err.Error(), "invalid_code") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("slack ok:true accepted", func(t *testing.T) {
		tokens, err := ParseTokenResponse([]byte(`{"ok":true,"access_token":"a"}`))
		if err != nil || tokens.AccessToken != "a" {
			t.Fatalf("tokens = %+v, err = %v", tokens, err)
		}
	})
	t.Run("empty access token rejected", func(t *testing.T) {
		if _, err := ParseTokenResponse([]byte(`{}`)); err == nil {
			t.Fatal("empty response accepted")
		}
	})
}

func TestStoredTokenProviderRefreshless(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTokens(dir, &StoredTokens{AccessToken: "tok", ExpiresAt: 0}); err != nil {
		t.Fatal(err)
	}
	p, err := NewStoredTokenProvider(StoredTokenConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// Non-expiring token is returned as-is, no refresh attempted.
	got, err := p.Token()
	if err != nil || got != "tok" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	// After invalidation there is nothing to fall back to.
	p.Invalidate()
	if _, err := p.Token(); err == nil {
		t.Fatal("invalidated refresh-less token still returned")
	}
}

func TestStoredTokenProviderRefresh(t *testing.T) {
	var refreshCalls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "r1" {
			t.Errorf("form = %v", r.PostForm)
		}
		if r.PostForm.Get("client_id") != "cid" || r.PostForm.Get("client_secret") != "csec" {
			t.Errorf("client auth missing: %v", r.PostForm)
		}
		fmt.Fprint(w, `{"access_token":"new-tok","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	dir := t.TempDir()
	// Expired access token + refresh token → refresh on first Token().
	if err := SaveTokens(dir, &StoredTokens{AccessToken: "old", RefreshToken: "r1", ExpiresAt: time.Now().Add(-time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	p, err := NewStoredTokenProvider(StoredTokenConfig{
		StateDir: dir, TokenURL: tokenSrv.URL, ClientID: "cid", ClientSecret: "csec",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := p.Token()
	if err != nil || got != "new-tok" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if refreshCalls.Load() != 1 {
		t.Errorf("refresh calls = %d", refreshCalls.Load())
	}

	// Second call: fresh token cached, no second refresh.
	if _, err := p.Token(); err != nil {
		t.Fatal(err)
	}
	if refreshCalls.Load() != 1 {
		t.Errorf("cached token not used; refresh calls = %d", refreshCalls.Load())
	}

	// Refresh token was preserved (response had none) and persisted.
	saved, err := LoadTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccessToken != "new-tok" || saved.RefreshToken != "r1" {
		t.Errorf("persisted = %+v", saved)
	}
}

func TestNewStoredTokenProviderWithoutTokens(t *testing.T) {
	_, err := NewStoredTokenProvider(StoredTokenConfig{StateDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("err = %v", err)
	}
}

// --- SSE transport ---

type staticProvider struct {
	token       string
	invalidated atomic.Int32
}

func (p *staticProvider) Token() (string, error) { return p.token, nil }
func (p *staticProvider) Invalidate()            { p.invalidated.Add(1) }

func TestSSEJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	tr, err := NewSSEClientTransport(srv.URL, WithTokenProvider(&staticProvider{token: "tok"}))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	msg, ok := tr.ReadLine()
	if !ok {
		t.Fatal("ReadLine closed")
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(msg, &parsed); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if string(parsed["id"]) != "1" {
		t.Errorf("id = %s", parsed["id"])
	}
}

func TestSSEStreamResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{}}\n\n")
		fmt.Fprint(w, ": comment ignored\n")
		fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/x\"}\n\n")
	}))
	defer srv.Close()

	tr, err := NewSSEClientTransport(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	first, ok := tr.ReadLine()
	if !ok || !strings.Contains(string(first), `"id":7`) {
		t.Fatalf("first = %q, %v", first, ok)
	}
	second, ok := tr.ReadLine()
	if !ok || !strings.Contains(string(second), "notifications/x") {
		t.Fatalf("second = %q, %v", second, ok)
	}
}

func TestSSE401RetryOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	provider := &staticProvider{token: "tok"}
	tr, err := NewSSEClientTransport(srv.URL, WithTokenProvider(provider))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); err != nil {
		t.Fatalf("Send after retry: %v", err)
	}
	if provider.invalidated.Load() != 1 {
		t.Errorf("Invalidate calls = %d, want 1", provider.invalidated.Load())
	}
	if calls.Load() != 2 {
		t.Errorf("HTTP calls = %d, want 2", calls.Load())
	}
	if _, ok := tr.ReadLine(); !ok {
		t.Fatal("no message after retry")
	}
}

func TestSSEErrorStatusSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	tr, err := NewSSEClientTransport(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	err = tr.Send([]byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v", err)
	}
}

func TestSSESessionTerminationOnClose(t *testing.T) {
	var deleteSeen atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") == "sess-9" {
				deleteSeen.Store(true)
			}
			return
		}
		w.Header().Set("Mcp-Session-Id", "sess-9")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer srv.Close()

	tr, err := NewSSEClientTransport(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)); err != nil {
		t.Fatal(err)
	}
	tr.ReadLine()
	tr.Close()
	if !deleteSeen.Load() {
		t.Error("session DELETE not sent on Close")
	}
	// Send after close fails.
	if err := tr.Send([]byte(`{}`)); err == nil {
		t.Error("Send after Close succeeded")
	}
}

func TestSSEEmptyEndpoint(t *testing.T) {
	if _, err := NewSSEClientTransport(""); err == nil {
		t.Fatal("empty endpoint accepted")
	}
}
