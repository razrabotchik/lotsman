# ADR-0020: Bounds that were nominal, made actual

- **Status**: Accepted
- **Date**: 2026-10-10
- **Decision owner**: docs/improvements.md P2 items 6–12 and 14; FR-14–17, FR-32, FR-35, FR-36, FR-48

## Context

The P2 review found limits that were stated and not held: a describe budget the result could
exceed by four times, a body read with no time limit, a call count with no limit, a token refresh
that could throw away the token it was meant to keep, a redaction rule that accepted credentials it
then did not redact, and an audit log that recorded only the calls that got far enough. Each of the
decisions below makes a stated bound an enforced one; none relaxes a control.

## Decision

1. **`describe_operation` is bounded on the wire, and large schemas come in pages.** The budget is
   48 000 bytes for the whole result as sent — structured content plus its text copy, which is a
   JSON string and therefore larger. A schema that does not fit without its prose is split into
   pages: argument groups, then `$defs` by name, packed in a fixed order, each page a fragment of
   the same document. A part too large for any page is listed as `undescribable` with its size.
   The handler builds the result itself: the SDK's typed output path re-encodes through `float64`,
   which rounded schema bounds past 2^53. That costs `describe_operation` its inferred
   `outputSchema`.

2. **A request has 30 s to arrive in full** (`http.Server.ReadTimeout`). It bounds the read and
   nothing after it; a test holds that a handler outliving it keeps its context.

3. **`execution.maxConcurrentCalls`, default 64, process-wide.** A call over it is refused at once
   with class `policy`, before the network; it is not queued. One limit for the process, so a
   reload cannot reset it. In-flight calls and the limit are exported as gauges.

4. **One `execution.timeout` per logical call.** A mint, the request, and the FR-35 retry share it.

5. **A 401 names the token it refused.** Minted tokens carry a generation; the transport records
   which one a request carried, and invalidation drops the cache only if it still holds that one.
   A late 401 keeps the fresh token, and twenty 401s for one token cost one mint. Waiting for
   another caller's mint is abandoned when the call is.

6. **A credential shorter than six characters is refused at resolution**, naming its reference.
   Redaction cannot hide it without mangling everything else; accepting it meant printing it.
   Usernames are exempt. Redaction replaces longest values first, in one pass.

7. **Every operation call is audited once**, including those refused before a runner: arguments
   the SDK's schema check rejects, unknown tools, unresolvable search-mode ids (`refused`,
   `usage`). Describe and search tools are not operation calls and leave no record. Call duration
   is a histogram rather than a sum and a count.

8. **Tool names are unique by construction and by check.** Built-in names are reserved, every
   collision-suffixed name is re-checked, and the server refuses — loudly — to register a second
   tool under a taken name rather than let the SDK replace one.

9. **`body: null` is a body.** It is sent as JSON `null` where the schema allows it and refused by
   validation where it does not; it is never silently treated as absent.

## Consequences

- `describe_operation` gained `page` (input) and `page`, `pages`, `note`, `undescribable`
  (output); search mode's `tools/list` is 4 656 bytes, measured in docs/corpus.md.
- The configuration gained one field outside the specification's example; README says so.
- Not done: an outbound rate limit (no scope has been chosen to limit by), and counters for mints
  and reloads.
