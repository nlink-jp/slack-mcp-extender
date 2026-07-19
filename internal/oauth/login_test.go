package oauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/slack-mcp-extender/internal/config"
	"github.com/nlink-jp/slack-mcp-extender/internal/transport"
)

// testConfig builds a workspace config wired to a fake token endpoint.
// callback_scheme http keeps the test browserless-friendly (the driver GETs
// the callback URL directly).
func testConfig(t *testing.T, tokenURL string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	body := fmt.Sprintf(`{
	  "oauth": {
	    "authorize_url": "https://slack.example.invalid/authorize",
	    "token_url": %q,
	    "client_id": "EXAMPLE_CLIENT_ID",
	    "client_secret": "EXAMPLE_SECRET",
	    "scopes": ["chat:write", "files:write"],
	    "callback_scheme": "http"
	  }
	}`, tokenURL)
	path := filepath.Join(dir, "ws.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// driveCallback simulates the browser: parse the authorize URL the flow
// produced, then hit the local callback with the given query values.
func driveCallback(t *testing.T, authURL string, override url.Values) {
	t.Helper()
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Errorf("authorize URL unparsable: %v", err)
		return
	}
	q := parsed.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		t.Errorf("redirect_uri unparsable: %v", err)
		return
	}
	cb := url.Values{"state": {q.Get("state")}}
	for k, vs := range override {
		cb[k] = vs
	}
	redirect.RawQuery = cb.Encode()
	// The GET's response body is irrelevant to the assertions (Login's
	// return value is what matters) and racing the post-error server
	// shutdown can surface EOF here — tolerate it.
	resp, err := http.Get(redirect.String())
	if err != nil {
		t.Logf("callback GET (tolerated): %v", err)
		return
	}
	resp.Body.Close()
}

func TestLoginHappyPath(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		f := r.PostForm
		if f.Get("grant_type") != "authorization_code" || f.Get("code") != "test-code" {
			t.Errorf("form = %v", f)
		}
		if f.Get("client_id") != "EXAMPLE_CLIENT_ID" || f.Get("client_secret") != "EXAMPLE_SECRET" {
			t.Errorf("client auth = %v", f)
		}
		if f.Get("code_verifier") == "" || f.Get("redirect_uri") == "" {
			t.Errorf("PKCE/redirect missing: %v", f)
		}
		// Slack-style envelope with a non-rotating user token.
		fmt.Fprint(w, `{"ok":true,"access_token":"EXAMPLE_USER_TOKEN","token_type":"user"}`)
	}))
	defer tokenSrv.Close()

	cfg := testConfig(t, tokenSrv.URL)
	var authorizeURL string
	err := Login(cfg, Options{
		Timeout: 10 * time.Second,
		OpenBrowser: func(u string) {
			authorizeURL = u
			go driveCallback(t, u, url.Values{"code": {"test-code"}})
		},
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// The authorize URL carried the security parameters.
	q, _ := url.Parse(authorizeURL)
	params := q.Query()
	for _, key := range []string{"client_id", "redirect_uri", "state", "code_challenge", "scope"} {
		if params.Get(key) == "" {
			t.Errorf("authorize URL missing %s: %s", key, authorizeURL)
		}
	}
	if params.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", params.Get("code_challenge_method"))
	}
	if got := params.Get("scope"); got != "chat:write files:write" {
		t.Errorf("scope = %q", got)
	}

	// Tokens landed in the state dir with expires_at=0 (non-expiring).
	tokens, err := transport.LoadTokens(cfg.StateDir)
	if err != nil {
		t.Fatalf("LoadTokens: %v", err)
	}
	if tokens.AccessToken != "EXAMPLE_USER_TOKEN" || tokens.ExpiresAt != 0 {
		t.Errorf("tokens = %+v", tokens)
	}
}

func TestLoginStateMismatchRejected(t *testing.T) {
	cfg := testConfig(t, "https://token.example.invalid")
	err := Login(cfg, Options{
		Timeout: 10 * time.Second,
		OpenBrowser: func(u string) {
			go driveCallback(t, u, url.Values{"code": {"x"}, "state": {"forged"}})
		},
	})
	if err == nil || !strings.Contains(err.Error(), "state parameter mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginProviderErrorSurfaced(t *testing.T) {
	cfg := testConfig(t, "https://token.example.invalid")
	err := Login(cfg, Options{
		Timeout: 10 * time.Second,
		OpenBrowser: func(u string) {
			go driveCallback(t, u, url.Values{"error": {"access_denied"}, "error_description": {"user said no"}})
		},
	})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginSlackErrorEnvelope(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error":"invalid_grant_type"}`)
	}))
	defer tokenSrv.Close()

	cfg := testConfig(t, tokenSrv.URL)
	err := Login(cfg, Options{
		Timeout: 10 * time.Second,
		OpenBrowser: func(u string) {
			go driveCallback(t, u, url.Values{"code": {"c"}})
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid_grant_type") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginTimeout(t *testing.T) {
	cfg := testConfig(t, "https://token.example.invalid")
	err := Login(cfg, Options{
		Timeout:     200 * time.Millisecond,
		OpenBrowser: func(string) {}, // nobody completes the flow
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
}

func TestGenerateLoopbackCert(t *testing.T) {
	cert, err := generateLoopbackCert()
	if err != nil {
		t.Fatalf("generateLoopbackCert: %v", err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		t.Fatal("no leaf")
	}
	if len(leaf.DNSNames) == 0 || leaf.DNSNames[0] != "localhost" {
		t.Errorf("DNSNames = %v", leaf.DNSNames)
	}
	if len(leaf.IPAddresses) != 2 {
		t.Errorf("IPAddresses = %v", leaf.IPAddresses)
	}
}
