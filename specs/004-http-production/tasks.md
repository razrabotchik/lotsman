# Tasks: Production HTTP profile (M3)

**Input**: plan.md, spec.md, docs/spec.md FR-68–75, FR-79–85, §7.4–7.7
**Convention**: [P] = parallelizable. Every task leaves main green. Checkpoints are points at
which the work could stop and still be worth shipping.
**Scope decision**: inbound auth is split — `none` and `static-bearer` land here, the OAuth
resource server (FR-80–82) becomes feature 005. M3's exit criterion *auth tests green* is
therefore only fully met at the end of 005, and the release review says so rather than reading
the criterion down to what shipped.

## Step 1: Transport and its guards  ✅ CHECKPOINT: an MCP client reaches lotsman over HTTP, and a stranger does not

- [x] T301 httpserver: `serve --transport=stdio|http` (default stdio) and `--listen`
      (default `127.0.0.1:8080`), serving `mcp.NewStreamableHTTPHandler` with
      `StreamableHTTPOptions{Stateless: true}` — the sessionless `2026-07-28` profile of FR-69
      → The same `mcpserver.Server` instance backs both transports. A behaviour that differs by
        transport is a bug, so there must be nowhere for one to be written.
      → `--transport=http` without a spec is a usage error, not an empty catalog on a socket
        (plan decision 6).
      → The `server:` section of docs/spec.md §5.1 is read for the first time, `logLevel`
        included — which meant resolving configuration *before* building the logger, since the
        file is now one of the places the level can be stated. A documented example that does not
        parse is a document that lies, and strict decoding leaves no third option.
- [x] T302 bind guard (FR-70): a non-loopback `--listen` with `inboundAuth.mode: none` refuses at
      startup with exit 2, before the socket is opened
      → The opt-in that FR-70 permits is a flag whose name states what it does
        (`--allow-unauthenticated-public-bind`), and it logs a warning on every start, not once.
      → Refusing at startup rather than per request: a deployment that comes up and then denies
        everything looks like an outage; one that will not come up looks like a mistake.
      → A hostname that is not literally `localhost` counts as public even if it resolves to
        127.0.0.1 today. Reading DNS at startup would make the safety of a configuration depend
        on an answer that can change afterwards without the process noticing.
- [x] T303 header guards (FR-71): `Origin` and `Host` are checked before any handler runs, against
      a configured allowlist that defaults to the bind address
      → This is the DNS-rebinding defence on the *inbound* side, and it must not borrow
        `internal/egress`: the two guards protect opposite directions and sharing code would let
        a change to one silently move the other.
      → Two of the three guards are somebody else's implementation: the SDK already rejects a
        request that arrived over loopback carrying a non-loopback Host, and `net/http`'s
        `CrossOriginProtection` already implements cross-origin rejection. A second opinion about
        either would be a second thing to keep correct, and the one that drifted would be the one
        nobody noticed.
      → An allowed origin the operator wrote down wrong is a startup error, not an origin quietly
        dropped from a list they believe is in force.
- [x] T304 graceful shutdown (FR-75): stop accepting, drain in-flight calls within a timeout,
      cancel what remains, close cleanly
      → A drain that outlives its budget is a hung deployment; a drain of zero is a dropped
        mutation. The budget is detached from the cancelled context it was granted under — a
        drain whose deadline has already passed is not a drain.
- [x] T305 [P] the stdio end-to-end suite runs over both transports from one table, unchanged
      → Including the zero-RoundTrip refusals and the approval round trip. 003's approval keeps no
        state between round trips (`internal/mcpserver/approval.go` never reads the SDK's
        client-supplied `RequestState`), which is what makes it correct here; the test asserts the
        prompt actually ran, because one that only checked the POST arrived would pass just as
        happily if approval had quietly stopped running over this transport.
      → US-5 came for free and is asserted now rather than at the end of step 3: two processes
        behind an alternating proxy answer one session's worth of traffic
        (`TestHTTPIsStatelessAcrossReplicas`). §7.7's deployment is a test rather than a diagram.

