# ADR-0002: symmetric download with write-side containment

> Status: Accepted — 2026-07-20

## Context

The official Slack MCP reads file *content into model context*
(`slack_read_file`) but offers no way to place a Slack file on the local
disk, where agents actually work with binaries, archives, or anything too
large for context. Upload without download is asymmetric: the extension
can send local artifacts to Slack but cannot retrieve the files teammates
share back.

Threat model: upload is an **egress** primitive (exfiltration risk —
contained by ADR'd read-side policy). Download is the mirror image, an
**ingress/write** primitive: an agent steered by untrusted Slack content
could try to write outside the sanctioned area, overwrite existing files,
or smuggle a hostile path through the Slack-supplied filename
(`../../evil`, `.zshrc`, …). The filename field of a Slack file is
attacker-controllable data.

## Decision

Add `ext_file_download` (file_id → local file), governed by a write-side
variant of the same containment policy, all checks on canonicalized paths:

1. **Destination containment**: the destination *parent directory* must
   exist and canonicalize (Abs+Clean+EvalSymlinks) into an allowed root —
   the same deny-by-default roots as upload; nothing about the
   destination may widen them.
2. **Filename sanitization**: the effective basename (agent-supplied
   `filename`, else the Slack file's name) is reduced to its base
   component; separators and parent references cannot survive. The
   hidden-component rule applies to it (no writing dotfiles).
3. **No overwrite**: an existing target fails with a structured
   `already_exists` error; the agent picks another name. No overwrite
   flag in v1 — deletion is not this tool's business.
4. **Size cap**: the configured max_file_size applies; the size reported
   by files.info is checked before any bytes move, and the stream is
   hard-limited while writing (a lying size field cannot bypass the cap).
5. **Atomic write + ingress audit**: bytes land via tmp+rename, and every
   attempt — ok, denied, or error — is recorded in the same audit log as
   uploads (direction is evident from the tool name).

Implementation: files.info (metadata + url_private_download) →
authenticated GET → contained write. Requires the `files:read` scope,
which the recommended scope set already includes.

## Consequences

- Upload and download are symmetric in capability and in governance;
  the operator's `allowed_roots` bound both directions of file flow.
- A Slack-side filename can never influence *where* a file lands, only
  (after sanitization) what it is called.
- Agents needing textual file content should still prefer the official
  `slack_read_file`; `ext_file_download` is for getting real files onto
  disk.
