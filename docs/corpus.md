# Vendor corpus: what lotsman makes of real specifications

Measured at the M0 gate (T022) with `lotsman inspect --json` over the documents pinned in
`testdata/corpus/MANIFEST.json`. Fetch them with `make corpus`; the manifest pins an immutable
ref and a sha256 for each, so these numbers are reproducible or the fetch fails.

The corpus is not a scoreboard. It exists to answer one question per document: *when lotsman
refuses, is the reason a design flaw or a feature that has not been written yet?*

| Document | Operations | Supported | Executable (default policy) | Verdict |
|---|---|---|---|---|
| Kubernetes `apps/v1` (v1.31.0) | 77 | 65 | 38 | translated |
| Stripe (v1301) | 559 | 0 | 0 | refused per matrix |
| DigitalOcean (pinned commit) | — | — | — | refused before parsing |
| GitLab (v17.5.0-ee) | — | — | — | refused before parsing |

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

**Catalog size:** 65 tools serialize to **4.2 MB** of `tools/list` payload. That is the number
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

Refused before parsing: the root document is a 112 KB index whose `$ref`s point at **662 sibling
files**. File reference resolution with root confinement is T025; until it exists, resolving one
means reading a file chosen by an untrusted document, so the closure budget refuses the document as
a whole rather than emitting 662 identical diagnostics.

**Found by this run:** the default `MaxRefDocuments` of 64 is an order of magnitude below what a
real exploded spec needs. T025 should set it from this evidence rather than from intuition.

## GitLab — the wrong-version case

Refused before parsing: `doc/api/openapi/openapi_v2.yaml` is **Swagger 2.0**, not OpenAPI 3.x. A
compatibility adapter is explicitly outside this release.

**Found by this run:** lotsman used to pass libopenapi's own message through —
*"supplied spec is a different version (oas2). Try 'BuildV2Model()'"* — which is a sentence about
someone else's Go API, not about the operator's document. The version is now read from the root
before the parser is handed the bytes (pipeline.md stage 1.1), and the refusal names Swagger 2.0
and says it is not part of this release.
