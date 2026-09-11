# lotsman Constitution

<!-- Governing principles. Every spec, plan, task and PR is checked against this document.
     Full project specification: docs/spec.md (v1.1.3). Pipeline detail: docs/pipeline.md. -->

## Core Principles

### I. Lotsman Doesn't Guess (NON-NEGOTIABLE)
If an API operation cannot be translated safely, it is not executed. Ambiguity (serialization,
security, effect, media type) → the operation is `rejected` or `partially_supported` with a
machine-readable reason, never approximated. `--lax` may shrink the catalog; it never relaxes
execution correctness, egress, auth or mutation policy.

### II. Strict & Fail-Closed Defaults
Read-only by default. Mutations require explicit `execution.allowMutations` plus policy rules.
Missing client capability for a required interactive approval = refusal before network, not
silent execution. Redirects, remote `$ref`, retry: off by default.

### III. Server-Side Policy Is the Only Security Boundary
Tool annotations are UX hints. MCP interactive approval (input-required/MRTR) is a UX safety
mechanism and MUST NOT be treated as authentication, authorization or proof of human presence.
Enforcement lives in: inbound authorization + server-side policy + effect policy.

### IV. Determinism
Same spec + config + version ⇒ identical operation keys, tool names, catalog order, schemas
and digest. No map-iteration-order dependence. Golden tests enforce this.

### V. Untrusted Inputs Everywhere
The OpenAPI document, `$ref` targets, descriptions, recipes, tool arguments, upstream responses
and inbound tokens are untrusted. Descriptions are sanitized (they reach the LLM context);
spec fetch / ref resolution / egress each have their own network policy (SSRF, redirects,
private ranges, DNS rebinding).

### VI. Secrets Are References, Never Literals
`env:` / `file:` / `keyring:` references only. No literal secrets in CLI args, config exports,
recipes, logs, errors, audit events or tool output. Canary-secret tests enforce redaction.

### VII. Minimal Dependency Surface
Short go.mod is a feature (SBOM, supply chain). No DI frameworks, no retry libraries, no HTTP
client frameworks, no logging frameworks beyond `log/slog`. A new dependency needs a reason a
few hundred lines of our own code cannot provide. Target: ≤7 direct deps after M1.

### VIII. Internal Until Proven Public
All packages live in `internal/` until the domain model survives M0–M1 and an external consumer
exists. The OpenAPI parser (libopenapi) is confined to its adapter package; the rest of the code
depends only on the normalized domain model (IR).

### IX. Abstractions Earn Their Existence
A new interface/abstraction appears only with a second implementation, an explicit test seam,
or a confirmed next-milestone need. Recipes, gateway, RBAC, admin UI are deferred hypotheses —
they must not leak premature infrastructure (Redis/SQLite/web) into the core.

### X. Every Step Ships Working Software
Tracer-bullet development: main is always green; each task ends in something runnable or a
passing test. Tests are added in the same task as the functionality (pipeline pitfalls checklist
is the test plan, not future work).

## Constraints

- Language: Go ≥ 1.25 (floor of official MCP go-sdk). MCP protocol: 2026-07-28 via
  `modelcontextprotocol/go-sdk` v1.7+, legacy compatibility through the SDK.
- License: Apache-2.0. DCO for contributions.
- Platforms: linux/amd64+arm64, darwin/arm64(+amd64), windows/amd64. Single static binary.
- stdio transport: stdout is protocol-only; all logging to stderr.

## Governance

Constitution supersedes other practices. Amendments via PR with rationale; architectural
decisions recorded as ADRs in docs/adr/. The frozen product spec (docs/spec.md v1.1.3) changes
only through ADRs produced by milestone spikes. Complexity must be justified against
Principles VII and IX.

**Version**: 1.0.0 | **Ratified**: 2026-09-11
