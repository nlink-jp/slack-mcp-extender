# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.3.1] - 2026-09-13

### Fixed

- **The path errors still told the model to register `allowed_roots` in the
  operator config** — a key 0.3.0 removed and now refuses to load. They name
  what actually supplies the root: the `work_dir` of the call. The
  `requires workspace_dir` / `workspace_dir must be absolute` details say
  `work_dir` too, which is the argument that exists.
- `--help` still listed `allowed_roots` among what `init` asks for; `init`
  stopped asking in 0.3.0.
- The setup guide (both languages) documented `allowed_roots` as a config key
  to fill in. It now says the key is gone and what replaced it.
- The package doc described containment as operator-configured.

### Added

- A contract test walking every injected tool: no retired name in a schema or
  a description, and `work_dir` declared and required on all three. ADR-0003
  asked for this test and the release shipped without it.

## [0.3.0] - 2026-09-13

### Changed

- **Breaking: `workspace_dir` is now `work_dir`, and every injected tool
  requires it.** It means the absolute path of a directory the caller can read
  back, and it *is* the containment boundary: an upload comes from inside it, a
  download lands inside it, and `dest_dir` defaults to it. See
  [ADR-0003](docs/en/adr/0003-work-dir-as-containment.md); organization ADR-021.
- **Breaking: `allowed_roots` is removed from the config**, and a config still
  carrying it fails at startup with the replacement named. The list could not
  express what it was for — prefix matching has no per-repository granularity,
  so covering a work root meant naming the home directory, which admits the
  credential files the list existed to keep out. What an operator could write
  was a narrow exchange directory, which in practice meant the file an agent was
  working on could not be sent. `init` no longer asks for roots.
- The work directory is validated before it is trusted: absolute, no `~`, no
  `..`, exists, writable, and never a system location, the home directory
  itself, or a credential directory (`~/.ssh`, `~/.aws`, `~/.gnupg`,
  `~/.config/gcloud`, `~/Library/Keychains`, `~/.claude`, `~/.codex`) — checked
  on both spellings of the path, as given and symlink-resolved.
- A runtime may supply the directory instead of the model: the server reads
  `_meta["jp.nlink/work_dir"]` when the argument is absent. The argument wins.
- Everything ADR-0002 decided stands — canonicalization, hidden-component
  rejection, regular files only, size caps, never overwriting, the audit log.
  Only the reference point moves, from the matched root to the work directory.

### Added

- `work_dir_required`, `work_dir_invalid`, `work_dir_not_found`,
  `work_dir_not_writable`, `work_dir_denied` — the fleet's codes, so a caller
  sees the same vocabulary from every nlink-jp MCP server.

## [0.2.0] - 2026-07-20

### Changed

- **Breaking**: the injected tools moved into an explicit `ext_`
  namespace — `upload_file` → `ext_file_upload`, `upload_file_to_thread`
  → `ext_file_upload_to_thread` (ADR-0001). Extension tools can now
  never collide with — or mask — official `slack_*` tools. No
  compatibility aliases.

### Added

- `ext_file_download`: the symmetric counterpart of upload — save a
  Slack file (`file_id`) to the local disk (ADR-0002). Write-side
  containment mirrors the read side: destination parent must resolve
  into `allowed_roots`, Slack-supplied filenames are sanitized to a bare
  basename (name-only influence), hidden names rejected, existing
  targets never overwritten, size cap enforced against the declared size
  and on the wire, atomic writes, ingress audit entries.
- Live E2E upload→download roundtrip test.

## [0.1.0] - 2026-07-20

### Added

- Default Slack App icon (`docs/slack-app-icon.png`, 1024×1024, SVG
  source included): paperclip + upload arrow on the manifest's
  background color.
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
