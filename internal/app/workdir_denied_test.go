package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/slack-mcp-extender/internal/config"
	"github.com/nlink-jp/slack-mcp-extender/internal/transport"
)

// The layer this test observes is the construction of the injected tools:
// buildProxy filling InjectedTools.ServerDirs from the loaded config. It is
// the half internal/proxy cannot see — that package's tests declare a state
// directory themselves, so they would keep passing if the assembled server
// declared none. The state directory is per-workspace and only the config
// knows where it is, which is why the list cannot live in the resolver.
func TestBuildProxyDeclaresTheServersOwnDirs(t *testing.T) {
	path := writeWorkspaceConfig(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.SaveTokens(cfg.StateDir, &transport.StoredTokens{AccessToken: "tok"}); err != nil {
		t.Fatal(err)
	}

	p, err := buildProxy(cfg, strings.NewReader(""), &bytes.Buffer{}, func(string, ...any) {})
	if err != nil {
		t.Fatalf("buildProxy: %v", err)
	}
	defer func() { _ = p.Upstream.Close() }()

	// The token store is the entry that matters: an upload comes from inside
	// work_dir and leaves the machine, so a work_dir here means "send
	// tokens.json to Slack" (ADR-021 §4, §7).
	if !contains(p.Injected.ServerDirs, cfg.StateDir) {
		t.Errorf("ServerDirs = %v, want it to declare the state directory %q",
			p.Injected.ServerDirs, cfg.StateDir)
	}
	// The config file carries the OAuth client secret, so its directory is
	// the server's own too.
	if !contains(p.Injected.ServerDirs, filepath.Dir(path)) {
		t.Errorf("ServerDirs = %v, want it to declare the config directory %q",
			p.Injected.ServerDirs, filepath.Dir(path))
	}
}

func contains(haystack []string, want string) bool {
	for _, got := range haystack {
		if got == want {
			return true
		}
	}
	return false
}
