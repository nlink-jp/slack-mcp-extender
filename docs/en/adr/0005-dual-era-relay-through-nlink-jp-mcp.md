# ADR-0005: Pass both eras through unchanged — add, through nlink-jp/mcp, what the specification requires of an HTTP client, and shape the injected tools' answers to the era

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-27 |
| Binds | slack-mcp-extender |
| Decision makers | nlink-jp maintainers |
| Triggered by | Organization ADR-030 (dual-era support for MCP 2026-07-28, relays first; §1 and §2 amended on 2026-11-27 to match this design) and the release of the shared module nlink-jp/mcp v0.1.0 |

## Context

### slack-mcp-extender today

It relays the official Slack MCP (`mcp.slack.com/mcp`, Streamable HTTP) transparently to a stdio agent, with
exactly two seams: the injected tools' (`ext_*`) definitions are added to `tools/list` results, and a
`tools/call` naming an injected tool is answered locally (`internal/proxy`; CLAUDE.md "Transparency"). A POST to
the upstream carries only `Content-Type`, `Accept`, `Mcp-Session-Id` (when the upstream issued one) and
`Authorization` (`internal/transport/sse.go`).

### What is missing (specification, measurement, code)

1. **The required header is not sent even in the legacy era.** Since 2025-06-18 an HTTP client must carry the
   negotiated version in `MCP-Protocol-Version`. Requests are served today only because a server **MAY** treat
   a request without the header as 2025-03-26 (confirmed in the code).
2. **Modern requests cannot carry the required headers.** Deriving `MCP-Protocol-Version`, `Mcp-Method` and
   `Mcp-Name` from the body, and `Mcp-Param-*` for arguments marked with `x-mcp-header`, is a MUST for a
   2026-07-28 HTTP client; so is excluding tools whose marks are invalid.
3. **Local answers do not match the era.** A result to a modern request needs `resultType`, and should carry
   `serverInfo` in `_meta` (SHOULD). The injected tools' results carry neither today (confirmed in the code).
4. **One over-long line stops it.** Agent lines are read with `bufio.Scanner` (limit 1 MiB); a line over the
   limit leaves the read loop and exits. Upstream SSE is read with a 1 MiB-per-line `bufio.Scanner` too; a line
   over it silently ends that stream, and the request waits for the 120-second timeout (confirmed in the code).
5. **The upstream's JSON body is read without a bound.** An `application/json` answer is read with
   `io.ReadAll` (confirmed in the code).
6. **Non-2xx answers are handled incompletely.** An `application/json` body is delivered whatever the status
   and without checking that it is JSON-RPC. When a JSON-RPC error body has `id: null` it matches no pending
   request. The proxy handles one request at a time and reads no further agent lines while it waits for a
   forwarded answer, so the whole of the agent's input stops until the 120-second timeout. A non-JSON 4xx
   becomes a send failure answered with `-32603` (confirmed in the code).
7. **Malformed JSON from the agent gets no answer.** An unreadable line is logged and dropped; if it was a
   request, the agent keeps waiting (confirmed in the code).

The peers (measured again on 2026-11-27, versions included, recorded): the official Slack MCP (Slack MCP 1.0.0)
is legacy only (`-32601` for `server/discover`, `initialize` 2025-06-18). Claude Code 2.1.311 is dual-era.

## Decision

§1, §2, the upstream items of §3, and §5–§8 are the same decisions as mcp-bridge ADR-0008, written in the same
words (the two relays keep one shape). The differences are the injected tools (§3's first item and §4) and the
bound values (agent lines and SSE stay at 1 MiB).

### 1. Dependency

`github.com/nlink-jp/mcp` is added to `require` — inside CLAUDE.md's rule (the standard library and nlink-jp
modules that hold the same rule).

### 2. The era passes through; it is not converted

The upstream sees the era the agent opened. slack-mcp-extender answers neither `initialize` nor
`server/discover` itself (ADR-030 §1 as amended). The relay is dual-era whenever its agent is. An agent that
speaks only the legacy era reaches a modern-only upstream after its own migration.

### 3. Bodies are changed in exactly four ways

