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
  never a system location, the home directory itself, or a credential
  directory — and then it is the *only* root: an upload comes from inside it,
  a download lands inside it, and nothing outside it can be reached. Never
  widen it from Slack-derived values, and never add a second source of roots.
- **Path checks run on canonicalized paths** (Abs + Clean + EvalSymlinks),
  then: containment in the work_dir → regular-file-only → hidden-component
  rejection (relative to the work_dir) → size cap. Do not reorder checks to run
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
