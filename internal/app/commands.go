package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nlink-jp/slack-mcp-extender/internal/config"
	"github.com/nlink-jp/slack-mcp-extender/internal/containment"
	"github.com/nlink-jp/slack-mcp-extender/internal/oauth"
	"github.com/nlink-jp/slack-mcp-extender/internal/proxy"
	"github.com/nlink-jp/slack-mcp-extender/internal/transport"
	"github.com/nlink-jp/slack-mcp-extender/internal/upload"
)

// parseConfigFlag parses a --config flag from args.
func parseConfigFlag(name string, args []string, stderr io.Writer) (string, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the workspace config file")
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	if *cfgPath == "" {
		fmt.Fprintf(stderr, "%s: --config is required (one config per Slack workspace)\n", name)
		return "", false
	}
	return *cfgPath, true
}

// runMCP starts the stdio MCP server: transparent proxy + injected tools.
func runMCP(args []string, stdout, stderr io.Writer) int {
	cfgPath, ok := parseConfigFlag("mcp", args, stderr)
	if !ok {
		return exitError
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	logf := func(format string, a ...any) { fmt.Fprintf(stderr, format, a...) }
	p, err := buildProxy(cfg, os.Stdin, stdout, logf)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	defer p.Upstream.Close()

	logf("slack-mcp-extender: proxy started (upstream=%s, allowed_roots=%d, state=%s)\n",
		cfg.Upstream.URL, len(cfg.AllowedRoots), cfg.StateDir)
	if err := p.Run(); err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	return exitOK
}

// buildProxy assembles policy, tokens, transport, and injected tools from a
// loaded config. Split from runMCP for testability.
func buildProxy(cfg *config.Config, in io.Reader, out io.Writer, logf func(string, ...any)) (*proxy.Proxy, error) {
	policy, err := containment.NewPolicy(cfg.AllowedRoots, cfg.AllowHidden, cfg.MaxFileSize)
	if err != nil {
		return nil, fmt.Errorf("containment policy: %w", err)
	}

	tokens, err := transport.NewStoredTokenProvider(transport.StoredTokenConfig{
		StateDir:         cfg.StateDir,
		TokenURL:         cfg.OAuth.TokenURL,
		ClientID:         cfg.OAuth.ClientID,
		ClientSecret:     cfg.OAuth.ResolveClientSecret(),
		ClientAuthMethod: cfg.OAuth.ClientAuthMethod,
	})
	if err != nil {
		return nil, err
	}

	up, err := transport.NewSSEClientTransport(cfg.Upstream.URL, transport.WithTokenProvider(tokens))
	if err != nil {
		return nil, fmt.Errorf("upstream transport: %w", err)
	}

	return &proxy.Proxy{
		Upstream: up,
		Injected: &proxy.InjectedTools{
			Policy:   policy,
			Uploader: &upload.Uploader{Tokens: tokens},
			Audit:    &upload.AuditLog{Path: filepath.Join(cfg.StateDir, "audit.jsonl")},
			Logf:     logf,
		},
		In:        in,
		Out:       out,
		TimeoutMs: cfg.TimeoutMs,
		Logf:      logf,
	}, nil
}

// runLogin performs the interactive OAuth flow for one workspace.
func runLogin(args []string, stdout, stderr io.Writer) int {
	cfgPath, ok := parseConfigFlag("login", args, stderr)
	if !ok {
		return exitError
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if err := oauth.Login(cfg, oauth.Options{Out: stdout}); err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	return exitOK
}

// runConfig implements `config show` and `config validate`.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "config: subcommand required (show or validate)")
		return exitError
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show", "validate":
	default:
		fmt.Fprintf(stderr, "config: unknown subcommand %q (want show or validate)\n", sub)
		return exitError
	}

	cfgPath, ok := parseConfigFlag("config "+sub, rest, stderr)
	if !ok {
		return exitError
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	switch sub {
	case "show":
		data, err := json.MarshalIndent(cfg.Redacted(), "", "  ")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitError
		}
		fmt.Fprintln(stdout, string(data))
	case "validate":
		fmt.Fprintf(stdout, "%s: OK\n", cfg.Path)
		for _, w := range cfg.Warnings() {
			fmt.Fprintf(stdout, "warning: %s\n", w)
		}
	}
	return exitOK
}
