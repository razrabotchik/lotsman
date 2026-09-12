# ADR-0001: OpenAPI parser (libopenapi)

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M0 gate (T022)

## Context

lotsman needs an OpenAPI 3.0/3.1 parser that survives real vendor documents, keeps line/column
provenance for diagnostics, and does not become the project's architecture. The alternatives
considered were `pb33f/libopenapi`, `getkin/kin-openapi`, and parsing YAML directly into lotsman's
own model.

The verdict was deliberately deferred to the M0 gate so it could be made against measurements
rather than README claims.

## Decision

**libopenapi, confined to `internal/openapi`.**

It survived the corpus: 5.2 MB of Stripe in 0.40 s at 150 MB RSS, 833 KB of Kubernetes in 0.22 s,
with circular references detected and reported rather than hung on. It keeps YAML node positions,
which is what makes a diagnostic point at a line instead of at a concept. It collects structural
errors with `errors.Join` instead of failing at the first one, which is what lets a report list
every problem in a document rather than the first.

Parsing YAML by hand was rejected once the 3.0→3.1 differences were enumerated
(`nullable`, boolean exclusive bounds, `example`/`examples`, `$ref` resolution and cycle
detection): that is a parser, and writing one is not this project's contribution.

The confinement rule is what makes the choice reversible, and it held for the whole of M0: no
libopenapi type appears outside the adapter, and every downstream package depends on
`internal/domain` alone (Constitution VIII).

## Consequences and known sharp edges

Three surprises are worth recording, because each one cost a debugging session and each is a
reason the confinement rule earns its keep:

1. **The default configuration logs to stdout.** On stdio transport that corrupts the JSON-RPC
   stream. `Parse` now requires a caller-supplied logger and falls back to stderr, never to
   upstream's default (found in T007).
2. **`BasePath` is not a passive hint.** Setting it — the obvious way to tell the parser where the
   document came from — switches the rolodex into indexing every YAML and JSON file under that
   directory, which is precisely the arbitrary-file read that root confinement exists to prevent.
   lotsman carries the root path itself and leaves `BasePath` unset (ADR-0006).
3. **Version errors speak about the library's API.** `"supplied spec is a different version (oas2).
   Try 'BuildV2Model()'"` is not a sentence for an operator. lotsman decides the version from the
   document before handing over the bytes (found by running GitLab at the gate).

No cancellation hook exists, so the parse deadline bounds waiting rather than work; the byte, node
and `$ref` budgets are what keep the work finite (ADR-0006).
