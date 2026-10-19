# Implementation Plan: file inputs in the core (feature 009, stage 1)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement tasks.md task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Branch**: `009-file-inputs` | **Spec**: ./spec.md | **ADR**: docs/adr/0022-file-inputs-and-binary-bodies.md
| **Frozen spec**: docs/spec.md §1.4, §4.5, §4.6, §6.1, FR-29, FR-32

**Goal:** an operator who configures `execution.fileInputs` can call an operation whose body is
`multipart/form-data` or `application/octet-stream`; the model passes a path, lotsman streams that
file inside the configured roots and limits, and every refusal has its own code and happens before
the network. Stage 2 (the granitstones integration against a PHP fixture) gets its own plan.

**Architecture:** three separable pieces.

1. **Recognition and publication** — `internal/openapi`: a binary property becomes a
   `{path, filename?, contentType?}` object in the published schema, and `domain.BodySpec` grows a
   kind plus an ordered part list so the builder never re-reads the OpenAPI document.
2. **The sandbox** — a new `internal/fileinput` package: a path argument plus operator limits in,
   an open file descriptor with its verified size and detected type out, or a reason code. It knows
   nothing about OpenAPI, configuration structs or HTTP (Constitution VIII), which is also the seam
   PRO per-client roots will use (ADR-0022 decision 6).
3. **Serialization** — `internal/requestbuild`: a multipart or single-file body whose total length
   is computed before the request and whose content streams from disk through `io.Pipe`.

Everything else is wiring: the configuration, the transport gate, and making `inspect`,
`explain-call`, the approval prompt and the audit event tell the truth.

**Tech stack:** Go 1.25, standard library only — `mime/multipart`, `net/textproto`, `io`,
`crypto/sha256`, `path/filepath`, `syscall` for `O_NOFOLLOW`. No new dependency (Constitution VII).

## Global constraints

- No new direct dependency. `golang.org/x/sys` is not added: `syscall.O_NOFOLLOW` is in the
  standard library on the Unix platforms we ship, and the Windows path is `Lstat`-based.
- With `execution.fileInputs` absent, nothing changes: same catalog digest for a document without
  binary bodies, same JSON call path, no reachable file-reading code (success criteria).
- Same arguments ⇒ byte-identical body apart from the multipart boundary (Constitution IV). Part
  order is the published property order, never a map iteration.
- Every refusal is before the network and carries its own reason code (Constitution I, §6 of spec).
- A request whose body holds descriptors is closed on every path that does not send it
  (ADR-0022 consequence). This is the one new invariant in `internal/mcpserver/runner.go`.
- File content, and any full path, stay out of stdout, logs, audit events and tool output; the
  refusal returned to the client does carry the client's own path and the configured roots
  (ADR-0022 decision 8).
