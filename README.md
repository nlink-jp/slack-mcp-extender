# slack-mcp-extender

> Implemented, unit-tested, and **end-to-end verified against a real
> workspace** (live proxy transparency, root and thread attachments,
> containment denials with audit records). macOS binaries are Developer ID
> signed and notarized. See the [RFP](docs/en/slack-mcp-extender-rfp.md)
> for the design.

A per-workspace MCP proxy that **transparently forwards Claude's official
Slack MCP** (`mcp.slack.com/mcp`) while **injecting the capability it
lacks: moving real files between Slack and the local disk**.

On the Claude side it looks like a single Slack connector — every official
tool passes through unmodified — plus three injected tools in an explicit
`ext_` namespace (they can never collide with official `slack_*` tools):

| Tool | Does |
|---|---|
| `ext_file_upload` | post a local file as a root-message attachment |
| `ext_file_upload_to_thread` | post a local file as a thread-reply attachment (`thread_ts`) |
| `ext_file_download` | save a Slack file (`file_id`) to the local disk — never overwrites |

Transfers run under the **same user token** the proxy already holds — one
OAuth session, one identity, no second credential.

## Why user identity (a deliberate deviation)

The other chatops-series tools (swrite, stail, slack-router) are
bot-authenticated. This tool deliberately uses a **user token** instead:
it extends the official Slack connector, which operates as *you*, so the
files it posts should carry your identity too. For bot-identity uploads,
use [swrite](https://github.com/nlink-jp/swrite).

## Security model

This tool relays untrusted Slack content, reads and writes local files, and
moves data in both directions — an exfiltration primitive on the way out
and a write primitive on the way in, if left unconstrained. File access in
**both directions** is therefore confined to the **`work_dir` the calling
agent names on every call**:

- canonicalized containment (Abs + Clean + EvalSymlinks), deny-by-default
- hidden path components (`.git`, `.env`, `.ssh`, …) rejected below the work_dir
- regular files only, size-capped (declared size **and** on the wire),
  structured `path_denied` errors, egress/ingress audit log
- downloads never overwrite, and a Slack-side filename can influence only
  the (sanitized) name of the saved file, never where it lands
- the work_dir is validated before it is trusted (absolute, existing,
  writable, never a system location, your home directory itself, a
  credential directory such as `~/.ssh`, or **this server's own config and
  state directories** — the state directory holds `tokens.json`, and an
  upload leaves the machine), and it is the **only** root — never widened
  from Slack-derived values

**The credential list is also applied to the file, not only to the
directory.** That list — the credential and agent-control places under your
home that gem-agent and lagent use (`~/.ssh`, `~/.aws`, `~/.kube`,
`~/.config/gcloud`, `~/.config/gh`, `~/.gnupg`, `~/.netrc`,
`~/Library/Keychains`, `~/.claude`, `~/.codex`, `~/.config/{gem-agent,lagent}`,
…), any `.env`, and this server's own directories, compared by file identity
and by folded name ([nlink-jp/pathguard](https://github.com/nlink-jp/pathguard))
— is a floor rather than a boundary, and a floor tested only against `work_dir` is stepped
over by naming the directory just above a credential one: `~/.config` is not
itself on the list, so it passed as a work directory, and a call naming
`gcloud/credentials.db` under it was never looked at. Every caller-named path
is now checked against the same list at the point of use, with every
symlink on the way followed so an innocuous-looking link cannot stand in for
its target, and in both directions: an upload whose real path lands in one of
those locations is refused — and because an upload leaves the machine, so is
one named as a secret (`id_rsa`, `credentials.json`, `*service-account*.json`)
or whose path passes through a credential directory or file name (`.ssh`,
`.aws`, `.config/gcloud`, `.npmrc`, `.netrc`, `.git-credentials`,
`.bash_history`, `.docker/config.json` and the like) wherever it sits — and so
is a download destination that would write into one. Whether a path exists
never changes the answer: a credential file that does not exist is refused
the same way as one that does, and a path outside the work_dir is refused as
outside whether or not it is there. A file whose directory is a system
location, or your home directory itself (reached through a `work_dir` above
it), is refused too. The refusal is a structured `path_denied` error with
`reason: sensitive_path` naming the path and pathguard's own reason in
`floor_reason` (`sensitive_path`, `server_dir`, `system_dir`, `home_dir`,
`unresolvable_path`, `home_unknown`, `unconfigured` — the values
`work_dir_denied` gives in `reason`); it is written to the audit log like any
other denial, and ordinary files inside an ordinary work_dir are unaffected.

## Installation

Download the latest binary for your platform from
[Releases](https://github.com/nlink-jp/slack-mcp-extender/releases)
(macOS builds are Developer ID signed and notarized), or build from
source:

```bash
make build   # outputs dist/slack-mcp-extender (never `go build` directly)
make test    # go test -race -cover ./...
```

## Setup

New to slack-mcp-extender? See the **[Slack Setup Guide](docs/en/slack-setup.md)**
for step-by-step instructions — creating the Slack App from the bundled
[app manifest](docs/slack-app-manifest.yaml), writing the workspace config
(start from [config.example.json](config.example.json)), logging in, and
registering the server in Claude Desktop.

```bash
slack-mcp-extender init                              # scaffold a workspace config interactively
slack-mcp-extender config validate --config <path>   # check the workspace config
slack-mcp-extender login --config <path>             # OAuth (once per workspace)
slack-mcp-extender mcp --config <path>               # run the stdio MCP server
```

`init` asks for the OAuth client, the secret storage (environment variable
recommended), and the allowed roots, writes the config (0600), and prints
the login command plus the Claude Desktop registration snippet. Prefer it
over hand-editing; [config.example.json](config.example.json) documents the
full field set.

`--config` accepts a full path, or a bare workspace name resolved in
`~/.config/slack-mcp-extender` (`.json` appended automatically):
`login --config myworkspace` finds `myworkspace.json` there.

Slack user tokens are workspace-scoped: create **one config and one Claude
Desktop MCP registration per workspace**.

## Documentation

- [Slack Setup Guide](docs/en/slack-setup.md)
  ([日本語](docs/ja/slack-setup.ja.md)) —
  app manifest: [docs/slack-app-manifest.yaml](docs/slack-app-manifest.yaml)
- [RFP (English)](docs/en/slack-mcp-extender-rfp.md) /
  [RFP (日本語)](docs/ja/slack-mcp-extender-rfp.ja.md)

## License

[MIT](LICENSE)
