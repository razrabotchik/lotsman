# v0.1.0-alpha: acceptance review

The frozen specification lists eleven acceptance criteria for "the first stable core version"
(docs/spec.md §13). This release is an **alpha**, and the honest statement is which of them hold
now, which are out of scope for it, and where the evidence is. A criterion is not marked met
because someone remembers implementing it; it is marked met because a test fails when it stops
being true.

| # | Criterion | Status | Evidence |
|---|---|---|---|
| 1 | `inspect` deterministically explains support, with JSON output | **met** | `TestInspectJSONContract`, `TestInspectIsDeterministic`, `TestReportIsByteIdenticalAcrossRuns` |
| 2 | Local OAS 3.0 and 3.1 work over stdio; GET and JSON POST serialize correctly | **met** | `TestAcceptanceUS1` (live server, both verbs in one session) |
| 3 | Path/query arrays and required parameters covered e2e; unsupported styles never approximated | **met** | `TestAcceptanceUS1` (repeated-key arrays), `TestSerializationTable`, `TestUnsupportedStylesRejectTheOperation` |
| 4 | In read-only mode, write/destructive/unknown never reach the upstream | **met** | `TestServeMutationPolicyEndToEnd`, `TestMutationBlockedByDefault` (zero RoundTrips asserted) |
| 5 | Search mode cannot call a mutation through a read tool | **out of scope** | search mode is feature 002 |
| 6 | Interactive approval fails closed without client capability | **out of scope** | approval is M2 (FR-44–46); M1 gates mutations by policy only |
| 7 | A canary secret appears in no stdout, stderr, result, error or report | **met** | `TestCanarySecretNeverLeaks` (four channels, live credential), `internal/redact` |
| 8 | A redirect to another origin or a private address is blocked; Authorization is not forwarded | **met** | `TestClientDeniesRedirects`, `TestPrivateAddressesAreRefusedForHostnameOrigins`, `TestDialGuardRefusesRebindingEndToEnd` |
| 9 | Hot reload does not drop a working catalog on an invalid candidate | **out of scope** | hot reload is not in this release |
| 10 | MCP conformance for the selected protocol revisions is green | **partial** | the official Go client drives the real binary over stdio in every e2e test; a separate conformance suite has not been run |
| 11 | The release carries checksums and an SBOM; the container runs nonroot | **partial** | `.goreleaser.yaml` produces checksums and SBOMs, verified with `goreleaser check` and a snapshot build; no container image is published in this release |

## What this release is

A local stdio runtime that turns an OpenAPI document into MCP tools and refuses, by name, what it
cannot translate exactly. Measured against vendor specifications (docs/corpus.md): Kubernetes
`apps/v1` 65 of 77 operations, DigitalOcean 631 of 659, Stripe 0 of 559 for reasons that are named
in the support matrix rather than discovered at run time.

## What it is not

No HTTP transport, no search mode, no OAuth, no interactive approval, no recipes, no hot reload.
Cookie parameters, form-urlencoded bodies and Swagger 2.0 are refused with reason codes rather
than half-supported.

## Known limits worth stating before someone finds them

- **A large catalog is large.** Kubernetes `apps/v1` publishes 2.23 MB of tool definitions; the
  transport delivers it in 146 ms, but a model's context is the real constraint. Search mode (002)
  exists for this, and `inspect` reports `catalog.serializedBytesEstimate` so the problem is
  visible before it is felt.
- **An exploded specification costs memory.** DigitalOcean's ~2,900-document closure peaks at
  262 MB RSS, over NFR-11's 200 MiB budget, because the closure is parsed in full before any
  operation is enumerated (docs/benchmarks.md).
- **`ambiguous_security` is only evaluated against configured profiles.** With no profiles, no
  alternative is satisfiable and the operation is blocked rather than called ambiguously.
- **The desktop-client profile is assumed, not fully measured.** Name length, charset and catalog
  budget are coded conservatively; the open questions and why they do not block are in
  docs/adr/0004-mcp-client-notes.md.
