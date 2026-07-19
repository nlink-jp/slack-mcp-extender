// Package app implements the slack-mcp-extender command-line interface:
// subcommand dispatch for the mcp / init / login / config lifecycle. Core
// logic (proxy, OAuth, upload, containment) lives in dedicated packages;
// this package is the thin I/O shell around them.
package app

import (
	"fmt"
	"io"
)

// Exit codes.
const (
	exitOK    = 0 // success
	exitError = 2 // usage / validation / operational error
)

// Run dispatches a subcommand and returns a process exit code.
func Run(args []string, version string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitError
	}
	cmd, rest := args[0], args[1:]
	_ = rest
	switch cmd {
	case "mcp":
		return notImplemented(stderr, "mcp")
	case "init":
		return notImplemented(stderr, "init")
	case "login":
		return notImplemented(stderr, "login")
	case "config":
		return notImplemented(stderr, "config")
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "slack-mcp-extender "+version)
		fmt.Fprintln(stdout, "Transparent proxy for the official Slack MCP (mcp.slack.com/mcp) with injected file-attachment upload tools.")
		return exitOK
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", cmd)
		usage(stderr)
		return exitError
	}
}

// notImplemented reports a scaffold-stage stub subcommand.
func notImplemented(w io.Writer, cmd string) int {
	fmt.Fprintf(w, "slack-mcp-extender %s: not implemented yet (scaffold stage)\n", cmd)
	return exitError
}

func usage(w io.Writer) {
	fmt.Fprint(w, `slack-mcp-extender — transparent proxy for the official Slack MCP with file-attachment upload tools

Usage:
  slack-mcp-extender <command> [flags]

Commands:
  mcp --config <path>      Run the stdio MCP server (transparent proxy + injected upload tools)
  init                     Interactively create a workspace config (allowed_roots, OAuth client)
                           and print the Claude Desktop registration snippet
  login --config <path>    Run the OAuth authorization_code flow and store tokens
                           (once per workspace)
  config <show|validate>   Show / validate a workspace config
  version                  Print the version

Slack user tokens are workspace-scoped: create one config per workspace and
register one MCP server per workspace in Claude Desktop.
`)
}
