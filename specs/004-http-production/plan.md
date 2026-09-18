# Implementation Plan: Production HTTP profile (M3)

**Branch**: `004-http-production` | **Spec**: ./spec.md | **Frozen spec**: docs/spec.md §4.10, §7.4–7.7, §8

## Summary

004 takes a runtime that one person starts by hand and makes it something an operator deploys:
reachable over HTTP, authenticated at the door, replaceable without a restart, observable after the
fact, and shipped as an image. The order below is not the order the roadmap lists them in, because
one of these gates all the others — an endpoint that is reachable before it can refuse a stranger
is the one mistake this feature can make that the rest of the codebase cannot undo.

So: the transport and its guards first, together, in one checkpoint. Nothing about
`--transport=http` lands without the bind rule and the header checks in the same step.

## Technical Context

- **No new module for the transport.** `mcp.NewStreamableHTTPHandler` with
  `StreamableHTTPOptions{Stateless: true}` is in go-sdk v1.7.0, already a direct dependency; in
  stateless mode each POST gets a temporary session and GET/DELETE answer 405, which is the
  `2026-07-28` profile FR-69 names.
- **Approval already survives statelessness, by accident of having been done right.** 003 returns
  an input-required result and re-runs the handler when the client calls again with the answer
  (ADR-0013); `internal/mcpserver/approval.go` never stores anything between round trips and never
  reads the SDK's `RequestState`. Over a stateless transport that is the difference between working
  and not. It needs an assertion, not a redesign — and the client-controlled `RequestState` must
  stay unused, since a value the caller supplies cannot carry a permission.
- **Inbound auth is half-shipped in the SDK.** `auth.RequireBearerToken` (middleware, challenge
  construction) and `auth.ProtectedResourceMetadataHandler` exist; `auth.TokenVerifier` is a
  function *we* supply. Static-bearer is a constant-time compare behind that interface. OAuth mode
  is a JWKS/introspection verifier — real work, and where a dependency decision lands
  (`golang-jwt/jwt/v5` is already in the SDK's own graph, so it would become a direct require
  rather than a new module in the build).
- **Outbound egress is done and must not be re-opened.** `internal/egress` already holds criterion
  8. 004 adds inbound guards; the two live on opposite sides of the process and share no code.
- **Testing**: the stdio e2e suite is the specification of correct behaviour, so the HTTP work is
  measured by making it run over both transports rather than by new assertions of its own. Beyond
  that: a two-process round-robin e2e (US-5), a reload test whose candidate is deliberately broken,
  a canary through the audit sink, and the SDK's `conformance/` suite against the real binary.

## Architecture

```text
cmd/lotsman           serve --transport, --listen, --inbound-auth; startup refusal for an
                      unauthenticated non-loopback bind
internal/httpserver   NEW: the stateless handler, bind/Origin/Host guards, graceful drain
internal/inbound      NEW: TokenVerifier implementations (static bearer; oauth), PRM handler,
                      trusted-proxy handling for X-Forwarded-*
internal/catalog      unchanged; Build already returns an immutable value
internal/reload       NEW: candidate build, validate, digest-compare, atomic pointer swap
internal/mcpserver    reads the catalog through a snapshot accessor instead of a field
internal/audit        event type + stderr sink, written at the last pipeline stage (§7.5)
```

`internal/mcpserver` changing how it reaches the catalog is the one intrusive edit, and it is the
whole of reload's cost: everything else in that package already treats the catalog as immutable, so
the change is from "a catalog" to "the catalog as of when this call started" (§7.6, step 7).

## Proposed decisions on the spec's open questions

These are proposals. Questions 1 and 4 change the size of the feature and should be confirmed
before tasks.md exists.

1. **Split inbound auth: `none` and `static-bearer` here, `oauth` as its own feature (005).**
   FR-79 already ranks them that way, and the checkpoint convention (X) says a step must be
   shippable. A deployment behind an ingress that terminates OAuth is a real deployment, and
   static-bearer serves it honestly. The cost is that M3's exit criterion — *auth tests green* — is
   only fully met at the end of 005, which should be said out loud rather than quietly redefined.
   `inboundAuth.mode: oauth` is refused with "not implemented in this build" rather than absent, so
   a config written against the frozen spec fails loudly.
2. **Reload triggers on `SIGHUP` and on an explicit `--watch`, not on an endpoint.** A signal is
   free, has no surface, and is what a sidecar or a config-map reloader already sends. A watch is
   the same code path behind a debounce. An admin endpoint is a new authenticated surface with
   write semantics, and it belongs to the control-plane discussion (M6), not to a transport.