- **Testing (maintainer's decision, 2026-10-16, deviation from Constitution X):** while integrating,
  verify each task with `go build ./...` plus the one smoke command the task names; the full suite
  is the last task. `go test ./...` must pass at every commit — existing tests are updated in the
  task that breaks them.
- Commit messages in Russian, one line, in the style of `git log`, ending with the `Co-Authored-By`
  line.

## Technical context

- `internal/openapi/body.go:113` `selectMediaType` refuses everything but JSON; `jsonMediaType` is
  the only constant it knows. `buildBody` already carries an operator pin (feature 008), so FR-29
  needs no new rule — multipart becomes selectable when it is alone or pinned.
- `domain.BodySpec` (`internal/domain/domain.go:546`) is `{MediaType, Required, Description,
  Schema}`. It gains `Kind` (`json` | `multipart` | `binary`) and `Parts []BodyPart`
  (`{Name, Binary, ContentTypes []string, HeaderContentType string}`) in published property order.
  `Kind` is part of the catalog digest input only through the published schema — adding a field to
  the IR must not move the digest of a JSON operation, which the determinism test pins.
- `requestbuild.encodeBody` (`internal/requestbuild/requestbuild.go:101`) returns
  `(io.Reader, contentType, error)` and `Build` passes it to `http.NewRequestWithContext`. For a
  streamed body the request also needs `ContentLength` and a `Body` that closes descriptors, so
  `encodeBody` returns a small `encodedBody{reader io.ReadCloser, contentType string, length int64}`.
  `http.NewRequestWithContext` sets `ContentLength` from a `*bytes.Reader` but not from a pipe, so
  it is assigned explicitly; `Request.GetBody` is left nil (no redirects — FR default is `deny`).
- `runner.execute` (`internal/mcpserver/runner.go:195`) builds the request, then checks egress, then
  asks for approval, then sends. The build closure is re-run for the one FR-35 retry, which already
  matches "a streamed body is rebuilt, never re-sent". The pending-approval and egress-refusal paths
  gain `req.Body.Close()`.
- `approvalMessage`/`argumentSummary` (`internal/mcpserver/approval.go:160,182`) render the prompt
  from raw arguments. A file-input object must render as basename + size + detected type, which
  means the prompt needs the resolved files, not the arguments — the resolution result is carried
  from `build()` to `check()`.
- `audit.Event` (`internal/audit/event.go`) gains `Files []audit.FileRecord`
  (`{Name, Bytes, ContentType, SHA256}`), filled after the send, since the hash is known only then
  (ADR-0022 decision 7). `SchemaVersion` moves to 3.
- `egress.Policy.CallBudget` (`internal/egress/policy.go:109`) returns `Budget.Total`. A call
  carrying files adds `fileInputs.uploadTimeout`; the budget is computed per call, so this is a
  parameter at the call site, not a new policy field.
- `config.Execution` (`internal/config/config.go:136`) is where `FileInputs` lands, with
  `validateExecution` (`:680`) refusing `enabled` without `roots` or `allowedContentTypes`, a root
  that is not an existing directory, `/` as a root, a non-parsing content type, and a non-positive
  limit. `config.Runtime` carries the resolved roots; `export.go` round-trips the section.
- The transport gate is in the CLI, where the transport is known: `serve` refuses to publish file
  inputs over HTTP without `allowHTTPTransport`, and `config check` warns when it is set.

## Files

```text
docs/adr/0022-file-inputs-and-binary-bodies.md  new: the decision, threat model, core-vs-PRO
internal/domain/domain.go              BodyKind, BodyPart, BodySpec.{Kind,Parts}; 11 reason codes
internal/fileinput/fileinput.go        new: Limits, Resolver, Resolved, Open; path and fd checks
internal/fileinput/open_unix.go        new: O_NOFOLLOW open + fstat
internal/fileinput/open_windows.go     new: Lstat + reparse-point refusal
internal/fileinput/doc.go              new: package contract and threat model
internal/openapi/binary.go             new: binary-property detection (3.0 and 3.1), encoding sets
internal/openapi/body.go               multipart/octet-stream selection, part list, published schema
internal/openapi/schema.go             fileInputSchema: the {path, filename, contentType} object
internal/requestbuild/multipart.go     new: part assembly, computed length, io.Pipe streaming
internal/requestbuild/requestbuild.go  encodedBody; ContentLength; file resolution through Options
internal/config/config.go              FileInputs, validation
internal/config/runtime.go             Runtime.FileInputs (resolved roots and limits)
internal/config/export.go              export the section
internal/cli/serve.go                  build the resolver; transport gate; pass to the server
internal/cli/explain.go                resolve files, print parts, close descriptors, no network
internal/cli/check.go                  warning for allowHTTPTransport
internal/mcpserver/runner.go           close the body when not sent; upload budget; audit files
internal/mcpserver/approval.go         file lines in the prompt
internal/mcpserver/server.go           resolver into the runner
internal/audit/event.go                FileRecord; SchemaVersion 3
internal/catalog/report.go             file-inputs note and the disabled reason in verdicts
docs/spec.md §1.4 §4.5 §4.6, FR-86..FR-89   the frozen spec, through this ADR
README.md, docs/improvements.md        capability matrix, config example, deferred shapes
examples/fileupload/                   new: a spec with the four covered body shapes (tests)
```

## Review focus

1. A document with no binary bodies and no `fileInputs`: catalog digest byte-identical to `main`
   (determinism golden), and `encodeBody` takes exactly today's path.
2. `Content-Length` equals the bytes actually written, for an ASCII filename, a non-ASCII filename,
   an empty text field and a 20 MiB file. A mismatch aborts the request rather than sending a short
   body.
3. A symlink inside a root pointing outside it, and a symlink swapped in as the final component
   after `EvalSymlinks`: both refused, by containment and by `O_NOFOLLOW` respectively.
4. A file that grows between `fstat` and the read: the call fails with `file_input_changed`, and no
   truncated body is sent.
5. Approval going pending, then the client coming back: descriptors from the first build are
   closed, the second build opens its own, and nothing is read twice from a half-consumed pipe.
6. The canary suite: a file whose content and whose path both contain the canary leaves nothing in
   stdout, the log, the audit event or the tool result; the refusal returned to the client contains
   the client's path and no file content.

Each of these has a test in the final task.
