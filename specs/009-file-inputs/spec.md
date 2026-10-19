# Feature 009: file inputs — `multipart/form-data` and binary request bodies

**Status**: design, 2026-10-19. docs/spec.md §1.4 lists "multipart и произвольные binary uploads"
among the non-goals of the first stable version and §4.5 publishes `application/json` bodies only;
this feature removes that non-goal for the operator's own local files and narrows it to what stays
out (§6). ADR-0022 records the decision, the file-input shape and the threat model.

## Why this exists

A catalog operator adds granite memorials to granitstones.com.ua through its own OpenAPI document.
Every product carries 3–10 photographs that exist as **local files** on the operator's machine — a
folder from the supplier, a dump from a phone. They are nowhere on the internet yet.

`POST /api/products/{id}/images` declares a `multipart/form-data` body, so lotsman refuses it as
`unsupported_media_type` (`internal/openapi/body.go:113`). The two workarounds do not survive
contact with the job:

- **base64 inside a JSON body.** The model would have to write megabytes of base64 into the tool
  arguments: expensive in tokens, slow, past output limits, and broken by the first typo.
- **"let the API fetch a URL".** The photographs are not published, and publishing them somewhere
  just to upload them is the manual step the agent was supposed to remove.

`multipart/form-data` is the second most common way to send an HTTP request body. A bridge from
OpenAPI to MCP that cannot upload a file is not a smaller product, it is an unfinished one — so this
lands in the Apache core, not in a PRO build (ADR-0022, decision 6).

## The idea in one paragraph

lotsman publishes operations whose body is `multipart/form-data` or `application/octet-stream`. A
binary field is published as **a reference to a local file**, never as content: the model passes a
path, lotsman opens that file at execution time inside operator-configured roots and streams it into
the request body. The bytes of the file never pass through the model, and the ability to read local
files is off until an operator turns it on and says which directories it covers.

## Scope

In:

- `multipart/form-data` request bodies: one binary field plus primitive text fields.
- `application/octet-stream` (and a vendor-declared `image/*`-style single-part type): the whole
  body is one file.
- Recognizing binary fields in OAS 3.0 (`format: binary`) and OAS 3.1 (`contentMediaType` without
  `contentEncoding`, empty schema beside `encoding`).
- `execution.fileInputs`: off by default, roots, per-file and per-call byte caps, content-type
  allowlist, an explicit opt-in for the HTTP transport.
- Reading a named regular file inside a root, with symlink, type and size enforcement on the open
  file descriptor.
- A streamed body with a `Content-Length` computed before the request starts.
- `explain-call`, the approval prompt, `inspect`, the capability report and the workbench all
  telling the truth about file inputs.

Out (§6 has the reasoning):

- Arrays of files (`type: array, items: {format: binary}`) and non-primitive non-binary properties.
- Binary **responses** — downloading a file from an API.
- `application/x-www-form-urlencoded` — a separate feature.
- lotsman fetching a URL itself: a new egress surface. If the API accepts a URL, the API fetches it.
- Directory expansion, globs, relative paths, `~` expansion: the client enumerates files, lotsman
  reads the ones it is named.
- `format: byte` stays an ordinary base64 string property, as today.

## User scenarios

- **US-1 (P1)**: As the granitstones operator with `fileInputs` configured for
  `/home/user/catalog-photos`, I say "upload every photo from `/home/user/catalog-photos/AB-123/` to
  product 512, the first one as the main image". The agent lists the directory with its own
  filesystem tools and calls `uploadProductImage` once per file; each call sends a byte-exact JPEG.
  *Acceptance*: the fixture API receives the expected `sha256`, filename and fields per call.
- **US-2 (P1)**: As an operator who has not enabled file inputs, `inspect` tells me
  `uploadProductImage` needs `execution.fileInputs` instead of a flat `unsupported_media_type`.
  *Acceptance*: reason code `file_inputs_disabled` with that remedy in text and `--json`.
- **US-3 (P1)**: As a security reviewer, a prompt injection in an upstream response that makes the
  model call the tool with `path: /home/user/.ssh/id_rsa` gets a refusal before any network request,
  and neither the file's content nor anything about it beyond the refusal reaches the API.
  *Acceptance*: `file_input_outside_roots`, no outbound request, nothing in the audit event but the
  rejected path's basename.
- **US-4 (P2)**: As an operator, `lotsman explain-call uploadProductImage --args …` shows me the
  file that would be read — basename, size, detected type — and the multipart parts that would be
  sent, without sending them.
