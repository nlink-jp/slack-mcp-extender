# ADR-0001: `ext_` namespace for injected tools

> Status: Accepted — 2026-07-20
> Supersedes the tool names recorded in the RFP (`upload_file`,
> `upload_file_to_thread`).

## Context

The injected tools shipped in v0.1.0 as `upload_file` and
`upload_file_to_thread` — generic names indistinguishable from official
Slack MCP tools (`slack_*`) in a merged tools/list.

Two problems surfaced:

1. **Collision masking.** The proxy resolves name collisions
   deterministically in favor of the injected (local) tool. With generic
   names, if Slack ever ships an official `upload_file`, the proxy would
   silently mask the new official capability — the exact opposite of this
   tool's "extend, never alter" philosophy.
2. **Attribution.** Agents and humans reading tool lists or tool-use logs
   cannot tell which capabilities are official and which come from this
   extension.

## Decision

Injected tools live in an explicit `ext_` namespace:

| v0.1.0 | v0.2.0 |
|---|---|
| `upload_file` | `ext_file_upload` |
| `upload_file_to_thread` | `ext_file_upload_to_thread` |
| — | `ext_file_download` (ADR-0002) |

The rename is breaking and ships without compatibility aliases: v0.1.0 was
released one day earlier and the sole known consumer is the operator who
requested the rename. The collision-handling machinery stays in place as
defense in depth.

## Consequences

- Upstream namespace (`slack_*`) and extension namespace (`ext_*`) can
  never collide; official additions are never masked.
- Tool provenance is self-describing in every tools/list and log line.
- Any future injected tool must use the `ext_` prefix.
