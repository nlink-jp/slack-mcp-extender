package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validBody is a minimal valid config document.
const validBody = `{
  "oauth": {
    "authorize_url": "https://slack.com/oauth/v2_user/authorize",
    "token_url": "https://slack.com/api/oauth.v2.user.access",
    "client_id": "EXAMPLE_CLIENT_ID",
    "client_secret_env": "SLACK_MCP_EXTENDER_CLIENT_SECRET",
    "scopes": ["chat:write", "files:write"],
    "callback_port": 7777
  },
  "allowed_roots": ["/tmp"]
}`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ws.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidWithDefaults(t *testing.T) {
	path := writeConfig(t, validBody)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Upstream.URL != DefaultUpstreamURL {
		t.Errorf("upstream default = %q", cfg.Upstream.URL)
	}
	if cfg.MaxFileSize != DefaultMaxFileSize {
		t.Errorf("max_file_size default = %d", cfg.MaxFileSize)
	}
	if cfg.TimeoutMs != DefaultTimeoutMs {
		t.Errorf("timeout_ms default = %d", cfg.TimeoutMs)
	}
	if cfg.OAuth.CallbackScheme != "https" {
		t.Errorf("callback_scheme default = %q", cfg.OAuth.CallbackScheme)
	}
	// State dir defaults to <basename>.state next to the config.
	if want := filepath.Join(filepath.Dir(path), "ws.state"); cfg.StateDir != want {
		t.Errorf("state_dir = %q, want %q", cfg.StateDir, want)
	}
}

func TestLoadRejectsLooseMode(t *testing.T) {
	path := writeConfig(t, validBody)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("world-readable config accepted: %v", err)
	}
}

func TestLoadStrictDecodeRejectsUnknownField(t *testing.T) {
	path := writeConfig(t, `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"]},"allowd_roots":["/tmp"]}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("typo field accepted: %v", err)
	}
}

func TestLoadRejectsTrailingData(t *testing.T) {
	path := writeConfig(t, validBody+"\n{}")
	if _, err := Load(path); err == nil {
		t.Fatal("trailing data accepted")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestValidateErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"bad upstream", `{"upstream":{"url":"not a url"},"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"]}}`, "upstream.url"},
		{"ftp upstream", `{"upstream":{"url":"ftp://x"},"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"]}}`, "upstream.url"},
		{"missing oauth", `{"oauth":{"scopes":["s"]}}`, "required"},
		{"both secrets", `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","client_secret":"x","client_secret_env":"Y","scopes":["s"]}}`, "mutually exclusive"},
		{"empty scopes", `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":[]}}`, "scopes"},
		{"bad callback scheme", `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"],"callback_scheme":"gopher"}}`, "callback_scheme"},
		{"bad auth method", `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"],"client_auth_method":"magic"}}`, "client_auth_method"},
		{"relative root", `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"]},"allowed_roots":["relative"]}`, "absolute"},
		{"negative size", `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"]},"max_file_size":-1}`, "max_file_size"},
		{"negative timeout", `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","scopes":["s"]},"timeout_ms":-5}`, "timeout_ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.body)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestResolveClientSecret(t *testing.T) {
	o := &OAuth{ClientSecret: "literal"}
	if got := o.ResolveClientSecret(); got != "literal" {
		t.Errorf("literal: %q", got)
	}
	t.Setenv("SLACK_MCP_EXTENDER_TEST_SECRET", "from-env")
	o = &OAuth{ClientSecretEnv: "SLACK_MCP_EXTENDER_TEST_SECRET"}
	if got := o.ResolveClientSecret(); got != "from-env" {
		t.Errorf("env: %q", got)
	}
	o = &OAuth{}
	if got := o.ResolveClientSecret(); got != "" {
		t.Errorf("none: %q", got)
	}
}

func TestWarnings(t *testing.T) {
	path := writeConfig(t, `{"oauth":{"authorize_url":"a","token_url":"t","client_id":"c","client_secret":"x","scopes":["chat:write"]}}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	warnings := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{"files:write", "allowed_roots", "client_secret_env"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warnings missing %q: %s", want, warnings)
		}
	}

	// A fully-provisioned config warns about nothing.
	path = writeConfig(t, validBody)
	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("unexpected warnings: %v", w)
	}
}

func TestRedacted(t *testing.T) {
	cfg := &Config{OAuth: OAuth{ClientSecret: "super-secret"}}
	red := cfg.Redacted()
	if red.OAuth.ClientSecret != "[redacted]" {
		t.Errorf("secret not redacted: %q", red.OAuth.ClientSecret)
	}
	if cfg.OAuth.ClientSecret != "super-secret" {
		t.Errorf("original mutated: %q", cfg.OAuth.ClientSecret)
	}
	// Empty secret stays empty (not replaced by the marker).
	empty := (&Config{}).Redacted()
	if empty.OAuth.ClientSecret != "" {
		t.Errorf("empty secret became %q", empty.OAuth.ClientSecret)
	}
}
