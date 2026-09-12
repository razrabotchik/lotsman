# ADR-0004: MCP client field notes

- **Status**: Accepted for the server-side profile at the M0 gate (T022); the desktop-client
  table below stays open (it needs a human in front of the GUI) and does not block the gate --
  see "Why the open table does not block the gate".
- **Date**: 2026-09-11
- **Context**: research.md open item 4 — "Claude Desktop name limits, schema strictness,
  tools/list size behavior → portable tool profile defaults (name ≤64, catalog budget)".
- **Scope**: what real MCP clients do with what we publish. Everything here is a constraint on
  `internal/catalog` (T010, T011) before it generates a single tool from a real specification.

## Why this ADR exists

The catalog is generated once and served to whatever client the operator runs. If a client
silently truncates tool names, rejects a schema keyword or drops a large `tools/list`, the
failure surfaces as "the agent doesn't see my API" — far away from the code that caused it.
So the client's behaviour is captured as data here, and the tool profile is derived from it
rather than from the MCP specification alone.

## Verified in this spike (server side)

Setup: `lotsman serve` over stdio, MCP SDK v1.7.0, exercised three ways — the official Go MCP
client in-process (`internal/mcpserver` tests), the same client driving the real binary as a
subprocess (`cmd/lotsman/e2e_test.go`), and hand-written JSON-RPC frames piped into the binary.

1. **Framing**: newline-delimited JSON, one frame per line, on stdout. Nothing else may be
   written there — all logs go to stderr (enforced by lint rule `forbidigo` outside `cmd/`).
2. **Version negotiation works downward**: a client announcing `2025-06-18` gets
   `"protocolVersion":"2025-06-18"` back from an SDK whose latest is `2026-07-28`. Legacy
   desktop builds therefore do not need a separate code path from us.
3. **`tools/list` carries cache metadata** (`ttlMs`, `cacheScope`) even for a session that
   negotiated `2025-06-18`. Older clients must ignore unknown fields; if one does not, that is
   a client bug we can only work around by pinning the advertised revision. *To watch.*
4. **Input schemas are published with `additionalProperties:false`** when inferred from a Go
   struct. This matches data-model.md invariant 4 (an unknown argument must fail validation,
   never be dropped), and confirms the grouped input schema of T014–T018 can rely on the same
   shape end to end.
5. **Results are duplicated**: a typed handler returns both `structuredContent` and a
   `content[0].text` JSON rendering of it. Clients that predate structured content still show
   something useful. Budget both when sizing responses in T033.
6. **The handshake is strictly ordered**: sending `notifications/initialized` before the
   `initialize` response arrives is logged as `initialized before initialize`. Real clients
   wait; our test harness had to as well.
7. **A client closing stdin mid-flight is normal**, and the SDK reports it as
   `server is closing: EOF` (JSON-RPC code `-32004`) from `Server.Run`, logged at ERROR by the
   SDK's own logger. Taken literally this makes every ordinary quit look like a crash — a
   non-zero exit code makes a desktop client mark the server "failed".
   **Decision**: `mcpserver.ServeStdio` treats `-32004`/`io.EOF` as a clean disconnect (exit 0)
   and demotes the SDK's ERROR line to DEBUG (`internal/mcpserver/sdklog.go`). The SDK exports
   the wire code (`jsonrpc.Error`) but no sentinel error for it, so the code is matched
   numerically — revisit on SDK upgrades.
8. **A third-party client connects to the binary as built**: `claude mcp add lotsman-t004 --
   $PWD/bin/lotsman serve` followed by `claude mcp list` reports `✔ Connected` (Claude Code,
   2026-09-11). This exercises registration, spawn, handshake and shutdown, but not the tool
   *presentation* questions below.
9. **Claude Desktop app round trip confirmed** (2026-09-11): registered `lotsman-ping` in
   `claude_desktop_config.json`, asked the agent to call `ping`. Response contained `pong: true`,
   the correct echo, build version `5d9117d-dirty`, and server timestamp
   `2026-09-11T17:51:12Z`. Confirms the stdio handshake and single-tool round trip work
   end-to-end from the GUI client, not just Claude Code or the in-process test harness. The
   table below still needs a human because its questions (name limits, schema strictness, a
   large `tools/list`, etc.) require more than one tool and larger schemas to exercise —
   revisit once T010/T011 publish a real catalog.

## Verified at the M0 gate (T022)

Measured with the official Go MCP client driving the real binary over stdio, serving the
Kubernetes `apps/v1` corpus document (65 published tools with full grouped input schemas):

