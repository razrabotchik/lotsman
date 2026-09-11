# Feature Specification: Core Runtime (M0–M1)

**Feature Branch**: `001-core-runtime`
**Status**: Ready for planning
**Source**: docs/spec.md v1.1.3 (§3.1, §4, §13) — this document scopes the first milestone slice.

## Overview

lotsman loads a local OpenAPI 3.0/3.1 specification, normalizes it into a strict internal
model, and serves the supported operations as MCP tools over stdio — with argument validation,
read-only-by-default policy and honest reporting of what is and is not supported.

Out of scope for this feature (later specs): search mode (002), HTTP transport & inbound auth
(003), OAuth upstream (004), recipes, gateway, admin.

## User Scenarios

- **US-1 (P1)**: As a developer, I run `lotsman serve ./openapi.yaml`, add it to Claude
  Desktop, and my agent can call the API's read operations. *Acceptance*: a GET with path and
  query parameters and a JSON POST (when mutations enabled) execute correctly end-to-end.
- **US-2 (P1)**: As a developer, I run `lotsman inspect ./openapi.yaml` **before** serving and
  see exactly which operations are supported / partially supported / rejected, and why.
  *Acceptance*: every rejected operation carries a machine-readable reason code;
  `--json` output follows a versioned schema.
- **US-3 (P1)**: As a security engineer, I rely on read-only default: no write/destructive/
  unknown operation reaches the network unless explicitly allowed. *Acceptance*: with default
  config, a POST call attempt is refused before any connection is opened.
- **US-4 (P2)**: As a developer, my API key or bearer token is provided via `env:` reference
  and never appears in logs, errors, tool descriptions or results. *Acceptance*: canary-secret
  test suite passes.
- **US-5 (P2)**: As a developer feeding lotsman a messy real-world spec, I use `--lax` to serve
  the supported subset while seeing warnings for the rest. *Acceptance*: GitLab spec serves
  its supported subset; nothing unsupported executes.

## Functional Requirements (scope of this feature)

Mapped to docs/spec.md numbering; the frozen spec is the authority on details.

- Spec loading: FR-1 (OAS 3.0/3.1, YAML/JSON), FR-3 (file/stdin), FR-4 (local refs confined
  to root), FR-6 (cycles without panic), FR-7 (errors with JSON Pointer), FR-8 (`--lax`),
  FR-9 (parse limits incl. YAML alias budget).
- Capability report: FR-10–FR-13, FR-12a (first-class report; `inspect diff` may land in 002).
- Tool profile: FR-14–FR-19 (deterministic names ≤64 chars, sanitized budgeted descriptions).
- Schemas: FR-20–FR-25 (JSON Schema 2020-12, OAS 3.0 normalization, grouped inputs always,
  server-side argument validation).
- Serialization: FR-26–FR-29 (M1 matrix: path/simple, query/form, header/simple, cookie/form,
  JSON body; per-location style/explode defaults; percent-encoding; CRLF defense).
- Execution: FR-30–FR-39 (servers resolution, timeouts, redirects off, retry off, bounded
  response, header allowlist, isError mapping, explain-call).
- Effect & policy: FR-40–FR-43 + EffectDecision(source, confidence) + suspicious-verb warnings.
  Interactive approval (FR-44–46) lands with mutations UX in 002; M1 gates mutations by policy only.
- Auth: FR-55–FR-62 (security OR/AND semantics; apiKey/basic/bearer via secretRef; redaction).
- Transport & CLI: FR-68 (stdio, logs to stderr), FR-72 (atomic reload may defer to 003),
  FR-76–FR-78 (`serve`, `inspect [--json]`, `validate`, `operations`, `explain-call`,
  `version`; distinct exit codes).

## Success Criteria (measurable)

1. M0 gate: three dissimilar real specs (GitLab, DigitalOcean, Kubernetes) pass the declared
   support profile with no engine patches; the 4 spike questions answered in ADRs 0001–0004.
2. Acceptance criteria 1–5, 7 of docs/spec.md §13 pass (inspect determinism; OAS 3.0+3.1 GET
   and JSON POST over stdio; unsupported styles never execute approximately; read-only
   enforcement; canary secrets never leak).
3. Parse+normalize p95 < 2 s for a 5 MiB / 1000-operation spec on the CI runner; RSS < 200 MiB.
4. Two consecutive runs on the same spec+config produce byte-identical catalog and digest.
5. All 14 pipeline pitfalls (docs/pipeline.md checklist) are covered by at least one test each.

## Assumptions & Dependencies

- Official MCP go-sdk v1.7+ provides stdio transport and tool registration (validated in T003).
- Swagger 2.0, remote refs, URL spec sources, multipart: explicitly out of scope (docs/spec.md §1.4, FR-2/3/5).
- Corpus specs are vendored in testdata/corpus with pinned digests.
