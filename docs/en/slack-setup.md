# Slack Setup Guide

This guide walks through everything needed to run slack-mcp-extender
against one Slack workspace: creating (or reusing) a Slack App, writing the
workspace config, logging in, and registering the MCP server in Claude
Desktop.

Slack user tokens are **workspace-scoped**. Repeat steps 3–6 once per
workspace you want to use; the Slack App itself (step 1–2) can be shared.

## 1. Create the Slack App

You need a Slack App that can issue a **user token** with the scopes in
[`docs/slack-app-manifest.yaml`](../slack-app-manifest.yaml). The manifest's
scope set covers **every tool of the official Slack MCP server** (search,
message read/send, conversation create, reactions, canvases, file
read/download, emoji, user profiles — per the
[official tool⇄scope table](https://docs.slack.dev/ai/slack-mcp-server/))
plus `files:write` for the injected upload tools; the manifest's comments
map each scope to the tools it unlocks so you can trim deliberately. A
missing scope surfaces later as a `missing_scope` error on that upstream
tool. Slack's MCP endpoint accepts internal (workspace-installed) apps —
no directory listing needed.

**Option A — new dedicated app (recommended for a clean start):**

1. Open <https://api.slack.com/apps> → **Create New App** → **From a
   manifest**.
2. Pick the target workspace.
3. Paste the contents of [`docs/slack-app-manifest.yaml`](../slack-app-manifest.yaml)
   and create the app.

**Option B — reuse an existing app** (e.g. one already used by another
CLI): open the app's settings and confirm/add:

- **OAuth & Permissions → Scopes → User Token Scopes**: everything in the
  manifest — `files:write` is the one the upload tools require.
- **OAuth & Permissions → Redirect URLs**: `https://localhost:7777/callback`
  (must match `oauth.callback_port` in your config).
- **MCP enablement**: the app must have MCP enabled
  (`settings.is_mcp_enabled: true` in manifest terms — check the app's
  settings/manifest). Without it, Slack's MCP endpoint rejects the
  connection. If the app already works with another MCP proxy against
  `mcp.slack.com`, this is already on.

The scopes your config *requests* must be a subset of what the app
*permits* — requesting a scope the app does not declare fails at the
authorize step. Either add the missing scopes on the app side, or trim
your config's `oauth.scopes` to what the app permits (losing the
corresponding upstream tools).

> Adding a scope to an already-authorized app requires a **re-consent**
> (step 5) in every workspace, and re-consent may rotate the user token.
> If another tool shares this app with its own token store, verify it
> still works afterwards.

**App icon (optional):** manifests cannot carry an icon, so upload
[`docs/slack-app-icon.png`](../slack-app-icon.png) (1024×1024; SVG source
sits next to it) under **Basic Information → Display Information** if you
want the app to be recognizable in the workspace.

## 2. Collect the client credentials

In the app's **Basic Information → App Credentials**, note the
**Client ID** and **Client Secret**. The secret goes into an environment
variable, never into a committed file.

## 3. Write the workspace config

**The easy path** — let `init` scaffold it interactively (client ID, secret
storage, callback port, allowed roots), validate it, and print the
follow-up commands:

```bash
slack-mcp-extender init
```

**The manual path** — copy [`config.example.json`](../../config.example.json)
to a private location, one file per workspace:

```bash
mkdir -p ~/.config/slack-mcp-extender
cp config.example.json ~/.config/slack-mcp-extender/myworkspace.json
chmod 600 ~/.config/slack-mcp-extender/myworkspace.json
```

Edit it:

- `oauth.client_id` — from step 2.
- `oauth.client_secret_env` — the name of the environment variable holding
  the secret (recommended), or use `oauth.client_secret` for a literal
  value (the file is then the secret store; keep it 0600).
- `oauth.scopes` — must match the user scopes the app permits and include
  `files:write`.
- `allowed_roots` — the **absolute** directories the upload tools may read
  from. This is the security boundary: nothing outside these roots can be
  uploaded, hidden entries (`.git`, `.env`, …) below them are rejected,
  and an empty list denies all file access. Choose the narrowest
  directories that work — e.g. a dedicated exchange directory, or your
  agent's session/output area.

Check the result:

```bash
slack-mcp-extender config validate --config ~/.config/slack-mcp-extender/myworkspace.json
```

## 4. Log in (once per workspace)

```bash
export SLACK_MCP_EXTENDER_CLIENT_SECRET='...'   # if using client_secret_env
slack-mcp-extender login --config ~/.config/slack-mcp-extender/myworkspace.json
```

The browser opens Slack's consent page. Two expected quirks:

- After consenting, the browser lands on `https://localhost:7777/callback`
  served with a **self-signed certificate** — it will warn "not secure".
  Click through; the callback never leaves your machine.
- If the port is busy, change `oauth.callback_port` **and** the app's
  Redirect URL together.

Tokens are stored in the config's state directory
(`<config-basename>.state/tokens.json`, mode 0600).

## 5. Re-consent after scope changes

If you later add a scope (e.g. `files:write` to a previously authorized
app), run `login` again in **each** workspace — the token must be
re-issued with the new scope set.

## 6. Register in Claude Desktop

Add one MCP server entry **per workspace** to Claude Desktop's MCP
settings:

```json
{
  "mcpServers": {
    "slack-myworkspace": {
      "command": "/path/to/slack-mcp-extender",
      "args": ["mcp", "--config", "/Users/you/.config/slack-mcp-extender/myworkspace.json"]
    }
  }
}
```

Restart Claude Desktop. The connector exposes every official Slack MCP
tool unchanged, plus `ext_file_upload`, `ext_file_upload_to_thread`, and `ext_file_download`.

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `no stored tokens (run ... login first)` | Step 4 not done for this config, or the state dir moved. |
| `HTTP 401: authentication failed after token refresh` | Token revoked — run `login` again. |
| Tool error `path_denied` | The file resolves outside `allowed_roots` (or is hidden / too large / not a regular file). The error's `details` say which rule and which roots. |
| Tool error `slack_api_error: not_in_channel` | The authorizing user is not a member of the target channel — join it in Slack first. |
| Browser warning on the callback | Expected (self-signed loopback TLS) — click through. |
| A freshly created channel is not found by the search tools | Slack's search index lags new channels. Ask the agent to use a channel ID directly, or wait for indexing. |
| `start callback server on port 7777` | Port busy — change `oauth.callback_port` and the app's Redirect URL together. |
