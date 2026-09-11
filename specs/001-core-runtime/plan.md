# Implementation Plan: Core Runtime (M0–M1)

**Branch**: `001-core-runtime` | **Spec**: ./spec.md | **Pipeline detail**: docs/pipeline.md

## Summary

Tracer-bullet build: first a thin end-to-end channel (Claude Desktop → stdio MCP → HTTP call),
then widen it capability by capability. Build-time pipeline (load→parse→refs→normalize→catalog)
is separated from runtime path (validate→serialize→auth/egress→shape) by the domain IR.

## Technical Context

- **Language**: Go ≥ 1.25 (SDK floor)
- **Dependencies (M1 target ≤7 direct)**:
  `modelcontextprotocol/go-sdk` v1.7+ (MCP, stdio, MRTR),
  `pb33f/libopenapi` (parse; confined to adapter),
  `santhosh-tekuri/jsonschema/v6` (2020-12 validation; compare with libopenapi-validator in T-spike),
  `spf13/cobra` (CLI), `knadh/koanf/v2` + `gopkg.in/yaml.v3` (config).
  Explicitly excluded: DI frameworks, retry libs, HTTP client libs, zap/logrus, bleve, viper.
- **Testing**: golden (spec+config → catalog/report), httptest integration, fuzz (yaml load,
  names, serialization, truncation), e2e via official MCP client over stdio, canary secrets.
- **Performance goals**: spec.md Success Criteria #3; tool list built once per catalog snapshot.

## Architecture

Packages (all `internal/`, Constitution VIII):

```text
cmd/lotsman        wiring only (manual DI, ~50 lines; no fx/wire — Constitution VII)
internal/config    koanf loading, precedence defaults<file<env<flags; secretRef type
internal/specsource file/stdin + limits (bytes, YAML alias budget, time)
internal/openapi   libopenapi adapter + version-specific normalization → emits IR only
internal/domain    Operation, InputModel, SecurityAlternative, EffectDecision, Diagnostic
internal/catalog   deterministic toolgen: names, grouped schemas, budgets, digest
internal/policy    effect classification, read-only gate, allow/deny
internal/requestbuild  OAS serialization (style/explode matrix), URL build
internal/auth      providers: apikey/basic/bearer; secretRef resolution; redaction helpers
internal/egress    origin allowlist, redirect deny, limits (full policy grows in 003)
internal/response  bounded reader, truncation safety, header allowlist, isError mapping
internal/mcpserver tool registration, stdio serving, MCP result mapping
internal/audit     event structs + stderr sink (storage sinks deferred)
internal/buildinfo version/commit/SDK/protocol revisions
```

Runtime call order (docs/pipeline.md §5–8):
policy → validate args → serialize → egress check → auth (last before wire) → bounded read → shape → audit.

## Milestone Mapping

- **Phase A = M0 spike (gate)**: T001–T022. Exit: 4 ADR answers (libopenapi fitness,
  normalization weight, serialization complexity, IR viability) on GitLab/DO/K8s corpus.
- **Phase B = M1 hardening**: T023–T040. Exit: spec.md Success Criteria all green,
  v0.1.0-alpha released.

## Constitution Check

- I/II: rejection verdicts + read-only default land before mutations are possible (T024 before T026). ✔
- IV: sorted traversal everywhere; determinism golden test T021. ✔
- VI: secretRef type introduced with auth task, canary tests same task. ✔
- VII: 5 direct deps planned. ✔  VIII/IX: all internal, no speculative interfaces. ✔
- X: every task ends runnable/tested; main always green. ✔

## Risks

1. **OpenAPI semantics long tail** (top risk per reviews) → mitigated by support matrix +
   reject-not-guess + corpus tests; timebox per edge case, reject when over budget.
2. libopenapi API surprises → confined to adapter; ADR-0001 records fallback (kin-openapi) cost.
3. jsonschema validator mismatch after normalization → dual-run spike task T015.
4. MCP client quirks (Claude Desktop tool name limits, schema strictness) → T003 validates early.
