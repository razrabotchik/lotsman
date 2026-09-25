# v0.1.0-alpha: acceptance review

**Nothing is tagged.** This is the review that would precede a tag: what holds, what does not, and
where the evidence is. Cutting the release is a separate decision.

The frozen specification lists eleven acceptance criteria for "the first stable core version"
(docs/spec.md §13). The honest statement is which of them hold now, which are out of scope for an
alpha, and where the evidence is. A criterion is not marked met because someone remembers
implementing it; it is marked met because a test fails when it stops being true.

Scope of this review: features 001 (core runtime), 002 (search mode), 003 (selection, overrides
and interactive approval — the M2 tail), 004 (the production HTTP profile — M3) 005 (inbound
OAuth — the M3 tail) and 006 (service OAuth upstream — M4a), all complete — 45, 14, 12, 21, 15 and
12 tasks respectively.

| # | Criterion | Status | Evidence |
|---|---|---|---|
| 1 | `inspect` deterministically explains support, with JSON output | **met** | `TestInspectJSONContract`, `TestInspectIsDeterministic`, `TestReportIsByteIdenticalAcrossRuns` |
| 2 | Local OAS 3.0 and 3.1 work over stdio; GET and JSON POST serialize correctly | **met** | `TestAcceptanceUS1` (live server, both verbs in one session) |
| 3 | Path/query arrays and required parameters covered e2e; unsupported styles never approximated | **met** | `TestAcceptanceUS1` (repeated-key arrays), `TestSerializationTable`, `TestUnsupportedStylesRejectTheOperation` |
| 4 | In read-only mode, write/destructive/unknown never reach the upstream | **met** | `TestServeMutationPolicyEndToEnd`, `TestMutationBlockedByDefault` (zero RoundTrips asserted), and per-operation rules on top: `TestDeniedByRuleNeverReachesTheNetworkInToolsMode`, `TestAllowListRefusesTheUnlistedMutationEndToEnd` |
| 5 | Search mode cannot call a mutation through a read tool | **met** | `TestCallReadOperationRefusesNonReads` (zero RoundTrips for a POST and a DELETE, called with valid arguments so the effect gate is the only thing that can refuse, and asserted to refuse *by name*), `TestServeSearchModeEndToEnd`, `TestMutatingToolIsAbsentWhenMutationsAreNotAllowed` |
| 6 | Interactive approval fails closed without client capability | **met** | `TestServeMutationPolicyEndToEnd/fails_closed_when_the_client_cannot_be_asked` (real binary over stdio, zero upstream requests), `TestApprovalFailsClosedWithoutClientCapability`, `TestApprovalFailsClosedInSearchMode`, `TestDeclinedApprovalRefusesBeforeTheNetwork` |
| 7 | A canary secret appears in no stdout, stderr, result, error or report | **met** | `TestCanarySecretNeverLeaks` (four channels, live credential), `internal/redact` |
| 8 | A redirect to another origin or a private address is blocked; Authorization is not forwarded | **met** | `TestClientDeniesRedirects`, `TestHostnameResolvingIntoAPrivateRangeIsRefused`, `TestHostnameIsNeverTreatedAsLiteral`, `TestResolvedSecretsAreRegisteredForRedaction` (the credential is applied by the innermost round tripper, so no layer above a refused hop ever holds it) |
| 9 | Hot reload does not drop a working catalog on an invalid candidate | **met** | `TestABrokenDocumentDoesNotDisturbAServingCatalog` (a running server is handed a document that is not a document; the next call is served by the catalog that was already working), `TestABrokenCandidateLeavesTheWorkingCatalogServing`, `TestWatchRepublishesAChangedDocument`, `TestSIGHUPReloads` |
| 10 | MCP conformance for the selected protocol revisions is green | **partial** | `TestHTTPConformance` and `TestStdioConformance` speak JSON-RPC to the real binary on both transports — the sessionless `2026-07-28` profile, the legacy handshake, method and header rules, unknown methods, malformed input. The cross-SDK conformance runner from the modelcontextprotocol project is external tooling and has still not been run |
| 11 | The release carries checksums and an SBOM; the container runs nonroot | **partial** | `.goreleaser.yaml` produces checksums and SBOMs, verified with `goreleaser check` and a snapshot build whose binary reports the injected version. The image is built and exercised on every change (`make image-check`, CI `image` job): distroless, `nonroot:nonroot`, running under `--read-only --network none`. Nothing is published to a registry — see below |

## What this would release

A local stdio runtime that turns an OpenAPI document into MCP tools and refuses, by name, what it
cannot translate exactly. Measured against vendor specifications (docs/corpus.md): Kubernetes
`apps/v1` 65 of 77 operations, DigitalOcean 631 of 659, Stripe 0 of 559 for reasons that are named
in the support matrix rather than discovered at run time.

