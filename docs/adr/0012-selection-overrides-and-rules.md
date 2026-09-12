# ADR-0012: Selection, overrides and mutation rules

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: feature 003 (M2 tail), tasks T201–T208

## Context

Until now the operator had one lever between "everything" and "nothing": `--allow-mutations`.
The frozen specification names three more (FR-41 allow/deny rules, `catalog.includeTags`,
`operationOverrides`) and fixes their *fields* but not their YAML, and `EffectSourceLocalOverride`
had been in the domain model since 001 with nothing able to produce it. Three consequences were
visible on the corpus: a 631-operation catalog could not be narrowed to the tags in use, a POST
the operator knew to be a read stayed `unknown` forever, and enabling mutations enabled all of
them at once.

## Decision

1. **Selection removes; rules refuse.** `catalog.includeTags` and `enabled: false` say *this is
   not part of the surface*: the operation leaves `tools/list` and the search index, and appears
   in the report as `excluded` with `excluded_by_selection` or `disabled_by_override`. A deny rule
   says *you may not call this*: the tool stays published and refuses by name, like every
   policy-blocked tool since ADR-0008 §4. These are two different statements and they get two
   different outcomes.
2. **An excluded operation is still in the report.** A filter that hid its own effect would be
   worse than no filter; `Totals.Excluded` is its own number because it is the only one a reader
   fixes by editing their own configuration rather than waiting for a release.
3. **An override may classify, hide or bind — never translate.** `effect` sets
   `source=local_override, confidence=explicit`, which is the only path in the codebase that
   produces an explicit effect (docs/spec.md 4.7) and the only way a suspicious `GET` returns to
   `read`. It cannot publish an operation lotsman refused to translate: support is not a matter of
   opinion. The scanner's warnings survive an override, because "this GET is named rebuild" stays
   worth reading after someone decided it is a read anyway.
4. **An override that matches nothing is an error, not a smaller catalog.** `ValidateOverrides`
   runs before anything is served and exits with a usage error. So does a match on a colliding
   `operationId` (docs/spec.md 5.2) and two overrides claiming one operation: applying either
   would be a guess about which line the operator reviewed.
5. **Deny wins, and applies to reads too.** FR-41 gates *mutations* with the allow list, so a read
   needs no rule to license it — but a deny rule is the operator saying "not this one", and
   refusing a read on request can never be the unsafe answer. Requiring `allowMutations` before an
   endpoint could be hidden would mean making things worse to make them better.
6. **Two lists, not one ordered list.** `allowRules` and `denyRules` are evaluated as deny-then-
   allow rather than first-match-wins, because first-match-wins makes the safety of a
   configuration depend on the order somebody pasted it in.
7. **Rules match the four axes FR-41 names and no more**: namespace, operationKey, tag, effect.
   Fields within a rule are ANDed, rules are ORed, and an empty rule is refused at load time
   rather than interpreted as "everything". Namespace is matched although only `default` exists:
   a policy written for one API should keep meaning the same thing when a second arrives.
8. **Selection matches the sanitized tag vocabulary** — the one the report and `list_tags` show.
   A filter that had to be written against the raw document would be a filter nobody could read
   off lotsman's own output.
9. **`authProfile` pins credential selection per operation**, and in doing so fixed a real gap:
   `auth.bind` took the first compatible profile when two satisfied one scheme, which is the
   arbitrary choice FR-57 forbids — deterministic (sorted) and still a credential on the wire
   because it sorted early. Two profiles for one scheme now refuse with `ambiguous_security`, and
   the refusal names the profiles and says how to choose one. This is a behaviour change for a
   configuration that previously "worked": it worked by luck.

## Consequences

- `policy.Evaluate` takes a `Subject` (key, tags, effect) rather than an effect: rules match on
  identity, which an effect decision does not carry. There is no default Subject, so a caller that
  forgets a field fails to compile rather than evaluating against an empty one.
- The catalog digest changes when selection or an override changes, which is correct under
  data-model.md invariant 1 (spec + config) — two different published surfaces are not equal.
- `lotsman operations` and `inspect` report the operations as the configuration leaves them, not
  as the document wrote them. `catalog.Overlay` is shared by both so they cannot disagree.
- Config decoding stays strict, so a typo in an override is a startup error rather than a control
  that silently did not apply.
