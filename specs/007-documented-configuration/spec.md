# Feature 007: the configuration the specification documents

**Status**: opened after `TestTheSpecificationsOwnExampleDoesNotParseYet` was written. It is not a
roadmap milestone; it is a defect with eight names.

## Why this exists

The example configuration in docs/spec.md §5.1 does not parse. Strict decoding refuses eight
fields, and copying that example is the first thing an operator does.

The awkward part is not the refusal — strict decoding is deliberate and right. It is that lotsman
**implements** most of what it refuses, at exactly the documented values:

| The example says | lotsman does | Where |
|---|---|---|
| `catalog.mode: auto` | the same, from `--mode` | `cmd/lotsman` |
| `catalog.maxSerializedBytes: 120000` | `DefaultMaxSerializedBytes = 120_000` | `internal/catalog` |
| `catalog.descriptionBytesPerTool: 1200` | `descriptionByteBudget = 1200` | `internal/catalog` |
| `execution.timeout: 30s` | `DefaultBudget()` | `internal/egress` |
| `execution.maxResponseBytes: 524288` | `MaxBodyBytes = 512 * 1024` | `internal/response` |
| `execution.redirects: deny` | an unconditional refusal | `internal/egress` |
| `execution.defaultPolicy: read-only` | `allowMutations: false` | `internal/policy` |
| `spec.source`, `root`, `remoteRefs`, `strict` | the command line, and refusals | `cmd/lotsman` |

Every one of those constants carries a comment in the source saying it came from this example. The
values were taken from the specification; the ability to set them was not. An operator reading the
specification is being shown a configuration language that does not exist.

There is a second, quieter reason. Three of these settings are **security budgets** — the response
cap, the timeout, the catalog size — and an operator who cannot lower one has to accept lotsman's
number or nothing. A budget nobody can tighten is a default pretending to be a policy.

## Scope

In: the eight fields above, each either read and honoured, or accepted and verified — where
"verified" means the one value lotsman can actually deliver is accepted and anything else is
refused by name rather than ignored.

Out: any field the frozen specification does not already document. This feature closes a gap
between the document and the code; it is not an invitation to invent configuration. Hot-reloading
these settings (FR-72 republishes a catalog, not a policy). Per-operation budgets.

## User scenarios

- **US-1 (P1)**: As an operator, I copy the example from the specification and lotsman starts.
  *Acceptance*: the YAML in §5.1, read out of the document itself, parses and produces a runtime
  whose settings are the ones it names.
- **US-2 (P1)**: As a security engineer, I lower the response cap and the timeout below lotsman's
  defaults, and the lower numbers are what hold. *Acceptance*: a response over the configured cap
  is truncated at *that* number; a call that outruns the configured timeout is refused with the
  configured duration in the message.
- **US-3 (P1)**: As an operator, a setting lotsman cannot honour is refused at load, not ignored.
  *Acceptance*: `redirects: follow` and `remoteRefs: true` and `defaultPolicy: allow-all` each fail
  with a message naming the field and what lotsman does instead.
- **US-4 (P2)**: As an operator, my configuration file can name the document to serve, so that one
  file describes a deployment. *Acceptance*: `spec.source` is used when the command line gives no
  document, and the command line wins when it does (FR-62).
- **US-5 (P2)**: As an operator, two ways of saying the same thing cannot disagree silently.
  *Acceptance*: `defaultPolicy: read-only` with `allowMutations: true` is refused as a
  contradiction rather than resolved by precedence nobody can see.

## Requirements carried from the frozen spec

§5.1 (the example, which is the specification of the field names), §5.2 (`apiVersion` is required;
an unknown major schema is refused; env substitution is not templating), FR-62 (defaults < file <
environment < flags — the reason the command line still wins), FR-3 (sources are file and stdin;
`spec.source` names one of those and nothing remote), FR-5 (remote `$ref` is off, and
`remoteRefs: true` is therefore not a setting lotsman has), FR-32 (the timeout covers connect, TLS,
header and body phases), FR-33 (redirects are refused), FR-36 (the response cap is a hard byte
limit and truncation must not return broken JSON as valid JSON), FR-19 (the per-tool description
ceiling), FR-47/48 (catalog budget and mode).

## Non-negotiables

- **A configured budget may tighten, never loosen past what the code can guarantee.** A
  `maxResponseBytes` above lotsman's ceiling is refused rather than honoured: the cap exists to
  bound memory, and an operator raising it is asking for a promise this runtime does not make.
- **Accepting a field is not the same as implementing it.** Where only one value is deliverable,
  that value is accepted and every other is refused *by name*, with the message saying what lotsman
  does instead. Silently ignoring a field is the failure mode strict decoding exists to prevent,
  and re-introducing it inside a field would be worse than the current error.
- **Two spellings of one setting must not disagree.** `defaultPolicy` and `allowMutations` overlap;
  a contradiction is a refusal, never a precedence rule an operator has to learn.
- **The command line still wins** (FR-62). A configuration file that could override a flag would
  make a one-off `--read-only` unreliable, which is the opposite of why it exists.
- **Determinism is unaffected.** These settings change what is published and how much, so they
  change the catalog digest — which is correct and must be visible in the report, not silent.

## Open questions

1. **Is "accept and verify" honest, or theatre?** `redirects: deny` and `remoteRefs: false` have
   exactly one legal value. Accepting them lets the documented example work and turns
   `redirects: follow` from a mystery into a refusal. The alternative reading is that a field with
   one value is a field pretending to be a choice, and the example should lose it instead — but the
   specification is frozen and changing it needs an ADR.
2. **Where does `maxResponseBytes` live?** `response.MaxBodyBytes` is a package constant read by
   `FromHTTP`. Threading a limit through means either a parameter on `FromHTTP` (touching every
   caller and every test) or a field on the runner that owns the call. The second keeps the blast
   radius small; the first makes the bound impossible to forget.
3. **Does `spec.source` belong to this feature at all?** It changes where the document comes from,
   which is closer to FR-3 than to §5.1's other fields, and it interacts with `--spec`, with
   `serve SPEC`, and with the relative path the file is resolved against. It may deserve its own
   step or its own feature.
4. **What does `spec.root` mean when `source` is stdin?** Today a document from stdin has no
   directory and every file `$ref` is refused for that reason. A configured `root` would give one —
   which is either a useful capability or a way to let a piped document read the filesystem.
5. **Should the catalog budget fields change the digest?** They change what is published, so the
   digest should move; but `descriptionBytesPerTool` changes only prose, and FR-74 promises a digest
   moves on a *substantive* change. Prose is part of what a model sees, so this is probably
   substantive — but it should be decided rather than fall out of the implementation.
