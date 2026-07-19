# RFP: slack-mcp-extender

> Generated: 2026-07-19
> Status: Draft

## 1. Problem Statement

Claude's official Slack connector (`mcp.slack.com/mcp`) can read messages, post
messages, and download files, but **cannot post file attachments**. Reading and
ordinary message posting are already well covered by the official connector and
existing tools (swrite/scat/stail/scli, etc.); the only missing piece is file
attachment.

slack-mcp-extender is a per-workspace MCP proxy that **transparently forwards**
the official Slack MCP while **injecting** file-upload tools (attachment posted
as a root message, and attachment posted as a thread reply). On the Claude side
it still looks like a single Slack connector — now with attachment capability.

Target user: an operator using the official Slack MCP on Claude Desktop
(including cowork) who wants to post artifact files to Slack as attachments
(for now, the sole operator of the nlink-jp org).

## 2. Functional Specification

### Commands / API Surface

The CLI is limited to the minimum needed for lifecycle management (no CLI upload
subcommand — bot-identity uploads are already served by swrite).

| Command | Role |
|---|---|
| `slack-mcp-extender mcp --config <path>` | Start the stdio MCP server (transparent proxy + tool injection) |
| `slack-mcp-extender init` | Interactive config scaffolding (including allowed_roots registration) + output of a Claude Desktop registration JSON snippet |
| `slack-mcp-extender login --config <path>` | Run the OAuth authorization_code flow and store tokens (once per workspace) |
| `slack-mcp-extender config ...` | Show / validate config |

#### MCP surface

- **Transparent proxy**: relays all upstream (`mcp.slack.com/mcp`, SSE) tools,
  notifications, and responses unmodified. No tool-name rewriting.
- **Two injected tools**:

| Tool | Arguments | Behavior |
|---|---|---|
| `upload_file` | `channel` (required), `file` (required), `workspace_dir` (optional), `comment` (optional), `filename` (optional) | Upload a file and post it as a root message attachment in the channel |
| `upload_file_to_thread` | the above + `thread_ts` (required) | Upload a file and post it as a thread-reply attachment |

- Implementation uses the Slack external upload 3-step
  (`files.getUploadURLExternal` → POST to the upload URL →
  `files.completeUploadExternal`). Passing `channel_id`
  (+ `initial_comment` / `thread_ts`) to `completeUploadExternal` folds upload
  and share into one operation, so no orphaned files are ever created.
- If a name collision with an upstream tool is detected during the tools/list
  merge, log a warning and route that name deterministically to the injected
  (local) tool.

### Input / Output

**File input (two modes, unified under a single containment)**:

1. **workspace_dir mode**: the agent specifies the working directory via the
   `workspace_dir` tool argument, and `file` is resolved relative to it (same
   shape as voice-studio-mcp / video-studio-mcp). **workspace_dir must be
   agent-specified** — under cowork the agent owns the session working
   directory, and a config-fixed directory would make the tool inoperable.
2. **Direct path mode**: `file` is an absolute path.

In both modes, the resolved path is **canonicalized (Abs + Clean +
EvalSymlinks)** and then verified to be contained under one of the
operator-configured **`allowed_roots`**. The `workspace_dir` argument itself is
untrusted input; the containment boundary is always the config-side
`allowed_roots`.

- **Deny-by-default**: with no `allowed_roots` configured, all file operations
  are refused. `init` registers allowed_roots interactively (suggesting
  candidates such as the cowork sessions parent directory).
- Regular files only (directories, symlinks resolving outside allowed_roots,
  devices, and sockets are rejected).
- **Hidden path component rejection (defense-in-depth)**: after
  canonicalization, any path whose part **relative to the matched
  allowed_root** contains a component starting with `.` (`.git` / `.env` /
  `.ssh` / `.aws`, etc. — directories and files alike) is rejected. The check
  is limited to components below the root so that configurations where the
  allowed_root itself lives under a dot directory (e.g., a cowork sessions
  parent) keep working — the path up to the root is trusted as explicitly
  operator-approved. Disguises via symlinks resolving into dot paths are
  caught automatically because the check runs after EvalSymlinks. Opt-out via
  `allow_hidden` in config (default false; control stays out-of-band only).
