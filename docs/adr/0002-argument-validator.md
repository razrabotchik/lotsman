# ADR-0002: Argument validator

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M0 implementation review (T015 spike)

## Context

Arguments arriving from a model are untrusted input at the point where they turn into a URL.
FR-23 requires them to be validated against the published input schema before the network, and
FR-22's grouped shape means the thing being validated is a JSON object with
`additionalProperties: false` at every level -- not an OpenAPI request.

Three candidates:

1. **santhosh-tekuri/jsonschema v6** -- a JSON Schema 2020-12 implementation that passes the
   official JSON-Schema-Test-Suite. Validates the published schema as data. One dependency
   (`golang.org/x/text`, for localized messages).
2. **pb33f/libopenapi-validator** -- validates an `*http.Request` against the OpenAPI document.
3. **google/jsonschema-go** -- already in the module graph, because the MCP SDK uses it for tool
   schema inference *and* for validating tool arguments.

## Decision

**santhosh-tekuri/jsonschema v6, on the published grouped schema, in `internal/argvalidate`.**

libopenapi-validator is rejected on three independent grounds, any one of which is enough:

- it validates a request that has already been built, and FR-23 requires refusal *before* the
  request exists -- lotsman would have to serialize an argument in order to find out it was
  invalid;
- it validates against the OpenAPI document, so libopenapi types would have to leave the adapter
  package (Constitution VIII);
- it does not validate the grouped argument object, which is the actual contract with the model.

google/jsonschema-go is not adopted as *the* validator even though it is already present, because
the MCP SDK's validation is the SDK's behaviour, not lotsman's boundary: it applies only on the
typed `mcp.AddTool` path, and a future registration path (search mode, policy tools) would
silently lose it.

The schema is marshalled and re-read as JSON before compiling, so the compiled schema is
byte-for-byte the document the client was shown. Formats are asserted rather than treated as
annotations: if a spec says a path parameter is a uuid, a value that is not one must not reach the
API. Unknown formats stay annotations, so this cannot reject an OAS format the library has never
heard of.

## Consequences

- Two validators run on every call, and this is deliberate. The SDK checks arguments against the
  same published schema and usually rejects first; lotsman's check is the one that enforces
  formats, produces a deterministic single-line message naming the offending location, and carries
  an `errs` class for T034 to map onto an MCP prefix. The difference is observable and tested:
  `TestCallCatalogToolEnforcesFormatsTheSDKTreatsAsAnnotations` fails if only the SDK validates.
- A schema that does not compile makes the tool publish as *not executable* rather than
  unvalidated: an argument that cannot be checked must never be serialized.
- Direct dependencies rise to four (go-sdk, libopenapi, yaml/v4, jsonschema/v6), plus
  `golang.org/x/text` indirectly. Still inside the Constitution VII target of seven.
- The spike was a design comparison plus an empirical check of the chosen library against the
  published schemas (see `internal/argvalidate`), not a full three-way bake-off. If a 2020-12
  corner case ever disagrees between the two validators that do run, the disagreement is
  observable in tests rather than in production.
