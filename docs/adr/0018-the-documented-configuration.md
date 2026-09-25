# ADR-0018: Accepting a setting lotsman cannot change

- **Status**: Accepted
- **Date**: 2026-09-25
- **Decision owner**: feature 007, tasks T601–T613; docs/spec.md §5.1, §5.2

## Context

The example configuration in the frozen specification did not parse. Strict decoding refused eight
fields, and copying that example is the first thing an operator does.

The awkward part was not the refusal. It was that lotsman implemented most of what it refused, at
exactly the documented values — 120 000 bytes of catalog budget, 1 200 bytes of description per
tool, a 524 288-byte response cap, a 30-second timeout — with every one of those constants carrying
a comment in the source saying it came from this example. The values had been taken from the
specification; the ability to set them had not.

Three of the eight were also **security budgets**. An operator who cannot lower a response cap has
to accept lotsman's number or nothing, and a budget nobody can tighten is a default pretending to
be a policy.

## Decision

1. **Every documented field is read, or accepted and verified.** "Verified" means the one value
   lotsman can deliver is accepted and every other is refused *by name*, with the message saying
   what lotsman does instead. `redirects: follow` now produces "a redirect is never followed …
   (FR-33); only `deny` is" rather than an unknown-field error that explains nothing.

2. **A field with one legal value still earns its place.** It looks like ceremony and it is not: an
   operator who writes `remoteRefs: true` believes remote references will be fetched. Learning
   otherwise from a refusal that names FR-5 is different from learning it from a decoder complaint,
   and very different from not learning it at all. The alternative — amending the frozen
   specification to drop the field — is a larger change for a smaller gain.

3. **Accepting a field without honouring it is forbidden.** That is the silent-ignore strict
   decoding exists to prevent, and doing it *inside* a field would be worse than the original
   error. It changed the plan once: `spec.strict` was scheduled two steps later than the step that
   made the field parse, so it moved forward rather than shipping as a setting nothing read.

4. **A budget may be tightened, not loosened.** `maxResponseBytes` above the compiled ceiling is
   refused: the cap bounds memory this process has to find, and an operator raising it is asking
   for a promise the runtime does not make.

5. **The per-phase timeouts are derived from the total.** `DefaultBudget`'s doc comment had always
   claimed they were, while they were four independent numbers that happened to suit 30 seconds.
   Once the total became configurable, an operator asking for 5s would have got a 20s
   response-header limit. `BudgetFor` derives them in the proportions the documented default used.

6. **Two spellings of one setting must not disagree.** `defaultPolicy: read-only` with
   `allowMutations: true` is refused as a contradiction. lotsman does not pick one and does not
   invent a precedence an operator would have to learn. This is the only cross-field rule in the
   package; a second would be a reason to reconsider the field rather than to add another rule.

7. **`spec.source` resolves against the configuration file, and the command line still wins.** A
   path resolved against the working directory would make a deployment's file work from one place
   and fail from every other, including wherever a service manager starts the process. And a file
   that could override an argument would make a one-off `inspect OTHER.yaml` unreliable (FR-62).

8. **`spec.root` widens `$ref` confinement, so only the operator may state it.** It is the same
   shape as `--base-url` for egress: the boundary is the operator's to declare and never the
   document's. Inside the stated root a reference is still confined; outside it is still refused.
   With a document read from stdin the field is refused outright — a piped document has no
   directory, and a root written for another document is not its own.

## Consequences

- `TestTheSpecificationsOwnExampleParses` reads §5.1 out of docs/spec.md and asserts both that it
  parses and that it resolves to the values it names. It cannot drift from the specification,
  because it does not contain a copy of it. A field added to §5.1 fails here until it is real.
- Its predecessor was the same test inverted: it asserted *which* fields were refused, and it
  failed at every step of this feature as the list shrank. That is what made the work visible.
- Turning `descriptionByteBudget` and `response.MaxBodyBytes` from constants into parameters is
  where defaults get lost. Zero means the documented default in exactly one place per setting, and
  a test asserts that a zero value behaves like the number rather than like infinity.
- `response.FromHTTP` takes the limit as an argument rather than reading a field, so a call path
  that forgets the bound does not compile.
- Writing the digest test for the description budget turned up something larger: the catalog digest
  did not include the **mode**, and `tools/list` differs entirely between the two. See the entry in
  docs/release-v0.1.0-alpha.md.
