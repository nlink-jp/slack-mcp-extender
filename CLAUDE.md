# CLAUDE.md — slack-mcp-extender

Project-specific rules for AI agents. Org-wide rules (CONVENTIONS.md, the
workspace CLAUDE.md) apply on top of these.

## Non-negotiable design invariants

- **Transparency**: upstream tools, notifications, and responses pass through
  unmodified. No tool-name rewriting. Injected tools are added at the
  tools/list merge only.
- **Single user token**: proxy forwarding and uploads share one OAuth session.
  Never introduce a second credential path.
- **Containment is the caller's `work_dir`, named on every call** (ADR-0003).
  It is validated before it is trusted — absolute, existing, writable, and
  never a system location, the home directory itself, a credential
  directory, or one of **this server's own config and state directories**
  (`config.Config.ServerOwnedDirs`, passed to `workdir.Resolve` on every
  call: the state directory holds `tokens.json` and an upload leaves the
  machine) — and then it is the *only* root: an upload comes from inside it,
  a download lands inside it, and nothing outside it can be reached. Never
  widen it from Slack-derived values, and never add a second source of roots.
- **Path checks run on the placed path** — where it is or would be, every
  link followed, a dangling one by its target (`workdir.Where`; for a path
  that exists, what EvalSymlinks returns) — with the path as named passed to
  the floor alongside. Both directions keep one order — place → credential
  floor → containment in the work_dir → the directory's check (a system
  directory or the home directory itself) → everything that depends on what
  exists there — so **whether a path exists never changes the answer**,
  message and details included (existence is checked at the place, never
  re-walked from the spelling; the one known exception is a hard link to a
  credential file planted outside the work_dir). After those: an upload (`Policy.Resolve`) —
  exists (EvalSymlinks; judged again if it resolves elsewhere) →
  regular-file-only → hidden-component rejection (relative to the work_dir)
  → size cap; a download (`Policy.ResolveNewFile`) — exists and is a
  directory → hidden-component rejection → nothing there yet. Do not reorder
  checks to run on raw input strings, do not put an existence-dependent check
  before the floor, and do not give a path that does not resolve a code path
  of its own.
- **The credential floor is applied to the file, not only to the work_dir**
  (ADR-021 §7 — the list is a floor, not a boundary). The judgement is
  nlink-jp/pathguard's (ADR-0004): `containment.Policy` consults
  `workdir.UploadDenied` (Outbound policy — an upload leaves the machine, so a
  credential name is refused wherever it sits) for the resolved upload source
  and `workdir.DownloadDenied` (Local policy) for the joined download target.
  An accepted work directory does not make its contents safe to send: never
  let a new tool reach a path without going through `Policy.Resolve` /
  `Policy.ResolveNewFile`, and never introduce a list or a name comparison of
  its own.
- **No third-party Go dependencies** — the standard library and modules of
  the nlink-jp organization that hold the same rule (today nlink-jp/pathguard)
  only.
- **swrite stays untouched**: bot-identity uploads belong to swrite, not here.

## Secrets

- Never commit clientId/clientSecret, tokens, or workspace-specific values.
  Config examples use placeholders only.
- Token stores and state directories are per-workspace and live outside the
  repository.

## Build & test

- `make build` (never `go build` directly — outputs to `dist/`)
- `go test ./...` must pass before every commit; containment unit tests are
  the highest-priority suite in this repository.
