# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.4.1] - 2026-09-21

### Fixed

- **`make verify-release` now fails closed.** Its last block chained unzip, the
  packaged binary's `--version` and `spctl` with `&&` and ended the whole chain
  in `|| true`, so a zip that did not unpack or a binary that did not run exited
  0 and the upload proceeded. Each step is now judged on its own, the packaged
  binary's `--version` must contain the tag being released, and only the
  informational `spctl` line may be ignored. Matches the org template
  (CONVENTIONS.md §Code Signing → Verifying a release).

### Security

- **The credential floor's list is now held by a test, not only its scenario.**
  The floor that refuses a credential file named inside an accepted work
  directory (0.4.0) was pinned by the case it was built for —
  `~/.config` with `gcloud/credentials.db` under it — plus this server's own
  state directory. Every other entry of the list (`~/.ssh`, `~/.aws`,
  `~/.gnupg`, `~/.config/{gem-agent,lagent}`, `~/.claude`, `~/.codex`,
  `~/Library/Keychains`) was unexercised for a file, so one removed in passing
  would have taken no test with it. A table now refuses a file under each, and
  a second test holds the implementation's list to the expected one so an entry
  added or dropped fails. No behaviour change: the floor itself is unchanged.

## [0.4.0] - 2026-09-21

### Security

- **The credential denial list is now applied to the file a call names, not
  only to the `work_dir` it was resolved under.** ADR-021 §7 states the list
  is "a floor, not a boundary", and a floor tested only against the directory
  argument is stepped over by naming the directory one level above a
  credential one: `~/.config` is not itself a denied tree, so it was accepted
  as a work directory, and `ext_file_upload` would then post
  `gcloud/credentials.db` under it to a Slack channel. The same shape covered
  `~/Library` above `Library/Keychains`, and the parent of the state
  directory above `tokens.json` — the work_dir check refuses the state
  directory, but not its parent. `workdir.DeniedPath` exports the one list and
  `containment.Policy` applies it as stage 2 of `Resolve`, ahead of
  containment so the refusal names the credential rather than the root it
  escaped, and to the joined target in `ResolveNewFile` so a caller-named
  download destination cannot land on a sensitive path either. Symlinks are
  resolved before the comparison and both spellings are checked, so a link in
  an innocuous directory cannot stand in for its target — including a link
  whose target stays *inside* the work_dir, which containment cannot see at
  all. Refusals are the existing structured `path_denied` error with
  `reason: sensitive_path`, they name the path, and they are audited. The
  `work_dir` validation itself is unchanged: this is an additional check at
  the point of use, not a replacement.
- **`work_dir` may no longer be this server's own config or state
  directory.** Organization ADR-021 §4 closes the work-directory checks with
  "not a system location … and not the server's own config or state
  directory" → `work_dir_denied`, and this server's `workdir` package had no
  hook for it at all. The consequence here is sharper than elsewhere in the
  fleet, because ADR-021 §7's one exception is this server: an upload is
  taken from inside `work_dir` and **leaves the machine**. A caller could
  pass `work_dir = <state dir>` and have `ext_file_upload` post
  `tokens.json` — the OAuth access and refresh tokens — into a Slack
  channel, or the workspace config with its client secret, on a model's
  say-so. Now refused, subdirectories included: the state directory, the
  directory the config was loaded from, and `~/.config/slack-mcp-extender`
  (so other workspaces' configs are covered too, not only this one's).
- `workdir.Resolve` and `workdir.Validate` take the list as a parameter
  rather than reading a package variable set at startup: a variable has an
  initialization order, and a call that arrived before it was set would
  resolve with nothing denied and look exactly like a call that was allowed.
  The existing system-location, home-directory and credential checks are
  unchanged.

### Fixed

- **A failed write to the transfer audit log is now reported.** The log records
  that a file left this machine; its close was deferred and its error discarded,
  so a full disk could lose the line that had just been written.

## [0.3.2] - 2026-09-14

### Added

- `TestEveryRequiredNameIsDeclared` — a schema that lists a name in `required`
  without declaring it in `properties` makes a strict client refuse the whole
  tool list (Vertex AI: "schema at top-level requires unspecified property").
  data-toolbox-mcp shipped exactly that and broke a session outright; the
  existing contract test checked declared ⇒ required only, so the fleet is
  pinned in both directions now.

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
