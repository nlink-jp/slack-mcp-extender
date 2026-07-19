# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - Unreleased

### Added

- `--config` accepts a bare workspace name (resolved in
  `~/.config/slack-mcp-extender`, `.json` appended automatically) in
  addition to a path.
- Live E2E suite (`make e2e`, build-tag gated): drives the built binary
  against the real Slack MCP — transparency, containment denials (no
  Slack side effects), and an opt-in posting test (root + thread
  attachments with audit verification).
- `init` command: interactive per-workspace config scaffolding — OAuth
  client identity, secret storage (environment variable recommended),
  callback port, and allowed_roots registration — writing a validated
  0600 config and printing the login command plus the Claude Desktop
  registration snippet.
- Phase 1 core: transparent MCP proxy to the official Slack MCP
  (`mcp.slack.com/mcp`, Streamable HTTP/SSE) with two injected tools —
  `upload_file` (root-message attachment) and `upload_file_to_thread`
  (thread-reply attachment) — implemented via the Slack external upload
  3-step under the same user token as the proxy connection.
- Path containment: operator-configured `allowed_roots`, deny-by-default,
  canonicalized checks (containment → regular-file-only → hidden-component
  rejection → size cap), structured `path_denied` errors, and a JSONL
  egress audit log.
- OAuth2 authorization_code login (`login`) with PKCE over an HTTPS
  loopback callback; tokens stored per workspace (0600), refresh-less
  non-expiring Slack tokens supported.
- Per-workspace JSON config (strict decode, 0600, `client_secret_env`)
  with `config show` (redacted) and `config validate` (warnings).
- Project scaffold: CLI dispatch skeleton, org-standard Makefile (signed
  builds, notarized packages, Homebrew tap generation), documentation
  set, and the RFP
  (`docs/ja/slack-mcp-extender-rfp.ja.md`, `docs/en/slack-mcp-extender-rfp.md`).
