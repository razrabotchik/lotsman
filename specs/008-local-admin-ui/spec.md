# Feature 008: corrections, and a local workbench to make them

**Status**: design approved 2026-10-16. Not a roadmap milestone: docs/spec.md §10 defers an admin
interface to M6, and Constitution IX lists it among deferred hypotheses. This feature brings a
*local, single-user* slice of it forward; ADR-0021 records why (stage 1, task 1).

## Why this exists

`lotsman inspect` is honest about what it refuses, and then the operator is on their own. Some
refusals are a decision lotsman must not make but a person can: Kubernetes `apps/v1` loses 12 of
77 operations because each PATCH offers four media types (docs/corpus.md), and FR-29 promised a
media-type override that the code does not have (`internal/openapi/body.go:88`). Other gaps are
the document's: a vendor spec that says `integer` where the API takes a string. Today the only
fix is editing the vendor's file, which is lost on the next update.

Two things are missing:

1. **A language for corrections** that survives a spec update and is reviewed like the rest of the
   configuration — the reason `operationOverrides` exist, extended from *what an operation does*
   to *what the document says about it*.
2. **A place to make them quickly**: problems highlighted, every change shown with its consequence
   before anything is written. That is `lotsman ui`.

Answering through MCP tools in the chat was considered and rejected: the model fills the tool
arguments, so a model — or a prompt injection in an upstream response — could "answer" for the
person and mark a destructive operation `read`. Corrections are made by a person in a channel the
model does not touch (Constitution III).

## Scope

In:
- New correction fields in `lotsman.yaml` (§1) and standard OpenAPI Overlay input (`spec.overlays`).
- Catalog reload re-reads the catalog-shaping part of the configuration.
- `internal/workbench`: re-runs the pipeline on a draft and reports the difference.
- `lotsman inspect --diff-against OLD.yaml`.
- `lotsman ui`: a local web workbench over the same engine.

Out: adding parameters or operations; editing servers, security schemes or `$ref`s; support for
constructs that need new code (`deepObject`, form bodies, multipart, cookies) — the workbench
names them, it cannot fix them; exporting corrections as an Overlay; multi-user, remote or
authenticated admin (that is the PRO/M6 shape); MCP tools for corrections; hot-reloading policy,
execution or credentials (FR-72 republishes a catalog, not a policy — as in feature 007).

## User scenarios

- **US-1 (P1)**: As an operator of Kubernetes `apps/v1`, I pick `application/merge-patch+json` for
  the twelve PATCH operations and they are published. *Acceptance*: `inspect` reports 77 supported.
- **US-2 (P1)**: As an operator whose vendor spec declares a parameter with the wrong type, I
  correct it in `lotsman.yaml` without touching the vendor file; the next spec update keeps my
  correction or, if the parameter is gone, `serve` refuses to start and names the stale correction.
- **US-3 (P1)**: As an operator, I run `lotsman ui ./openapi.yaml`, see the operations that need a
  decision first, fix them, and see before saving what was fixed, what broke and what else changed.
- **US-4 (P2)**: As an operator running `serve --transport=http --watch`, a correction saved by the
  workbench is in the published catalog within one poll, without a restart.
- **US-5 (P2)**: As a reviewer in CI, `inspect --config new.yaml --diff-against old.yaml` tells me
  which operations a configuration change fixes, breaks or alters.

## 1. Correction language

```yaml
apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./apps-v1.yaml
  overlays:                                   # standard OpenAPI Overlay documents, in order
    - ./vendor-fixes.overlay.yaml

componentOverrides:                           # a shared schema: affects every operation using it
  - match: { schema: io.k8s.apimachinery.pkg.apis.meta.v1.DeleteOptions }
    patch:                                    # JSON Merge Patch (RFC 7396) over the schema
      properties:
        gracePeriodSeconds: { type: string }
    note: "the API takes a string"

operationOverrides:                           # existing section; new fields marked
  - match: { operationId: patchAppsV1NamespacedDeployment }
    mediaType: application/merge-patch+json   # new (FR-29)
    parameters:                               # new; this operation only
      - { name: limit, in: query, schema: { type: string } }
      - { name: dryRun, in: query, required: false }
    requestBody:                              # new; this operation only
      patch: { required: [metadata] }
    effect: write                             # existing
    enabled: true                             # existing
    authProfile: cluster                      # existing
    note: "checked on staging 2026-10-16"     # new, on every override
```

**Order.** Overlay documents → `componentOverrides` → operation `parameters`/`requestBody`/
`mediaType` → catalog decisions (`enabled`, `effect`, `authProfile`), which stay where they are
today (`internal/catalog/overlay.go`).

**Where they apply.**
- `spec.overlays` apply to the bytes of the root document before parsing, through libopenapi's
  overlay engine (already a dependency, v0.38.7 — Constitution VII holds). Overlay targets reach
  the root document only; for exploded specifications the native language is the tool. Overlay
  files are operator input, read from paths relative to the configuration like `spec.source`; a
  `$ref` an overlay writes into the document is confined to the spec root like any other. An
  overlay action whose target matches nothing is an error, not a warning.
