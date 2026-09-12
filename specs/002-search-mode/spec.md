# Feature 002: Search mode

**Status**: opened at the close of feature 001. Not started.

## Why this exists

Feature 001 shipped with a measurement that decides this feature's priority rather than an
intuition about it:

- Kubernetes `apps/v1` — 65 published tools — serializes to **2.23 MB** of `tools/list`.
- DigitalOcean — 631 tools — serializes to **743 KB**.
- The stdio transport delivers 4.26 MB in 146 ms, so the transport is not the constraint. The
  model's context window is.

One tool per operation is not a mode that scales down gracefully; past a certain catalog it does
not work at all. `--mode=search` is therefore not an optimization of the tools mode but the only
workable mode for specifications of that shape (FR-47–52).

## Scope

In: a local lexical index over the catalog, the five meta-tools, deterministic ranking, and the
re-validation that keeps a search result from becoming an authorization.

Out: semantic or multilingual retrieval (FR-53 permits claims only after a benchmark), remote
index services, and anything that would make the catalog non-deterministic.

## User scenarios

- **US-1 (P1)**: As a developer serving a large API, lotsman publishes five meta-tools instead of
  hundreds, and my agent still finds the operation it needs. *Acceptance*: on the Kubernetes and
  DigitalOcean corpus documents, `tools/list` is a small constant size, and a benchmark of real
  queries meets an agreed Recall@k.
- **US-2 (P1)**: As a security engineer, a search result cannot be used to call something the
  policy forbids. *Acceptance*: `call_read_operation` refuses an operation whose effect is not
  `read`, and every call re-validates identity, effect, arguments, auth and policy (FR-49, FR-50).
- **US-3 (P2)**: As a developer, `--mode=auto` picks the mode from the measured catalog size
  rather than from an operation count (FR-52). *Acceptance*: the threshold is expressed in
  serialized bytes and reported by `inspect`.

## Requirements carried from the frozen spec

FR-47 (tools mode), FR-48 (the five meta-tools), FR-49 (re-validation), FR-50 (a read tool never
calls another effect class), FR-51 (conservative annotations on the mutating call tool), FR-52
(auto mode by catalog weight), FR-53–54 (lexical index and its benchmark).

## Open questions

1. What ranking does an agent's phrasing actually need — the search benchmark corpus (open
   question 6 in docs/spec.md) has to exist before the ranking is tuned.
2. How does `describe_operation` present a 60 KB input schema without reintroducing the problem
   search mode exists to solve?
3. Does the catalog digest cover the index, or is the index derived and therefore out of the
   identity?
