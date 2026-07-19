package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/slack-mcp-extender/internal/config"
)

// runInitScripted drives runInit with scripted answers, one per prompt.
func runInitScripted(t *testing.T, answers []string) (exit int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := runInit(strings.NewReader(strings.Join(answers, "\n")+"\n"), &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestInitHappyPathEnvSecret(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "exchange")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ws.json")

	exit, stdout, stderr := runInitScripted(t, []string{
		"testws",              // workspace name
		cfgPath,               // config path
		"EXAMPLE_CLIENT_ID",   // client id
		"1",                   // secret storage: env var
		"MY_SECRET_ENV",       // env var name
		"7788",                // callback port
		root,                  // allowed root 1
		"",                    // finish roots
	})
	if exit != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", exit, stderr)
	}

	// File exists, 0600, loads clean.
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", fi.Mode().Perm())
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("generated config fails to load: %v", err)
	}
	if cfg.OAuth.ClientID != "EXAMPLE_CLIENT_ID" || cfg.OAuth.ClientSecretEnv != "MY_SECRET_ENV" || cfg.OAuth.ClientSecret != "" {
		t.Errorf("oauth = %+v", cfg.OAuth)
	}
	if cfg.OAuth.CallbackPort != 7788 {
		t.Errorf("port = %d", cfg.OAuth.CallbackPort)
	}
	if len(cfg.OAuth.Scopes) != len(config.DefaultScopes()) {
		t.Errorf("scopes = %d, want %d", len(cfg.OAuth.Scopes), len(config.DefaultScopes()))
	}
	if len(cfg.AllowedRoots) != 1 || cfg.AllowedRoots[0] != root {
		t.Errorf("roots = %v", cfg.AllowedRoots)
	}
	if cfg.Upstream.URL != config.DefaultUpstreamURL || cfg.OAuth.AuthorizeURL != config.DefaultAuthorizeURL {
		t.Errorf("endpoints = %+v", cfg)
	}

	// Next-steps output: login command + Claude Desktop snippet.
	for _, want := range []string{"login --config " + cfgPath, "mcpServers", "slack-testws"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q", want)
		}
	}
	// A fully-provisioned env-secret config has no warnings.
	if strings.Contains(stdout, "warning:") {
		t.Errorf("unexpected warnings in: %s", stdout)
	}
}

func TestInitLiteralSecretWarns(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "lit.json")
	exit, stdout, _ := runInitScripted(t, []string{
		"litws", cfgPath, "CID",
		"2",              // literal secret
		"LITERAL_VALUE",  // the secret
		"",               // port default
		"",               // no roots
	})
	if exit != exitOK {
		t.Fatalf("exit = %d", exit)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OAuth.ClientSecret != "LITERAL_VALUE" || cfg.OAuth.ClientSecretEnv != "" {
		t.Errorf("oauth = %+v", cfg.OAuth)
	}
	// Literal secret and empty roots both draw warnings.
	if !strings.Contains(stdout, "client_secret_env") || !strings.Contains(stdout, "allowed_roots") {
		t.Errorf("expected warnings, got: %s", stdout)
	}
}

func TestInitRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "exists.json")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, _, stderr := runInitScripted(t, []string{"x", cfgPath})
	if exit != exitError || !strings.Contains(stderr, "refusing to overwrite") {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr)
	}
}

func TestInitRootHandling(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "roots.json")
	existing := filepath.Join(dir, "yes")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	exit, _, _ := runInitScripted(t, []string{
		"rootws", cfgPath, "CID", "1", "ENV_NAME", "",
		"relative/nope",                  // skipped: not absolute
		file,                             // skipped: not a directory
		filepath.Join(dir, "missing"),    // does not exist...
		"N",                              // ...decline keeping it
		filepath.Join(dir, "later"),      // does not exist...
		"y",                              // ...keep anyway
		existing,                         // exists
		"",                               // finish
	})
	if exit != exitOK {
		t.Fatalf("exit = %d", exit)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "later"), existing}
	if len(cfg.AllowedRoots) != 2 || cfg.AllowedRoots[0] != want[0] || cfg.AllowedRoots[1] != want[1] {
		t.Errorf("roots = %v, want %v", cfg.AllowedRoots, want)
	}
}

func TestInitInvalidPort(t *testing.T) {
	dir := t.TempDir()
	exit, _, stderr := runInitScripted(t, []string{
		"pws", filepath.Join(dir, "p.json"), "CID", "1", "E", "notaport",
	})
	if exit != exitError || !strings.Contains(stderr, "invalid port") {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr)
	}
}

func TestExpandTilde(t *testing.T) {
	if got := expandTilde("~/x/y", "/home/u"); got != "/home/u/x/y" {
		t.Errorf("got %q", got)
	}
	if got := expandTilde("/abs/x", "/home/u"); got != "/abs/x" {
		t.Errorf("got %q", got)
	}
	if got := expandTilde("~", "/home/u"); got != "/home/u" {
		t.Errorf("got %q", got)
	}
}
