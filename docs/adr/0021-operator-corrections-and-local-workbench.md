# ADR-0021: Operator corrections, and a local workbench to make them

- **Status**: Accepted
- **Date**: 2026-10-16
- **Decision owner**: specs/008-local-admin-ui; FR-29; docs/spec.md §5.2, §10; Constitution IX

## Context

`inspect` names every refusal and then leaves the operator with two options: edit the vendor's
document, which the next update undoes, or live without the operation. Some refusals are a choice
lotsman must not make but a person can (Kubernetes `apps/v1`: 12 PATCH operations offering four
media types each). Some are the document's own mistakes. FR-29 promised a media-type override that
was never built.

docs/spec.md §10 defers an admin interface to M6, and Constitution IX lists it among the deferred
hypotheses that must not leak web infrastructure into the core.

## Decision

1. **Corrections are configuration.** `operationOverrides` gain `mediaType`, `parameters`,
   `requestBody.patch` and `note`; a new `componentOverrides` section patches a shared schema;
   `spec.overlays` applies standard OpenAPI Overlay documents. They are applied in the adapter,
   after schema normalization, because that is where the refusals they lift are decided.
2. **Strict, like overrides.** A correction that matches nothing is an error that names it. A patch
   cannot contain a `$`-keyword or a key that is not a string, and no correction field
   (`operationOverrides`, `componentOverrides`) touches servers, security schemes, method or path.
   An overlay is different in kind: it edits the document itself, so it can change anything the
   vendor could, and it is bound by the same `$ref` confinement, `--base-url` egress and policy as
   the vendor's text. Corrected operations carry `local_override` provenance into every report.
3. **Reload republishes corrections, not policy.** A reload that finds anything else changed in the
   configuration is refused and the running catalog stays (consistent with FR-72 and feature 007).
4. **The local workbench is brought forward from M6, narrowed.** `lotsman ui` is single-user,
   bound to loopback, edits only the local configuration, and lives in packages `serve` does not
   import. The multi-user, authenticated admin of §10 stays deferred.
5. **Corrections are not made through MCP tools.** The model fills tool arguments; a model or a
   prompt injection could "answer" for the person and lower an effect to `read`.

## Consequences

- FR-29 is implemented. Kubernetes `apps/v1` reaches 77/77 with twelve `mediaType` lines.
- Components in external files of an exploded specification cannot be patched in this release.
- A schema a correction produces is compiled at load; one that does not compile stops `serve`.
- Constitution IX's "admin UI" hypothesis is confirmed for the local, single-user case only.
