# ADR-0004: Leave path judgement to nlink-jp/pathguard — judge an upload as leaving the machine

> Status: Accepted — 2026-09-22. Amends [ADR-0003](0003-work-dir-as-containment.md) (the work_dir check and the credential floor).

## Context

Since ADR-0003, `work_dir` validation and the credential floor applied to files (`workdir.DeniedPath`)
lived in `internal/workdir`, a copy of voice-scribe's (the reference implementation of organization
ADR-021) that had drifted further: no `.env` rule, its own check order, and places compared **by
name**. APFS is case-insensitive by default, so the same place passed the checks under another
spelling. When the home directory could not be determined, the credential check passed everything.

The organization moved this judgement into one module (`nlink-jp/pathguard`, lib-series). It compares
places by file identity and by names folded the way the disk folds them, holds one list — the same
as gem-agent's and lagent's — and separates reads and writes (Local) from what leaves the machine
(Outbound).

## Decision

- Depend on `github.com/nlink-jp/pathguard` v0.2.0. CLAUDE.md's dependency rule is reworded to "no
  third-party modules; modules of this organization, which hold the same rule, are allowed" (the
  operator's decision, 2026-09-22).
- `internal/workdir` becomes an adapter. The call shape (`Resolve(arg, meta, serverDirs)`,
  `Validate(dir, serverDirs)`) stays; `Error` and the codes are pathguard's, re-exported
  (`work_dir_denied` carries `reason` in `details`).
- The file floor is split by direction: `UploadDenied` (Outbound — an upload leaves the machine, so a
  credential name is refused wherever it sits) and `DownloadDenied` (Local). `containment.Policy`
  applies the first to an upload's source (`Resolve`) and the second to a download's target
  (`ResolveNewFile`).
- `work_dir_denied`'s `details` are returned to the caller (they were dropped).

## Consequences

- **Refused now on upload**: a file named as a secret (`id_rsa`, `credentials.json`,
  `*service-account*.json`) or lying in a credential directory, wherever it sits; the real places
  under your home from the runtimes' list (newly `~/.kube`, `~/.config/gh`, `~/.netrc` and the
  rest); every spelling; and wherever a link directly inside one of those directories points.
- **`.env` is refused now** (`sensitive_path`). With `allow_hidden=true` it could be uploaded. Three
  containment tests used `.env` as their example of a hidden file; they now use an ordinary hidden
  file, and a separate test pins `.env` as `sensitive_path`. The templates (`.env.example` and the
  like) pass.
- **An unknown home refuses everything.** When `$HOME` names another directory than the account's
  home, both are protected.
- With no copy here, a fix to the judgement is a pathguard release and a one-line dependency update.

## References

- Organization ADR-021 (the work-dir contract of the file-mediated MCP servers)
- ADR-0003 (the containment boundary is a per-call `work_dir`)
- nlink-jp/pathguard's RFP (`docs/en/pathguard-rfp.md`)
