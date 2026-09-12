# Feature 003: Selection, overrides and interactive approval

**Status**: opened at the close of feature 002. This is the remainder of M2 —
`filters, overrides, … interactive approval` (docs/spec.md §12). `catalog budgets`,
`lexical search`, `read/mutating tools` and `explain-call` shipped in 001 and 002.

## Why this exists

Three gaps, each visible in something already written down rather than in an intuition.

1. **The operator cannot narrow the catalog.** DigitalOcean publishes 631 tools. An operator who
   needs droplets does not need the other 580, and today has no way to say so: `catalog.includeTags`
   is in the example configuration (docs/spec.md §5.1) and is read by nothing. Search mode made a
   large catalog *servable*; it did not make it *smaller*.
2. **Effect classification cannot be corrected.** `POST` is `unknown` by construction and a
   suspicious `GET` is raised to `unknown` on purpose (§4.7). Both are only supposed to be
   fail-closed *until a reviewed override states the effect* — and `EffectSourceLocalOverride`
   exists in the domain model with nothing that can ever produce it. The result today is
   all-or-nothing: `--allow-mutations` unlocks every non-read operation at once, including the ones
   the operator would never sanction, because there is no vocabulary for "this POST is a read" or
   "never call this one".
3. **Acceptance criterion 6 is unmet.** "Обязательное подтверждение fail-closed при отсутствии
   client capability" (§13.6, FR-44–46) is one of the two criteria still marked *out of scope* in
   docs/release-v0.1.0-alpha.md. It is also Principle II of the constitution, quoted verbatim:
   *missing client capability for a required interactive approval = refusal before network*.

## Scope

In: publication filters, operation overrides, mutation allow/deny rules (FR-41), and interactive
approval over MCP elicitation (FR-44–46), with the report and `explain-call` accounting for every
one of them.

Out: recipes (M5 — an override read from a signed, versioned artefact is a different trust
question from one an operator wrote next to their config), namespaces beyond the single `default`
one that exists, audit sinks, and any external approval provider (FR-44a defers Slack/Web UI
approval explicitly).

## User scenarios

- **US-1 (P1)**: As an operator of a 631-operation API, I publish only the tags my agent works
  with, and the report tells me exactly what I excluded. *Acceptance*: `catalog.includeTags`
  shrinks `tools/list` on the DigitalOcean corpus document; every excluded operation appears in the
  report with a reason code, so the shrinking is auditable rather than mysterious.
- **US-2 (P1)**: As an operator, I state the effect of an operation the heuristic could not
  classify, and lotsman treats my statement as explicit rather than inferred. *Acceptance*: a
  `POST` overridden to `read` executes under read-only policy; a `GET` overridden to `destructive`
  is refused under it; both carry `source=local_override, confidence=explicit` into the report.
- **US-3 (P1)**: As a security engineer, enabling mutations does not mean enabling *all* of them.
  *Acceptance*: with `allowMutations: true` and a deny rule on a tag, an operation carrying that
  tag is refused with zero RoundTrips; with a non-empty allow list, anything unmatched is refused.
- **US-4 (P1)**: As a user, a mutation asks me before it happens, and if my client cannot ask, it
  does not happen. *Acceptance*: with `interactiveApproval: always` and a client that declared no
  elicitation capability, a mutating call is refused before the network (zero RoundTrips); with a
  client that declines the prompt, likewise.
- **US-5 (P2)**: As an operator, `explain-call` shows the selection, the override provenance, the
  rule that matched and whether approval will be requested — before anything runs.

## Requirements carried from the frozen spec

FR-41 (allow/deny by namespace, operationKey, tag, effect), FR-42/43 (annotations are hints and
cannot relax the gate — unchanged, re-asserted against the new rules), FR-44 (approval modes),
FR-44a (approval is UX, never a security boundary), FR-45 (fail-closed without capability),
FR-46 (payload shows identity and a redacted argument summary, never secrets or full bodies),
§4.7 (only a reviewed override may return a suspicious operation to `read`), §5.1
(`catalog.includeTags`, `execution.interactiveApproval`, `operationOverrides`), §5.2 (a match on a
colliding `operationId` is an error; method+path is unambiguous within a namespace).

## Non-negotiables

- An excluded, disabled or denied operation is **accounted for**, never silently absent: the report
  is the product (FR-12a), and a filter that hides its own effect is worse than no filter.
- An override may make policy **stricter** without ceremony and **looser** only by being explicit;
  it can never make an operation *supported* that lotsman refused to translate. Selection is not a
  translation verdict.
- Approval is asked **after** the gate has already allowed the call, never instead of it (FR-44a).
  A declined prompt refuses; an accepted prompt authorizes nothing that policy had not already
  authorized.
- Everything stays deterministic: same spec + same config ⇒ same catalog, same digest, same rule
  verdicts (Principle IV).

## Open questions

1. Does a denied-by-rule operation stay published as a refusing tool, or disappear from the
   catalog? Publishing it teaches a model what exists and what it may not do; hiding it stops the
   model from trying. 002 already answered the same question in the opposite direction for
   `call_mutating_operation` (absent when there is nothing to mutate), and the two answers need a
   reason to differ.
2. Should approval have its own timeout, or inherit the call's? A client that never answers holds
   a session open either way; only one of the two makes that visible.
3. Approval is the first thing in the runtime with an obvious audit event and no audit sink
   (`internal/audit` is a doc.go). Does 003 write the event, or is that M3's metrics/audit slice?
