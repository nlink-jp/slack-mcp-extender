package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/slack-mcp-extender/internal/config"
	"github.com/nlink-jp/slack-mcp-extender/internal/transport"
)

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		name                                 string
		args                                 []string
		wantExit                             int
		wantStdout                           string // substring expected on stdout ("" = none checked)
		wantStderr                           string // substring expected on stderr ("" = none checked)
	}{
		{"no args shows usage on stderr", nil, exitError, "", "Usage:"},
		{"version", []string{"version"}, exitOK, "slack-mcp-extender v1.2.3", ""},
		{"--version alias", []string{"--version"}, exitOK, "slack-mcp-extender v1.2.3", ""},
		{"help", []string{"help"}, exitOK, "Usage:", ""},
		{"-h alias", []string{"-h"}, exitOK, "Usage:", ""},
		{"unknown command", []string{"bogus"}, exitError, "", `unknown command "bogus"`},
		{"init still stub", []string{"init"}, exitError, "", "not implemented"},
		{"mcp requires config", []string{"mcp"}, exitError, "", "--config is required"},
		{"login requires config", []string{"login"}, exitError, "", "--config is required"},
		{"config requires subcommand", []string{"config"}, exitError, "", "subcommand required"},
		{"config rejects unknown sub", []string{"config", "frobnicate"}, exitError, "", "unknown subcommand"},
		{"config show requires config flag", []string{"config", "show"}, exitError, "", "--config is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := Run(tt.args, "v1.2.3", &stdout, &stderr)
			if got != tt.wantExit {
				t.Errorf("Run(%v) exit = %d, want %d", tt.args, got, tt.wantExit)
			}
			if tt.wantStdout != "" && !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestUsageListsAllCommands(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	for _, cmd := range []string{"mcp", "init", "login", "config", "version"} {
		if !strings.Contains(buf.String(), cmd) {
			t.Errorf("usage output missing command %q", cmd)
		}
	}
}

// writeWorkspaceConfig writes a valid workspace config (0600) with a
// literal secret and one allowed root, returning its path.
func writeWorkspaceConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "exchange")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{
	  "oauth": {
	    "authorize_url": "https://slack.example.invalid/authorize",
	    "token_url": "https://slack.example.invalid/token",
	    "client_id": "EXAMPLE_CLIENT_ID",
	    "client_secret": "EXAMPLE_SECRET",
	    "scopes": ["chat:write", "files:write"]
	  },
	  "allowed_roots": [%q]
	}`, root)
	path := filepath.Join(dir, "ws.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigShowRedactsSecret(t *testing.T) {
	path := writeWorkspaceConfig(t)
	var stdout, stderr bytes.Buffer
	if got := Run([]string{"config", "show", "--config", path}, "v", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", got, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "EXAMPLE_SECRET") {
		t.Error("literal secret leaked in config show")
	}
	if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "EXAMPLE_CLIENT_ID") {
		t.Errorf("show output = %s", out)
	}
}

func TestConfigValidateReportsWarnings(t *testing.T) {
	path := writeWorkspaceConfig(t)
	var stdout, stderr bytes.Buffer
	if got := Run([]string{"config", "validate", "--config", path}, "v", &stdout, &stderr); got != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", got, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "OK") {
		t.Errorf("validate output = %s", out)
	}
	// The literal secret draws a warning.
	if !strings.Contains(out, "client_secret_env") {
		t.Errorf("expected secret warning, got: %s", out)
	}
}

func TestConfigLoadErrorSurfaced(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := Run([]string{"config", "validate", "--config", filepath.Join(t.TempDir(), "missing.json")}, "v", &stdout, &stderr)
	if got != exitError || stderr.Len() == 0 {
		t.Fatalf("exit = %d, stderr = %q", got, stderr.String())
	}
}

func TestBuildProxyWithStoredTokens(t *testing.T) {
	path := writeWorkspaceConfig(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.SaveTokens(cfg.StateDir, &transport.StoredTokens{AccessToken: "tok"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	p, err := buildProxy(cfg, strings.NewReader(""), &out, func(string, ...any) {})
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	defer p.Upstream.Close()
	if p.TimeoutMs != config.DefaultTimeoutMs {
		t.Errorf("TimeoutMs = %d", p.TimeoutMs)
	}
	if p.Injected == nil || p.Injected.Audit.Path != filepath.Join(cfg.StateDir, "audit.jsonl") {
		t.Errorf("injected = %+v", p.Injected)
	}
}

func TestBuildProxyWithoutTokensExplains(t *testing.T) {
	path := writeWorkspaceConfig(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildProxy(cfg, strings.NewReader(""), &bytes.Buffer{}, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("err = %v, want login hint", err)
	}
}

func TestBuildProxyBadRootExplains(t *testing.T) {
	// A config whose allowed root no longer exists must fail at startup
	// with a containment error, not silently narrow the policy.
	dir := t.TempDir()
	body := fmt.Sprintf(`{
	  "oauth": {"authorize_url": "a", "token_url": "t", "client_id": "c", "scopes": ["files:write"]},
	  "allowed_roots": [%q]
	}`, filepath.Join(dir, "gone"))
	path := filepath.Join(dir, "ws.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildProxy(cfg, strings.NewReader(""), &bytes.Buffer{}, func(string, ...any) {})
	if err == nil || !strings.Contains(err.Error(), "containment policy") {
		t.Fatalf("err = %v", err)
	}
}