3. **FR-73's notification is honest about the transport.** Stdio and legacy stateful connections
   get `tools/list_changed` after a successful publish. The stateless profile gets FR-74's cache
   hints and nothing else, because there is no connection to push to — and the docs say so rather
   than implying a guarantee the profile cannot make.
4. **Metrics without a client library.** A `/metrics` endpoint in the Prometheus text format,
   generated from counters the runtime already has reasons to keep, served only in the HTTP profile
   and only on the bind the operator chose. Constitution VII, and U13 asks for aggregated
   statistics rather than for a particular exposition library.
5. **The audit event carries a schema version from its first line.** The report's precedent
   applies: anything anyone might parse is a promise, and versions are cheap before there are
   readers and expensive after.
6. **`--transport=http` without a spec is refused.** Over stdio an empty catalog is a developer
   convenience with one user. On a listening socket it is a reachable endpoint that answers
   `tools/list` with nothing, which is indistinguishable from a broken deployment — and reload is a
   replacement mechanism, not a bootstrap one.

## Checkpoints

Each is a point where the work could stop and still be worth shipping.

1. **Transport + guards.** `--transport=http` serving stateless Streamable HTTP, loopback default,
   startup refusal for an unauthenticated non-loopback bind, Origin/Host checks, graceful drain.
   The stdio e2e suite runs over HTTP unchanged.
2. **Inbound auth (none | static-bearer).** Bearer middleware, FR-84 challenges, trusted-proxy
   rules, and the FR-83 canary proving an inbound token never reaches an upstream request.
3. **Hot reload.** Copy-on-write publish, a broken candidate leaving the working catalog intact,
   in-flight calls finishing on their snapshot — acceptance criterion 9.
4. **Audit + metrics.** The event, the stderr sink, the fifth canary channel, `/metrics`.
5. **Image + conformance.** Distroless nonroot on a read-only root filesystem, built in CI with the
   existing checksums and SBOM; the SDK conformance suite against the real binary — criteria 10
   and 11, and with them the last of the eleven.

Criterion: after 1–3, two replicas behind round-robin (US-5) is demonstrable, which is the
roadmap's own exit test.

## Constitution Check

- **I (exactness)**: a transport neither adds nor removes a supported operation. The catalog built
  for HTTP is byte-identical to the one built for stdio, and a golden test says so. ✔
- **II (fail closed)**: every new refusal is a refusal — an unauthenticated non-loopback bind does
  not start, a foreign Origin does not reach a handler, an unverifiable token does not reach the
  catalog, a failed reload does not publish. ✔
- **III (approval is not a boundary)**: unchanged and re-asserted over the new transport; inbound
  identity is explicitly not upstream authority (FR-83). ✔
- **IV (determinism)**: the digest must be identical across replicas or FR-74's cache hints are
  wrong; asserted by building the same document in two processes. ✔
- **VI (secrets)**: the audit sink becomes the fifth channel the canary test covers, before it has
  a second consumer. ✔
- **VII (dependencies)**: zero new modules for checkpoints 1–5 under the proposals above; the
  decision that would add one (OAuth verification) is deferred to 005 where it can be argued on its
  own merits. ✔
- **IX (no premature abstraction)**: `internal/audit` gets a type when it gets a sink, not before —
  which is the reason 003 left it empty. ✔
- **X (main stays green)**: five checkpoints, each a runnable server.

## Risks

1. **An HTTP endpoint is a new attack surface, and the old ones were all local.** Mitigated by
   ordering: bind rules and header checks ship in the same checkpoint as the listener, never after
   it, and the dangerous opt-in FR-70 permits is a flag whose name says what it does.
2. **Reload is where immutability goes to die.** A catalog reached through a pointer invites a
   caller to re-read it mid-call and observe two different catalogs in one request. Mitigated by
   taking the snapshot once, at the start of a call, and passing the value — the same discipline
   the overlay already follows in `catalog.Build`.
3. **The stateless profile makes approval's correctness load-bearing.** If any part of the approval
   round trip ever comes to depend on process memory, it will work on one replica and fail behind a
   load balancer — intermittently, which is the worst way to find out. Mitigated by the round-robin
   e2e running a mutation *through* the approval round trip, across two processes.
4. **Conformance may fail on things 001–003 decided.** The suite tests the protocol, not lotsman's
   policy, and a refusal lotsman considers correct could be a conformance failure. Discovering that
   late would be expensive; the suite should be run early, read once, and its verdicts triaged
   before checkpoint 5 depends on them.