- **US-5 (P2)**: As an operator running `--transport=http`, file inputs are refused until I opt in a
  second time, because a remote client would otherwise read the server's disk.
  *Acceptance*: `file_inputs_transport_not_allowed`; `config check` warns when the opt-in is set.

## 1. Recognizing a binary field

A property is binary when, after normalization to JSON Schema 2020-12:

- OAS 3.0: `type: string` with `format: binary`. `format: byte` is **not** binary — it stays a
  base64 string the model may legitimately write for small values.
- OAS 3.1: `contentMediaType` present and `contentEncoding` absent.
- The schema is empty (`{}` or absent) **and** `encoding[name].contentType` names the type: refused
  by name. *Decided during implementation (2026-10-19):* a schema that constrains nothing cannot
  validate an argument (FR-23), and normalization refuses it before a part list could be built. The
  refusal says which line of the document would fix it, and the spelling is in
  docs/improvements.md.
- `application/octet-stream`, or a single `image/*`, `audio/*`, `video/*`, `application/pdf`-style
  concrete type declared as the only media type of the body: the whole body is one file, whatever
  the schema says.

A `multipart/form-data` body is translated only when it is an object schema whose properties are
exactly one binary property plus primitive (`string`, `number`, `integer`, `boolean`) properties.
Anything else — two binary properties, an array of files, an object or array property, a
`oneOf`/`anyOf` at the top — is `unsupported_body_schema` with the shape named in the message
(Constitution I: a part serialization lotsman is not sure of is not approximated). The deferred
shapes are recorded in docs/improvements.md.

## 2. How a file field appears in the tool's input schema

```json
{
  "type": "object",
  "required": ["path"],
  "additionalProperties": false,
  "properties": {
    "path": {
      "type": "string",
      "description": "Absolute path to a local file on the machine running lotsman. Pass the path, never the file's content."
    },
    "filename": { "type": "string", "description": "Name sent to the API; defaults to the file's own name." },
    "contentType": { "type": "string", "description": "Media type sent for this part; defaults to the type detected from the file's content." }
  }
}
```

The tool description says, in words the model reads: *pass a path to a local file; do not read the
file and do not pass its content*. The `octet-stream` body publishes the same object as the whole
body argument.

`filename` is a name, not a path: a separator, `..`, a control character or a NUL refuses the call
(`file_input_invalid_filename`). `contentType` must parse and must be inside both the operation's
`encoding.contentType` set and `allowedContentTypes`.

## 3. Trust boundary and configuration

Tool arguments are untrusted (docs/spec.md §6.1, Constitution V), and an upstream response can
carry a prompt injection that proposes a path. Reading a local file by argument is therefore a new
boundary, and it is drawn in the configuration, not in the model's reasoning.

```yaml
execution:
  fileInputs:
    enabled: true                  # absent/false: binary bodies are refused, nothing can be read
    roots:                         # required when enabled; files only inside these directories
      - /home/user/catalog-photos
    maxFileBytes: 20971520         # 20 MiB per file; default
    maxTotalBytes: 104857600       # 100 MiB per call; default
    allowedContentTypes:           # required when enabled; detected type must match
      - image/jpeg
      - image/png
      - image/webp
    allowHTTPTransport: false      # default; see below
    uploadTimeout: 2m              # extra body-phase budget for a call carrying files
```

- **Off by default.** Without `enabled: true`, an operation with a binary body is published nowhere
  and `inspect` says `file_inputs_disabled` with "enable execution.fileInputs". `enabled: true` with
  no `roots` or no `allowedContentTypes` is a configuration error, not a wide-open default.
- **Roots are canonical directories.** Each root is resolved with `filepath.EvalSymlinks` +
  `filepath.Abs` at load; a root that does not exist, is not a directory, or is `/` is refused.
- **The path check happens on the open file, not on the string.** The argument must be absolute and
  clean (`..`, a relative path, `~`, an empty path, a NUL: refused without touching the
  filesystem). It is then resolved with `EvalSymlinks`, checked for containment in a root by path
  component, and opened with `O_NOFOLLOW`; `fstat` on the descriptor decides type and size, and
  every subsequent operation uses that descriptor. A directory, symlink, FIFO, socket, device or
  anything under `/proc`, `/sys`, `/dev` is refused by that check rather than by a path pattern.
  (Windows has no `O_NOFOLLOW`: the final component is `Lstat`-ed and a reparse point is refused.
  ADR-0022 records the residual intermediate-directory race on both platforms.)
- **Size is enforced twice.** `fstat` must be within `maxFileBytes`, and the read is an
  `io.LimitReader` to the same bound: a file that grows after `fstat` fails the call instead of
  sending a truncated or oversized body. The sum across one call is bounded by `maxTotalBytes`.
