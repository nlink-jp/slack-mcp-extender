# AGENTS.md — slack-mcp-extender

## What this is

A per-workspace MCP proxy (CLI + stdio MCP server) that **transparently
forwards Claude's official Slack MCP** (`mcp.slack.com/mcp`, SSE) while
**injecting file-attachment upload tools** — the one capability the official
connector lacks. Three injected tools in the ext_ namespace (`ext_file_upload` for
root-message attachments, `ext_file_upload_to_thread` for thread
replies, `ext_file_download` for saving Slack files to disk) run the Slack
external upload 3-step (`files.getUploadURLExternal` → POST → 
`files.completeUploadExternal`) under the **same user token** the proxy holds
(single OAuth session; app shared with scli; user scope `files:write`).
File access is confined to the `work_dir` each call names (ADR-0003)
(canonicalized containment, deny-by-default, credential floor,
hidden-component rejection, size cap) because the tool is otherwise an
exfiltration primitive.
References the mcp-guardian skeleton (proxy/SSE/OAuth/tools-merge) but is a
full new build with no governance machinery. No third-party dependencies
(only nlink-jp/pathguard, itself standard library only).

**Status: released** (v0.1.0). Proxy, OAuth login, injected tools,
containment, interactive `init`; ~90% coverage per package;
real-workspace E2E codified in `e2e/`. Design record: the RFP under
`docs/`.

## Build & test

```bash
make build      # → dist/slack-mcp-extender  (NEVER `go build` directly)
make test       # go test -race -cover ./...  (offline; mocked upstream)
make test-linux # same suite on Linux (container)
make check      # lint + test + build-all
make build-all  # cross-compile linux/{amd64,arm64}, darwin/arm64, windows/amd64
make e2e        # LIVE tests vs the real Slack MCP (see below)
make verify-release  # gate: .notarized marker + freshness (run before upload)
```

Live E2E (`make e2e`, `//go:build e2e` in `e2e/`): requires
`SLACK_MCP_EXTENDER_E2E_CONFIG` pointing at a logged-in workspace config;
runs transparency + containment-denial tests (no Slack side effects).
Additionally set `SLACK_MCP_EXTENDER_E2E_CHANNEL` (channel ID) to run the
posting test (real root + thread attachments).

Go 1.25+. **No third-party dependencies** — the standard library and
nlink-jp/pathguard only.
Module path: `github.com/nlink-jp/slack-mcp-extender`.

## Structure