Two publication modes. Tools mode gives one tool per operation. Search mode gives five meta-tools
over a local lexical index for catalogs that do not fit a model's context — a constant 5.5 KB of
`tools/list` whether the API has 65 operations or 631 — with the effect gate re-applied on every
call, so a search result is discovery and never permission.

Four levers between "everything" and "nothing": a tag filter on what is published, per-operation
overrides that can state an effect the heuristic would not guess, allow/deny rules by namespace,
operation, tag and effect, and a confirmation prompt before every mutating call that refuses when
the client cannot be asked. Every one of them is accounted for in the report: an operation
excluded by a filter is *reported* as excluded, never quietly absent.

## What 004 added

An HTTP deployment. `serve --transport=http` publishes the stateless Streamable HTTP profile of
MCP `2026-07-28`, and "stateless" is a tested property rather than a flag: two processes behind an
alternating proxy serve one session's worth of traffic, approval round trip included. The endpoint
binds loopback unless told otherwise, refuses to start on a public interface with nothing
authenticating it, and checks `Origin` and `Host` before a request reaches a handler. Inbound
authorization is `none`, a static bearer, or a full OAuth resource server (005): issuer, audience,
expiry, scopes and signature against a cached key set, or RFC 7662 introspection for opaque
tokens. Every refusal is byte-identical from outside; the reason goes to the log. The Protected
Resource Metadata document answers without a token, because a client reads it in order to find out
how to authenticate — and it is the only route on the bind that does.

The catalog can be replaced under a running server, by signal or by watching the document, and a
candidate that fails leaves the working catalog serving. Every completed call leaves an audit
record — operation, effect, decision, origin without its query, status, timing, sizes — and the
same events feed a `/metrics` endpoint in the Prometheus text format, behind the same inbound
authorization as everything else.

## M3's own exit criterion

The roadmap states one for each milestone. M3's is *«≥2 replicas за round-robin; conformance и
auth/egress security tests зелёные»* (docs/spec.md §12).

- **Two replicas behind a round robin**: `TestHTTPIsStatelessAcrossReplicas` — two processes, an
  alternating proxy, one session's worth of traffic, no sticky routing.
- **Egress security tests**: unchanged from 001–003 and still green; acceptance criterion 8.
- **Auth security tests**: green as of 005. Inbound authorization has a production mode, and its
  refusals are a table asserted against the real middleware rather than a helper.
- **Conformance**: green for the suite that exists (`TestHTTPConformance`, `TestStdioConformance`,
  both transports, JSON-RPC level). The external cross-SDK runner has still not been run, which is
  the same gap criterion 10 names and is not closed by this feature.

So: met, with conformance qualified exactly as criterion 10 qualifies it.

## What 006 added

Upstream credentials that are obtained rather than configured. An API whose `security` is an
OAuth2 client-credentials flow is now callable: lotsman mints the token from a client id and
secret, over the same egress-governed client every API call goes through, and caches it per
credential rather than per operation. A `401` causes exactly one re-mint and one retry — FR-35,
which had been vacuously satisfied since 001 because nothing in the build could refresh anything.

## What it is not

No delegated OAuth — the flows that need a person are M4b and start only when a real user needs
them — no recipes, no multi-API namespaces.
Cookie parameters, form-urlencoded bodies and Swagger 2.0 are refused with reason codes rather
than half-supported. Search ranking is lexical; semantic retrieval would need the same benchmark
to earn a claim. No container image is pushed anywhere: building one is done and tested, but
publishing it needs a registry, a signing story and a retention policy, and none of those has been
decided.

Every test named above exists and is run by `make test`; the names are checkable on purpose, since a
review that cites a test nobody can find is a review nobody can trust. Two of them had to be
corrected while writing this: the DNS-rebinding tests were renamed when the guard was rewritten to
resolve names itself, and the document still pointed at the old ones.

## Known limits worth stating before someone finds them

- **A large catalog is large, and search mode moves the cost rather than removing it.** Kubernetes
  `apps/v1` publishes 2.23 MB of tool definitions in tools mode and 5.5 KB in search mode, but the
  schema a model needs to create a Deployment is still 38.9 KB *after* every description is
  dropped. `inspect` reports the measurement either way, so the trade is visible before it is felt.
- **A path template containing `?` or `#` used to change the request silently.** Everything after
  the separator was dropped by URL parsing, so lotsman made a request the document did not
  describe — an approximation, which is the one thing this runtime is built not to do. A template
  with a control character was refused, but at call time and with a message blaming the operator's
  base URL. Both are now refused while the document is read, as `invalid_path`, and the message
  names the reason without quoting the template. (A control character could not have been smuggled
  onto the wire: `net/url` rejects it. The fault was in where and how it was reported.)
