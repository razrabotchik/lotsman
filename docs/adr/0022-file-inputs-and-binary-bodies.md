# ADR-0022: File inputs — multipart and binary request bodies

- **Status**: Accepted
- **Date**: 2026-10-19
- **Decision owner**: specs/009-file-inputs; docs/spec.md §1.4, §4.5, §6.1; FR-29; Constitution I, II, V, VII

## Context

`application/json` is the only request body lotsman sends (`internal/openapi/body.go`,
`selectMediaType`), and docs/spec.md §1.4 lists "multipart и произвольные binary uploads" among the
non-goals of the first stable version. docs/improvements.md says multipart is added when a real
integration scenario asks for it.

One has. The granitstones.com.ua catalog is maintained through its own OpenAPI document; every
product carries 3–10 photographs that exist only as local files on the operator's machine.
`POST /api/products/{id}/images` takes `multipart/form-data`, so the operation is refused and the
scenario stops. base64 in a JSON body makes the model emit megabytes of base64 — token cost, output
limits, and one typo away from a corrupt image. "Let the API fetch a URL" needs the photographs
published first, which is the manual step the agent was meant to remove.

The hard part is not multipart serialization. It is that the content has to come from the local
filesystem, by a path that arrives in untrusted tool arguments (§6.1, Constitution V) and can be
proposed by a prompt injection in an upstream response.

## Decision

1. **Binary content is passed by reference to a local file, never as content.** A binary field is
   published as `{path, filename?, contentType?}` with `additionalProperties: false`. lotsman opens
   the file at execution time and streams it. The bytes never pass through the model, so the cost
   is one path string per file.
2. **The boundary is the operator's configuration, not the model's judgement.**
   `execution.fileInputs` is absent by default; an operation with a binary body is then refused with
   `file_inputs_disabled`. Enabling it requires `roots` and `allowedContentTypes` — there is no
   "enabled and unconstrained" state.
3. **Enforcement happens on the open file descriptor.** The argument must be an absolute, clean
   path; it is resolved with `EvalSymlinks`, checked for containment in a root component by
   component, opened with `O_NOFOLLOW`, and every later decision (regular file, size, content type,
   the bytes sent) is made through that descriptor. Size is bounded at `fstat` and again by an
   `io.LimitReader`, so a file that grows between the two fails the call instead of sending a body
   that does not match its `Content-Length`. The content type is detected from the content and
   matched against both the allowlist and the operation's `encoding.contentType`; the extension is
   never consulted.
4. **File inputs are a stdio feature unless an operator says otherwise.** Over the HTTP transport a
   remote client would be reading the server's disk, so it needs `allowHTTPTransport: true`, and
   `config check` warns about it by name.
5. **The body streams with a computed `Content-Length`, never chunked.** Every component of a
   multipart body's length is known before the request (boundary, part headers, text values,
   `fstat` sizes), so the request declares its length and still streams from disk. The reference
   backend is PHP behind Apache/nginx, where a chunked upload arrives with an empty `$_FILES`;
   chunked would have made the feature work on our fixture and fail on the user's server.
6. **This belongs in the Apache core, not in a PRO build.** Core is what makes an API usable;
   paid features are what an organization needs to run that at scale. A body format is the first
   kind. Gating it would also mean two implementations of the path sandbox, and the sandbox is the
   part that must have exactly one. The PRO line around this feature is per-client roots and quotas
   for a multi-tenant HTTP deployment, upload accounting, content policy beyond a type allowlist,
   and audit export — which is why root resolution goes through a seam that takes the call's
   context rather than reading the global configuration directly.
7. **No hash in the approval prompt.** Hashing before the call either reads the file twice — and the
   second read may differ, so the prompt would describe something other than what is sent — or
   holds the file in memory, which decision 5 refuses. The prompt shows basename, size and detected
   type; the `sha256` is computed over the streamed bytes and recorded in the audit event.
8. **The error returned to the client echoes the path the client supplied**, together with the
   configured roots, because a model that cannot see why its path was refused cannot correct it and
   will guess instead. Logs and audit events carry basename, size, type and hash only. This is a
   refinement of the §6.2 redaction rule, not an exception to it: a secret is file content and
   configuration, not an argument quoted back to its author.
9. **Only shapes whose serialization is certain are published.** One binary property plus primitive
   properties, or a single-file `application/octet-stream` body. Two binary properties, an array of
   files, an object or array property, `style`/`explode` on a multipart part, or a top-level
   `oneOf` are `unsupported_body_schema` with the shape named (Constitution I). The deferred shapes
   are listed in docs/improvements.md.
10. **FR-29 keeps deciding the media type.** JSON still wins when an operation offers both; a
    multipart body is sent when it is the only declared type or when
    `operationOverrides[].mediaType` pins it (feature 008). No new ranking, no guessing between two
    schemas.

## Consequences

- docs/spec.md §1.4 loses "multipart и произвольные binary uploads" as a blanket non-goal; §4.5
  gains `multipart/form-data` and `application/octet-stream` for the shapes of decision 9. Binary
  **responses**, `application/x-www-form-urlencoded`, and lotsman fetching a URL remain out.
- A new package, `internal/fileinput`, owns path resolution, the open, and the limits. It has no
  dependency on `internal/openapi` or `internal/config` types (Constitution VII, VIII); the CLI
  translates the configuration into it.
- `requestbuild.Build` can now return a request whose body owns open file descriptors. Every path
  that does not send the request — an approval going `input-required`, an egress refusal, a failed
  retry rebuild — must close the body. This is the one new invariant in the call path.
- A file is opened before the approval prompt, because the limits must be enforced before a human
  is asked about a call that could not happen anyway. It is closed if the prompt is pending, and the
  call is rebuilt from scratch when the client comes back.
- An intermediate directory of a path can still be swapped between `EvalSymlinks` and the open. The
  final component cannot (`O_NOFOLLOW`), containment is re-checked on the resolved path, and the
  roots are operator-chosen directories. Closing the remaining race needs `openat2(RESOLVE_BENEATH)`
  — Linux-only and a new syscall surface; accepted as a residual risk for a single-user local tool,
  revisited if per-client roots arrive with PRO.
- On Windows there is no `O_NOFOLLOW`: the final component is `Lstat`-ed and a reparse point is
  refused, which is a check with a window rather than an atomic one. Recorded as a platform
  difference in the README capability matrix.
- `execution.timeout` is no longer the whole story for a call that carries files:
  `fileInputs.uploadTimeout` (default 2m) extends the body phase. A call without file inputs keeps
  exactly today's budget.
- With `fileInputs` absent, no file-reading code is reachable, the catalog digest of a document
  without binary bodies does not move, and the call path for a JSON body is unchanged.
