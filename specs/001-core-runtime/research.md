# Research: Core Runtime

Consolidated findings from two external architecture reviews and market analysis (Aug–Sep 2026).
Decisions here feed plan.md; open items become ADRs during the M0 spike.

## Decisions

| # | Decision | Rationale | Alternatives rejected |
|---|---|---|---|
| R1 | MCP SDK: `modelcontextprotocol/go-sdk` v1.7+ | Official, Google co-maintained; MCP 2026-07-28 (stateless HTTP, MRTR) + legacy compat; Go floor 1.25 since v1.4.1 | mark3labs/mcp-go (pre-v1 era choice) |
| R2 | Parser: `pb33f/libopenapi`, adapter-confined | Only Go parser solid on both 3.0 & 3.1 and huge specs; refs/cycles handling | kin-openapi (weak 3.1) |
| R3 | Own domain IR between parser and everything else | Parser replaceability, golden-testability, normalization point | Using parser AST throughout |
| R4 | Args validation: JSON Schema 2020-12, grouped inputs ALWAYS | Grouped = stable schemas under API evolution (flat-until-collision breaks determinism promise) | flat-until-collision |
| R5 | HTTP: stdlib net/http + RoundTripper chain | Policy as composable, testable layers; no framework defaults to fight | resty, retryablehttp |
| R6 | Effect model with EffectDecision{source, confidence} + verb scanner (warning only) | Real APIs have mutating GETs; provenance keeps honesty | plain method→effect map |
| R7 | Interactive approval ≠ security boundary | input_required can be auto-fulfilled by clients; no proof of human | treating MRTR as authz |
| R8 | Search mode meta-tools split call_read/call_mutating (feature 002) | Preserves effect semantics on MCP surface | universal call_operation |
| R9 | Scaling: stateless Streamable HTTP (feature 003) | MCP 2026-07-28 removed protocol sessions; round-robin LB is the normal path; Redis only for app state | sticky sessions by default |
| R10 | License Apache-2.0; DCO; name `lotsman` (checked clean 2026-08-31) | Patent grant for infra tool; naming: brandable, security+guidance metaphor | MIT; mcp-* names (saturated) |
| R11 | No DI framework, manual wiring in cmd/ | ~15 constructors, linear graph; explicitness; SBOM | uber/fx, wire |

## Market context (positioning input)

Adjacent projects: Tyk api-to-mcp (Node, converter), OpenMCP (registry of generated servers),
IBM ContextForge (Python heavyweight gateway, 4.3k★), plus many small openapi-mcp converters.
Differentiation is NOT "yet another converter" but: capability report + reject-not-guess +
single security-first Go binary. Promise: *"lotsman doesn't guess."*

## Open items → ADRs (resolved during M0)

1. **ADR-0001** libopenapi fitness on corpus (GitLab/DO/K8s): parse fidelity, memory, API ergonomics; fallback cost estimate.
2. **ADR-0002** Validator: santhosh-tekuri/jsonschema/v6 vs libopenapi-validator after our normalization (dialect correctness, performance).
3. **ADR-0003** IR shape verdict: what survived the corpus, what changed; grouped-input schema examples.
4. **ADR-0004** MCP client field notes: Claude Desktop name limits, schema strictness, tools/list size behavior → portable tool profile defaults (name ≤64, catalog budget).
5. (Deferred to 004) Minimal cross-platform secure token store without breaking single-binary UX.
6. (Deferred to 002) Search benchmark corpus with natural-language queries; Recall@k targets.
