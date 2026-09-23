# Tasks: Inbound OAuth (M3 tail)

**Input**: plan.md, spec.md, docs/spec.md §8, FR-79–85
**Convention**: [P] = parallelizable. Every task leaves main green. Checkpoints are points at
which the work could stop and still be worth shipping.
**Dependency decision**: `github.com/golang-jwt/jwt/v5` verifies signatures. It is already in
`go.sum` — the MCP SDK pulls it — so nothing new enters the build and `go.mod` only stops
understating what is already there. The algorithm allowlist stays configuration and stays ours.

## Step 1: Metadata  ✅ CHECKPOINT: a client can find out where to get a token

- [x] T401 config: `inboundAuth.oauth` with `issuer`, `resource`, `audience`, `requiredScopes`,
      `algorithms`, `jwksURI`, `jwksTTL`; strict decoding, and every field that has no safe
      default is required rather than inferred
      → `mode: oauth` stops being a refusal and starts being a configuration. The refusal it
        replaces named feature 005 by number, so the error that replaces *it* must be about the
        configuration, never about the build.
      → An `issuer` that is not an HTTPS URL is refused at load. So is a `resource` that is not
        an absolute URI: it is the audience value a token has to carry, and a typo there is an
        endpoint that refuses every token for a reason nobody can see.
- [x] T402 inbound: the Protected Resource Metadata document (RFC 9728, FR-80), served from
      `/.well-known/oauth-protected-resource` **outside** the guard
      → The one route that will ever live outside it, and it needs a test saying so: the MCP
        endpoint and `/metrics` on the same bind must still refuse without a token.
      → Built with `oauthex.ProtectedResourceMetadata` and served by
        `auth.ProtectedResourceMetadataHandler`; the SDK already implements the CORS rules that
        make a browser-based client able to read it.
      → It skips authentication and *only* authentication: mounted inside the Host and Origin
        checks, not in front of them. The exception is about the loop a token-gated discovery
        document would create, not about the route being special.
      → What it says is thin on purpose — the resource, the authorization servers, the scopes.
        It is public, so it carries nothing an operator would mind a stranger reading, and a test
        asserts the JWKS URI is not in it.
      → The well-known URL is derived from the resource identifier rather than configured: two
        fields that have to agree are two fields that will not.
- [x] T403 [P] challenges name the document (FR-84, RFC 9728 §5.1): a 401 from the MCP endpoint
      carries `resource_metadata="…"` so a client that arrived with nothing learns where to go
      → 004's bare `WWW-Authenticate: Bearer` stays the answer for `static-bearer`, which has no
        metadata to point at. The scheme is the same; what it advertises is not.

## Step 2: Validation  ✅ CHECKPOINT: a real identity provider's token opens the door, and only it

- [x] T404 inbound: the `auth.TokenVerifier` for JWTs — issuer, `exp`/`nbf`, audience/resource
      binding, and the algorithm allowlist, checked before the signature is
      → Claims first, signature second, because a token for a different resource should be
        refused whether or not its signature is good, and the cheap check should not wait behind
        the expensive one.
      → `alg` comes from the operator's allowlist, never from the token header. Trusting the
        header is the oldest way to turn a public key into a shared secret.
      → A token with no `exp` is refused. The SDK's middleware already refuses one, and this
        refuses it before the middleware gets the chance, so the reason is ours to state.
      → "Claims first, signature second" was dropped. `golang-jwt` validates in its own order
        behind one call, and splitting it to save work on a doomed token would mean two places
        that decide whether a token is acceptable — the cheaper one running first and the two
        able to disagree. The saving was hypothetical; the risk was not.
- [x] T405 signature: verification against a key selected by `kid` from the JWKS
- [x] T406 scopes (FR-84): a token missing a required scope is 403, and the challenge names the
      scope without naming anything else
      → The SDK's `RequireBearerTokenOptions.Scopes` already does the comparison and the header.
        What matters here is that the refusal carries no operation, no policy and no hint about
        what a sufficient token would have reached.
- [x] T407 [P] the refusal table: wrong issuer, wrong audience, expired, not-yet-valid, unknown
      `kid`, bad signature, `alg: none`, an algorithm outside the allowlist, no `exp`, missing
      scope — each a row, each asserted against the real middleware with zero RoundTrips upstream
      → Against the middleware rather than a helper: the thing under test is what a request
        actually meets. Authentication code that fails, fails open.
      → Eleven rows, and a twelfth assertion that the four most interesting ones are
        *indistinguishable from outside*: same status, same body, same challenge. A caller who
        could tell "unknown issuer" from "wrong audience" would be reading the configuration one
        guess at a time. The SDK writes the verifier's error message into the 401 body, so every
        refusal returns the bare sentinel and the reason goes to the log.
      → `aud` gets two rows of its own, because a token minted for a different resource of the
        same issuer is where resource servers get this wrong.

## Step 3: JWKS lifecycle  ✅ CHECKPOINT: a key rotation does not need a restart, and an outage does not open the door

- [x] T408 jwks: fetch, parse (RSA and EC), cache with a TTL, select by `kid`
      → Default TTL 15 minutes, written down here and in the ADR rather than left in a constant:
        it is a decision about how long a revoked key keeps working.
- [x] T409 rotation: an unknown `kid` triggers at most one refetch per cooldown window
      → The bound is the point. Without it a forged `kid` turns every request into an outbound
        fetch, and the identity provider finds out about it before we do.
      → The test found a real one on the first run: the cooldown clock was shared with the
        ordinary TTL refresh, so a rotation happening just after a routine refresh went unnoticed
        for a whole window. The two clocks are now separate, and a set fetched during *this*
        call is never refetched again inside it — asking twice could not say anything new.
- [x] T410 [P] fail closed: a fetch that fails leaves the previous key set in force; with no
      previous key set every token is refused
      → "Could not check" is not "fine". This is the same rule as `unknown` effects, arriving in
        a different package.

## Step 4: Opaque tokens  ✅ CHECKPOINT: a provider that issues no JWTs is still usable

- [ ] T411 config + inbound: RFC 7662 introspection — endpoint, client id, client secret through
      the existing secret references, timeout
      → Configured *instead of* a JWKS, not alongside: two ways to validate one token is two
        answers waiting to disagree. The config refuses both at once.
- [ ] T412 introspection: `active: false`, a non-200, a timeout and an unparseable body are all
      refusals; `active: true` carries scopes and subject forward
- [ ] T413 [P] the canary gains the introspection client secret: two credentials in one process,
      neither in any channel

## Step 5: Docs and the criterion  ✅ CHECKPOINT: M3's exit criterion is answered

- [ ] T414 the audit event carries the subject — who asked, never what they hold
      → Recorded, not enforced. Per-subject authorization is RBAC and §8 defers RBAC to the
        gateway layer; an operator who saw `subject` in a rule would reasonably assume the rest.
- [ ] T415 [P] docs: an ADR for the resource-server decisions (including what FR-82 means for
      something that registers no clients), README, and M3's exit criterion in
      docs/release-v0.1.0-alpha.md — met, or told plainly what is still missing
