# Tasks: file inputs in the core (feature 009, stage 1)

**Input**: plan.md, spec.md §1–§7 stage 1, ADR-0022
**Convention**: every task ends with `go build ./... && go test ./...` green and a commit. Smoke
commands assume `make build` has produced `bin/lotsman`. Tests beyond each task's smoke check are
task 11 (maintainer's decision; see plan.md "Global constraints").

**Before task 1**: branch `009-file-inputs` from a clean `main`. If the working tree holds someone
else's uncommitted changes, stop and ask the maintainer.

---

### Task 1: ADR-0022 and the feature documents

**Files:** create `docs/adr/0022-file-inputs-and-binary-bodies.md`, `specs/009-file-inputs/{spec,plan,tasks}.md`

- [x] **Step 1**: write the ADR (decisions 1–10, consequences, the core-vs-PRO line).
- [x] **Step 2**: write spec.md and plan.md.
- [x] **Step 3**: commit.

---

### Task 2: reason codes and the IR

**Files:** `internal/domain/domain.go`

- [ ] **Step 1**: add the eleven reason codes of spec §6, each with the one-line comment style of
  the existing block, saying what it refuses and that it happens before the network.
- [ ] **Step 2**: add `BodyKind` (`BodyJSON`, `BodyMultipart`, `BodyBinary`) and
  `BodyPart{Name, Binary bool, ContentTypes []string}`; add `Kind` and `Parts` to `BodySpec`.
  `Kind` empty means JSON, so every existing construction of a `BodySpec` keeps its meaning and no
  golden file moves.
- [ ] **Step 3**: `go build ./... && go test ./...`, commit.

---

### Task 3: `internal/fileinput` — the sandbox

**Files:** create `internal/fileinput/{doc.go,fileinput.go,open_unix.go,open_windows.go}`

- [ ] **Step 1**: `doc.go` states the contract: a path from untrusted arguments plus operator
  limits in; an open descriptor, its verified size and its detected content type out, or a reason
  code. No OpenAPI, configuration or HTTP types (Constitution VIII).
- [ ] **Step 2**: `Limits{Roots []string, MaxFileBytes, MaxTotalBytes int64, AllowedContentTypes []string}`
  and `New(Limits) (*Resolver, error)`: roots are `EvalSymlinks`+`Abs`-resolved at construction, a
  root that is missing, not a directory, or `/` is an error, content types are parsed once.
- [ ] **Step 3**: `Resolver.Open(req Request) (*File, error)` where `Request{Path, Filename, ContentType}`:
  1. reject a path that is empty, relative, unclean, `~`-prefixed, or holds a NUL —
     `file_input_invalid_path`, without touching the filesystem;
  2. `EvalSymlinks`, then containment in a root compared component by component (not by string
     prefix) — `file_input_outside_roots`;
  3. open with `O_NOFOLLOW` (Unix) or `Lstat`-then-open with a reparse-point refusal (Windows) —
     `file_input_not_regular_file` / `file_input_unreadable`;
  4. `fstat` the descriptor: regular file, size within `MaxFileBytes` —
     `file_input_not_regular_file`, `file_input_too_large`;
  5. detect the type from the first 512 bytes read through the descriptor, then `Seek(0)`; match
     `AllowedContentTypes` — `file_input_type_not_allowed`;
  6. validate `Filename` as a name (no separator, no `..`, no control character) —
     `file_input_invalid_filename`; default it to the file's own name.
  Every failure closes the descriptor. A `Session` accumulates `MaxTotalBytes` across one call —
  `file_input_total_too_large`.
- [ ] **Step 4**: `File.Reader()` returns an `io.Reader` limited to the `fstat` size that reports
  `file_input_changed` when the file is longer or shorter, and tees into a `sha256` hash whose
  value is readable after the copy. `File.Close` is idempotent.
- [ ] **Step 5**: minimal smoke test (a temp root, one file in it, one outside), then commit.

---

### Task 4: recognizing a binary body in `internal/openapi`

**Files:** create `internal/openapi/binary.go`; edit `body.go`, `schema.go`

- [ ] **Step 1**: `binary.go`: `isBinaryProperty` for OAS 3.0 (`format: binary`, never `byte`),
  OAS 3.1 (`contentMediaType` without `contentEncoding`), and the empty-schema-plus-`encoding`
  spelling; `encodingContentTypes` parsing the comma-separated set of `encoding[name].contentType`;
  `singleFileMediaType` for `application/octet-stream` and a concrete non-text type declared alone.
- [ ] **Step 2**: `schema.go`: `fileInputSchema()` building the published
  `{path, filename?, contentType?}` object of spec §2, with `additionalProperties: false` and the
  "pass a path, never the content" wording.
- [ ] **Step 3**: `body.go`: `selectMediaType` admits `multipart/form-data` and the single-file
  types — alone or pinned, JSON still winning when both are offered (FR-29 unchanged). `buildBody`
  translates a multipart object schema into `BodySpec{Kind: multipart, Parts: …}` with the file
  property replaced by `fileInputSchema()`, and refuses with `unsupported_body_schema` naming the
  shape for: no binary property, two binary properties, an array of files, a non-primitive
  non-binary property, `style`/`explode` in `encoding`, a top-level combinator. A single-file body
  becomes `BodySpec{Kind: binary}` whose whole schema is `fileInputSchema()`.
- [ ] **Step 4**: smoke: `bin/lotsman inspect examples/fileupload/openapi.yaml` publishes the
  covered operations and names the refused shapes. Commit.

---

### Task 5: `execution.fileInputs` in the configuration

**Files:** `internal/config/{config.go,runtime.go,export.go}`

- [ ] **Step 1**: `FileInputs{Enabled, Roots, MaxFileBytes, MaxTotalBytes, AllowedContentTypes,
  AllowHTTPTransport, UploadTimeout}` on `Execution`, with the documented defaults of spec §3
  (20 MiB, 100 MiB, 2m) applied on resolve, not on parse.
- [ ] **Step 2**: `validateExecution` refuses: `enabled` with no `roots`; `enabled` with no
  `allowedContentTypes`; a root that is relative, missing, not a directory, or `/`; a content type
  that does not parse or carries parameters; a non-positive or inverted limit (`maxFileBytes >
  maxTotalBytes`); an unparsable `uploadTimeout`. A disabled section with fields set is accepted and
  stays inert — it is how an operator keeps a configuration ready.
- [ ] **Step 3**: `Runtime` carries the resolved limits; `export.go` round-trips the section with
  byte counts as written.
- [ ] **Step 4**: smoke: `bin/lotsman config check` on a good and on three bad configurations.
  Commit.

---

### Task 6: multipart and single-file serialization

**Files:** create `internal/requestbuild/multipart.go`; edit `requestbuild.go`

- [ ] **Step 1**: `Options.Files *fileinput.Resolver` (nil: no file body can be built — a
  `BodySpec` of kind multipart/binary then fails as an internal error, since publication already
  depends on the resolver existing).
- [ ] **Step 2**: `encodeBody` returns `encodedBody{reader io.ReadCloser, contentType string,
  length int64}`; the JSON path keeps today's behaviour exactly. `Build` assigns
  `req.ContentLength` and leaves `GetBody` nil.
- [ ] **Step 3**: `multipart.go`: resolve each file through the resolver *before* writing anything;
  compute the exact body length from the boundary, the part headers rendered the way
  `mime/multipart` renders them, the text values and the `fstat` sizes; stream through `io.Pipe`
  with a 32 KiB copy buffer; abort the pipe with `file_input_changed` when a file does not deliver
  its stated size. A fixed boundary is accepted from tests only (determinism), random otherwise.
- [ ] **Step 4**: text parts: primitives in published property order, `true`/`false` and numbers as
  the JSON value reads, `encoding[name].contentType` honoured on a text part.
- [ ] **Step 5**: single-file body: the file is the body, `Content-Type` is the declared type or the
  detected one when the operation declared a wildcard.
- [ ] **Step 6**: smoke: a test fixture server echoing what it parsed; one multipart and one
  octet-stream call compared by `sha256`. Commit.

---

### Task 7: the call path

**Files:** `internal/mcpserver/{runner.go,server.go,approval.go}`, `internal/cli/serve.go`

- [ ] **Step 1**: carry the resolver into the runner; the call budget adds `uploadTimeout` when the
  operation's body kind is multipart or binary.
- [ ] **Step 2**: close `req.Body` on every path that does not send the request: an egress refusal,
  a pending approval, a rebuild for the retry, and the unexpanded-template guard. A test asserts the
  descriptor count does not grow over a hundred pending approvals.
- [ ] **Step 3**: the approval prompt lists each file as basename, size and detected type — no full
  path, no hash, no content (ADR-0022 decision 7).
- [ ] **Step 4**: the transport gate: `serve --transport=http` with file inputs enabled and
  `allowHTTPTransport` unset refuses to publish those operations with
  `file_inputs_transport_not_allowed`; with it set, `config check` and the startup log warn and name
  the roots.
- [ ] **Step 5**: `fileInputs` absent: the operations are published nowhere and `inspect` says
  `file_inputs_disabled` with "enable execution.fileInputs".
- [ ] **Step 6**: smoke: `bin/lotsman serve examples/fileupload/openapi.yaml` against the fixture
  through an MCP client call. Commit.

---

### Task 8: audit and `explain-call`

**Files:** `internal/audit/event.go`, `internal/mcpserver/runner.go`, `internal/cli/explain.go`

- [ ] **Step 1**: `FileRecord{Name, Bytes, ContentType, SHA256}` and `Event.Files`; `SchemaVersion`
  to 3 with the comment saying what version 3 added. The hash is the streamed one, so the record is
  filled after the send; a refused call records the files it resolved with an empty hash.
- [ ] **Step 2**: `explain-call` resolves every file input, prints basename, size, detected type and
  the parts that would be sent, closes the descriptors and makes no request. Root, size, type and
  filename refusals surface here with their codes.
- [ ] **Step 3**: smoke: `bin/lotsman explain-call uploadProductImage --args …` for a good path and
  for a path outside the roots. Commit.

---

### Task 9: reports and the workbench

**Files:** `internal/catalog/report.go`, `internal/workbench/*`

- [ ] **Step 1**: an operation with a binary body carries a `file_inputs` note into `inspect` text
  and `--json`; a disabled configuration shows `file_inputs_disabled` as the reason with its remedy.
- [ ] **Step 2**: the workbench names `file_inputs_disabled` as a configuration problem and shows
  the roots and limits for such an operation. It still reads no file and takes no path from the
  browser.
- [ ] **Step 3**: smoke: `bin/lotsman inspect --json` on the fixture spec, enabled and disabled.
  Commit.

---

### Task 10: documentation

**Files:** `docs/spec.md`, `README.md`, `docs/improvements.md`, `examples/fileupload/`

- [ ] **Step 1**: docs/spec.md: §1.4 loses the blanket multipart non-goal and names what stays out;
  §4.5 gains the two media types and the covered shapes; §4.6 notes the upload budget; new
  FR-86..FR-89 (file inputs off by default with roots and limits; enforcement on the descriptor;
  computed `Content-Length`, never chunked; the audit record), with the changelog entry pointing at
  ADR-0022.
- [ ] **Step 2**: README: capability matrix rows, the `execution.fileInputs` example, the Windows
  difference, and the sentence that tells an operator the model passes paths.
- [ ] **Step 3**: docs/improvements.md: tick the multipart line; add the deferred shapes (arrays of
  files, object parts, urlencoded, binary responses, MCP `roots` intersection, `openat2`).
- [ ] **Step 4**: `examples/fileupload/openapi.yaml` with the four covered shapes and the refused
  ones, plus a README line. Commit.

---

### Task 11: the suite (spec §8)

**Files:** tests across `internal/fileinput`, `internal/openapi`, `internal/requestbuild`,
`internal/mcpserver`, `internal/cli`, `internal/config`

- [ ] **Step 1**: fixture-API tests: `sha256`, filename, part order, per-part `Content-Type`,
  `Content-Length` present and correct; multipart with text fields, octet-stream, a non-ASCII
  filename, a text part with `encoding.contentType`.
- [ ] **Step 2**: the negative table of spec §8, one case per reason code, each asserting no
  outbound request was made.
- [ ] **Step 3**: canary suite over stdout, logs, audit events and tool output, with the canary in
  both the file's content and its path; plus the assertion that the client-facing refusal does carry
  the client's path.
- [ ] **Step 4**: memory bound over a 20 MiB send; determinism golden with a fixed boundary; catalog
  digest unchanged for a document without binary bodies.
- [ ] **Step 5**: `unsupported_body_schema` table for every deferred shape; `explain-call` output per
  refusal class; the approval prompt's fields; fuzz over the path argument (NFR-6).
- [ ] **Step 6**: `make check` and `make cover`; update docs/benchmarks.md if a number moved.
  Commit.
