# Implementation Plan: Service OAuth (M4a)

**Branch**: `006-service-oauth` | **Spec**: ./spec.md | **Frozen spec**: docs/spec.md §4.9, FR-35, FR-63

## Summary

One new credential provider and one new behaviour. The provider mints an access token from a
client id and secret; the behaviour is that a credential can now *expire*, which is the first time
anything in this runtime has had to cope with a secret going stale between two calls.

The order follows what breaks if it is missing: the binding rules first, because a profile nothing
can bind to is a profile nobody can use; the provider second; the expiry behaviour last, because
it is the only part that changes a call path rather than adding to one.

## Technical Context

- **`golang.org/x/oauth2` is already in `go.sum`**, pulled by the MCP SDK. Its `clientcredentials`
  package is this exact flow, and it already handles the two client-authentication styles and the
  caching token source. Using it promotes an existing module to a direct require — the same
  situation as `golang-jwt` in 005, with the same answer.
- **`internal/auth` already has the seam.** `Credential` carries a `config.Profile` and the
  document's `SecurityRequirement`; `transport.apply` is a switch on the scheme. A minting scheme
  is a new case plus a place to keep the token, not a new architecture.
- **Egress already governs every outbound request**, and the token endpoint is one. The token
  source must be built over a client that goes through `egress.Policy`, not over
  `http.DefaultClient` — otherwise the first thing this feature does is open a hole in the floor
  the last five closed.
- **Testing**: a token endpoint in-process that counts mints, so "one token per expiry, not one
  per call" and "exactly one retry" are counts rather than opinions. Plus the canary, with the
  client secret and the minted token both in one process, and the e2e M4a's exit criterion asks
  for — against a real provider, which in a test means a real OAuth2 token endpoint rather than a
  stub that returns a fixed string.

## Architecture

```text
internal/config      scheme oauth2-client-credentials: tokenURL, clientID,
                     clientSecretRef, scopes, audience, authStyle
internal/auth        oauth2.go: NEW — the minting provider and its token cache
                     select.go: an oauth2 security scheme becomes satisfiable
                     transport.go: the new scheme applies a minted bearer
internal/mcpserver   runner: one refresh-and-retry on 401 (FR-35), recorded
internal/audit       the event says whether a credential was refreshed
```

The refresh-and-retry is the only edit to an existing call path, and it is deliberately in the
runner rather than in the transport: §7.5 puts the retry coordinator above auth application, and a
retry decided inside a `RoundTripper` would be invisible to the audit record that has to mention
it.

## Proposed decisions on the spec's open questions

Question 1 changes what enters the build and should be confirmed; the rest stand as decisions
unless someone objects.

1. **Use `golang.org/x/oauth2/clientcredentials`.** Already in `go.sum`, so nothing new is
   downloaded or audited. The flow is small but its details are not — `expires_in` arriving as a
   JSON string, `client_secret_basic` versus `client_secret_post`, the skew a token source needs
   so a token is not presented in the second it expires. Those are bugs found one provider at a
   time, and this is a library that already found them.
2. **No token store, and no cross-replica coordination — with the reason written down.** In
   service mode a token is derived from a secret the process already holds, so persisting it saves
   one round trip and costs a bearer token at rest. Client credentials has no refresh token to
   rotate (RFC 6749 §4.4.3), so two replicas minting their own is not a race but the normal case.
   FR-66 and FR-67 are met in the only way that makes sense here — an in-memory cache, one mint
   per expiry — and the requirements are recorded as belonging to M4b rather than quietly skipped.
3. **Scopes are configuration, not a per-operation check.** The profile requests the scopes the
   operator configured; an operation that names a scope the profile did not request is *not*
   refused locally. Refusing would be lotsman deciding it knows better than the provider about a
   token it has already been given, and providers differ on whether scopes are even enforced. The
   report says which scopes a profile requests, so the mismatch is visible without being fatal.
4. **The token endpoint's origin must be allowlisted like any other.** Deriving it from the
   profile would mean a configuration file could widen egress by naming a URL, which is exactly
   the property `--base-url` exists to deny the *document*. An operator adds one origin; the
   alternative is a rule with an exception in it.
5. **A 401 that cannot be refreshed is the API's own answer.** It goes to the caller as FR-39
   already requires, unchanged and unexplained: lotsman adding "and this credential cannot be
   refreshed" would be commentary on a message that belongs to the API.

## Checkpoints

1. **Binding.** An `oauth2` security scheme with a `clientCredentials` flow can be satisfied by a
   profile; `inspect` and the report say so. Nothing is minted yet — the operation stops being
   refused for "no compatible credential" and starts being refused for "not implemented", which is
   a smaller lie and a visible step.
2. **Minting.** The provider, over the egress-governed client, with the token cached until it
   expires and one mint under concurrency. A call reaches the API with a minted bearer.
3. **Expiry.** One refresh-and-retry on 401 (FR-35), the audit event saying it happened, and no
   retry for any other status or any other scheme.
4. **Canary and the e2e.** The client secret and the minted token through every channel, and the
   end-to-end M4a's exit criterion names.
5. **Docs.** ADR, README, the support matrix — `oauth2` moves out of the refused column, with the
   `authorizationCode` and `implicit` flows staying in it and saying why.

## Constitution Check

- **I (exactness)**: an operation becomes callable because a credential can now satisfy it, not
  because anything about its translation changed. ✔
- **II (fail closed)**: a token that cannot be minted is a refusal before the API is called; a
  token endpoint outside the egress allowlist is a refusal; a second 401 is not a third attempt. ✔
- **III**: unaffected — a minted credential grants nothing the policy had not already allowed. ✔
- **IV (determinism)**: the catalog and its digest do not depend on whether a token was obtained.
  A profile changes what is *bindable*, which is already reflected in the report. ✔
- **VI (secrets)**: two secrets now exist in one process — the client secret and the token it
  mints — and both join the redaction registry. The canary covers both. ✔
- **VII (dependencies)**: no module enters the build; one already in `go.sum` becomes direct,
  pending confirmation. ✔
- **IX**: no `TokenStore` interface with one in-memory implementation. It arrives with M4b, which
  is the feature that needs more than one. ✔
- **X**: five checkpoints, each a runnable server.

## Risks

1. **A retry is a second request, and a second request to a mutating operation is a second
   mutation.** §7.5 is explicit that a retry coordinator may not repeat a mutation on a generic
   transport error. A 401 is not a generic error — the request provably did not take effect — but
   the distinction has to be in the code and not only in this sentence.
2. **The token endpoint is a new outbound destination and an easy one to forget in the egress
   allowlist.** Mitigated by refusing at the point of use with a message naming both the profile
   and the origin, rather than a generic egress denial the operator has to work backwards from.
3. **A cached token is state, and this codebase has been carefully stateless.** Mitigated by the
   cache being per-profile, in memory, and invisible to the catalog: nothing about what is
   published or reported depends on it.
4. **`expires_in` is a promise, not a fact.** Providers round, clocks drift, and a token can be
   rejected while the cache still believes in it. That is precisely what checkpoint 3 is for, and
   the retry is the mitigation rather than a wider skew.