- **A refusal used to quote the document's path template verbatim.** Every tool error, every
  approval prompt and every search result carried it — so a path containing a newline could put a
  line of its own inside lotsman's own message to a model. The template a request is built from
  still has to match the API exactly and is untouched; the places that only *read* it now use a
  cleaned, bounded copy, the same treatment a description has always had. The words in a payload
  survive, as they do in any description: what stops is the structure that makes them look like
  something other than a path.
- **A parameter name could carry anything into a model's context.** Every other string a document
  contributes there is sanitized and bounded — tool names, descriptions, tags, examples — but a
  parameter name is published verbatim as a schema property key, because it is the key the model
  sends and the key lotsman puts on the wire. A name containing a newline, or four thousand bytes
  long, went straight through. It cannot be cleaned without publishing a schema that does not
  describe the API, so the operation is refused instead: a control character or a name over 256
  bytes is `invalid_parameter`, and the refusal does not quote the name it is refusing. No corpus
  document is affected.
- **Schema `examples` used to reach a model uncleaned.** Descriptions and titles from an
  untrusted document are stripped of HTML and control characters and budgeted before they are
  published (Constitution V); `examples` is prose that reaches the same place and was the one
  piece that got there verbatim. It is now cleaned the same way. `enum` and `const` are
  deliberately *not*: they are constraints, and rewriting them would change what the schema
  accepts, which is a worse failure than the prose they might carry. No corpus document changes
  as a result — the surface was open rather than exploited.
- **The 24 KB `describe_operation` budget used to remove more than prose.** Reducing a schema to
  fit dropped every map key named `description`, `title` or `examples` — including under
  `properties`, where those are the names of the API's own fields. An API with a field called
  `description` therefore lost it from the reduced schema, so a call that worked in tools mode was
  rejected in search mode as an unknown argument, while the caller was told only
  `descriptionsOmitted`. The prune now recurses solely where a value is known to be a schema, and
  leaves instance data and unrecognised keywords exactly as found. Found by writing the test the
  function never had.
- **The specification's own example configuration does not parse.** Copying the YAML from
  docs/spec.md §5.1 gets a startup error listing eight unknown fields — and several of them
  describe settings lotsman does implement at exactly the documented values: the catalog budget of
  120 000 bytes, the per-tool description ceiling of 1 200, the response cap of 524 288, the 30s
  timeout. Each of those constants carries a comment saying it came from this example. The values
  were taken from the specification; the ability to set them was not. The gap is pinned by
  `TestTheSpecificationsOwnExampleDoesNotParseYet`, which reads §5.1 out of the document and fails
  if the list of refused fields changes in either direction.
- **Criterion 5's evidence was not testing criterion 5.** `TestCallReadOperationRefusesNonReads`
  called the POST and the DELETE with no arguments, so both were refused — by argument validation,
  for a missing required field. The test therefore passed with the effect gate removed, and a
  mutation called with *valid* arguments would have gone through. It now passes valid arguments and
  asserts the refusal names the effect class. Found by `make mutate`.
- **Nothing asserted that a response body is bounded on the way *in*.** `MaxBodyBytes` cuts the
  body that is handed back, and the truncation tests covered that — but without the limiting
  reader, `io.ReadAll` pulls the whole thing into memory first and the result is cut afterwards, so
  every assertion still held while an upstream chose this process's footprint. There is now a test
  that counts the bytes actually read. It was found by `make mutate`, not by reading the code.
- **An MCP result is carried twice** — as `structuredContent` and as the text fallback for clients
  that predate it — so a response costs roughly double its payload on the wire. The 24 KB
  `describe_operation` budget therefore buys about 50 KB.
- **Search latency is now measured too, and the budget turns out to be decoration.** NFR-10 asks
  for p95 under 50 ms over a thousand operations; the measured p95 is 257 µs, about two hundred
  times under. The test asserts a second, tighter threshold so that an order-of-magnitude
  regression fails somewhere rather than nowhere (docs/benchmarks.md).
- **Search recall is measured, not claimed**: Kubernetes Recall@5 1.00 / MRR 0.53, DigitalOcean
  0.75 / 0.65 (`internal/searchindex/recall_test.go`). The two fail in opposite directions:
  Kubernetes because its operations are near-duplicates and the ambiguity is in the question,
  DigitalOcean because of morphology ("droplet" does not match "droplets") and intent no word
  carries. NFR-10 (search p95 < 50 ms) is deliberately unmeasured rather than assumed.
