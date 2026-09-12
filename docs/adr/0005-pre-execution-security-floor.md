# ADR-0005: Security floor before real HTTP execution

- **Status**: Accepted
- **Date**: 2026-09-11
- **Decision owner**: M0 implementation review

## Context

The tracer bullet enabled a real parameterless GET before the planned egress package, redirect policy and full parameter/auth model existed. A path without `{...}` was treated as parameterless even when the operation declared query/header/cookie parameters, a request body or inherited security. Partial libopenapi models with document errors were also allowed to feed `serve`.

That sequencing violated the governing rule that missing semantics cause refusal before network. It also let Go's default HTTP client follow redirects even though redirects are documented as disabled by default.

## Decision

1. Translation support, MCP publication and runtime executability are separate states. Supported tools may remain discoverable during a tracer bullet, but machine-readable `executionBlockers` prevent request construction and network I/O.
2. Until parameters, JSON body and auth are implemented, their presence adds a blocker. Explicit `security: []` correctly overrides inherited security and does not add an auth blocker.
3. Document-level error diagnostics are fatal to `serve` in both strict and lax modes. Strict mode also refuses any rejected/partial operation; lax mode may remove those operations but never clear execution blockers.
4. Redirects are denied from the first real HTTP call, including when a caller injects an `http.Client`; the default transport does not inherit proxy settings from the environment.
5. Until full `allowedOrigins`/CIDR/DNS policy exists, every real call requires an explicit `--base-url`; a server URL authored only by the untrusted spec is not egress authorization.
6. Suspicious mutation-like GET/HEAD/OPTIONS operations become effect `unknown` and are blocked until an explicit reviewed override.
7. Root digests use `sha256:<hex>`. Exploded specs will add a deterministic root+refs manifest so ref-only changes affect identity.
8. URL validation errors never echo the supplied URL because it may contain credentials or other canary secrets.

## Consequences

- The Step 4 demo must pass `--base-url`; existing spec-server inheritance remains available for inspection and later policy evaluation but is not sufficient to authorize traffic.
- T014/T020/T030 remove their corresponding blockers only after validation, serialization and auth tests pass.
- T032 expands the minimal egress floor rather than introducing it after traffic already exists.
- Capability reports and CLI output must show both support and executability.
