# CLAUDE.md — slack-mcp-extender

Project-specific rules for AI agents. Org-wide rules (CONVENTIONS.md, the
workspace CLAUDE.md) apply on top of these.

## Non-negotiable design invariants

- **Transparency**: upstream tools, notifications, and responses pass through
  unmodified. No tool-name rewriting. Injected tools are added at the
  tools/list merge only.
- **Single user token**: proxy forwarding and uploads share one OAuth session.
  Never introduce a second credential path.
- **Containment is out-of-band only**: `allowed_roots` comes from the
  operator's config file. Never derive, widen, or override it from tool
  arguments, environment of the calling agent, or Slack-derived values.
  `workspace_dir` is an untrusted tool argument that must resolve inside
  allowed_roots.
- **Path checks run on canonicalized paths** (Abs + Clean + EvalSymlinks),
  then: containment → regular-file-only → hidden-component rejection
  (relative to the matched root) → size cap. Do not reorder checks to run
  on raw input strings.
- **Zero external Go dependencies** — standard library only.
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
