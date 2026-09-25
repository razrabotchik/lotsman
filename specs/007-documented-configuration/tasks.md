# Tasks: the documented configuration

**Input**: plan.md, spec.md, docs/spec.md §5.1, §5.2
**Convention**: [P] = parallelizable. Every task leaves main green. Checkpoints are points at
which the work could stop and still be worth shipping.
**Note**: `TestTheSpecificationsOwnExampleDoesNotParseYet` stays true and passing until step 5.
It is the measure of this feature, and it is honest for it to keep failing the example until the
last field is handled.

## Step 1: The catalog block  ✅ CHECKPOINT: the budgets an operator is shown are the budgets they can set

- [x] T601 config: `catalog.mode`, `catalog.maxSerializedBytes`, `catalog.descriptionBytesPerTool`
      with strict decoding and validation; resolved under the flags (FR-62)
      → A budget of zero or less is refused rather than read as "no budget". The documented values
        become the defaults they already are, in one place, so that `Resolve` and the catalog do
        not each have an opinion.
- [x] T602 catalog: `descriptionBytesPerTool` becomes an `Options` field
      → It is a constant used inside the sanitizers today. Zero means the default in exactly one
        place; a test asserts a zero value behaves like 1 200 rather than like no limit, because
        that is the way this kind of change goes wrong.
- [x] T603 [P] the report shows the description budget in force, as it already shows the catalog
      budget, and a golden test pins that changing either moves the digest
      → It moves the digest on purpose: prose is part of what a model sees, so a client that
        cached the longer descriptions would notice (FR-74).
      → Writing that test found something bigger. The digest did not include the **mode**, and
        `tools/list` differs entirely between the two — one tool per operation, or five meta-tools
        over the same catalog. So a client could cache a tool list against a digest, the operator
        could flip the mode, and the digest would say nothing had changed. `reload` had the same
        blind spot from the other side: it publishes only when the digest moves, so a mode change
        alone decided there was nothing to publish. The mode is now part of what the digest
        identifies, which is "what a client will be shown" rather than "which tools exist".
      → The catalog budget on its own does *not* move the digest, and that is correct: it changes
        no tool. It moves it when it flips the mode. Both halves are asserted, because the
        interesting one is the half that must not move.

## Step 2: The execution budgets  ✅ CHECKPOINT: a security budget can be tightened

- [x] T604 config + egress: `execution.timeout` builds the budget, instead of `DefaultBudget()`
      being the only possibility
      → `DefaultBudget()` already cites `execution.timeout: 30s` in its doc comment. This is that
        comment becoming true — and the comment said more than the code did: it claimed the
        per-phase limits were *derived* from the total, while they were four independent numbers.
        Fixed phases would have given an operator asking for 5s a 20s response-header limit, so
        `BudgetFor` now derives them in the same proportions the documented default used.
- [x] T605 response: `FromHTTP` takes the byte limit; `MaxBodyBytes` stays as the default *and* as
      the ceiling a configured value may not exceed
      → A parameter rather than a field on the caller, so that a caller which forgets it does not
        compile. The cap bounds memory; an operator raising it is asking for a promise this
        runtime does not make, so raising it is refused.
- [x] T606 [P] each budget tested at a *configured* value different from the default
      → A test at the default proves only that the default still works. And the mutation check has
        to keep catching a removed bound now that the bound arrives as an argument: it reported
        `PATTERN GONE` on the very line this task changed, which is the behaviour that rule exists
        for.

## Step 3: The verified enums  ✅ CHECKPOINT: a setting lotsman cannot honour says so

- [x] T607 config: `execution.redirects` and `spec.remoteRefs` accept the one value lotsman
      delivers and refuse every other *by name*, saying what lotsman does instead
      → Accepting a field is not implementing it. The gain is that `redirects: follow` stops being
        an unknown-field error that explains nothing and becomes a refusal that explains the rule
        (FR-33, FR-5).
      → T611 came forward into this step, because leaving it for later would have meant accepting
        `spec.strict` without honouring it — the silent-ignore this whole feature exists to avoid.
        `strict: false` is now the file spelling of `--lax`, and the flag wins in both directions.
      → The refused-fields list got *more precise* rather than shorter here: `spec` became `source`
        and `root`, because the section now exists and only those two fields do not.
- [x] T608 config: `execution.defaultPolicy`, and the contradiction rule against `allowMutations`
      → Two spellings of one setting must not disagree silently. `read-only` with
        `allowMutations: true` is refused as a contradiction, not resolved by a precedence an
        operator would have to learn.
      → This is the first cross-field rule in the package. A second one would be a reason to
        reconsider the field rather than to add another rule, and the code says so.

## Step 4: The document itself  ✅ CHECKPOINT: one file describes a deployment

- [x] T609 config + cmd: `spec.source` names the document when the command line gives none; the
      command line wins when it does (FR-62)
      → The path resolves relative to the configuration file, not the working directory: a
        deployment's file should mean the same thing from any directory, including wherever a
        service manager starts the process. Asserted by running the binary from a third directory.
      → Reading the configuration before the document meant reordering `inspect`, `operations` and
        `validate`, which all parsed the document first. Leaving them would have given a configured
        source that worked in `serve` and not in `inspect` — a trap, and `inspect` disagreeing with
        `serve` about which document it is looking at is worse than useless.
- [x] T610 `spec.root`, refused when the source is stdin
      → A piped document has no directory, and giving it one would let a document arriving over a
        pipe read the filesystem relative to a path written for a different document.
      → The root must exist and the source must be inside it. A configuration whose entry document
        is already outside its own confinement boundary cannot mean what it says.
- [x] T611 [P] `spec.strict` is the file spelling of `--lax` inverted, with the same contradiction
      rule as `defaultPolicy`

## Step 5: The example  ✅ CHECKPOINT: the specification is usable

- [x] T612 replace `TestTheSpecificationsOwnExampleDoesNotParseYet` with its opposite: §5.1, read
      out of the document, parses and produces the runtime it describes
      → Read out of the document rather than copied, so the test cannot drift from the
        specification it is about. It asserts both halves: that §5.1 parses, and that it resolves
        to the values it names.
      → Its predecessor was this test inverted, and it failed at every step as the refused-fields
        list shrank — which is what made the work visible rather than a claim at the end.
- [x] T613 [P] docs: an ADR for the accept-and-verify decision, README for the fields that are now
      real, and the release review's known-limits entry retired