- A size cap is set in config (default 50 MB, adjustable; a self-defense limit
  independent of Slack's own limits).

**Output**:

- Success: a structured result including
  `{ ok: true, file_id, channel_id, filename, size }`.
- Failure: structured errors `{ code, message, details }`. Path rejection uses
  `code: "path_denied"` with the offending path and allowed_roots in details
  (hidden-component rejection sets `details.reason: "hidden_component"`).
- **Minimal audit log**: append-only record in the state directory of when /
  which file / to which channel (thread) was uploaded (egress record).

### Configuration

- **Per-workspace config file** (JSON, 0600). Slack user tokens are
  workspace-scoped, so config, token store, state directory, and allowed_roots
  are all isolated per workspace.
- **MCP registration in Claude Desktop is also per workspace** (e.g.
  `slack-ext-<ws>`). No multiplexing of multiple workspaces in one process
  (it would cause tools/list name collisions and break transparency).
- Config fields: upstream URL / OAuth (authorizeUrl, tokenUrl, clientId,
  clientSecret, scopes, callback settings) / allowed_roots / max_file_size /
  state_dir.
- The OAuth client (clientId/clientSecret) may be shared across configs; tokens
  and consent are per workspace.
- clientSecret should preferably be supplied via environment variable, and **no
  environment-specific value is ever committed to the repository** (examples
  use placeholders only).

### External Dependencies

- Upstream: `https://mcp.slack.com/mcp` (SSE)
- Slack Web API: `files.getUploadURLExternal` / `files.completeUploadExternal` /
  OAuth `oauth/v2_user/authorize` and `oauth.v2.user.access`
- Credential: user token of the existing self-owned Slack App (shared with scli)
- External Go library dependencies: **zero** (standard library only)

## 3. Design Decisions

- **Language**: Go. Single binary, zero external dependencies, Developer ID
  signed + notarized — matching the org's distribution standard. mcp-guardian
  provides prior in-house experience with stdio⇔SSE proxying and OAuth.
- **Full new build referencing the mcp-guardian skeleton**: only the proxy
  pipe / SSE transport / authorization_code OAuth (token store & refresh) /
  tools/list merge / tools/call routing / JSON-RPC framing are used as
  reference — no dependency, no copying. governance / classify / state /
  receipt / otlp / webhook / mask are deliberately left out, to avoid mixing
  the responsibilities of a governance proxy (mcp-guardian) and a
  feature-extension proxy (this tool).
- **swrite untouched**: swrite is a bot-identity design; bringing a user token
  into it would be a category error (already rejected). This tool operates
  under the **user's own identity (user token)**, consistent with the official
  connector.
- **Single token**: proxy forwarding and uploads use the same user token (same
  OAuth session). The existing app shared with scli already permits the user
  scope `files:write` at the app level, so no second credential is needed.
- **Threat model**: this tool (1) relays untrusted Slack content to the LLM,
  (2) reads local files, and (3) sends data to an external service (Slack) —
  i.e., an exfiltration primitive. Containment (allowed_roots) is therefore
  **defined only out-of-band by the operator (config)** and never derived from
  tool arguments or Slack-derived values.
- **Explicitly out of scope**: reimplementing read/post (covered by the
  official connector and existing tools) / bot workflows (covered by swrite) /
  governance & telemetry / multi-workspace multiplexing in one process /
  serving over Streamable HTTP (stdio suffices for Claude Desktop; revisit if
  needed).

## 4. Development Plan

### Phase 1: Core

- JSON-RPC framing + stdio⇔SSE transparent proxy
- OAuth authorization_code flow + token store & refresh
- tools/list merge + tools/call routing (including collision detection)
- Two injected tools (external upload 3-step; root / thread)
- Path containment (canonicalization + allowed_roots containment +
  deny-by-default + regular-file-only + hidden-component rejection + size cap)
- Tests: **containment unit tests are the top priority** (`..` traversal /
  symlink escape / relative-path tricks / deny with no roots configured /
  hidden components specified directly, reached via symlink resolution, and
  allowed_root itself under a dot directory).
  Verify merge, routing, and transparency with a mock upstream + dummy MCP
  client harness.

### Phase 2: Features

- Lifecycle CLI (`init` with interactive allowed_roots registration and Claude
  Desktop registration snippet output / `login` / `config`)
- Minimal audit log
- Error table (catalog of structured error codes)
- Multi-workspace ergonomics (smooth handling of multiple configs)

### Phase 3: Release

- README.md / README.ja.md / CHANGELOG.md / AGENTS.md / LICENSE (MIT)
- Real-workspace E2E (root attachment, thread attachment, path_denied,
  re-consent path)
- Signing + notarization; org release procedure (12 steps)
- Add as submodule to the chatops-series umbrella; update org profile / web
  catalog / homebrew tap; check-org.sh green

Each phase is independently reviewable. Phase 1 can be fully verified against a
mock upstream.

## 5. Required API Scopes / Permissions

Slack user token scopes (existing app shared with scli; already permitted at
the app level):

- Existing read/post set: `chat:write`, `channels:history`, `channels:read`,
  `groups:history`, `groups:read`, `im:history`, `im:read`, `mpim:history`,
  `mpim:read`, `search:read`, `users:read`
- **Added**: `files:write` (required for upload; must be added to the OAuth
  requested scopes, with one re-consent per workspace)
- `files:read` is not required.

Note: re-consent may rotate the user token. If scli keeps a separate token
store, verify scli connectivity after re-consent (re-login if needed).

## 6. Series Placement

Series: **chatops-series**

Reason: it naturally belongs with the Slack ChatOps tools
(swrite/scat/stail/slack-router/md-to-slack). Existing series tools are
bot-authenticated, whereas this tool uses a user token — a **deliberate
deviation** to align identity with the official connector, to be documented in
the README.

## 7. External Platform Constraints

- The legacy `files.upload` API is deprecated — the external upload 3-step is
  mandatory.
- Without `channel_id`, `completeUploadExternal` leaves the file attached to no
  message (orphaned) — structurally avoided by making channel a required
  argument.
- Slack does **not support attaching files to an already-posted message** —
  an attachment is always posted as a new root message or a thread reply.
- Slack user tokens are workspace-scoped — per-workspace config + MCP
  registration is an architectural precondition.
- Adding scopes requires re-consent per workspace. Because the app is shared,
  token rotation may affect scli.
- Subject to Slack Web API rate limits and Slack-side file size limits.
- `mcp.slack.com/mcp` is Slack-official, but its SSE endpoint's specification
  and availability may change (the largest upstream-dependency risk of this
  tool).

---

## Discussion Log

1. **Origin**: the official Slack connector can download files but cannot
   attach them. Since reading/ordinary posting are already well served, the
   direction was set to "specialize in attachment posting."
2. **swrite extension idea → rejected**: adding an `mcp` subcommand to swrite
   was considered, but rejected because (a) swrite is bot-only by design and
   bringing in a user token would be a category error, and (b) upload should be
   folded into posting via `channel_id` on `completeUploadExternal`. swrite
   stays untouched.
3. **Standalone upload MCP idea → evolved**: via a plan for a user-identity
   upload-only MCP, the concept evolved into "a proxy that transparently
   forwards the official Slack MCP and injects only upload."
4. **Feasibility proven**: confirmed that mcp-guardian already proxies
   `mcp.slack.com/mcp` (SSE upstream) with Slack user OAuth
   (authorization_code) **in production use**. Since guardian holds the user
   token itself and the scopes are client-specified, adding `files:write` to
   the requested scopes closes the design with a **single token**.
5. **Decision to build new**: rather than piggybacking on mcp-guardian
   (extending metatool), build a dedicated tool referencing guardian's skeleton
   (proxy/OAuth/merge/routing), keeping governance and feature-extension
   responsibilities separate. Installing a self-owned app into the workspace is
   acceptable.
6. **Credential settled**: the existing app shared with scli already permits
   user scope `files:write` at the app level. Adding it to the OAuth requested
   scopes + one re-consent per workspace completes the single-token design.
7. **File input design**: support both workspace_dir and direct path. The
   initial plan made a config-fixed dedicated workspace_dir the default
   allowed root, but was corrected: **under cowork the agent owns the working
   directory, so workspace_dir must be a tool argument (agent-specified) or the
   tool is inoperable**. Containment is unified under operator-configured
   allowed_roots; the default is full deny + interactive registration via
   `init`.
8. **Threat model**: relaying untrusted Slack content + local file read +
   external egress = an exfiltration primitive. Requirements: allowed_roots
   defined only out-of-band (config), never derived from tool arguments or
   Slack-derived values; canonicalization + containment check; structured
   path_denied; minimal audit log.
9. **Multi-workspace**: as with mcp-guardian, separate config + MCP
   registration per workspace (tokens are workspace-scoped; multiplexing is
   rejected as it causes tool-name collisions and breaks transparency).
10. **Open items settled**: name = slack-mcp-extender / CLI = lifecycle-minimum /
    injected tools = two (root / thread separated) / allowed_roots default =
    full deny + registration via `init`.
11. **Hidden path component rejection added**: since exfiltration targets
    (`.ssh`/`.aws`/`.env`/`.git`, etc.) concentrate under dot paths, dot
    component rejection was added as defense-in-depth inside allowed_roots.
    The check is limited to components relative to the allowed_root (so
    dot-parented roots keep working), with `allow_hidden` (default false) as
    opt-out.