```
main.go                  package main; version via -ldflags; calls app.Run
internal/app/            CLI dispatch + command wiring (buildProxy)
internal/jsonrpc/        JSON-RPC types + raw-preserving tools/list merge
internal/containment/    path policy (6-stage canonical checks) — the
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

- **`make lint` is green, and every unchecked return says why.** `.golangci.yml`
  excludes only `fmt.Fprint*` (writes to the CLI's own streams); every other
  deliberate discard is `_ =` with the reason beside it. Two were not
  deliberate: the audit log's `Close` was deferred and its error thrown away —
  that record is the evidence a file left the machine, so it is checked now —
  and the download's temp-file cleanup discarded silently. 37 findings stood
  before 2026-09-21.

- Slack user tokens are **workspace-scoped**: one config + one Claude Desktop
  MCP registration per workspace. No multiplexing in one process (tool-name
  collisions would break transparency).
- `work_dir` is an **agent-supplied tool argument, required on every injected
  tool** (the calling agent owns its session directory; a config-fixed one
  would be inoperable). It is untrusted until validated — absolute, existing,
  writable, not a system or credential location, and not one of this
  server's own config or state directories — and then it *is* the
  containment root: uploads come from inside it, downloads land in it.
- **`workdir.Resolve`/`Validate` take the server's own directories as a
  parameter, not a package variable.** A variable has an initialization
  order, and a call that arrived before it was set would resolve with
  nothing denied and look exactly like a call that was allowed. The list
  comes from `config.Config.ServerOwnedDirs` — the state directory
  (`tokens.json`, the audit log), the directory the config was loaded from
  (the config carries the OAuth client secret), and `DefaultConfigDir`
  (where bare `--config <name>` resolves, so other workspaces' configs are
  covered too) — is carried on `proxy.InjectedTools.ServerDirs`, filled once
  in `buildProxy`, and reaches the resolver through `policyFor`, the single
  place a work directory is resolved. This matters more here than anywhere
  else in the fleet: ADR-021 §7's one exception is that an upload leaves the
  machine, so a `work_dir` in the state directory means "send tokens.json to
  Slack". Two tests pin the two halves:
  `proxy.TestWorkDirRefusesServerStateDir` (the tools pass the list) and
  `app.TestBuildProxyDeclaresTheServersOwnDirs` (the assembled server fills
  it).
- **The credential list runs twice: on `work_dir`, and on every path a call
  names inside it.** ADR-021 §7 calls the list a floor, not a boundary, and a
  floor applied only to the directory argument is stepped over by naming the
  directory one level above a credential one — `~/.config` is not itself a
  denied tree, so it passed `workdir.Validate`, and `gcloud/credentials.db`
  under it reached `ext_file_upload` unexamined. The same holds for
  `~/Library` above `Library/Keychains`, and for the parent of the state
  directory above `tokens.json`. The list and its comparison are
  nlink-jp/pathguard's (ADR-0004; do not grow a second one). The containment
  policy applies `workdir.UploadDenied` (Outbound policy) to an upload's
  source in `Resolve` and `workdir.DownloadDenied` (Local policy) to a
  download's target in `ResolveNewFile`, in both **right after resolving and
  ahead of containment**, so the refusal names the credential rather than
  the root it escaped. Both get the path as named as well as resolved
  (pathguard follows every link hop, so a chain through a credential
  directory is seen), and on the path as named alone when it does not
  resolve. **Whether a path exists never changes the answer**: nothing that
  depends on what exists (is it a directory, is it a regular file, is
  something already there) runs before the floor and containment, and a path
  that does not resolve is placed by its deepest existing ancestor before it
  is called `not_found` (`landing`) — a caller must not learn which secrets
  exist, inside or outside the work directory
  (`TestExistenceIsNotRevealed`). After containment, `workdir.DirDenied`
  (pathguard's `CheckBeneath`) refuses a file whose directory is a system
  directory or the home directory itself, which the file policies leave out
  by design; the credential and server places it would also apply are left
  to the file policies, or a Python venv named `.env` would be refused. On
  upload, a `.env` or a key under a `.ssh` directory anywhere is refused as
  `sensitive_path` before the hidden-component rule; on download only the
  real places under the home are (a download named `id_rsa`, or into
  `evidence/.../.ssh/`, is ordinary). Reason code: `sensitive_path`, with
  pathguard's own reason in `details.floor_reason` (`sensitive_path`,
  `server_dir`, `system_dir`, `home_dir`, `unresolvable_path`,
  `home_unknown`, `unconfigured`) — the vocabulary `work_dir_denied` carries.
  Two of the cases are only reachable through this stage: a symlink whose
  target is sensitive but still inside the work_dir, and a destination path
  inside an accepted work_dir. Tests:
  `proxy.TestUploadRefusesACredentialFileUnderAnAcceptedWorkDir` and the rest
  of `internal/proxy/sensitive_path_test.go` (home directory redirected with
  `t.Setenv`; pathguard also protects the account's real home, so the tests
  stat the operator's real credential directories, read-only), the direction
  split and the rest in `internal/containment/pathguard_floor_test.go`, and
  the serverDirs mechanics in `internal/containment`. The **list** itself is
  pathguard's and is tested there; `check-org.sh` holds it equal to the one
  gem-agent and lagent declare.
- Hidden-component rejection applies to path components **below the work_dir**
  only (the work directory itself may live under a dot directory).
- `.env` **is** on the credential floor (pathguard; ADR-0004): it is refused
  as `sensitive_path` even with `allow_hidden=true`, because with the opt-out
  set it could otherwise be uploaded. Its templates (`.env.example`,
  `.env.sample`, `.env.template`, `.env.dist`) are not.
- `config.Config.ServerOwnedDirs` hands pathguard absolute paths: a relative
  `state_dir` or `$HOME` would otherwise make every call fail as
  `unconfigured`, blaming the caller's `work_dir`.
- The OAuth requested scopes must include `files:write`; adding it requires
  one re-consent per workspace, and token rotation can affect scli (shared
  app) if token stores are separate.
- `completeUploadExternal` without `channel_id` orphans the file — channel is
  a required tool argument by design.
