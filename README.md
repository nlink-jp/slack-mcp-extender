# slack-mcp-extender

> **Status: pre-release.** The Phase 1 core (transparent proxy, OAuth login,
> injected upload tools, path containment) is implemented, unit-tested, and
> **end-to-end verified against a real workspace** (live proxy transparency,
> root and thread attachments, containment denials with audit records).
> Not yet released. See the [RFP](docs/en/slack-mcp-extender-rfp.md) for
> the design.

A per-workspace MCP proxy that **transparently forwards Claude's official
Slack MCP** (`mcp.slack.com/mcp`) while **injecting the one capability it
lacks: posting file attachments**.

On the Claude side it looks like a single Slack connector — every official
tool passes through unmodified — plus two injected tools:

| Tool | Posts the file as |
|---|---|
| `upload_file` | a root message attachment in a channel |
| `upload_file_to_thread` | a thread-reply attachment (`thread_ts`) |

Uploads use the Slack external upload 3-step under the **same user token**
the proxy already holds — one OAuth session, one identity, no second
credential.

## Why user identity (a deliberate deviation)

The other chatops-series tools (swrite, stail, slack-router) are
bot-authenticated. This tool deliberately uses a **user token** instead:
it extends the official Slack connector, which operates as *you*, so the
files it posts should carry your identity too. For bot-identity uploads,
use [swrite](https://github.com/nlink-jp/swrite).

## Security model

This tool relays untrusted Slack content, reads local files, and sends data
out — an exfiltration primitive if left unconstrained. File access is
therefore confined to operator-configured **`allowed_roots`**:

- canonicalized containment (Abs + Clean + EvalSymlinks), deny-by-default
- hidden path components (`.git`, `.env`, `.ssh`, …) rejected below the roots
- regular files only, size-capped, structured `path_denied` errors, audit log
- containment is defined **only** in the operator's config — never from tool
  arguments or Slack-derived values

## Installation

```bash
make build   # outputs dist/slack-mcp-extender (never `go build` directly)
make test    # go test -race -cover ./...
```

Pre-built, signed and notarized binaries will be published on the Releases
page once v0.1.0 ships.

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
