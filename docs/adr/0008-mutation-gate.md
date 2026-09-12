# ADR-0008: The mutation gate

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M0 implementation review (T019, T020)

## Context

Step 6 gives lotsman the ability to send a request body -- that is, to change someone else's
state. The gate that decides whether it may had to exist *before* that ability, not after, or
the first release with a body would also be the first release that could delete something by
accident.

## Decision

1. **POST, PUT and PATCH infer `unknown`, not `write`.** The method cannot tell a draft save
   from a payment, and `write` would be a claim lotsman cannot support. `unknown` is refused by
   the default policy exactly like a write, but a report can say which of the two it is, and a
   later recipe or override can state the truth explicitly. DELETE infers `destructive`;
   GET/HEAD/OPTIONS infer `read`.
2. **An unset effect counts as unknown.** A caller that forgets to classify gets a refusal, never
   a free pass.
3. **The suspicious-verb scanner matches whole tokens.** `GET /cache/refresh` is caught;
   `GET /updates` is not, because "updates" is not "update". Identifiers are split on camelCase
   and punctuation, so `rebuildCache`, `rebuild_cache` and `/rebuild-cache` all yield "rebuild".
   The scanner reads the operationId and the path only -- **never the summary**: that is untrusted
   prose from the document, and letting it move an effect would give the spec author's
   copywriting a vote in policy. Its only power is to demote `read` to `unknown`; it can never
   authorize anything.
4. **Publication is not permission.** Every *supported* operation is published, mutating ones
   included, with annotations derived conservatively from the effect (anything not a known read is
   advertised as potentially destructive). A policy-blocked call refuses before the network and
   says which code blocked it and what would change it. A model that can see the tool and read the
   refusal is better off than one guessing why a documented endpoint is missing. *Rejected*
   operations remain unpublished: those lotsman cannot translate at all.
5. **Policy blockers are reported separately from execution blockers.** "lotsman cannot do this
   yet" and "the operator said no" call for different actions -- wait for a release, or change the
   configuration -- so they are different fields with different reason codes, never merged into
   one "blocked" flag.
6. **The catalog digest depends on the policy.** Two runs with the same spec and different
   `allowMutations` produce different catalogs, which is what data-model.md invariant 1 says
   ("spec + config"). A digest that ignored policy would call two different tool surfaces equal.
7. **A spec may not parameterize a protected header.** `Authorization`, `Host`, `Content-Type`,
   `Content-Length`, `Cookie`, `Transfer-Encoding` and friends are refused at translation time
   (the operation is rejected) and again in the request builder. Otherwise a document could hand a
   model the ability to supply its own credentials, contradict the body being sent, or corrupt the
   framing.
8. **Header values are refused, not sanitized.** Only printable US-ASCII passes; a CR, LF, NUL or
   any other control character fails the call (pitfall #10). Stripping the offending byte would
   send a header the caller did not ask for, which is a guess about intent.
9. **One request media type, chosen deterministically.** Exact `application/json` wins; otherwise a
   single `+json` structured type. Several competing JSON types, or none, rejects the operation
   with `unsupported_media_type` (FR-29) -- "take the first one" is how a converter posts JSON to
   a multipart endpoint.
10. **A body schema that accepts anything is refused.** An empty schema cannot validate an
    argument, and FR-23 requires validation before the network. Conditional subschemas (`not`,
    `if`/`then`/`else`) and resolved reference cycles are refused for the same reason; `allOf`,
    `oneOf` and `anyOf` are republished as authored.

## Consequences

- `--allow-mutations` (and its explicit opposite `--read-only`) gate the whole non-read surface.
  Per-operation allow/deny rules by namespace, key, tag and effect (FR-41) refine this gate when
  the config layer lands; they do not replace it.
- `request_body_not_implemented` is retired: a JSON body is implemented, and any other media type
  is a rejection rather than a temporary gap.
- Header parameters no longer block execution. Cookie parameters still do.
- `lotsman operations` reports the effect and answers the operator's real question -- would this
  run? -- by applying the same default policy the server uses, with `--allow-mutations` to preview
  the other setting.
- Schema `default`s are stripped from body schemas too, at every nesting level, for the reason
  ADR-0007 records: the MCP SDK would otherwise fill them in behind lotsman's back.
- Interactive approval (FR-44–46) is still out of scope; M1 gates mutations by policy only, and
  approval is a UX mechanism that is explicitly not a security boundary (FR-44a).
