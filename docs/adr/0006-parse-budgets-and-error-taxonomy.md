# ADR-0006: Parse budgets and the error taxonomy

- **Status**: Accepted
- **Date**: 2026-09-11
- **Decision owner**: M0 implementation review (T013d, T013e)

## Context

ADR-0005 closed the network side of the pre-execution floor. Two holes remained on the
input side. First, stage 0 bounded bytes and wall-clock time but nothing structural: a
2 KB YAML anchor bomb is well under every limit lotsman had, and libopenapi expands it
before anyone can object. Ref resolution had no depth, document or size budget at all,
and the directory a document was loaded from was discarded, so the stage that will
resolve `$ref` had no confinement root to enforce.

Second, every failure was a `fmt.Errorf` string. The CLI collapses all of them into exit
code 1, and the layers still to come (argument validation, effect policy, auth, egress)
would each invent their own vocabulary before T034 has to unify them into the exit codes
and MCP prefixes that `contracts/cli.md` freezes.

## Decision

1. **Expansion is measured, never performed.** Stage 0 decodes into a YAML node tree —
   which keeps aliases as alias nodes rather than expanding them — and computes the
   expanded node count with memoization and saturating arithmetic. A 9x9 bomb (387M
   expanded nodes) is refused in microseconds. The budgets are node count, alias count,
   nesting depth and expanded node count; the depth limit also bounds lotsman's own
   recursion, so a deeply nested document cannot exhaust the stack.
2. **A document that is not valid YAML or JSON fails at stage 0**, in terms of its
   source, before OpenAPI semantics are mentioned.
3. **`libopenapi`'s `BasePath` is deliberately left unset even though the root path is
   known.** Setting it switches the rolodex into indexing every YAML and JSON file under
   that directory — the arbitrary-file read that root confinement exists to prevent.
   `Source.RootPath` is therefore carried into the parse stage as lotsman's own
   confinement root, with `AllowFileReferences` and `AllowRemoteReferences` off. T025
   turns file references on with an explicit policy rather than as a side effect of a
   path assignment.
4. **Every `$ref` leaving the root document is refused** with the
   `external_ref_unsupported` reason code, a JSON Pointer and a line/column. A document
   read from stdin says so explicitly: it has no directory, so no future policy can make
   a relative reference resolvable.
5. **The `$ref` closure is budgeted before it is resolved**: the audit runs on the raw
   node tree, counting distinct referenced documents, aggregate bytes and the longest
   local `$ref` chain. A cycle is cut, not reported — truncation policy and the
   `cyclic_schema_truncated` diagnostic belong to T025; the budget only has to terminate.
6. **`MaxOperations` is checked after the model is built but before the IR is**, since
   libopenapi offers no earlier count. The byte and node budgets are what make reaching
   that point cheap.
7. **The parse deadline bounds waiting, not work.** libopenapi exposes no cancellation
   hook, so the deadline releases lotsman while the abandoned goroutine finishes on its
   own; the byte, node and `$ref` budgets are what keep that work finite. A deadline is
   classified as an invalid spec; an operator's cancellation is not.
8. **Error classes are `internal`, `usage`, `spec_invalid`, `unsupported`, `policy`,
   `auth`, `upstream`.** The set is closed, the carrier interface is unexported so it
   cannot be extended from outside, wrapping preserves the class, an outer re-tag wins,
   and an unclassified error reads as `internal` — a missing annotation degrades to "our
   fault", never to "safe".
9. **T034 owns the mapping to exit codes and MCP prefixes.** Until then the class is
   carried and logged but the CLI still uses 0/1/2, so `contracts/cli.md` is broken
   only once, deliberately.

## Consequences

- `openapi.Parse` takes a context and an `Options` value; the namespace/logger positional
  arguments are gone.
- A spec split across files stops working until T025 — correctly, since resolving one
  means reading a file chosen by an untrusted document. `lotsman operations` still lists
  what it can and names every refused reference.
- `internal/errs` must stay dependency-free: everything imports it, including `domain`'s
  consumers, so it cannot import anything that imports it back.
- The document and byte budgets of the ref closure are enforced on a closure that is
  currently always the root document plus the files it merely *asks* for. T025 extends
  the accounting instead of introducing it.
