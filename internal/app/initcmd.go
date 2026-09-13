package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nlink-jp/slack-mcp-extender/internal/config"
)

// runInit interactively scaffolds a per-workspace config: identity of the
// OAuth client, secret storage, callback port, and — the security decision —
// the transfer limits. The containment boundary is not configured here any
// more (ADR-0003). It ends by printing the login
// command and the Claude Desktop registration snippet.
func runInit(stdin io.Reader, stdout, stderr io.Writer) int {
	in := bufio.NewScanner(stdin)
	ask := func(prompt, def string) string {
		if def != "" {
			fmt.Fprintf(stdout, "%s [%s]: ", prompt, def)
		} else {
			fmt.Fprintf(stdout, "%s: ", prompt)
		}
		if !in.Scan() {
			return def
		}
		answer := strings.TrimSpace(in.Text())
		if answer == "" {
			return def
		}
		return answer
	}

	fmt.Fprintln(stdout, "slack-mcp-extender init — one config per Slack workspace")
	fmt.Fprintln(stdout, "(prerequisite: a Slack App created from docs/slack-app-manifest.yaml — see the setup guide)")
	fmt.Fprintln(stdout)

	// Workspace name → default config path and server name.
	name := ask("Workspace name (used for the config filename and the server name)", "myworkspace")

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	defaultDir, err := config.DefaultConfigDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	defaultPath := filepath.Join(defaultDir, name+".json")
	cfgPath := expandTilde(ask("Config file path", defaultPath), home)
	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Fprintf(stderr, "init: %s already exists — refusing to overwrite (remove it first, or pick another path)\n", cfgPath)
		return exitError
	}

	// OAuth client identity.
	var clientID string
	for clientID == "" {
		clientID = ask("Slack App Client ID (Basic Information → App Credentials)", "")
		if clientID == "" {
			fmt.Fprintln(stdout, "Client ID is required.")
		}
	}

	fmt.Fprintln(stdout, "Client secret storage:")
	fmt.Fprintln(stdout, "  1) name of an environment variable holding it (recommended — config stays secret-free)")
	fmt.Fprintln(stdout, "  2) literal value inside the config file (kept 0600)")
	var secretEnv, secretLiteral string
	switch ask("Choose", "1") {
	case "2":
		for secretLiteral == "" {
			secretLiteral = ask("Client secret value (input is echoed — clear your scrollback if that matters)", "")
		}
	default:
		secretEnv = ask("Environment variable NAME", "SLACK_MCP_EXTENDER_CLIENT_SECRET")
	}

	portStr := ask("OAuth callback port (must match the app's redirect URL)", strconv.Itoa(config.DefaultCallbackPort))
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		fmt.Fprintf(stderr, "init: invalid port %q\n", portStr)
		return exitError
	}

	// The containment boundary is no longer set here: it is the work_dir the
	// caller names on every call (ADR-0003). An operator allowlist could not
	// express what it was for — prefix matching has no per-repository
	// granularity, so covering a work root meant naming the home directory,
	// which admits the files the list existed to keep out.
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Uploads are confined to the work_dir the calling agent names on each call,")
	fmt.Fprintln(stdout, "so there is no allowlist to configure here. A file outside that directory")
	fmt.Fprintln(stdout, "cannot be sent to Slack.")

	cfg := config.Config{
		Upstream: config.Upstream{URL: config.DefaultUpstreamURL},
		OAuth: config.OAuth{
			AuthorizeURL:    config.DefaultAuthorizeURL,
			TokenURL:        config.DefaultTokenURL,
			ClientID:        clientID,
			ClientSecret:    secretLiteral,
			ClientSecretEnv: secretEnv,
			Scopes:          config.DefaultScopes(),
			CallbackPort:    port,
		},
		MaxFileSize: config.DefaultMaxFileSize,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if err := os.WriteFile(cfgPath, append(data, '\n'), 0o600); err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	// Round-trip through the real loader so init can never produce a config
	// the other commands reject.
	loaded, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "init: wrote %s but it fails to load: %v\n", cfgPath, err)
		return exitError
	}
	fmt.Fprintf(stdout, "\nWrote %s (0600)\n", cfgPath)
	for _, w := range loaded.Warnings() {
		fmt.Fprintf(stdout, "warning: %s\n", w)
	}

	// Next steps.
	exe, err := os.Executable()
	if err != nil {
		exe = "slack-mcp-extender"
	}
	snippet, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"slack-" + name: map[string]any{
				"command": exe,
				"args":    []string{"mcp", "--config", cfgPath},
			},
		},
	}, "", "  ")
	fmt.Fprintf(stdout, `
Next steps:

1. Authorize this workspace (opens a browser):

   %s login --config %s

2. Register the server in Claude Desktop's MCP settings:

%s

3. Restart Claude Desktop. The Slack connector gains upload_file and
   upload_file_to_thread on top of the official tools.
`, exe, cfgPath, indent(string(snippet), "   "))
	return exitOK
}

// expandTilde resolves a leading ~/ against the home directory.
func expandTilde(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