- **An exploded specification costs memory.** DigitalOcean's ~2,900-document closure peaks at
  262 MB RSS, over NFR-11's 200 MiB budget, because the closure is parsed in full before any
  operation is enumerated. Two cheaper remedies were tried and measured: the Go runtime's own
  `GOMEMLIMIT` reaches 215 MB at twice the wall time — still over — and a `$ref` resolution cache
  took 18% off wall time and 8 MB off the peak. The structural fix, lazy parsing of referenced
  documents, is unchanged and still not done (docs/benchmarks.md).
- **`ambiguous_security` is only evaluated against configured profiles.** With no profiles, no
  alternative is satisfiable and the operation is blocked rather than called ambiguously. Two
  profiles satisfying one scheme now refuse as ambiguous instead of taking the first — that was a
  credential chosen by sort order, and it is a behaviour change for a configuration that
  previously appeared to work (ADR-0012 §9). `operationOverrides[].authProfile` is the way out.
- **Approval is a prompt, not a permission.** The protocol cannot promise a human saw it: a client
  may answer on its own, and lotsman cannot tell (FR-44a). It can only subtract from what the
  policy already allowed, and the gate stands whether or not anyone was asked.
- **A legacy `initialize` handshake negotiates `2025-11-25`, not `2026-07-28`.** This is correct
  rather than a shortfall — `initialize` is the handshake the new revision removed, so a client
  using it is by definition not speaking the new one — but it means `mcpProtocolVersion` in
  `lotsman version` is the revision this build *targets and serves*, not the one every client will
  end up on. The sessionless path is proven separately (`TestHTTPConformance`).
- **An unknown method over stateless HTTP is answered with a 400 and a plain-text body**, where
  JSON-RPC 2.0 §5.1 asks for a `-32601` frame. The refusal is safe and the server stays up; the
  behaviour is the SDK's transport layer, not lotsman's, and the conformance test asserts only the
  safe property so that it does not have to be rewritten as a failure the day the SDK conforms.
- **Reload is an HTTP property.** A stdio session resolves its server once and cannot be handed
  another, so `--watch` over stdio is refused rather than silently ignored — and `tools/list_changed`
  is not implemented at all, because no transport in this build both reloads and has a connection
  to push to (ADR-0015).
- **The reload watcher compares size and modification time.** An edit that changes neither is
  missed; `SIGHUP` covers the operator who needs more than that.
- **A minted token lives in memory and nowhere else.** No token store, and no cross-replica
  refresh coordination: in service mode the token is derived from a secret the process already
  holds, and client credentials has no refresh token to rotate. FR-66 and FR-67 were written for
  delegated mode; what they ask for here is an in-memory cache, and that is what they get
  (ADR-0017).
- **Scopes a profile requests are not checked against what an operation declares.** Refusing
  locally would be overruling the provider about a token it already issued. The report shows
  both, so a mismatch is visible without being fatal.
- **A validated subject is recorded, not enforced.** The audit event names who asked; no rule can
  match on it. Per-subject authorization is RBAC, and §8 defers RBAC to the gateway layer — a
  half-built version would be worse than none, because an operator who saw `subject` in a rule
  would reasonably assume the rest.
- **An RSA key from a key set is accepted only between 2048 and 8192 bits.** The upper bound is
  not tidiness: verifying RS256 against a 4-Mbit modulus takes about 43 seconds, a 1 MiB key set
  document can carry 6.3 Mbit, and the verification happens before a caller is authenticated. A
  provider that legitimately publishes a larger key will be refused, and that is the intended
  trade (ADR-0016).
- **A cached key set means a revoked key keeps working for up to its TTL** (15 minutes by
  default). An unknown `kid` provokes at most one refetch per minute, which bounds what a forged
  one can cost; the same bound means a rotation can take a minute to be noticed.
- **Introspection puts the authorization server on the hot path.** Every call becomes an
  authenticated round trip to the provider, bounded by a timeout that refuses rather than waits.
  In exchange a revoked token stops working immediately instead of at its expiry.
- **DPoP and sender-constrained tokens are not implemented**, because the Go SDK does not
  implement them — its own conformance baseline excludes `auth/dpop`. Claiming them would be a
  claim about somebody else's code.
- **Metrics have no authentication of their own.** `/metrics` sits behind whatever inbound
  authorization the MCP endpoint has, which means an unauthenticated loopback deployment exposes
  it to anything on the machine — the same trust boundary the MCP endpoint already has there.
- **The desktop-client profile is assumed, not fully measured.** Name length, charset and catalog
  budget are coded conservatively; the open questions and why they do not block are in
  docs/adr/0004-mcp-client-notes.md.