- **Content type comes from the content.** `http.DetectContentType` over the first 512 bytes, then
  matched against `allowedContentTypes` and against `encoding.contentType` (a comma-separated list
  is a set, as in the reference operation). The extension is never trusted; a mismatch between the
  detected type and a client-supplied `contentType` is a refusal, not an override.
- **HTTP transport.** A remote client must not read the disk of the machine lotsman runs on. File
  inputs work on `stdio` only unless `allowHTTPTransport: true`, which `config check` reports as a
  warning naming the roots it exposes.
- **Logs, audit, errors.** The audit event and the logs carry basename, size, detected type and the
  `sha256` of what was sent — never the content, never the full path. The *error returned to the
  client* does echo the path the client itself supplied, plus the configured roots, because the
  client needs it to correct itself and it is the client's own string (refinement of docs/spec.md
  §6.2 redaction: a secret is content and configuration, not an argument echoed back to its author).
- MCP `roots` from the client are not consulted in this release; intersecting them with the
  operator's roots is a note in docs/improvements.md. The operator's configuration is the boundary.

## 4. Building the request

- **Parts.** The binary property becomes the file part. Primitive properties become text parts in
  the property order of the published schema (Constitution IV: the body is byte-identical for the
  same arguments). `true`/`false`, numbers and integers are rendered the way the JSON value reads;
  a string is sent as-is. `encoding[name].contentType` on a text part is honoured; `style` and
  `explode` for multipart are not implemented and an operation that declares them is refused.
- **`Content-Disposition`.** `mime/multipart` escapes the quotes and CR/LF of a filename; a
  non-ASCII filename is sent as UTF-8 in `filename` per RFC 7578. Verified by test, not by reading
  the standard library.
- **Streaming with a known length.** The whole body length is computed before the request: the
  boundary, every part's headers, every text part's value and every file's `fstat` size are known.
  The request carries an explicit `Content-Length` and the body streams from disk through
  `io.Pipe`, so a 20 MiB upload does not become 20 MiB of heap. Chunked encoding is not used: the
  reference backend is PHP 5.6 behind Apache/nginx, where a chunked upload reaches `$_FILES` empty
  or not at all. If the streamed size does not match the computed length, the request is aborted
  before completion and the call fails.
- **`application/octet-stream`.** The body is the file; `Content-Type` is the operation's declared
  type, or the detected type when the operation declares a wildcard.
- **Media type choice (FR-29).** JSON still wins when an operation declares both; a multipart body
  is chosen when it is the only declared type, or when
  `operationOverrides[].mediaType: multipart/form-data` pins it (feature 008). No new selection
  rule, no guessing between a JSON and a multipart schema.

## 5. Existing mechanisms

- **Mutation gate and approval.** An upload is a POST/PUT/PATCH: a mutation, with everything that
  already implies. The approval prompt shows the operation, the file's basename, its size and its
  detected type — not the full path, not the content, and not a hash (see below).
- **No hash in the approval prompt.** A hash shown before the call would either require reading the
  file twice — the second read can differ, so the prompt would not describe what is sent — or hold
  it in memory, which §4 refuses. The `sha256` is computed over the bytes as they stream and lands
  in the audit event, which is where "what exactly went out" belongs.
- **`explain-call`.** Resolves every file input and reports basename, size, detected type and the
  parts that would be sent, with no network request. Root, size, type and filename refusals surface
  here, so an operator can test a path without touching the API.
- **Retry.** Mutations are not retried, as today. A streamed body cannot be re-read, so any future
  retry re-resolves the files and rebuilds the body from scratch.
- **Timeouts.** The body-phase budget of FR-32 gains `uploadTimeout` (default 2m) for a call that
  carries file inputs; 20 MiB does not fit in a 30 s total budget on a slow uplink.
- **`inspect` / capability report.** An operation with a binary body is supported either way. When
  file inputs are not available it is **published and never executable**, carrying
  `file_inputs_disabled` (or `file_inputs_transport_not_allowed`) and the line of configuration that
  would fix it. *Decided during implementation (2026-10-19), replacing "rejected" above:* this is
  what lotsman already does for an operation no configured credential can authenticate, and a
  translation that succeeded should not be reported as a translation that failed.
- **Workbench (`lotsman ui`).** Shows the roots and the limits for an operation with file inputs,
  and names `file_inputs_disabled` as a thing the configuration fixes. It never reads a file and
  accepts no path from the browser (feature 008, §4).