- Component, parameter, body and media-type corrections are inputs to `openapi.Parse`, applied in
  the adapter before an operation is translated into the IR — that is where
  `unsupported_parameter_style` and `unsupported_media_type` are decided, so a correction applied
  later could not lift them (Constitution VIII: libopenapi stays inside the adapter).
- An operation-level patch to a parameter or body that reaches a shared schema through `$ref`
  patches a copy for that operation only. Changing it everywhere is `componentOverrides`.

**`mediaType`.** Must be one of the media types the operation declares and one lotsman can send.
It replaces the choice in `selectMediaType`; it never makes an unsupported type supported.

**Strictness**, matching existing overrides:
- A correction that matches nothing — no operation, no parameter by `name`+`in`, no component — is
  a startup error naming it, not a smaller catalog. One that matches more than one thing is an
  error too.
- A patch must not contain `$ref`, and nothing in this language can touch `servers`,
  `securitySchemes`, method or path. A correction cannot widen what the document may read or where
  a request may go.
- A parameter correction may set `schema` (replaces the parameter's schema), `required`, `style`
  and `explode`. `required: false` on a path parameter is refused. `patch` (body and components)
  merges. Corrections apply to schemas *after* lotsman normalizes them, so they are written in
  JSON Schema 2020-12 (`type: [string, "null"]`, not `nullable: true`). A correction cannot rescue
  a schema normalization itself refused (a conditional subschema stays `unsupported_body_schema`);
  a parameter's `schema` replacement can, because it replaces rather than patches.
- A correction whose result no longer has a shape lotsman can carry (an object in a query value)
  rejects the operation with the usual reason code; that is a consequence the workbench shows, not
  a configuration error.
- `componentOverrides` match `#/components/schemas/NAME` in the root document. Components in
  external files of an exploded specification are not matched in this release; such a correction
  matches nothing and is refused by name.
- Corrected things carry provenance `local_override` with the `note` into `inspect` (text and
  `--json`) and `explain-call`, the same way effect sources do today. Nobody is surprised that a
  call does not follow the vendor document.
- Determinism (Constitution IV): the same spec + config yields the same catalog and digest; the
  digest covers the corrections.

All new fields are optional; existing configurations parse unchanged and `apiVersion` stays.

## 2. Reload

`SIGHUP` and `--watch` (ADR-0015) re-read the configuration file and every `spec.overlays` file
in addition to the specification. Only the catalog-shaping part is re-applied: `spec.overlays`,
`componentOverrides`, `operationOverrides`, `catalog`. If anything else differs from the running
configuration, the reload is refused, the running catalog stays, and the log says which section
needs a restart. A configuration that fails validation is refused the same way. `--watch` polls
the configuration and overlay files as it polls the spec. stdio still has no reload.

Nothing here touches the call path: corrections are applied when a catalog is built.

## 3. Workbench engine (`internal/workbench`)

Holds the source bytes, the configuration as loaded, and a draft (an ordered list of corrections).
On every draft change it runs the same pipeline `serve` runs — `specsource` → overlays →
`openapi.Parse` with corrections → `catalog.Overlay/Build` — and returns:

- per operation: before/after status, reason codes, effect, published argument schema;
- a summary: fixed (rejected → supported), broken (supported → rejected), altered (argument schema,
  effect or executability changed), and corrections that match nothing;
- per-field warnings for the edit that caused them;
- **widening changes**, flagged separately: effect lowered to `read` on a POST/PUT/PATCH/DELETE or
  on an operation whose inferred effect was not `read`; enabling an operation the previous
  configuration disabled; changing `authProfile`;
- the resulting `lotsman.yaml` and its textual diff against the file on disk.

Full re-run, not incremental: the pipeline takes 0.33 s on Stripe and DigitalOcean
(docs/corpus.md), and a separate incremental path would be a second implementation that can
disagree with `serve`. Revisit only if a measured workbench interaction exceeds ~1 s.

Writing: the YAML is edited as a `yaml.Node` tree so comments and key order survive. Save is
write-to-temp + rename in the same directory, and only if the file's content hash still equals the
one read at load; otherwise it refuses and the draft is kept.

`inspect --diff-against OLD.yaml` is the same comparison printed for CI: the summary and the
per-operation changes, `--json` included.

## 4. `lotsman ui`

```
lotsman ui [SPEC] [--config FILE] [--root DIR] [--port N] [--no-open]
```

Without `--config`, it uses `./lotsman.yaml` if present, else creates it on first save with
`spec.source` pointing at SPEC. It never makes an upstream request and never resolves a secret.

**Screen** (mockups chosen 2026-10-16):
- Left: a tree — first tag → path segments → operations; untagged operations under `untagged`.
  Branches show problem counts. Search, an "only problems" toggle, a "next problem →" button.
- Status per operation: needs a decision (amber) · needs new code or document error (red, with
  the reason and why the workbench cannot help) · disabled by policy or decision (grey) · works
  (teal) · pending edit (dot) · a separate group for stale corrections.
- Right: the form for one operation — reason and hint on top, then parameters, body, media type,
  effect, auth profile, and a dry-run tab (`explain-call` without the network). Warnings appear
  under the field that caused them after each edit. When an edit reaches a shared schema the form
  asks: this operation only, or everywhere (`componentOverrides`).
- Bottom: the pending tray — "N changes · +fixed · −broken · altered" — and "Review and save",
  which shows the full file diff. Widening changes are listed in their own red block and each needs
  its own tick. Narrowing changes need none.

**Local HTTP API** (for the embedded frontend only; no stability promise): `GET /api/state`,
`POST /api/draft`, `DELETE /api/draft/{id}`, `GET /api/diff`, `POST /api/save` (with the content
hash), `POST /api/explain`.

**The server writes files, so:**
- binds `127.0.0.1` only;
- a random token in the URL printed to the terminal (and opened in the browser), then a cookie;
- `Host` must be `127.0.0.1:PORT` or `localhost:PORT` (DNS rebinding); `Origin` checked on every
  non-GET request; no CORS; a strict Content-Security-Policy;
- writes only the configuration path fixed at start; no path is accepted from the browser;
- credentials are shown as references (`env:MY_TOKEN`); dry runs show `<env:MY_TOKEN>`.

**Frontend**: plain JavaScript ES modules and CSS, no framework, no Node toolchain — the build stays
`go build`. Embedded with `go:embed`, including IBM Plex (OFL) so the tool makes no network request
of its own. Visual language from `design/` (paper/ink, signal red only for refusals, teal accent).
Budget: under 400 KB added to the binary.

**Isolation from `serve`** (Constitution IX): `internal/ui` and `internal/workbench` are imported
only by the `ui` and `inspect` commands. `serve` gains no goroutine, handler or check; the call
path does not change.

## 5. Errors

- The specification does not parse (broken YAML, Swagger 2.0, a `$ref` escaping the root): the
  workbench still opens, shows the document error with its code and an empty tree; only
  `spec.overlays` can be edited.
- A correction is itself invalid (a `$ref` in a patch, a schema that no longer validates): the
  error is shown at the field; the draft does not take it.
- The file changed on disk while the workbench was open: save refuses and offers to reload; the
  draft survives.
- Reload with an invalid or policy-changing configuration: the running catalog stays; the log says
  why (§2).
- Stale corrections after a spec update: `serve` refuses to start and names each; the workbench
  shows them as a group with "remove" or "rebind".

## 6. Stages

Each stage ships on its own and is useful without the next.

1. **Corrections in the core.** ADR-0021 (the local workbench vs. Constitution IX and spec §10).
   `mediaType`, `parameters`, `requestBody`, `componentOverrides`, `note`, `spec.overlays`,
   provenance in `inspect`/`explain-call`, reload of the catalog-shaping configuration.
   Outcome: corrections by hand in YAML; Kubernetes `apps/v1` at 77/77 recorded in
   docs/corpus.md; docs/spec.md §5.2 and README updated.
2. **Workbench engine.** `internal/workbench`, `inspect --diff-against`.
3. **`lotsman ui`.** Local server, tree, form, tray, review and save.

## 7. Testing

**Deviation from Constitution X, decided by the maintainer on 2026-10-16:** during integration
only the minimal tests a step needs to be known working (build, one smoke check per slice); the
full suite is written at the end of each stage, before the stage is called done. `main` stays green
throughout.

The suite each stage ends with:
- **Stage 1**: table tests "spec + correction → reason codes" for every correction kind; stale and
  ambiguous matches; `$ref`-in-patch and overlay-outside-root canaries; corpus run with the
  Kubernetes `apps/v1` media-type configuration asserting 77 supported; determinism golden over a
  corrected catalog; reload accepts catalog changes and refuses policy changes; fuzz for patch
  application (NFR-6).
- **Stage 2**: "draft → fixed / broken / altered / widening" tables; the catalog built by the
  workbench equals byte for byte the one `serve` builds from the same files; YAML round-trip keeps
  comments and key order; save refuses on a changed file.
- **Stage 3**: requests without the token, with a foreign `Host`, with a foreign `Origin` are
  refused; the canary-secret suite runs against every API response; one Playwright smoke test:
  open, correct, save, file changed.

## Success criteria

- Kubernetes `apps/v1`: 77/77 supported with one `mediaType` override per PATCH operation (12);
  the workbench makes them with one "apply to all like this" action.
- A correction that no longer matches its spec stops `serve` and is named.
- From `lotsman ui` start to a saved fix of a `needs decision` operation: under five minutes for
  someone who has not read the docs.
- `serve` with no corrections configured: catalog digest, startup time and call latency unchanged
  (docs/benchmarks.md).
