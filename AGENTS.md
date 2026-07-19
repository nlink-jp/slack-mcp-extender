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

**Status: Phase 1 core implemented** (proxy, OAuth login, injected tools,
containment; ~90% coverage per package). Not yet E2E-verified against a
real workspace or released. `init` (interactive config scaffolding) is
Phase 2 (see the RFP under `docs/`).

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
main.go                  package main; version via -ldflags; calls app.Run
internal/app/            CLI dispatch + command wiring (buildProxy)
internal/jsonrpc/        JSON-RPC types + raw-preserving tools/list merge
internal/containment/    path policy (5-stage canonical checks) — the
                         highest-priority test suite in the repo
internal/config/         per-workspace JSON config (strict decode, 0600)
internal/transport/      Streamable HTTP/SSE client + token store/refresh
internal/oauth/          authorization_code login (PKCE, HTTPS loopback)
internal/upload/         Slack external upload 3-step + egress audit log
internal/proxy/          transparent pipe + merge + routing + injected tools
scripts/                 org codesign/notarize/brew templates (verbatim)
docs/{en,ja}/            RFP and future docs (ja = primary, *.ja.md suffix)
```

Key transparency detail: upstream tools/list entries are merged as raw
JSON (never decoded into structs), so upstream-only fields (title,
annotations, outputSchema, nextCursor) survive byte-for-byte.

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