## Step 2: Inbound authorization  ✅ CHECKPOINT: a deployed endpoint refuses an unauthenticated caller

- [x] T306 config: `inboundAuth: {mode: none|static-bearer|oauth, token: <secret ref>,
      trustedProxies: [...]}` with strict decoding
      → `mode: oauth` is refused as a capability this build does not have (exit 4, naming feature
        005), never ignored and never silently downgraded to `none`. A config written against the
        frozen spec must fail loudly on a binary that cannot honour it.
      → That exit code turned up a bug of its own: every command mapped a configuration error to
        exit 2 unconditionally, so a missing capability would have read as a misspelling. A
        config error now carries its class into the exit code.
      → A `tokenRef` set while the mode is `none` is refused too. A configured secret that
        authenticates nothing is not a default worth guessing at.
- [x] T307 inbound: a static-bearer `auth.TokenVerifier` behind the SDK's `RequireBearerToken`
      → Both sides are hashed before the comparison. Comparing raw values in constant time still
        leaks their length, and a length is a genuinely useful thing to learn about a secret you
        are guessing at; comparing digests costs one hash and leaks nothing.
      → The token arrives through the existing secret-reference machinery and joins the redaction
        registry like every other credential, even though it points the other way.
      → `AllowMissingExpiration` is on, and that is about the shape of the credential rather than
        its lifetime: the SDK rejects a token that states no expiry, which is right for an issued
        token and impossible for a configured one.
      → An empty or absent token in `static-bearer` mode is a startup error. "Authentication
        configured, no secret" must never resolve to "everyone is authenticated".
- [x] T308 challenges and proxies: 401/403 carry the metadata and scope hints FR-84 requires and
      nothing about internal policy; `X-Forwarded-*` is honoured only from configured proxies
      (FR-85)
      → A challenge that explains why a call was denied is a policy oracle for anyone who can
        reach the port. `WWW-Authenticate: Bearer` and nothing more: no realm naming the
        deployment, and no error description telling a stranger whether the token was missing or
        wrong. The SDK emits no challenge at all without metadata or scopes to advertise, so a
        bare 401 had to be given the scheme it was missing.
      → Forwarding headers are *removed* from an untrusted peer, not ignored. "Ignore them" is a
        rule every future reader of the request has to know; deleting them means a later feature
        that reads `X-Forwarded-For` gets the truth without having been told to be careful.
- [x] T309 [P] FR-83 canary: an inbound bearer token appears in no upstream request, and in none of
      the four channels the existing canary test already covers
      → Inbound identity is permission to talk to lotsman, never an upstream credential. The
        assertion is a test rather than a comment because the two tokens live in one process and
        both travel in an `Authorization` header, one inbound and one outbound.
      → The other half of FR-70 is now demonstrable and asserted: a public bind starts once
        something authenticates it. The rule was never about public interfaces as such.

## Step 3: Hot reload  ✅ CHECKPOINT: acceptance criterion 9

- [x] T310 mcpserver: reach the catalog through a snapshot taken once at the start of a call
      → §7.6 step 7: calls in flight finish on the catalog they started on. A handler that
        re-reads a pointer mid-call can observe two catalogs in one request, which is the failure
        reload is supposed to prevent rather than introduce.
      → The snapshot landed one layer further out than this task assumed, and `mcpserver` needed
        no change at all: a `Catalog` was already immutable and its runners already hold the tools
        they were built with. What moves is the whole server, resolved per request by the
        transport. The task was written expecting to edit the wrong package.
- [x] T311 reload: build candidate → parse, normalize, validate *completely* → digest and diff
      summary → atomic publish only if the digest changed (FR-72, §7.6)
      → A candidate that fails at any step leaves the working catalog serving and is reported. The
        test that matters feeds a deliberately broken document to a running server and asserts the
        next call still succeeds.
      → An unchanged digest publishes nothing: FR-74's cache hints are a promise that a digest
        moves only on a substantive change.
      → Swapping the server rather than editing its tool list is what makes this atomic at all.
        `AddTool`/`RemoveTools` on a live server are two critical sections, and a call arriving
        between them finds no tool — a half-applied reload, which is worse than none.
      → Which decides where reload is available: a transport that resolves its server once per
        session cannot be handed a new one, so this is an HTTP property and `--watch` over stdio
        is refused rather than quietly ignored.