- **The injected tools' definitions are added to `tools/list` results** (as today). The upstream result's other
  keys (including a modern result's `resultType`, `ttlMs`, `cacheScope` and `nextCursor`) remain.
- **Tools whose `x-mcp-header` marks are invalid are removed from modern `tools/list` results** (a MUST of the
  specification; `httpwire.Tracker.Incoming`; tools beyond 5000 are removed too). Names and reasons are logged.
  The removal happens before the injected tools are added.
- **A failure the upstream did not answer as JSON-RPC is answered with a synthesized `-32603`.** When a
  non-2xx JSON-RPC error body has no `id`, a `null` one, or a different one, the request's `id` is put in its
  place (`httpwire.Classify`; fixes Context 6). A 2xx `application/json` body that is not valid JSON also gets a
  `-32603` under the request's `id`.
- **An answer that is too large is answered with a synthesized `-32603`** (§6; fixes Context 4 and 5).

Every other body passes through with its content unchanged (compacted onto one line by `wire.Writer`).

### 4. The injected tools' answers match the era

An injected tool's `tools/call` is read with `server.Inspect`, and its result is shaped with
`server.ShapeResult`. For a modern request this adds `resultType: "complete"` and
`_meta["io.modelcontextprotocol/serverInfo"]` (slack-mcp-extender's name and version). For a legacy request the
answer is as today (fixes Context 3).

The extender does not check the version of a request it answers locally. Writing that check would copy what
`Frontend` holds (ADR-030 §2). Using `Frontend` would mean feeding it every line, `initialize` included, which
does not fit a relay that answers no `initialize` (§2). An agent with a wrong version is refused by the upstream
on that connection's other requests (`tools/list` and so on). Put as who gains what: the user's own agent could
call the user's own tools while claiming a wrong version — nothing new is gained.

### 5. Headers are derived from the body

Every POST to the upstream carries the headers `httpwire.Tracker.Outgoing` returns.

- Legacy messages: `MCP-Protocol-Version` set to the version `initialize` negotiated (fixes Context 1). Not on
  `initialize` itself.
- Modern messages: `MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name` and `Mcp-Param-*` (omitted when the value
  is absent or null; the Base64 sentinel when the value cannot be sent plainly).

When `Outgoing` returns an error:

- **A batch (an array body)**: sent as today, without protocol headers, and logged.
- **A marked argument that cannot be mirrored into a header**, or **a `tools/call` without `params.name`**: not
  sent upstream; the request is answered with `-32602` (the value is never put in the message).

### 6. Every response passes through the Tracker before delivery

- A 2xx JSON body and the body of each SSE `message` event (each member after a batch is opened) go through
  `Tracker.Incoming` before delivery.
- A non-2xx response is judged by `Classify` (Deliver, Synthesized, Drop). The 401 retry stays as it is and
  happens first.
- **Bounds**: a 2xx JSON body up to 8 MiB; beyond that the request's `id` gets `-32603` "upstream response
  larger than 8388608 bytes" (fixes Context 5). SSE: 1 MiB per line (as today) and also 1 MiB for one event's
  joined data lines. When either is exceeded, reading that stream stops, and if the request that opened it has
  not been answered yet, its `id` gets `-32603` (fixes Context 4).

### 7. A HeaderMismatch is retried once, inside the transport

**When**: the request sent is a `tools/call` to which `Outgoing` added `Mcp-Method` (a modern one), and its answer
is HTTP 400 with a JSON-RPC `error.code` of `-32020`. This is decided by reading its own request and the answer's
body.

1. The transport sends a `tools/list` of its own to the upstream. Its `id` is
   `"slack-mcp-extender-refresh-<n>"` (a string). Its `_meta` copies the original request's version,
   `clientCapabilities` and `clientInfo`. It follows `nextCursor` for up to 10 pages. The answers only feed
   `Tracker.Incoming`; they are not delivered to the agent.
2. The original request is sent once more, with headers derived again. Its answer is delivered as it is.
3. If the refresh fails, the original request is not resent and the first `-32020` is delivered. If it fails
   after the first page, the marks held are those of the pages read.

One retry per request; refreshes are serialized. This is the only request the extender originates.

### 8. Agent lines are read and written with nlink-jp/mcp/wire

The limit stays at 1 MiB. An over-long line is skipped and reading continues; the agent gets `-32700`
(`id: null`, "message longer than 1048576 bytes") and the event is logged (fixes Context 4). Malformed JSON also
gets `-32700` (`id: null`) (fixes Context 7; as in mcp-bridge). Writes go through `wire.Writer`.

### 9. What does not change

The single user token, the injected tools' namespace (ADR-0001), containment by work_dir (ADR-0003, 0004),
session handling (only when the upstream issues one), the per-request timeout (120 seconds), and a proxy that
handles one request at a time.

### 10. What the pre-release check changed (2026-11-27)

Four points the independent pre-release pass found the design did not define were decided by the operator. Where
§3–§9 touch them, this section prevails.

1. **A request that would stay unanswered gets `-32603`** (added to §3 and §6). When an SSE stream a request opened
   ends without that request's answer (an event whose data is not JSON is dropped and logged), and when the answer
   to a request is an empty 2xx (202 included), the request's `id` gets `-32603`. A 202 or empty 2xx answering a
   notification still gets nothing.
2. **The remaining cases of a request whose headers cannot be derived** (added to §5). A `prompts/get` without
   `params.name` and a `resources/read` without `params.uri` are not sent and get `-32602`, as a `tools/call`
   without a name does. A legacy message whose negotiated version holds characters a header cannot carry is sent
   without protocol headers and logged (the behaviour before the migration; one bad version does not make the whole
   session unusable). A modern request whose version or method cannot go into a header gets `-32602`.
3. **The DELETE that ends a session** (added to §9). The DELETE ending a session the upstream issued carries the
   negotiated `MCP-Protocol-Version` too (since 2025-06-18 an HTTP client puts the header on every later request).
4. **The refresh's deadline** (added to §7). One refresh (up to 10 pages) as a whole is cut off at 60 seconds, and
   past that it is a failed refresh (a per-page deadline let the agent's requests wait up to 10 minutes).

## Detailed design

| Component | Change | Oracle |
|---|---|---|
| `internal/proxy` (`proxy.go`) | Agent lines through `wire.Reader` (1 MiB) and `wire.Writer`. `-32700` for an over-long line and for malformed JSON | wire's contract (module detailed design §3.1) and §8 |
| `internal/proxy` (injected answers) | `server.Inspect` and `server.ShapeResult` | Specification basic §Responses (`resultType`), §4 |
| `internal/transport` (`sse.go`) | Holds a `Tracker`. `doPost` adds `Outgoing`'s headers and handles §5's errors. Responses through `Classify` and `Incoming`. The synthesized `-32603` and bounds of §3 and §6. The §7 retry | The specification's streamable-http sections |
| `internal/jsonrpc` | Confirm that `MergeToolsListResult` keeps the upstream's other keys (not expected to change) | §3 |

## Test design

- **Unit (transport)**: requests after a legacy `initialize` carry `MCP-Protocol-Version`. A modern `tools/call`
  carries the standard headers and the marks' `Mcp-Param-*`. Marks are learned on both JSON and SSE. A tool with
  an invalid mark is removed. A 400 with `id: null` arrives under the request's `id` (without waiting for the
  timeout). An HTML 502 and malformed JSON in a 2xx become `-32603`. A JSON body over 8 MiB, and an SSE line and
  event over 1 MiB, answer `-32603` under the request's `id`. A batch is sent without headers. An unmirrorable
  argument and a `tools/call` without a name are not sent and get `-32602`.
- **Unit (retry)**: the same cases as mcp-bridge ADR-0008 (success, a second failure, a failed refresh, refresh
  answers not delivered, one retry only, requests and codes that are not retried). `-race`.
- **Unit (proxy)**: a modern injected answer carries `resultType` and `serverInfo`; a legacy one is unchanged.
  Merging a modern `tools/list` keeps `resultType`, `ttlMs` and `cacheScope`. After an over-long line and after
  malformed JSON the relay keeps running and answers `-32700`. Every existing containment test passes unchanged.
- **Probe matrix**: as in mcp-bridge ADR-0008 (three upstreams × three probe clients; expected results from the
  specification's compatibility matrix; the HTTP upstream probes live in this repository's tests). The injected
  tools' definitions appear in `tools/list` in every combination.
- **Real peers (E2E)**: Claude Code 2.1.311 through the extender to Slack, every line recorded. `server/discover`
  gets `-32601` and the client falls back to the legacy era. POSTs to the upstream carry
  `MCP-Protocol-Version: 2025-06-18`. `tools/list` holds the official tools and `ext_*`. One read-only official
  tool can be called (`slack_read_user_profile`, oneself). The local answer path is checked by calling
  `ext_file_download` with a `work_dir` that does not exist: a structured error comes back without touching
  Slack (no upload). A modern local answer cannot be exercised against the real upstream, which is legacy; the
  probe matrix covers it.

## Development plan

1. Approval of this ADR (en and ja).
2. Dependency and line framing (§1, §8).
3. Headers and responses (§3, §5, §6).
4. Injected answers (§4).
5. Retry (§7).
6. Documents (README en/ja, CLAUDE.md's transparency item, AGENTS.md, CHANGELOG).
7. Real-peer E2E and one independent pre-release pass (against this ADR).
8. Release (minor: 0.6.0), after mcp-bridge's release.

## Consequences

- When Slack moves to the modern era, dual-era agents keep connecting. In the legacy era it speaks with the
  headers the specification requires. A large upstream answer or an `id: null` error no longer stops the agent's
  input.
- The transparency rule becomes "bodies pass through; the injected definitions are added, and only the tools the
  specification says must be removed are removed". CLAUDE.md is amended.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Convert the era | The same reasons as mcp-bridge ADR-0008; the extender's design promises a transparent relay |
| Keep the injected answers in the legacy shape | A modern client reads a result without `resultType` as `complete`, but that rule exists for legacy servers. With a modern upstream behind the relay, one connection would mix shapes |
| Check the version of locally answered requests in the extender | A copy of `Frontend`'s check; using `Frontend` needs a relay that answers `initialize`. Not checking gives no one anything new (§4) |
| Return a HeaderMismatch as it is | The agent cannot fix headers |

## References

- Organization ADR-030 (§1 and §2 amended on 2026-11-27), mcp-bridge ADR-0008
- nlink-jp/mcp v0.1.0 reference manual (`httpwire`, `wire`, `server`)
- MCP 2026-07-28 `basic/transports/streamable-http`, `basic/index` §Responses
- ADR-0001 (injected tool namespace), ADR-0003, 0004 (containment)
