// Command slack-mcp-extender is a per-workspace MCP proxy that transparently
// forwards Claude's official Slack MCP (mcp.slack.com/mcp, SSE) while
// injecting the one capability it lacks: posting file attachments. Two
// injected tools upload a local file and share it as a root message
// (upload_file) or a thread reply (upload_file_to_thread) using the Slack
// external upload 3-step, under the same user token the proxy already holds —
// a single OAuth session for both forwarding and upload. File access is
// confined to the work_dir each call names (canonicalized containment,
// deny-by-default, hidden path components rejected): the tool relays
// untrusted Slack content, reads local files, and sends data out, so every
// file argument is resolved inside that one directory. No third-party
// dependencies: the standard library and nlink-jp/pathguard, this
// organization's own module.
package main

import (
	"os"

	"github.com/nlink-jp/slack-mcp-extender/internal/app"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(app.Run(os.Args[1:], version, os.Stdin, os.Stdout, os.Stderr))
}
