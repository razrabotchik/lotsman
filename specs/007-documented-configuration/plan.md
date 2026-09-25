# Implementation Plan: the documented configuration

**Branch**: `007-documented-configuration` | **Spec**: ./spec.md | **Frozen spec**: docs/spec.md §5

## Summary

Eight fields, and they are not eight similar pieces of work. Three are already `Options` fields
waiting to be filled in. Two are package constants that have to become parameters, which is where
the real cost is. Two have exactly one legal value and need a validator rather than a setting. One —
`spec.source` — changes where the document comes from and is the only part that touches the CLI's
own shape.

So the order is by kind, cheapest first, and the last step is the one that makes the specification's
example parse. That is deliberate: until every field is handled the example still fails, and a
checkpoint that half-parses it would be a checkpoint nobody can describe.

## Technical Context

- **`catalog.Options` already has `Mode` and `MaxSerializedBytes`.** They are set from flags today
  and defaulted when zero. Reading them from the file is `applyCatalogFile` plus validation.
- **`descriptionByteBudget` and `response.MaxBodyBytes` are constants**, used deep inside
  `sanitizeText`-style helpers and `FromHTTP`. These are the two that ripple.
- **`egress.Budget` is already a struct with a `Total` and derived phases**, and `DefaultBudget()`
  already cites `execution.timeout: 30s`. Wiring is a constructor argument.
- **The contradiction check is new ground.** Nothing in `config` currently refuses a *combination*;
  every validator so far looks at one field. `defaultPolicy` versus `allowMutations` needs a
  cross-field rule, and that is worth doing once and naming clearly.
- **Testing**: the test that pins the gap becomes the test that pins the fix — it reads §5.1 out of
  docs/spec.md, so it cannot drift from the document. Each budget gets a test at a *configured*
  value different from the default, because a test at the default proves only that the default
  still works.

## Architecture

```text
internal/config     Spec{Source, Root, RemoteRefs, Strict}, Catalog{Mode, MaxSerializedBytes,
                    DescriptionBytesPerTool}, Execution{Timeout, MaxResponseBytes, Redirects,
                    DefaultPolicy}; cross-field validation; Runtime carries the resolved values
internal/catalog    Options.DescriptionBytesPerTool (constant becomes a field, default unchanged)
internal/response   FromHTTP takes the limit; MaxBodyBytes stays as the default and the ceiling
internal/egress     Budget built from the configured timeout
cmd/lotsman         file values under the flags (FR-62), and spec.source as a fallback
```

## Proposed decisions on the spec's open questions

1. **Accept and verify, and say so in the message.** A field with one legal value still earns its
   place: `redirects: follow` becomes "lotsman refuses redirects (FR-33); only `deny` is possible"
   instead of an unknown-field error that tells an operator nothing about why. The alternative —
   amending the frozen specification to drop the field — is a bigger change for a smaller gain, and
   would need an ADR to do properly.
2. **`FromHTTP` takes the limit.** The wider blast radius is the point: a caller that forgets to
   pass a bound will not compile, whereas a field on the runner can be left unset and default to
   something generous without anybody noticing. `MaxBodyBytes` stays as both the default and the
   ceiling a configured value may not exceed.
3. **`spec.source` is its own step, last but one.** It is the only field that changes where the
   document comes from, and it interacts with three existing ways of naming one. Keeping it separate
   means the rest of the feature is shippable without it.
4. **`spec.root` is refused when the source is stdin.** A piped document has no directory, and
   handing it one would let a document arriving over a pipe read the filesystem relative to a path
   the operator wrote for a different document. The refusal names both facts.
5. **Every one of these fields changes the digest, including the description budget.** Prose is part
   of what a model sees and what the catalog is measured at; a digest that ignored it would make
   FR-74's cache hint wrong for a client that had cached the longer descriptions. Substantive means
   "a client would notice", and a client would.

## Checkpoints

1. **The catalog block.** `mode`, `maxSerializedBytes`, `descriptionBytesPerTool` read from the
   file, under the flags, with the description budget becoming an `Options` field. The report shows
   the budget in force, which it already does for `maxSerializedBytes`.
2. **The execution budgets.** `timeout` and `maxResponseBytes`, both able to tighten and refused
   when they would loosen past what the code guarantees.
3. **The verified enums.** `redirects`, `remoteRefs`, `defaultPolicy`, and the cross-field
   contradiction rule. Nothing new is configurable; several things become explicable.
4. **`spec.source` and `spec.root`.** A configuration file can name the document, the command line
   still wins, and a stdin source refuses a root.
5. **The example parses.** `TestTheSpecificationsOwnExampleDoesNotParseYet` is replaced by its
   opposite, the README gains the fields that are now real, and an ADR records the
   accept-and-verify decision.

## Constitution Check

- **I (exactness)**: no setting makes an unsupported operation supported. The budgets change how
  much of a supported operation is *published*, which the report already accounts for. ✔
- **II (fail closed)**: every new value is validated, a budget may only tighten, and a value lotsman
  cannot deliver is refused rather than approximated. ✔
- **IV (determinism)**: the settings change the catalog and therefore the digest, deliberately and
  visibly, and a golden test pins each. ✔
- **VII (dependencies)**: none. ✔
- **IX (no premature abstraction)**: no settings framework. Each field is read where it is used and
  validated where it is parsed. ✔
- **X (main stays green)**: five checkpoints; the example parses only at the last, and the test that
  says it does not is honest until then. ✔

## Risks

1. **Turning a constant into a parameter is where defaults get lost.** A caller that passes zero
   would get "no limit" if the code reads the value naively. Mitigated by treating zero as "use the
   default" in exactly one place per setting, and by a test that a zero value behaves like the
   documented number rather than like infinity.
2. **`maxResponseBytes` touches the call path, which is the most tested part of the runtime and the
   most expensive to get wrong.** Mitigated by the mutation check: removing the bound must still
   fail `TestNoMoreThanTheLimitIsEverReadIntoMemory`, which now has to keep working through a
   parameter rather than a constant.
3. **A cross-field rule is a new kind of validation and invites more.** Mitigated by keeping it to
   the one contradiction the specification actually creates, and saying in the code that a second
   one would be a reason to reconsider the field rather than to add another rule.
4. **`spec.source` makes a configuration file able to change what is served**, which is a small
   trust shift: a file that used to describe how to serve now says what. Mitigated by the command
   line winning, by the path resolving relative to the configuration file rather than the working
   directory, and by it being a separate checkpoint that can be dropped.
