# Implementation Plan: the workbench and `lotsman ui` (feature 008, stages 2–3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement tasks-ui.md task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Branch**: `008-local-admin-ui-stage2-3` | **Spec**: ./spec.md §3–§5, §6 stages 2–3, §7 | **ADR**: docs/adr/0021

**Goal:** `lotsman ui ./openapi.yaml` opens a local browser workbench — API tree with problems
first, a smart form per operation whose every edit shows its consequence before anything is
written, a pending tray, and a review-and-save step that writes `lotsman.yaml` — plus
`lotsman inspect --diff-against OLD.yaml` for CI.

**Architecture:** `internal/workbench` is the engine with no HTTP: it holds the configuration as
a `yaml.Node` tree plus pending entries, renders the draft file, and compares two *snapshots* of
the pipeline. The pipeline itself is handed to it by `internal/cli` as functions
(`workbench.Pipeline{Evaluate, Explain}`), the way `reload.Source` is handed to the reload holder:
the workbench runs exactly the code `serve` runs, and stays testable with a fake.
`internal/ui` is the loopback HTTP server and the embedded frontend (plain ES modules, no
toolchain). `serve` imports neither.

**Tech stack:** Go 1.25+, `go.yaml.in/yaml/v4` Node API, `net/http`, `embed`; frontend: plain
JavaScript ES modules + CSS; IBM Plex woff2 (OFL) embedded. No new Go dependency.

## Global constraints

- No new direct Go dependency (Constitution VII); no Node toolchain — `go build` builds everything.
- libopenapi types stay in `internal/openapi` (Constitution VIII).
- `serve` imports neither `internal/workbench` nor `internal/ui`; its call path does not change
  (spec §4 "Isolation from serve").
- The workbench never makes an upstream request and never resolves a secret (spec §4).
- The UI server binds `127.0.0.1` only; random token → cookie; `Host` must be `127.0.0.1:PORT` or
  `localhost:PORT`; `Origin` checked on every non-GET; no CORS; strict CSP; writes only the
  configuration path fixed at start (spec §4).
- Save = write temp + rename in the same directory, only if the on-disk content hash equals the
  one the draft was based on; otherwise refuse and keep the draft (spec §3).
- YAML edits go through `yaml.Node` so comments and key order outside the edited entry survive
  (spec §3).
- Widening changes (effect lowered to `read` on a non-GET or on an operation whose inferred
  effect was not `read`; enabling an operation the previous configuration disabled; changing
  `authProfile`) each need an explicit acknowledgement — enforced server-side at save (spec §3, §4).
- Frontend + fonts add under 400 KB to the binary (spec §4).
- Same configuration ⇒ same snapshot: no map iteration into output (Constitution IV).
- **Testing (maintainer's decision, 2026-10-16):** while integrating, verify each task with
  `go build ./...`, `go test ./...` green, and the task's smoke command; the full suite is task 10.
- Commit messages in Russian, one line, ending with the `Co-Authored-By` trailer.
- The frontend's visible language is English (product is English-only at launch; memory
  "site-language").

## Technical context

- Stage 1 is on `main`: corrections live in `config.OperationOverride{MediaType, Parameters,
  RequestBody, Note, Effect, Enabled, AuthProfile}` and `config.ComponentOverride`; the adapter
  reports `openapi.Document.CorrectionProblems`; `cli.parseSpec` fails on them.
- The IR does not carry what the form needs for a *rejected* operation: the media types it
  offered, and parameters that were refused (they never reach `Input.Parameters`). Task 1 adds
  `Operation.RequestMediaTypes`, `Operation.DeclaredParameters`, `Operation.BodyComponent`.
- `cli` holds the pipeline (`parseSpec`, `catalogOptions`, `catalog.Build`,
  `checkCorrectedSchemas`). Task 3 splits a non-failing `loadDocument` out of `parseSpec` and adds
  `evaluate`, which turns every problem into data instead of an exit code.
- `config.Load(path)` reads a file; the workbench evaluates bytes that are not on disk yet. Task 3
  adds `config.LoadBytes(data, path)` (same validation and path resolution as `Load`).
- `explain-call`'s renderer (`explain(w, tool, args, runtime)`) is reused for the dry-run tab.

## Files

```text
internal/domain/domain.go               RequestMediaTypes, DeclaredParameter, BodyComponent on Operation
internal/openapi/{openapi,parameters,body,corrections}.go   fill them; root component names
internal/config/config.go               LoadBytes
internal/workbench/snapshot.go          Snapshot, Operation, Problem, Profile, Status classification
internal/workbench/compare.go           Compare: summary, per-op changes, warnings, widening
internal/workbench/textdiff.go          unified line diff (LCS)
internal/workbench/draft.go             Workbench: yaml.Node draft, entries, Render, Save
internal/workbench/pipeline.go          Pipeline{Evaluate, Explain} func types
internal/cli/evaluate.go                cli side of the Pipeline; loadDocument split from parseSpec
internal/cli/inspect.go                 --diff-against
internal/cli/ui.go                      `lotsman ui` command
internal/ui/server.go                   http server, guard (host/token/origin/CSP), API handlers
internal/ui/open.go                     open the browser
internal/ui/web/{index.html,app.js,api.js,tree.js,form.js,tray.js,style.css,fonts/*}
README.md, docs/spec.md §5.2, specs/008-local-admin-ui/spec.md status
```

## Rulings made while planning

- **Stale corrections:** the group offers "remove"; "rebind" is done by opening the target
  operation and recreating the entry there. A dedicated rebind control waits for a request.
- **Broken specification:** the workbench shows the document error and an empty tree; the
  overlays list is editable (add/remove a path) so an overlay can be the fix, as spec §5 says.
- **Edit granularity:** the draft holds at most one pending entry per operation key and one per
  component name — the form always submits the whole desired entry. `DELETE /api/draft/{id}`
  reverts that entry to what is on disk.

- **Shared-schema prompt:** the form asks "this operation only, or everywhere" for request
  bodies that reference a root component. A parameter's type is changed by replacing that
  parameter's schema, which is operation-local by construction.
- **Where an invalid correction is shown:** a refused change is reported in a banner at the top
  of the form ("The change was not taken: …") with the pipeline's message, which names the
  field; per-field placement of *consequences* (warnings) is as the spec says.
- **Browser smoke:** spec §7 names Playwright; the project has no Node toolchain and must not gain
  one, so the committed end-to-end test drives the same flow through the HTTP API, and the browser
  check is done by hand / with the Playwright MCP tools during task 10.

## Review focus

1. A comment inside `lotsman.yaml` next to an *unrelated* override survives an edit and a save.
2. The file is edited by hand while the UI is open: save refuses, the draft survives, reload shows
   the hand edit plus the pending entries re-applied.
3. A request with a foreign `Host`, a foreign `Origin`, or no token never reaches a handler — and
   the token is never accepted from a cross-site navigation that sets the cookie for an attacker.
4. A widening change saved without its acknowledgement is refused by the server even if the
   frontend is bypassed.
5. A specification that does not parse at all: the UI still opens and shows the error.

Each has a test in task 10.

## Models (subagent-driven execution)

| Tasks | Implementer | Reviewer |
|---|---|---|
| 1, 4, 6, 8 (adapter, yaml.Node, server security, smart form) | opus | opus |
| 2, 3, 5, 7, 9 | sonnet | sonnet |
| 10 (tests), final whole-branch review | opus | opus |
| 11 (docs) | sonnet | sonnet |

A controller unsure how to rule on a review finding hands the question to an opus subagent
rather than deciding alone.