- **Concurrency and memory.** A file input holds one descriptor and a 32 KiB copy buffer; the
  `maxConcurrentCalls` bound already caps how many exist at once.

## 6. New reason codes

`file_inputs_disabled`, `file_inputs_transport_not_allowed`, `file_input_outside_roots`,
`file_input_not_regular_file`, `file_input_unreadable`, `file_input_too_large`,
`file_input_total_too_large`, `file_input_type_not_allowed`, `file_input_invalid_path`,
`file_input_invalid_filename`, `file_input_changed`. Each is returned before the network and each
is distinguishable in a test (Constitution I: a refusal a client cannot branch on is not a refusal).

## 7. Stages

1. **Core.** ADR-0022; recognition (§1); the published file-input schema (§2); `execution.fileInputs`
   with validation (§3); the sandboxed opener; multipart and octet-stream serialization with a
   computed `Content-Length` (§4); reason codes; `inspect`/`explain-call`/approval/audit (§5);
   docs/spec.md §1.4 and §4.5, README, ADR, improvements.md.
2. **Reference integration.** The granitstones operation end to end against a PHP fixture
   (`$_FILES`, `upload_max_filesize` smaller than ours, a 413), recorded in docs/corpus.md.

## 8. Testing

**Deviation from Constitution X, as in feature 008 and by the same maintainer decision:** during
integration only `go build ./...` plus the one smoke check each task names; the full suite is
written at the end of the stage, before it is called done. `go test ./...` stays green at every
commit.

The suite stage 1 ends with:

- A fixture API that checks what arrived: `sha256` of the received file, filename, part order, text
  field values, `Content-Type` per part, and that `Content-Length` was present and correct.
  Operations covered: single file plus text fields; `application/octet-stream`; a file with a
  non-ASCII filename; a text part with `encoding.contentType`.
- Negative table, one case per reason code, each asserting **no outbound request**: path outside
  roots, `..`, relative path, `~`, NUL, symlink pointing out of a root, symlink as the final
  component, directory, FIFO, `/proc/self/environ`, file over `maxFileBytes`, call over
  `maxTotalBytes`, file grown after `fstat`, detected type outside the allowlist, detected type
  outside `encoding.contentType`, filename with a separator, missing file, unreadable file,
  `fileInputs` disabled, HTTP transport without the opt-in.
- Canary-secret suite over logs, audit events and tool output with a file whose content is the
  canary and whose path contains one too: neither appears. *Narrowed during implementation
  (2026-10-19):* `explain-call` does echo the arguments the operator typed on their own terminal,
  including the path, as it did before this feature; what must never appear anywhere is the file's
  content, and what must never reach a log or an audit event is the path.
- Memory: sending a 20 MiB file allocates no proportional buffer (`testing.AllocsPerRun`-style
  bound on peak heap over the send).
- Determinism golden: the same arguments produce a byte-identical body modulo the boundary, and the
  catalog digest of a spec with binary bodies is stable.
- `explain-call` output for every refusal class; the approval prompt's fields; an
  `unsupported_body_schema` table for each deferred shape of §1.
- Fuzz over the path argument (NFR-6) asserting no read outside the roots.

## Success criteria

- granitstones: "upload every photo from this folder to product 512" works end to end against the
  real API with `fileInputs` configured for one directory.
- No argument, including one written by a prompt injection, reads a file outside the configured
  roots; every refusal has its own code and happens before the network.
- A 20 MiB upload does not grow the process heap in proportion to the file.
- With `fileInputs` absent from the configuration: catalog digest, startup time and call latency are
  unchanged (docs/benchmarks.md), and no file-reading code is reachable.

## Reference operation

```yaml
/api/products/{id}/images:
  post:
    operationId: uploadProductImage
    summary: Add a photo to a product
    security: [{ apiKey: [] }]          # X-Api-Key header
    requestBody:
      required: true
      content:
        multipart/form-data:
          schema:
            type: object
            required: [file]
            properties:
              file:     { type: string, format: binary }
              is_main:  { type: boolean }
              alt:      { type: string }
              alt_ua:   { type: string }
              title:    { type: string }
              title_ua: { type: string }
          encoding:
            file: { contentType: 'image/jpeg, image/png, image/webp' }
    responses:
      '201': { description: Photo added (JSON with id and URL) }
```

Backend: PHP 5.6 (Apache + mod_php or nginx + php-fpm), ordinary `$_FILES` parsing. The task this
feature exists for: *"upload every photo from /home/user/catalog-photos/AB-123/ to product 512, the
first one as the main image"*.
