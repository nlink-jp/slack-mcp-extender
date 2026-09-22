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
  (`ResolveNewFile`), each on the path as named (cleaned) and as placed (pathguard follows every
  link hop).
- **Whether a path exists never changes the answer.** Both directions keep one order on one code
  path: place the path (`workdir.Where`, the last of pathguard's forms — every link followed, a
  dangling one by its target; for a path that exists, what EvalSymlinks returns) → floor →
  containment → the directory check → whatever depends on what exists (does it resolve, a directory,
  a regular file, nothing there yet). A missing credential file and an existing one are refused
  alike, and so are a missing and an existing path outside the work directory, message and details
  included. Existence is checked at the place, never re-walked from the spelling, which could step
  through a component the place skipped. Earlier versions of this fix gave a path that did not
  resolve a branch of its own, and each left one pair of answers apart. The one known exception: a
  hard link to a credential file, planted outside the work directory, is refused as a credential
  while a missing path there is outside — pathguard's identity comparison needs a file that exists,
  and it runs before containment so that a refusal names the credential.
- The file policies leave system places out by design, and a work directory above a system tree is
  reachable for a server running as root (`work_dir=/private` is stopped only as not writable).
  `DirDenied` applies pathguard's `CheckBeneath` to the directory the file lies in, in both
  directions, keeping every refusal but the credential and server places, which are left to the
  file policies (in practice: a system directory and the home directory itself), which judge them by
  direction (applied again, its rule for a directory named like a `.env` file refused a Python venv
  called `.env`).
- A floor refusal stays `path_denied` with `reason: sensitive_path` (what callers branch on), and
  pathguard's reason is added as `details.floor_reason` — the vocabulary `work_dir_denied` carries.
- `config.Config.ServerOwnedDirs` makes each directory absolute: pathguard refuses every call for a
  place without an absolute path, and a relative `state_dir` or `$HOME` would have done that.
- `work_dir_denied`'s `details` are returned to the caller (they were dropped).

## Consequences

- **Refused now on upload**: a file named as a secret (`id_rsa`, `credentials.json`,
  `*service-account*.json`) or whose path passes through a credential directory or file name
  (`.ssh`, `.aws`, `.npmrc`, `.netrc`, `.git-credentials`, `.bash_history`, `.docker/config.json`
  and the like), wherever it sits — a project's `.npmrc` included, with `allow_hidden`; the real places
  under your home from the runtimes' list (newly `~/.kube`, `~/.config/gh`, `~/.netrc` and the
  rest); every spelling; and wherever a link directly inside one of those directories points.
- **`.env` is refused now** (`sensitive_path`). With `allow_hidden=true` it could be uploaded. Three
  containment tests used `.env` as their example of a hidden file; they now use an ordinary hidden
  file, and a separate test pins `.env` as `sensitive_path`. The templates (`.env.example` and the
  like) pass.
- **An unknown home refuses everything.** When `$HOME` names another directory than the account's
  home, the credential and agent-control places are protected under both; this server's own
  directories follow `$HOME`, as they did before.
- **Refused now in both directions**: a file whose directory is a system location; a path that
  cannot be resolved (a chain of links that does not end, a NUL byte, over 4096 bytes), as
  `unresolvable_path`; a file directly in your home directory reached through a `work_dir` above
  it (`home_dir`). `/etc` is refused as a work directory on Linux too.
- With no copy here, a fix to the judgement is a pathguard release and a one-line dependency update.

## References

- Organization ADR-021 (the work-dir contract of the file-mediated MCP servers)
- ADR-0003 (the containment boundary is a per-call `work_dir`)
- nlink-jp/pathguard's RFP (`docs/en/pathguard-rfp.md`)
