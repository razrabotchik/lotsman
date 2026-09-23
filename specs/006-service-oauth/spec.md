# Feature 006: Service OAuth — client credentials upstream

**Status**: opened at the close of feature 005. This is M4a, *«client credentials:
token/refresh/cache»* (docs/spec.md §12), and its exit criterion is written there too: **e2e с
реальным провайдером**.

It is the first feature that touches the *outgoing* half of authentication. 005 decided who may
talk to lotsman; this decides what lotsman presents when it talks to an API.

## Why this exists

1. **Every credential lotsman can present today is a constant.** `apikey`, `basic` and `bearer`
   read a value from a reference and put it on a request (FR-58). That is the whole of the core's
   auth model, and it excludes every API whose access token is minted rather than issued once —
   which is most APIs an organisation runs itself.
2. **`security` already parses schemes lotsman cannot satisfy.** An OpenAPI document declaring
   `type: oauth2` with a `clientCredentials` flow is understood by the parser and then refused at
   binding, because no configured profile can ever satisfy it. The refusal is honest and it is
   also a wall: an operator holding a perfectly good client id and secret has nowhere to put them.
3. **FR-58 named the deadline and the shape.** *«OAuth2 client credentials (`service` mode) …
   этап M4; их наличие в модели auth заранее учитывается интерфейсом provider»*. The interface was
   accounted for; the provider was not written.
4. **FR-35 has never been exercised.** *«На `401` допускается один refresh+retry только когда auth
   provider может доказуемо обновить token»* — no provider in the build can refresh anything, so
   the rule has been vacuously satisfied since 001. A minted token is the first credential that
   can expire between two calls, which is the case the rule exists for.

## Scope

In: a `oauth2-client-credentials` profile scheme (FR-63) — token URL, client id, client secret as
a reference, scopes and an optional resource/audience; an in-process token cache with one mint per
expiry rather than one per call; a single refresh-and-retry on `401` (FR-35) recorded in the audit
event; and the binding rules of FR-55–57 extended to `oauth2` security schemes so that a document
declaring a client-credentials flow can be satisfied.

Out: delegated OAuth in every part (FR-64, FR-65 — M4b, and it starts only when a real user needs
it). Persistent token stores and cross-replica refresh coordination (FR-66, FR-67) — see open
question 2, because in service mode there is nothing that cannot be re-minted from a secret the
process already holds. Device flow, JWT-bearer and token exchange. Anything about inbound
identity: FR-83 stands, and a validated caller still never becomes the credential a call carries.

## User scenarios

- **US-1 (P1)**: As an operator, I configure a client id and secret for an API that issues its own
  access tokens, and lotsman gets one and uses it. *Acceptance*: a document declaring
  `oauth2`/`clientCredentials` binds to the profile, the first call mints a token, and the
  upstream receives `Authorization: Bearer <minted>`.
- **US-2 (P1)**: As an operator, a hundred calls do not mint a hundred tokens. *Acceptance*: the
  token is reused until it expires; concurrent calls that arrive with no valid token cause exactly
  one mint, not one each.
- **US-3 (P1)**: As a security engineer, the client secret never leaves the process except to the
  token endpoint. *Acceptance*: the canary covers it — it is not in the upstream API request, not
  in a tool result, not in a log, not in a report, not in an audit record.
- **US-4 (P2)**: As an operator, a token that expired early does not turn into a failed call.
  *Acceptance*: an upstream `401` causes exactly one re-mint and one retry; a second `401` is
  returned to the caller as the API's own answer, and the audit event says a refresh happened.
- **US-5 (P2)**: As an operator, a token endpoint that is down fails loudly and locally.
  *Acceptance*: a refusal naming the profile and the token endpoint, classified as a credential
  failure rather than an upstream one, with zero requests to the API itself.

## Requirements carried from the frozen spec

FR-55–57 (security inheritance, AND/OR semantics, no arbitrary choice between satisfiable
alternatives — unchanged, extended to `oauth2`), FR-58 (client credentials is `service` mode and
is M4), FR-59–61 (the client secret is a reference, resolved at use, redacted everywhere), FR-35
(one refresh and retry on 401, only where the provider can provably refresh, recorded in audit
metadata), FR-63 (one credential profile per API or tenant), FR-32 (the token request is subject
to the same timeout budgets as any other), §7.5 (auth is applied again on a permitted retry, and
the retry coordinator may not repeat a mutation because the transport returned a generic error).

## Non-negotiables

- **The token endpoint is egress.** It is an origin lotsman calls, so it is governed by the same
  allowlist, the same dial guard and the same redirect refusal as any API. A credential provider
  is not a hole in the egress floor.
- **A minted token is a secret.** It joins the redaction registry the moment it exists, and it is
  never written to a store, a log or an audit field. The audit record may say that a token was
  refreshed; it may not say what the token is.
- **One retry, and only for a credential that can be re-minted** (FR-35). Not a retry policy, not
  a backoff, not a second opinion about whether the API meant it — one, on 401, when the provider
  can prove it can produce a new token, and never for any other status.
- **A retry re-applies auth from scratch** (§7.5). The retried request carries the new token, not
  a copy of the old request with a header patched.
- **Nothing is minted speculatively.** No token is fetched at startup, because a process that
  cannot reach a token endpoint should still start, serve `inspect`, and refuse the calls that
  need it — not fail to come up.

## Open questions

1. **Which library mints the token?** `golang.org/x/oauth2` is already in `go.sum` — the MCP SDK
   depends on it — and its `clientcredentials` package is exactly this flow, with the token source
   caching and the two client-authentication styles already handled. The alternative is a form
   POST and a JSON unmarshal, perhaps sixty lines, with the subtleties (`expires_in` as a string,
   basic versus body authentication, clock skew) rediscovered one provider at a time.
2. **Does FR-67's token store apply here at all?** In service mode a token is derived from a
   secret the process already holds: persisting it saves one round trip at startup and costs a
   bearer token written somewhere that a client secret is not. Cross-replica refresh coordination
   (FR-66) has a similar shape — client credentials has no refresh token to rotate (RFC 6749
   §4.4.3), so two replicas minting their own is not a race. Both requirements look like they were
   written for delegated mode and inherited by this one.
3. **What binds an `oauth2` security scheme?** A profile satisfies a scheme by name or by an
   explicit `satisfies` list today. An `oauth2` scheme also declares *scopes* per operation, and
   an operation asking for a scope the profile was not configured with is a refusal lotsman could
   make before the call — or one it could leave to the API. The first is stricter and might be
   wrong about a provider that ignores scopes.
4. **Is the token endpoint's origin allowlisted separately from the API's?** They are different
   hosts for most providers. Requiring both in `allowedOrigins` is explicit and slightly tedious;
   deriving the token endpoint's origin from the profile is convenient and quietly widens what the
   process may call.
5. **What does a 401 with no refresh available look like?** An `apikey` profile cannot re-mint, so
   FR-35 says no retry. That is already the behaviour by absence; the question is whether the
   refusal should say "this credential cannot be refreshed" or stay silent, given the answer is
   really the API's own 401 and belongs to the caller.
