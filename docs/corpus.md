# Vendor corpus: what lotsman makes of real specifications

Measured at the M0 gate (T022) with `lotsman inspect --json` over the documents pinned in
`testdata/corpus/MANIFEST.json`. Fetch them with `make corpus`; the manifest pins an immutable
ref and a sha256 for each, so these numbers are reproducible or the fetch fails.

The corpus is not a scoreboard. It exists to answer one question per document: *when lotsman
refuses, is the reason a design flaw or a feature that has not been written yet?*

| Document | Operations | Supported | Executable (default policy) | Catalog (tools) | Catalog (search) | Verdict |
|---|---|---|---|---|---|---|
| Kubernetes `apps/v1` (v1.31.0) | 77 | 65 | 38 | 2.23 MB | **5.5 KB** | translated |
| DigitalOcean, exploded (pinned commit) | 659 | 631 | 0 (all need auth) | 743 KB | **5.5 KB** | translated |
| Stripe (v1301) | 559 | 0 | 0 | — | — | refused per matrix |
| DigitalOcean, root only | 0 | 0 | 0 | — | — | every reference refused |
| GitLab (v17.5.0-ee) | — | — | — | — | — | refused before parsing |

Numbers below were re-measured after Phase B step 8 (references, normalization, security).

## Kubernetes `apps/v1` — the reference case

77 operations, 65 supported, 12 rejected. Of the 65 published tools, 38 execute under the default
read-only policy; the other 27 are mutations awaiting `--allow-mutations` (17 unknown-effect
POST/PUT/PATCH, 10 destructive DELETE).

All 12 rejections are `unsupported_media_type`, and all 12 are PATCH operations offering four
competing patch media types at once (`application/apply-patch+yaml`, `application/json-patch+json`,
`application/merge-patch+json`, `application/strategic-merge-patch+json`). Choosing one of four
without being told which is exactly the guess FR-29 forbids; a media-type override in config lifts
these.

**Found by this run:** Kubernetes declares its DELETE bodies as `*/*`, which lotsman refused
outright — 27 operations lost to a wildcard nobody disputes. A sole wildcard range says "any media
type", and the schema beside it applies to whichever one is sent, so JSON is inside the set that
was declared. lotsman now sends JSON for a sole `*/*` or `application/*` and says so in the
`Content-Type`; a wildcard *alongside* a concrete type is still refused, because then two schemas
are in play. Supported operations went from 38 to 65.

**Catalog size:** 65 tools serialize to **2.23 MB** of `tools/list` payload (4.25 MB before
references were kept as references — ADR-0009). That is the number
that decides feature 002: one tool per operation is not viable for Kubernetes at any context
budget, and `--mode=search` (FR-48) is not an optimization but the only workable mode for specs of
this shape. The estimate is in every report (`catalog.serializedBytesEstimate`) precisely so this
is a measurement rather than an intuition.

## Stripe — the pathological-schema case

559 operations, all rejected, for two reasons that are both documented non-goals of M1
(docs/spec.md 4.5):

- `unsupported_media_type` × 559 — every Stripe request body is `application/x-www-form-urlencoded`;
- `unsupported_parameter_style` × 367 — Stripe uses `deepObject` query parameters throughout.

`authentication_not_implemented` also appears 559 times as an execution blocker. Nothing here is an
IR problem: Stripe needs form bodies and deepObject serialization, both named as separate work
items with their own tests.

Parsing the 5.2 MB document, normalizing it and producing the full report takes **0.40 s** and
**150 MB** RSS (NFR-11 budgets 200 MiB), and two runs produce byte-identical reports.

## DigitalOcean — the exploded-spec case

**659 operations, 631 supported, in 0.3 s.** The root is a 112 KB index whose `$ref`s point at 662
sibling documents; the transitive closure is about 2,900 files and 14 MB, every one of them
resolved relative to the document that referenced it, expanded through symlinks and checked to be
inside the spec root before being read (ADR-0009). The parser receives the verified list as an
allowlist, so the secrets file sitting next to a spec is never opened.

The 28 rejections are precise: 21 request bodies using conditional subschemas, 6 path parameters
declared as unions, one unsupported media type and one invalid parameter. All 631 published tools
need a bearer credential, so none is executable until the auth stage — which is the honest
statement about an API that authenticates everything.

**Found by this run:** the closure is far larger than the root suggests — 662 direct references,
~2,900 documents transitively. `MaxRefDocuments` is now 4,096, set from this measurement.

The root document *alone* is kept in the corpus as its own entry: downloading the index without
the files it points at is a real mistake, and lotsman answers it with 662 diagnostics that each
name the missing document and the pointer that asked for it, rather than one confusing failure.

## GitLab — the wrong-version case

Refused before parsing: `doc/api/openapi/openapi_v2.yaml` is **Swagger 2.0**, not OpenAPI 3.x. A
compatibility adapter is explicitly outside this release.

**Found by this run:** lotsman used to pass libopenapi's own message through —
*"supplied spec is a different version (oas2). Try 'BuildV2Model()'"* — which is a sentence about
someone else's Go API, not about the operator's document. The version is now read from the root
before the parser is handed the bytes (pipeline.md stage 1.1), and the refusal names Swagger 2.0
and says it is not part of this release.

## Search mode, measured

Search mode publishes five meta-tools regardless of catalog size, so `tools/list` is a constant
**5 492 bytes** for both Kubernetes (65 operations) and DigitalOcean (631). Against tools mode that
is 406× smaller for Kubernetes and 135× for DigitalOcean — and for a catalog that did not fit at
all, the comparison is not a ratio but a yes.

What a model then pays per step:

| Step | Kubernetes | DigitalOcean |
|---|---|---|
| `tools/list` | 5.5 KB | 5.5 KB |
| `search_operations` (5 hits) | 3.4 KB | 3.3 KB |
| `describe_operation` (create a Deployment) | 87 KB on the wire, `schemaBytes` 38.9 KB | — |

**Found by this measurement:** an MCP result is carried twice — once as `structuredContent` and
once as the text fallback the SDK generates for clients that predate it — so the wire cost of a
response is roughly double its payload. The 24 KB describe budget therefore buys about 50 KB on the
wire, and the Kubernetes Deployment schema exceeds it even after every description is dropped
(38.9 KB of pure constraints). That is the honest cost of knowing how to create a Deployment; the
alternative is a schema a model cannot rely on, and `schemaBytes` is reported so that asking again
is an informed decision rather than a surprise.

Ranking quality is measured separately and committed as a regression test
(`internal/searchindex/recall_test.go`): **Kubernetes Recall@5 1.00 / MRR 0.53**, **DigitalOcean
Recall@5 0.75 / MRR 0.65**. The two fail in opposite directions, and the reasons are recorded next
to the numbers.
