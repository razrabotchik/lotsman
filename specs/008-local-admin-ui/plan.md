# Implementation Plan: corrections in the core (feature 008, stage 1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement tasks.md task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Branch**: `008-local-admin-ui` | **Spec**: ./spec.md | **Frozen spec**: docs/spec.md §4.5, §5.2, FR-29

**Goal:** an operator can correct what a vendor document says — pick a media type, retype or
restyle a parameter, patch a body or a shared schema, apply a standard OpenAPI Overlay — in
`lotsman.yaml`, see the correction in `inspect`/`explain-call`, and have `serve --watch` pick it up
without a restart. Stages 2 (`internal/workbench`) and 3 (`lotsman ui`) get their own plans.

**Architecture:** corrections are configuration (`internal/config`), translated by the CLI into
`openapi.Corrections` and applied inside the adapter (`internal/openapi`) while each operation is
translated — after schema normalization, before the IR is final — so refusals decided there can be
lifted. Overlays are applied to the root bytes before parsing, in the same package (Constitution
VIII). Catalog decisions (`enabled`, `effect`, `authProfile`) stay in `internal/catalog`. Reload
re-resolves the configuration and refuses anything outside the catalog-shaping sections.

**Tech stack:** Go 1.25, `pb33f/libopenapi` v0.38.7 (its `overlay` package — no new dependency),
`go.yaml.in/yaml/v4`.

## Global constraints

- No new direct dependency (Constitution VII). libopenapi types stay in `internal/openapi`
  (Constitution VIII).
- Same spec + config ⇒ same catalog and digest (Constitution IV). Never iterate a map into output.
- A correction that matches nothing is an error naming it, never a smaller catalog (spec §1).
- A patch never contains a key starting with `$`; nothing touches servers, security schemes,
  method or path (spec §1).
- `serve`'s call path does not change; corrections apply when a catalog is built (spec §2).
- **Testing (maintainer's decision, 2026-10-16, deviation from Constitution X):** while
  integrating, verify each task with `go build ./...` plus the one smoke command the task names;
  the full suite is task 8. `go test ./...` must still pass at every commit — existing tests are
  updated in the task that breaks them.
- Commit messages in Russian, in the style of `git log` (one line, no prefix), ending with the
  `Co-Authored-By` line.

## Technical context

- `internal/openapi/body.go:88` `selectMediaType` is the FR-29 gap. `buildBody` gains the pin.
- `internal/openapi/parameters.go:56` `buildInput` reads style/explode/required straight off
  `*v3.Parameter`. A correction copies the parameter struct (never mutates it — path-level
  parameters are shared between operations) and passes schema replacements beside it.
- Normalized schemas are `domain.Schema` (`map[string]any`), JSON Schema 2020-12. Patches are
  RFC 7396 merges over those, so the patch language is the published language and libopenapi
  never sees a correction.
- `bundle.normalizeProxy` (`schema.go:66`) is the one place a `$ref` is still visible; component
  patches hook there, for both inlined scalar references and `$defs` entries.
- `catalog.ValidateOverrides` already refuses an override that matches no operation or several,
  so the adapter only reports what it alone can see: a missing parameter, a missing component, a
  media type the operation does not declare or lotsman cannot send.
- `parseSpec` (`internal/cli/serve.go:404`) is shared by serve, inspect, explain-call, validate and
  operations; it takes the resolved `config.Runtime` from now on, so every command sees the same
  corrected document.
- Reload: `serve`'s `source` closure captured the startup `runtime`. It now re-resolves through
  the same `resolveConfig` call with the same flag values and compares everything outside
  `Overrides`, `ComponentOverrides`, `Overlays`, `IncludeTags`, `Mode`, `MaxSerializedBytes`,
  `DescriptionBytesPerTool`.

## Files

```text
docs/adr/0021-operator-corrections-and-local-workbench.md   new: the decision vs Constitution IX, spec §10
internal/config/config.go          Spec.Overlays, ComponentOverride, ParameterOverride, BodyOverride,
                                   OperationOverride.{MediaType,Parameters,RequestBody,Note}, validation
internal/config/runtime.go         Runtime.{Overlays,ComponentOverrides}; Resolve copies them
internal/config/export.go          export the new sections
internal/domain/domain.go          Correction; Operation.Corrections
internal/openapi/corrections.go    new: Corrections types, matching, problems, merge patch, clone
internal/openapi/overlays.go       new: ApplyOverlays over libopenapi/overlay
internal/openapi/openapi.go        Options.Corrections; Document.CorrectionProblems; wiring in buildOperation
internal/openapi/body.go           pinned media type; body patch
internal/openapi/parameters.go     schema replacements
internal/openapi/schema.go         component patches in the bundle; schemaTypes helper
internal/catalog/catalog.go        Tool.Corrections (json:"-": not part of the digest)
internal/catalog/report.go         OperationVerdict.Corrections
internal/cli/corrections.go        new: config.Runtime → openapi.Corrections; reload comparison
internal/cli/serve.go              parseSpec(runtime); overlays; reload re-resolves config; watch set
internal/cli/{inspect,explain,validate,operations}.go   new parseSpec signature; provenance output
docs/spec.md §5.2, README.md, docs/corpus.md                documentation
```

## Review focus

1. A path-level parameter shared by two operations, corrected on one: the other must keep the
   document's version (the copy in `applyParameters`).
2. A body or parameter whose schema is a `$ref` into `$defs`, patched for one operation: the
   shared `$defs` entry must stay untouched for that operation's other uses and for other
   operations (`inlineTopRef` + `cloneSchema`).
3. A correction that makes the shape untranslatable (object in a query value): the operation is
   rejected with the ordinary code, the configuration is not an error.
4. Reload after editing `execution.allowMutations` in the same file as an override: refused as a
   whole, the running catalog stays, the log names `AllowMutations`.
5. An overlay that injects `$ref: ../../secrets.yaml`: refused by the existing confinement, because
   overlays are applied before `scanRefs`.

Each of these has a test in task 8.
