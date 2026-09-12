# ADR-0009: References stay references, and one normalizer

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M1 implementation review (T023–T026)

## Context

Until Phase B, lotsman walked the *resolved* schema tree and inlined everything it found. That
was the shortest path to a published schema, and it had three consequences the corpus made
impossible to ignore:

- Kubernetes published 4.25 MB of `tools/list` for 65 tools, because a component used in five
  places was serialized five times.
- A recursive schema (`Node.child: Node`) could not be published at all. The walk has to stop
  somewhere, and whatever it does at that point — truncate, refuse, invent a depth — is a
  statement about the API that the document never made.
- Every file reference was refused outright, which removed DigitalOcean (an index over ~2,900
  documents) from the addressable world entirely.

Separately, the parameter and body schema translations had grown as two code paths doing the same
OAS-3.0-to-2020-12 work with different bugs available to each.

## Decision

1. **A `$ref` is published as a `$ref`.** Each referenced component is normalized once into the
   tool's `$defs`, and every use points at it. JSON Schema 2020-12 resolves `$defs` natively, the
   MCP SDK accepts it, and lotsman's validator compiles the same document — all verified end to
   end, including validation *at depth* inside a recursive value.
2. **A cycle is no longer a refusal.** It closes in `$defs` exactly as the author wrote it. The
   earlier plan (cut at depth N, mark the operation partial) is obsolete: it existed to work
   around inlining, and nothing needs working around now.
3. **A reference to a scalar is inlined.** `"#/$defs/Limit"` pointing at `{"type":"integer"}` is
   an indirection that costs a reader more than it saves. Only composites are bundled.
4. **File references are followed, confined to the directory the document came from.** Every hop
   is resolved relative to the document that contains it, expanded through symlinks, and checked
   to be inside the root; the closure is walked transitively under document and byte budgets. The
   verified list is then handed to the parser as an allowlist, so a file that exists next to the
   spec but is never referenced is never read. Remote references are still never fetched, and a
   document from stdin has no root, so it can resolve nothing.
5. **One normalizer, two targets.** Parameters and bodies share the translation and differ only in
   the shapes they accept (a URL slot holds one string) and in the reason code they refuse with.
6. **`readOnly` properties are excluded from input schemas**, and dropped from `required` with
   them: in OAS, `required` + `readOnly` means required *in the response*. `writeOnly` survives,
   because that is exactly what an input is.
7. **Security keeps its shape**: alternatives are OR, requirements within one are AND, each
   carrying the scheme definition it names. An operation whose every alternative needs a credential
   the core providers cannot supply is rejected rather than published as a tool that always fails.

## Consequences

- Kubernetes: 4.25 MB → 2.23 MB of `tools/list` for the same 65 tools (−47%).
- DigitalOcean: from "refused before parsing" to 631 of 659 operations translated in 0.3 s, with a
  743 KB catalog.
- `MaxRefDocuments` moved from 64 to 4,096, set from the measured closure rather than from
  intuition — the corpus's second job after catching regressions.
- The parser's own log is discarded by default (`Options.ParserLog` reinstates it). Everything it
  can tell an operator reaches lotsman as a returned error and becomes a diagnostic with a pointer
  and a position; one exploded specification produced 662 log lines about "the rolodex" next to
  662 diagnostics naming the reference that could not be followed.
- `serve` and `inspect` cap how many diagnostics reach the log. The full list is the report's job.
- Circular-reference checking is disabled in the parser, because a circular reference is now a
  legitimate shape rather than a document error.