10. **A multi-megabyte `tools/list` is not a transport problem.** The 4.26 MB response was
    delivered in 146 ms over stdio, whole and well-formed. The constraint on catalog size is the
    model's context window, not the protocol -- which is why `catalog.serializedBytesEstimate`
    is a report field and why `--mode=search` (002) is a prerequisite for specs of this shape,
    not an optimization. Question 3 below is therefore answered for the wire and still open for
    what a client *does* with a payload that size.
11. **Grouped schemas are accepted as published.** `additionalProperties:false` at every level,
    nested objects, arrays of scalars, enums and the body group all survive the round trip, and
    the SDK validates arguments against them before the handler runs. `format` is the exception:
    JSON Schema treats it as an annotation and the SDK follows suit, which is why lotsman
    validates as well (ADR-0002).
12. **Annotations round-trip unchanged**: `readOnlyHint` and `destructiveHint` arrive as
    published, and nothing in the server reads them back -- policy never depends on a hint
    (Constitution III).

## Why the open table does not block the gate

Every assumption below is coded conservatively: a wrong guess costs a smaller catalog or a
blunter name, never a broken session or an unsafe call. Name length and charset are already
clamped to the strictest plausible client, the catalog budget is measured and reported rather
than assumed, and policy never depends on annotations. The remaining questions change defaults,
not behaviour, so they are answered when a human is in front of the application rather than
holding up Phase B.

## Open — to confirm on a desktop client

These need a human in front of the application; they decide the portable tool profile defaults
(T010, T011) and must be answered before the M0 gate.

| # | Question | Assumption we are coding against | Confirmed? |
|---|---|---|---|
| 1 | Maximum tool name length before rejection or truncation | 64 chars | ☐ |
| 2 | Accepted tool name charset | `[A-Za-z0-9_.-]` | ☐ |
| 3 | Behaviour on a large `tools/list` (hundreds of tools, ~MB of JSON): rejected, truncated, or just slow? | a catalog budget is required; exact number unknown → drives search mode (002) | ☐ |
| 4 | Schema strictness: are `$ref`/`$defs`, `oneOf`, `additionalProperties:false`, nested objects honoured? | honoured; `$ref` bundle kept rather than inlined (pipeline stage 2) | ☐ |
| 5 | Is `outputSchema`/`structuredContent` used, or only `content[0].text`? | both published | ☐ |
| 6 | How are `annotations.readOnlyHint` / `openWorldHint` surfaced? | UX hint only; policy never depends on them (Constitution III) | ☐ |
| 7 | How is a tool description rendered, and at what length is it cut? | sanitize + per-tool and per-catalog budget (T011) | ☐ |
| 8 | Is `isError: true` shown to the model as text, or swallowed? | shown; error bodies stay bounded (T033) | ☐ |
| 9 | Does the client restart the server on crash, and does it re-read `tools/list` after `notifications/tools/list_changed`? | assume no reload guarantees → catalog is built once per snapshot | ☐ |

## How to run the manual check (10 minutes)

```bash
make build                       # bin/lotsman
```

Claude Desktop — `claude_desktop_config.json`
(`~/.config/Claude/` on Linux, `~/Library/Application Support/Claude/` on macOS,
`%APPDATA%\Claude\` on Windows):

```json
{
  "mcpServers": {
    "lotsman-ping": {
      "command": "/abs/path/to/lotsman",
      "args": ["serve"]
    }
  }
}
```

Restart the application, then:

1. The server appears as connected and publishes exactly one tool, `ping`.
2. Ask the agent: *"call the ping tool with the message hello"* → the result contains
   `"pong": true`, `"echo": "hello"` and the build version.
3. Quit the application → `lotsman` exits 0 and its stderr log ends with `client disconnected`
   (no ERROR lines).
4. Answer as many rows of the table above as the UI reveals, and record the client version.

The same check against Claude Code, without touching any GUI:

```bash
claude mcp add lotsman-ping -- /abs/path/to/lotsman serve
claude mcp list          # expect: lotsman-ping … ✔ Connected
claude mcp remove lotsman-ping
```

## Consequences

- The tool profile is treated as **client-constrained, not spec-constrained**: names ≤64 chars
  from `[A-Za-z0-9_.-]`, collision resolved by a stable hash (T010), descriptions sanitized and
  budgeted (T011).
- Transport hygiene is a test, not a convention: stdout purity and clean-exit behaviour are
  asserted in `cmd/lotsman/e2e_test.go` and grow into the full e2e suite in T039.
- Any SDK upgrade re-runs this checklist; item 7 in particular depends on an unexported
  numeric code.
