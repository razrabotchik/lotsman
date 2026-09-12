# v0.1.0-alpha: acceptance review

**Nothing is tagged.** This is the review that would precede a tag: what holds, what does not, and
where the evidence is. Cutting the release is a separate decision.

The frozen specification lists eleven acceptance criteria for "the first stable core version"
(docs/spec.md §13). The honest statement is which of them hold now, which are out of scope for an
alpha, and where the evidence is. A criterion is not marked met because someone remembers
implementing it; it is marked met because a test fails when it stops being true.

Scope of this review: features 001 (core runtime), 002 (search mode) and 003 (selection,
overrides and interactive approval — the M2 tail), all complete — 45, 14 and 12 tasks
respectively.

| # | Criterion | Status | Evidence |
|---|---|---|---|
| 1 | `inspect` deterministically explains support, with JSON output | **met** | `TestInspectJSONContract`, `TestInspectIsDeterministic`, `TestReportIsByteIdenticalAcrossRuns` |
| 2 | Local OAS 3.0 and 3.1 work over stdio; GET and JSON POST serialize correctly | **met** | `TestAcceptanceUS1` (live server, both verbs in one session) |
| 3 | Path/query arrays and required parameters covered e2e; unsupported styles never approximated | **met** | `TestAcceptanceUS1` (repeated-key arrays), `TestSerializationTable`, `TestUnsupportedStylesRejectTheOperation` |
| 4 | In read-only mode, write/destructive/unknown never reach the upstream | **met** | `TestServeMutationPolicyEndToEnd`, `TestMutationBlockedByDefault` (zero RoundTrips asserted), and per-operation rules on top: `TestDeniedByRuleNeverReachesTheNetworkInToolsMode`, `TestAllowListRefusesTheUnlistedMutationEndToEnd` |
| 5 | Search mode cannot call a mutation through a read tool | **met** | `TestCallReadOperationRefusesNonReads` (zero RoundTrips for a POST and a DELETE), `TestServeSearchModeEndToEnd`, `TestMutatingToolIsAbsentWhenMutationsAreNotAllowed` |
| 6 | Interactive approval fails closed without client capability | **met** | `TestServeMutationPolicyEndToEnd/fails_closed_when_the_client_cannot_be_asked` (real binary over stdio, zero upstream requests), `TestApprovalFailsClosedWithoutClientCapability`, `TestApprovalFailsClosedInSearchMode`, `TestDeclinedApprovalRefusesBeforeTheNetwork` |
| 7 | A canary secret appears in no stdout, stderr, result, error or report | **met** | `TestCanarySecretNeverLeaks` (four channels, live credential), `internal/redact` |
| 8 | A redirect to another origin or a private address is blocked; Authorization is not forwarded | **met** | `TestClientDeniesRedirects`, `TestHostnameResolvingIntoAPrivateRangeIsRefused`, `TestHostnameIsNeverTreatedAsLiteral`, `TestResolvedSecretsAreRegisteredForRedaction` (the credential is applied by the innermost round tripper, so no layer above a refused hop ever holds it) |
| 9 | Hot reload does not drop a working catalog on an invalid candidate | **out of scope** | hot reload is not implemented; a catalog is built once per snapshot |
| 10 | MCP conformance for the selected protocol revisions is green | **partial** | the official Go client drives the real binary over stdio in every e2e test; a separate conformance suite has not been run |
| 11 | The release carries checksums and an SBOM; the container runs nonroot | **partial** | `.goreleaser.yaml` produces checksums and SBOMs, verified with `goreleaser check` and a snapshot build whose binary reports the injected version; no container image is built |

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

## What it is not

No HTTP transport, no OAuth, no recipes, no hot reload. Cookie parameters, form-urlencoded bodies
and Swagger 2.0 are refused with reason codes rather than half-supported. Search ranking is
lexical; semantic retrieval would need the same benchmark to earn a claim.

Every test named above exists and is run by `make test`; the names are checkable on purpose, since a
review that cites a test nobody can find is a review nobody can trust. Two of them had to be
corrected while writing this: the DNS-rebinding tests were renamed when the guard was rewritten to
resolve names itself, and the document still pointed at the old ones.

## Known limits worth stating before someone finds them

- **A large catalog is large, and search mode moves the cost rather than removing it.** Kubernetes
  `apps/v1` publishes 2.23 MB of tool definitions in tools mode and 5.5 KB in search mode, but the
  schema a model needs to create a Deployment is still 38.9 KB *after* every description is
  dropped. `inspect` reports the measurement either way, so the trade is visible before it is felt.
- **An MCP result is carried twice** — as `structuredContent` and as the text fallback for clients
  that predate it — so a response costs roughly double its payload on the wire. The 24 KB
  `describe_operation` budget therefore buys about 50 KB.
- **Search recall is measured, not claimed**: Kubernetes Recall@5 1.00 / MRR 0.53, DigitalOcean
  0.75 / 0.65 (`internal/searchindex/recall_test.go`). The two fail in opposite directions:
  Kubernetes because its operations are near-duplicates and the ambiguity is in the question,
  DigitalOcean because of morphology ("droplet" does not match "droplets") and intent no word
  carries. NFR-10 (search p95 < 50 ms) is deliberately unmeasured rather than assumed.
- **An exploded specification costs memory.** DigitalOcean's ~2,900-document closure peaks at
  262 MB RSS, over NFR-11's 200 MiB budget, because the closure is parsed in full before any
  operation is enumerated (docs/benchmarks.md).
- **`ambiguous_security` is only evaluated against configured profiles.** With no profiles, no
  alternative is satisfiable and the operation is blocked rather than called ambiguously. Two
  profiles satisfying one scheme now refuse as ambiguous instead of taking the first — that was a
  credential chosen by sort order, and it is a behaviour change for a configuration that
  previously appeared to work (ADR-0012 §9). `operationOverrides[].authProfile` is the way out.
- **Approval is a prompt, not a permission.** The protocol cannot promise a human saw it: a client
  may answer on its own, and lotsman cannot tell (FR-44a). It can only subtract from what the
  policy already allowed, and the gate stands whether or not anyone was asked.
- **The desktop-client profile is assumed, not fully measured.** Name length, charset and catalog
  budget are coded conservatively; the open questions and why they do not block are in
  docs/adr/0004-mcp-client-notes.md.
