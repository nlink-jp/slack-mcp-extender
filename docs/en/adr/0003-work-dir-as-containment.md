# ADR-0003: The containment boundary is a per-call `work_dir`; `allowed_roots` is removed

> Status: Accepted — 2026-09-13. Amends [ADR-0002](0002-symmetric-download-with-write-containment.md) (symmetric download with write containment).

## Context

ADR-0001/0002 confined both uploads and downloads to **operator-configured
`allowed_roots`**. The judgement that a boundary is needed — an exfiltration
primitive on the way out, a write primitive on the way in — stands untouched.
What changes is **where the boundary comes from**.

`allowed_roots` could not express what it was for: the matcher is a
resolved-path prefix test with no per-repository granularity. Covering a work
root that holds a hundred repositories means listing `~/works`, or `~`, and `~`
admits `.ssh` and `.aws` — at which point the list means nothing. What an
operator could actually write was a narrow exchange directory, which in practice
means "the file the agent is working on cannot be sent".

Pointwise designation was the missing piece, and organization ADR-021's
`work_dir` is exactly that: the caller names one directory per call.

## Decision

1. **`workspace_dir` is renamed `work_dir` and required by all three injected
   tools.** It means the absolute path of a directory the caller can read back.
2. **`work_dir` *is* the containment boundary.** `containment.Policy` is built
   per call with roots = [the resolved work_dir]: an upload comes from inside
   it, a download lands inside it, and `dest_dir` defaults to it.
3. **It is validated before it is trusted** — absolute, no `~`, no `..`, exists
   and is a directory, writable, and not a system location, the home directory
   itself, or a credential directory (`~/.ssh`, `~/.aws`, `~/.gnupg`,
   `~/.config/gcloud`, `~/Library/Keychains`, `~/.claude`, `~/.codex`). The
   check runs on both spellings of the path — as given and symlink-resolved —
   against both spellings of every entry. **This server is organization ADR-021
   §7's single exception**: the file it sends leaves the machine, so "the caller
   could have read this itself" is not the bound that matters, and the input must
   come from under `work_dir`.
4. **Resolution is argument → `_meta["jp.nlink/work_dir"]` → `work_dir_required`.**
5. **`allowed_roots` is deleted from the config**, and a config still carrying it
   fails at startup with the replacement named — a containment list the operator
   believes is in force, silently ignored, is the worst outcome available here.
   `init` no longer asks for roots.
6. Everything else ADR-0002 decided stands: canonicalization order, hidden-component
   rejection, regular files only, the size cap, never overwriting, the audit log.
   Only the reference point moves, from "the matched root" to "the work_dir".

## Consequences

- **Breaking.** A call sending `workspace_dir` is refused, and a config carrying
  `allowed_roots` will not start.
- No exchange directory has to be agreed in advance; an agent sends from the
  directory it is already working in.
- Containment is not weaker — it is narrower. "Any of the roots the operator
  listed" becomes "the one this call named".

## References

- Organization ADR-021 (the work-directory contract; this server is its §7
  upload exception)
- [ADR-0002](0002-symmetric-download-with-write-containment.md),
  [ADR-0001](0001-extension-tool-namespace.md)
