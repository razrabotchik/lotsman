# ADR-0011: Egress policy, response shaping and exit codes

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M1 implementation review (T032–T035)

## Context

Step 10 replaces the M0 floor (one `--base-url`, deny redirects) with the real outbound policy,
and finishes the two surfaces an operator and a model actually read: what comes back from an API,
and what a failure means.

## Decision

1. **The origin check is not enough, so it runs twice.** `CheckTarget` authorizes a URL against the
   allowlist before anything is attempted. Then, on *every connection*, the dial guard checks the
   address actually being connected to. A name can resolve to anything -- including
   `169.254.169.254`, which on most clouds hands out credentials to whoever asks -- and a name
   checked once can resolve differently a second later. That is DNS rebinding, and the only place
   to catch it is the dial.
2. **Naming a private address is intent; resolving into one is not.** An origin written as
   `http://127.0.0.1:8080` or `localhost` is allowed without ceremony: the operator meant it, and
   refusing would make lotsman useless against a local API while stopping no attack. A *hostname*
   that resolves into a private, loopback, link-local, CGNAT or unique-local range is refused
   unless `allowPrivateNetworks` says otherwise. Naming `[::1]` does not permit `127.0.0.1`: the
   permission is per address, not per "private".
3. **Every phase of a call has its own budget** (FR-32). One total timeout is not enough: a server
   that accepts the connection and then sends a byte a minute stays inside any total while holding
   the call open. Connect, TLS handshake, response header and idle-connection limits are separate,
   and the total still applies.
4. **Retry is absent, not present-and-disabled.** FR-34 permits it only for idempotent operations,
   bounded to transient failures, honouring `Retry-After`, and never for a mutation without an
   idempotency key -- none of which is expressible until the effect model and the config layer
   meet. A field that could be set to true before those rules exist would be a hole with a name.
5. **A response body is always text.** FR-37's example shows a decoded object, and lotsman
   deliberately does not do that: a truncated JSON document handed over as structured content is
   exactly pitfall #11. Text cannot claim to be well-formed. `truncated` and `receivedBytes` say
   what happened, and a cut that lands inside a multi-byte character is trimmed back to a rune
   boundary rather than shipping half of one.
6. **Response headers pass an allowlist** (FR-38), because the set of headers a vendor might use to
   carry something sensitive is open-ended and the set worth forwarding is not: what request this
   was, how much quota is left, when to come back, and what the body claims to be.
7. **Error classes become exit codes at the boundary, not on the class.** `internal/errs` knows
   nothing about processes; `cmd` maps `usage → 2`, `spec_invalid → 3`, `unsupported`/`policy → 4`,
   `auth → 5`, everything else → 1. "Refused by lotsman" and "refused by the operator's policy" are
   the same thing to a CI script: the document is fine, the call is not going to happen.
8. **`explain-call` computes, it does not describe.** Every line it prints comes from the code path
   a real call takes -- the same serializer, the same policy and auth decisions, the same egress
   check -- and then it stops. A re-implementation would be a description of a different call. The
   auth line names the profile and the *reference* (`env:GITLAB_TOKEN`), which is the useful part:
   it says which variable has to be set on the machine that will run this.
9. **`validate` answers with an exit code.** `inspect` explains a document; `validate` gates a
   pipeline, so its output is short and its codes are the contract: 3 for unusable, 4 for valid but
   with nothing publishable, 0 otherwise.
10. **A relative `servers` URL is refused by name** (pitfall #12). `/api/v2` means "wherever this
    document was served from", and a document read off disk was served from nowhere. The message
    says so and names `--base-url`, rather than reporting a generic parse failure.

## Consequences

- `egress.CheckTarget(url, baseURL)` and `egress.Client(base)` are gone; `egress.Policy` carries
  the allowlist, the budgets and the guard. `mcpserver.Options.Egress` defaults to authorizing
  exactly the operator's `--base-url`, which keeps the M0 behaviour as the zero-configuration case.
- A configuration can now name several origins (`execution.allowedOrigins`) and opt into private
  networks for an internal API behind a corporate DNS name.
- The MCP result grew `headers`, `truncated` and `receivedBytes`. Verified live: a 512 KB response
  reports `truncated: true` with `receivedBytes` at the limit, and a 302 comes back with its
  `location` header and without being followed.
