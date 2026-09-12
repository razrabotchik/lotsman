# Implementation Plan: Selection, overrides and interactive approval (M2 tail)

**Branch**: `003-filters-overrides-approval` | **Spec**: ./spec.md | **Config**: docs/spec.md §5

## Summary

Three capabilities that all answer the same question — *who decides what an agent may do* — and
today all have the same answer: the document, plus one boolean. 003 gives the operator a
vocabulary between "everything" and "nothing", and gives the human the prompt the constitution
already promised them.

The order is deliberate. Selection and overrides change what the **catalog** contains and what it
claims; rules change what the **gate** permits; approval changes what happens **between the gate
and the network**. Each layer is testable before the next exists, and each leaves main green.

## Technical Context

- **No new dependencies.** Elicitation is in go-sdk v1.7.0, already a direct dependency — as a
  multi-round-trip input request rather than `ServerSession.Elicit`, see the decisions below.
  Matching is string comparison over a handful of fields.
- **One boundary, still.** Rules are evaluated inside `policy`, which the catalog already consults
  and which knows nothing about MCP. Approval is asked in `mcpserver`, which knows nothing about
  how the verdict was reached. Neither can be reached around: a call that skips the gate is not a
  call path this codebase has.
- **Testing**: golden catalogs for selection and overrides (a filter that changes the digest must
  change it *visibly*), zero-RoundTrip assertions for every new refusal, and an e2e over stdio with
  a client that declares no elicitation capability — the exact shape of criterion 6.

## Architecture

```text
internal/config      operationOverrides, catalog.includeTags, execution rules + approval mode
internal/catalog     Selection and Overrides applied before tools are built; report accounts
internal/policy      Config gains Rules and a Subject; Evaluate takes the operation, not an effect
internal/mcpserver   approval.go: capability check, redacted prompt, accept/decline/cancel
```

`policy.Evaluate` changes shape — from `Evaluate(EffectDecision)` to `Evaluate(Subject)` — because
FR-41 matches on operationKey and tag, which an effect decision does not carry. That is the whole
reason the signature was provisional; the doc comment on `policy.Config` has said so since 001.

## Decisions on the spec's open questions

1. **Selection removes; rules refuse.** `includeTags` and `enabled: false` say *this is not part of
   the surface* — the operation is absent from `tools/list` and from the search index, and present
   in the report with its reason. A deny rule says *you may not call this* — the tool stays
   published and refuses by name, like every other policy-blocked tool since 001. The distinction
   is not a compromise between the two options; it is the difference between the two statements.
   It also keeps 002's answer intact: `call_mutating_operation` is absent when *nothing* is
   mutable, because a tool whose every call is a refusal teaches a model only to keep trying.
2. **Approval has no timeout, because it has no wait.** This started as "inherit the call's
   context and add a bounded default", and the SDK settled it during T210: on protocol
   `2026-07-28` a server may not open an elicitation while serving a request, and must return a
   multi-round-trip input request instead (SEP-2322) — which is what FR-44 specified. The handler
   returns input-required and the client calls again with the answer, so nothing is held open and
   a client that never comes back has simply not made the call (ADR-0013).
3. **Approval logs, it does not audit.** The decision goes to the structured stderr logger with the
   operation key, the effect and the outcome — never the arguments. `internal/audit` stays a
   `doc.go` until M3 gives it a sink; inventing an event format here would be an abstraction with
   one consumer and no reader (Principle IX).

## Config surface (docs/spec.md §5.1, extended by ADR-0012)

```yaml
catalog:
  includeTags: [droplets, images]        # documented in §5.1; nothing read it until now
execution:
  allowMutations: true
  interactiveApproval: always            # always | client-capability | never (FR-44)
  denyRules:                             # FR-41; deny always wins
    - { tag: billing }
    - { operationKey: "default:DELETE:/v2/droplets/{id}" }
  allowRules:                            # when non-empty, an unmatched mutation is refused
    - { effect: write }
operationOverrides:
  - match: { method: GET, path: /legacy/rebuild-cache }
    enabled: false
  - match: { operationId: createDraft }
    effect: write
    authProfile: example
```

Everything above is either verbatim from §5.1 or the smallest shape that satisfies an FR whose
wording fixes the *fields* (namespace, operationKey, tag, effect) but not the YAML. The frozen spec
changes only through an ADR, so the rule shape gets one.

## Constitution Check

- I: an override cannot make an untranslatable operation callable; it can only classify, disable or
  bind credentials to one lotsman already understands. ✔
- II: rules and approval both fail closed — an unmatched allow list refuses, a missing capability
  refuses, a timeout refuses, a decline refuses. ✔
- III: approval is asked only after the gate allowed the call, and its acceptance grants nothing.
  FR-44a is quoted in the code, not just in the spec. ✔
- IV: selection, overrides and rules are all order-independent and digest-visible; golden tests. ✔
- VII: no new dependency. ✔
- IX: no new package for approval — the mechanics live where the protocol lives. ✔
- X: four checkpoints, each a runnable server.

## Risks

1. **An override is a loaded gun**: `effect: read` on a POST makes it callable under the default
   policy. Mitigated by provenance everywhere (`source=local_override, confidence=explicit` in the
   report and in `explain-call`), and by the override being unable to touch translation support.
2. **Approval that nobody sees is worse than no approval** — FR-44a exists because a client may
   auto-accept. Mitigated by never counting it as a boundary: the mutation gate and the rules stand
   whether or not the prompt was shown, and the instructions text says so.
3. **`policy.Evaluate`'s new signature touches every caller.** Mitigated by the compiler: there is
   no default `Subject`, so a caller that forgets a field fails to build rather than evaluating
   against an empty one.