- [x] T312 triggers: `SIGHUP` and an explicit `--watch` with debounce (plan decision 2)
      → No admin endpoint. A reload endpoint is a new authenticated write surface and belongs to
        the control-plane discussion, not to a transport.
      → Polling, not a filesystem notification API: that would be a dependency for the job of
        noticing a file every couple of seconds, and a poll behaves the same through every kind of
        mount, including the container volumes this is for.
      → The debounce waits for the file to stop changing rather than firing on first movement. A
        document mid-write parses as a broken document, and reporting that failure would be
        reporting something that was never true.
- [x] T313 [P] FR-73/FR-74: `tools/list_changed` after a successful publish on stdio and legacy
      stateful connections; the stateless profile gets cache hints and a documented silence
      → Stating that the sessionless profile cannot push is more useful than implying a
        notification an operator would wait for.
      → And the stdio half of this task turned out not to exist: reload is an HTTP property (see
        T311), so there is no transport in this build that both reloads and has a connection to
        notify. `tools/list_changed` is deferred with the reason written down (plan decision 3,
        revised) rather than implemented for a case that cannot arise.
      → FR-74 therefore carries the whole weight, and it is two things, not one: `ttlMs` says when
        to ask again, and the catalog digest in `_meta` says whether the answer changed. The
        second is the honest half — reload refuses to republish an identical candidate, so a
        digest moves only when the catalog does.

## Step 4: Audit and metrics  ✅ CHECKPOINT: an executed call leaves a record, and no record leaks

- [x] T314 audit: the event type — schema version, operation key, effect, decision, origin without
      query, status, timing, sizes (§7.4) — written at the last stage of the pipeline (§7.5)
      → The version ships with the first event, on the report's precedent: anything anyone may
        parse is a promise, and versions are cheap before there are readers.
      → `internal/audit` has been a `doc.go` since 001 and gets its type now, with a sink, rather
        than earlier without one.
      → The record wraps the call rather than living inside it, so there is no return path out of
        a call that skips it — including the ones added later.
      → `input_required` is its own decision, not a refusal: waiting for a human means nothing
        reached the network and nothing was denied either, and a reader chasing an incident needs
        to tell those apart.
      → The path is the *template*, never the expansion. An expanded path carries the caller's
        arguments, and an argument is data lotsman was trusted with rather than data it may write
        down.
- [x] T315 sink: structured records through the process logger, never stdout (FR-68 does not
      lapse because a second transport exists)
      → Through the logger rather than beside it. Its outermost handler is the redaction filter
        (FR-61) and it is bound to stderr, so an audit record inherits both properties instead of
        re-deriving them — and a sink that opened its own writer would eventually get one wrong.
        That is also why the records are slog attributes rather than the JSON this task first
        said: the format follows the channel, and the channel is the one that redacts.
- [x] T316 the canary test grows a fifth channel — the audit sink — with the live credential
      → The record is checked for existence before it is checked for the canary: a channel nothing
        was written to is a channel nothing can leak through, and asserting against one proves
        nothing.
- [x] T317 [P] `/metrics` in the Prometheus text format, served in the HTTP profile only and on the
      operator's chosen bind, with no client library (plan decision 4, Constitution VII)
      → Counted from the same events the audit log records. A metric and a log line that disagree
        about how many calls were refused are worse than either alone.
      → Behind every guard the MCP endpoint is behind, inbound authorization included. A list of
        which operations an agent has been calling is not public information, and an endpoint left
        open because nobody thought about it is the failure this transport exists to avoid.
      → Statuses are counted by class. An API with a hundred distinct codes would otherwise turn
        one counter into a hundred time series to answer "are calls failing".
      → Two scrapes that saw the same events are byte-identical, and label values are escaped:
        every value today is build metadata or a digest, but a value that came from somewhere else
        one day must not be able to forge a sample line.

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
