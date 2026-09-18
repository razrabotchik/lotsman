# Tasks: Production HTTP profile (M3)

**Input**: plan.md, spec.md, docs/spec.md FR-68–75, FR-79–85, §7.4–7.7
**Convention**: [P] = parallelizable. Every task leaves main green. Checkpoints are points at
which the work could stop and still be worth shipping.
**Scope decision**: inbound auth is split — `none` and `static-bearer` land here, the OAuth
resource server (FR-80–82) becomes feature 005. M3's exit criterion *auth tests green* is
therefore only fully met at the end of 005, and the release review says so rather than reading
the criterion down to what shipped.

## Step 1: Transport and its guards  ✅ CHECKPOINT: an MCP client reaches lotsman over HTTP, and a stranger does not

- [ ] T301 httpserver: `serve --transport=stdio|http` (default stdio) and `--listen`
      (default `127.0.0.1:8080`), serving `mcp.NewStreamableHTTPHandler` with
      `StreamableHTTPOptions{Stateless: true}` — the sessionless `2026-07-28` profile of FR-69
      → The same `mcpserver.Server` instance backs both transports. A behaviour that differs by
        transport is a bug, so there must be nowhere for one to be written.
      → `--transport=http` without a spec is a usage error, not an empty catalog on a socket
        (plan decision 6).
- [ ] T302 bind guard (FR-70): a non-loopback `--listen` with `inboundAuth.mode: none` refuses at
      startup with exit 2, before the socket is opened
      → The opt-in that FR-70 permits is a flag whose name states what it does
        (`--allow-unauthenticated-public-bind`), and it logs a warning on every start, not once.
      → Refusing at startup rather than per request: a deployment that comes up and then denies
        everything looks like an outage; one that will not come up looks like a mistake.
- [ ] T303 header guards (FR-71): `Origin` and `Host` are checked before any handler runs, against
      a configured allowlist that defaults to the bind address
      → This is the DNS-rebinding defence on the *inbound* side, and it must not borrow
        `internal/egress`: the two guards protect opposite directions and sharing code would let
        a change to one silently move the other.
- [ ] T304 graceful shutdown (FR-75): stop accepting, drain in-flight calls within a timeout,
      cancel what remains, close cleanly
      → A drain that outlives its budget is a hung deployment; a drain of zero is a dropped
        mutation. Both are configurable and both are logged with the count.
- [ ] T305 [P] the stdio end-to-end suite runs over both transports from one table, unchanged
      → Including the zero-RoundTrip refusals and the approval round trip. 003's approval keeps no
        state between round trips (`internal/mcpserver/approval.go` never reads the SDK's
        client-supplied `RequestState`), which is what makes it correct here; a test must fail if
        that ever changes.

## Step 2: Inbound authorization  ✅ CHECKPOINT: a deployed endpoint refuses an unauthenticated caller

- [ ] T306 config: `inboundAuth: {mode: none|static-bearer|oauth, token: <secret ref>,
      trustedProxies: [...]}` with strict decoding
      → `mode: oauth` is refused as a capability this build does not have (exit 4, naming feature
        005), never ignored and never silently downgraded to `none`. A config written against the
        frozen spec must fail loudly on a binary that cannot honour it.
- [ ] T307 inbound: a static-bearer `auth.TokenVerifier` behind the SDK's `RequireBearerToken`
      → Constant-time comparison; the token arrives through the existing secret-reference
        machinery and is registered for redaction like every other credential.
      → An empty or absent token in `static-bearer` mode is a startup error. "Authentication
        configured, no secret" must never resolve to "everyone is authenticated".
- [ ] T308 challenges and proxies: 401/403 carry the metadata and scope hints FR-84 requires and
      nothing about internal policy; `X-Forwarded-*` is honoured only from configured proxies
      (FR-85)
      → A challenge that explains why a call was denied is a policy oracle for anyone who can
        reach the port.
- [ ] T309 [P] FR-83 canary: an inbound bearer token appears in no upstream request, and in none of
      the four channels the existing canary test already covers
      → Inbound identity is permission to talk to lotsman, never an upstream credential. The
        assertion is a test rather than a comment because the two token values sit in one process.

## Step 3: Hot reload  ✅ CHECKPOINT: acceptance criterion 9

- [ ] T310 mcpserver: reach the catalog through a snapshot taken once at the start of a call
      → §7.6 step 7: calls in flight finish on the catalog they started on. A handler that
        re-reads a pointer mid-call can observe two catalogs in one request, which is the failure
        reload is supposed to prevent rather than introduce.
- [ ] T311 reload: build candidate → parse, normalize, validate *completely* → digest and diff
      summary → atomic publish only if the digest changed (FR-72, §7.6)
      → A candidate that fails at any step leaves the working catalog serving and is reported. The
        test that matters feeds a deliberately broken document to a running server and asserts the
        next call still succeeds.
      → An unchanged digest publishes nothing: FR-74's cache hints are a promise that a digest
        moves only on a substantive change.
- [ ] T312 triggers: `SIGHUP` and an explicit `--watch` with debounce (plan decision 2)
      → No admin endpoint. A reload endpoint is a new authenticated write surface and belongs to
        the control-plane discussion, not to a transport.
- [ ] T313 [P] FR-73/FR-74: `tools/list_changed` after a successful publish on stdio and legacy
      stateful connections; the stateless profile gets cache hints and a documented silence
      → Stating that the sessionless profile cannot push is more useful than implying a
        notification an operator would wait for.

## Step 4: Audit and metrics  ✅ CHECKPOINT: an executed call leaves a record, and no record leaks

- [ ] T314 audit: the event type — schema version, operation key, effect, decision, origin without
      query, status, timing, sizes (§7.4) — written at the last stage of the pipeline (§7.5)
      → The version ships with the first event, on the report's precedent: anything anyone may
        parse is a promise, and versions are cheap before there are readers.
      → `internal/audit` has been a `doc.go` since 001 and gets its type now, with a sink, rather
        than earlier without one.
- [ ] T315 sink: structured JSON on stderr, never stdout (FR-68 does not lapse because a second
      transport exists)
- [ ] T316 the canary test grows a fifth channel — the audit sink — with the live credential
- [ ] T317 [P] `/metrics` in the Prometheus text format, served in the HTTP profile only and on the
      operator's chosen bind, with no client library (plan decision 4, Constitution VII)

## Step 5: Image and conformance  ✅ CHECKPOINT: criteria 10 and 11, and with them all eleven

- [ ] T318 distroless nonroot image on a read-only root filesystem, built in CI, reporting its
      injected version
- [ ] T319 release wiring: the image joins the checksums and SBOM `.goreleaser.yaml` already
      produces
- [ ] T320 the SDK's `conformance/` suite against the real binary, over both transports
      → Run this early rather than at the end: it tests the protocol, not lotsman's policy, and a
        refusal this codebase considers correct could still be a conformance failure. Discovering
        that in the last task of the last step is the expensive way (plan risk 4).
- [ ] T321 [P] docs: ADR for the transport and its bind rules, ADR for the reload model, README,
      and criteria 9/10/11 updated in docs/release-v0.1.0-alpha.md with the evidence that makes
      each one checkable
