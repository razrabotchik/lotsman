# ADR-0013: Interactive approval over multi-round-trip requests

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: feature 003 (M2 tail), tasks T209–T211; acceptance criterion 6

## Context

FR-44–46 require a confirmation before a mutating call, defaulting to `always`, failing closed
when the client cannot be asked. Constitution II states the same thing as a principle, and
docs/spec.md §13.6 as an acceptance criterion — the last one that was still unmet.

The obvious implementation, `ServerSession.Elicit` inside the tool handler, does not work and
should not: on protocol `2026-07-28` the SDK refuses it outright —

> "elicitation/create" cannot be sent while serving a request on protocol version 2026-07-28:
> return an InputRequests map instead (multi round-trip requests, SEP-2322)

which is exactly what FR-44 already said ("input-required/MRTR"), discovered from the other end.

## Decision

1. **Approval is a multi-round-trip input request.** The tool handler returns a `CallToolResult`
   carrying one `ElicitParams` under the id `lotsman.approval`. The client fulfils it and calls
   the tool again with the answer attached; the second call re-runs validation, request building
   and the egress check from the top and then reads the answer. Nothing is held open between the
   two: there is no wait to time out, and a client that never comes back has simply not made the
   call.
2. **Approval runs last, after everything that can refuse without a human.** Arguments are
   validated, the request is built, the egress policy is checked — and only then is anyone asked,
   so a prompt is only ever shown for a call that would otherwise happen, and the only thing a yes
   does is send it.
3. **Reads are never submitted for approval**, whatever the mode. The policy that let a read
   through is the one that classified it, and a prompt on every GET is how an operator learns to
   accept without reading.
4. **Fail-closed is the default.** `always` (the default) plus a client that declared no
   elicitation capability is a refusal with `approval_unavailable`, before the network, naming the
   setting that would change it. `client-capability` is the mode for an operator who decided
   otherwise and wrote it down; `never` turns the prompt off entirely. A decline, a cancel and a
   malformed answer are all refusals: only `accept` proceeds.
5. **It is not a security boundary, and the code says so.** The answer is whatever the client
   sent; a client may produce an `accept` without showing anything to anyone, and lotsman cannot
   tell. That is FR-44a. The gate — effect policy plus the operator's rules — has already run by
   the time anyone is asked, and approval can only subtract from what it allowed.
6. **The prompt shows identity, effect, target and a redacted argument summary** (FR-46). Path and
   query values are shown, because approving the deletion of "a droplet" is not a decision; header
   and cookie names are shown without values, because that is where a credential travels; the body
   is summarized by its field names only, because it is unbounded and it is the likeliest place
   for something nobody wants on a screen. The whole summary passes through `redact.String` and is
   capped: a prompt nobody can read is a prompt everybody accepts.
7. **It logs, it does not audit.** The decision reaches the structured stderr logger with the
   operation key, the effect and the outcome — never the arguments. `internal/audit` stays a
   `doc.go` until M3 gives it a sink; an event format with one producer and no reader would be an
   abstraction ahead of its use (Principle IX).

## Consequences

- Acceptance criterion 6 is met, proved end to end against the real binary over stdio:
  `TestServeMutationPolicyEndToEnd/fails_closed_when_the_client_cannot_be_asked` asserts the
  refusal and that the upstream received nothing.
- Every test that executes a mutation now needs a client that answers, which is the honest shape
  of the change: enabling mutations no longer means calls just happen.
- A client on an older protocol is served by the SDK's server-side middleware, which fulfils the
  input request by calling `elicitation/create` itself. Both paths end in the same handler.
- `--approval` on `serve` mirrors `execution.interactiveApproval` for the operator who wants to
  state it on the command line.
