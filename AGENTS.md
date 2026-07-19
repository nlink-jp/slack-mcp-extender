# AGENTS.md — slack-mcp-extender

## What this is

A per-workspace MCP proxy (CLI + stdio MCP server) that **transparently
forwards Claude's official Slack MCP** (`mcp.slack.com/mcp`, SSE) while
**injecting file-attachment upload tools** — the one capability the official
connector lacks. Two injected tools (`upload_file` for root-message
attachments, `upload_file_to_thread` for thread replies) run the Slack
external upload 3-step (`files.getUploadURLExternal` → POST → 
`files.completeUploadExternal`) under the **same user token** the proxy holds
(single OAuth session; app shared with scli; user scope `files:write`).
File access is confined to operator-configured `allowed_roots`
(canonicalized containment, deny-by-default, hidden-component rejection,
size cap) because the tool is otherwise an exfiltration primitive.
References the mcp-guardian skeleton (proxy/SSE/OAuth/tools-merge) but is a
full new build with no governance machinery. Zero external dependencies.

**Status: scaffold.** CLI dispatch and stubs only; proxy/OAuth/upload are
Phase 1 development (see the RFP under `docs/`).

## Build & test

```bash
make build      # → dist/slack-mcp-extender  (NEVER `go build` directly)
make test       # go test -race -cover ./...
make check      # lint + test + build-all
make build-all  # cross-compile linux/{amd64,arm64}, darwin/arm64, windows/amd64
```

Go 1.25+. **No external dependencies** — standard library only.
Module path: `github.com/nlink-jp/slack-mcp-extender`.

## Structure

```
main.go              package main; version via -ldflags; calls app.Run
internal/app/        CLI dispatch (mcp / init / login / config / version)
scripts/             org codesign/notarize/brew templates (copied verbatim)
docs/{en,ja}/        RFP and future docs (ja = primary, *.ja.md suffix)
```

Planned Phase 1 packages: `internal/jsonrpc` (framing), `internal/transport`
(SSE upstream + OAuth authcode/token store), `internal/proxy` (pipe +
tools/list merge + tools/call routing), `internal/upload` (external upload
3-step), `internal/containment` (path policy), `internal/config`
(per-workspace config).

## Gotchas

- Slack user tokens are **workspace-scoped**: one config + one Claude Desktop
  MCP registration per workspace. No multiplexing in one process (tool-name
  collisions would break transparency).
- `workspace_dir` is an **agent-supplied tool argument** (cowork owns its own
  session directory; a config-fixed dir would be inoperable) — but it is
  untrusted: containment is enforced solely against config-side
  `allowed_roots`.
- Hidden-component rejection applies to path components **below the matched
  allowed_root** only (a root itself may live under a dot directory).
- The OAuth requested scopes must include `files:write`; adding it requires
  one re-consent per workspace, and token rotation can affect scli (shared
  app) if token stores are separate.
- `completeUploadExternal` without `channel_id` orphans the file — channel is
  a required tool argument by design.
