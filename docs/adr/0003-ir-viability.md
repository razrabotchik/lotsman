# ADR-0003: IR viability — the M0 gate

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M0 gate (T022)

## Context

The M0 gate asks one question, at the last moment it is still cheap to answer:

> Did the intermediate representation hold without fundamental rework — proceed, or redesign the
> IR now?

The evidence is the tracer bullet itself (specsource → openapi → domain → catalog → mcpserver →
requestbuild → response), the golden fixtures, and the vendor corpus measured in docs/corpus.md.

## Decision

**Proceed. The IR held.** No redesign.

What the corpus and the M0 steps actually exercised:

- **Identity survived contact.** `operationKey` = `{namespace}:{METHOD}:{path}` never needed to
  change: Kubernetes and Stripe both contain duplicate and missing `operationId`s, and the key was
  unaffected because it never depended on them.
- **Diagnostics carried their provenance.** Every refusal in the corpus report has a machine-
  readable code, a message and a JSON Pointer, which is what made `inspect` useful on day one
  rather than a count of failures.
- **The three-axis model was the right shape, and grew a fourth cleanly.** Translation support,
  publication and executability (ADR-0005) absorbed policy as a fourth axis in T019 without
  touching the first three. The report tells the four apart — rejected, capability-blocked,
  policy-blocked, executable — and each points at a different remedy.
- **Effect as a decision, not a flag.** Carrying `{effect, source, confidence, warnings}` rather
  than a boolean is what lets the same field hold a method heuristic today and a reviewed override
  later, without a migration.
- **Determinism held at scale.** Two runs over 5.2 MB of Stripe produce byte-identical reports.

Fields were *added* during M0 — `Input`, `Effect`, `Servers`, `ExecutionBlockers` — and no field
had to be reinterpreted or removed. That is the distinction the gate was set up to detect: an IR
that grows is healthy, an IR that has to be reread is not.

## What the corpus says is missing (features, not design)

Every refusal in the corpus traces to an unwritten feature with an existing task, not to a shape
the IR cannot express:

| Finding | Task |
|---|---|
| Exploded specs (DigitalOcean: 662 documents) | T025 — file `$ref` with root confinement; also raise `MaxRefDocuments` from the measured 662 |
| `application/x-www-form-urlencoded` bodies (all of Stripe) | 4.5 matrix follow-up |
| `deepObject` query parameters (367 Stripe operations) | 4.5 matrix follow-up |
| Competing patch media types (12 Kubernetes PATCHes) | media-type override in config (FR-29) |
| Swagger 2.0 documents (GitLab) | compatibility adapter, explicitly post-core |
| `readOnly` properties published as inputs | T024 |

## Consequences

- **Search mode is not an optimization.** 65 Kubernetes tools serialize to 4.2 MB of `tools/list`.
  Feature 002 is a prerequisite for that class of spec, and `catalog.serializedBytesEstimate` in
  every report is how a user finds out before their context window does.
- Two corrections were made at the gate rather than deferred, because both were refusals lotsman
  could not defend: a sole `*/*` media type now sends JSON, and the version is decided before
  parsing. Both are recorded in docs/corpus.md with the numbers that prompted them.
- The corpus is now a committed manifest plus a smoke test, so the next change to translation
  support has to answer for the diff.
