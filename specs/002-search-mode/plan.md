# Implementation Plan: Search Mode (M2 slice)

**Branch**: `002-search-mode` | **Spec**: ./spec.md | **Runtime detail**: docs/pipeline.md §4

## Summary

Feature 001 publishes one tool per operation. That works up to a catalog that fits in a model's
context and then stops working at all: Kubernetes `apps/v1` serializes to 2.23 MB of `tools/list`
for 65 tools, DigitalOcean to 743 KB for 631. This feature adds the mode for that shape — five
meta-tools over a local lexical index — and the measured rule for choosing between the modes.

The same tracer-bullet discipline as 001: a thin end-to-end channel first (five tools published,
search returns *something*, a read call executes), then quality. And the same non-negotiable: a
search result is discovery, never authorization. Every call re-validates identity, effect,
arguments, auth and policy against the catalog, exactly as a tools-mode call does.

## Technical Context

- **No new dependencies.** BM25 over a few hundred documents is a page of arithmetic; a search
  library (bleve) is explicitly excluded by 001's plan and nothing has changed. Constitution VII.
- **Reuse, not reimplementation**: the meta-tools call the same `argvalidate` → `requestbuild` →
  `egress` → `auth` → `response` path as tools mode. Anything that only works in one mode is a
  second security boundary, and there is only ever one.
- **Testing**: golden index (same spec ⇒ byte-identical postings and scores), a query benchmark
  with committed expectations (FR-54), corpus catalogs as the scale test, e2e over stdio for all
  five tools, and the T019 mutation gate re-asserted through `call_mutating_operation`.

## Architecture

```text
internal/searchindex   tokenization, postings, BM25 scoring, deterministic tie-break
internal/catalog       gains Mode selection and the search-mode tool set
internal/mcpserver     registers either the tools or the five meta-tools
```

`internal/searchindex` depends on `domain` only. It is built from the catalog, which is immutable,
so the index is immutable with it: one index per snapshot, never mutated in place (FR-74, NFR-12).

## The five meta-tools (FR-48)

| Tool | Returns | Notes |
|---|---|---|
| `search_operations(query, filters, limit)` | ranked operation ids with one-line summaries | never schemas: that is what reintroduces the size problem |
| `describe_operation(id)` | the full published input schema for one operation | the only place a large schema is paid for, and only when asked |
| `list_tags()` | the tag vocabulary with counts | lets a model narrow before searching |
| `call_read_operation(id, params)` | the tool result | refuses anything whose effect is not `read` (FR-50) |
| `call_mutating_operation(id, params)` | the tool result | published only when mutations are enabled (FR-48) |

## Mode selection (FR-52)

`--mode=tools|search|auto`, default `auto`. Auto decides on the **measured serialized catalog**
already reported by `inspect` (`catalog.serializedBytesEstimate`), not on an operation count: 65
Kubernetes tools weigh more than 600 DigitalOcean ones, and a count cannot tell them apart. The
threshold is configuration with a documented default, and the chosen mode is reported.

## Constitution Check

- I: a query that matches nothing returns nothing; no "closest" operation is invented. ✔
- II/III: `call_read_operation` cannot reach a non-read effect; the mutating tool is absent unless
  mutations are enabled, and policy is re-evaluated per call rather than trusted from the search
  result. ✔
- IV: index construction and scoring are deterministic, including tie-breaks; golden test. ✔
- VII: no new dependency. ✔
- IX: `searchindex` exists because a second consumer (mode=auto's estimate) and a measured need
  both exist; no interface until a second ranking implementation does. ✔
- X: every step ends with a runnable server.

## Risks

1. **Recall is a product risk, not a code risk** — a model that cannot find the operation is worse
   off than with a truncated tools list. Mitigated by committing a query benchmark *before* tuning
   (FR-54) and by `list_tags` + filters as a deterministic fallback path.
2. **`describe_operation` reintroduces the size problem** if a model calls it for twenty operations
   in a row. Mitigated by a per-response budget and by returning references to shared `$defs`
   rather than inlining (001 already publishes schemas that way).
3. **Two modes, two code paths** is the real architectural risk. Mitigated by the meta-tools being
   thin wrappers over the same call path, asserted by running the same e2e scenarios in both modes.
