# Implementation Plan: Inbound OAuth (M3 tail)

**Branch**: `005-inbound-oauth` | **Spec**: ./spec.md | **Frozen spec**: docs/spec.md §8, FR-79–85

## Summary

005 turns "the caller knows the secret" into "the caller holds a token some identity provider
issued, for this resource, that has not expired, carrying these scopes". Everything else in the
runtime stays where it is: the mutation gate, the rules and the approval prompt were decided
before anyone knew who was asking, and knowing does not move them.

The order is the order a client meets them in. The metadata document first, because it is what a
client reads before it has anything to present and because it is the one route that lives outside
the guard. Then validation, which is the feature. Then the alternative token shape, which is a
different transport wearing the same interface.

## Technical Context

- **The SDK supplies the frame, not the check.** `auth.RequireBearerToken` already extracts the
  bearer, enforces scopes and expiry and builds the challenge; `auth.ProtectedResourceMetadataHandler`
  and `oauthex.ProtectedResourceMetadata` already serve RFC 9728; `auth.GetAuthServerMetadata`
  already does RFC 8414 discovery. What none of them supply is the `auth.TokenVerifier` itself —
  that function is ours, and it is where this feature lives.
- **Nothing new enters the build.** `github.com/golang-jwt/jwt/v5 v5.3.1` is in `go.sum` today,
  pulled by the MCP SDK; using it promotes an existing module to a direct require. JWKS fetching
  and caching are not in it and are ours: JSON, `math/big`, `crypto/rsa`, `crypto/ecdsa`.
- **004 left the shape.** `inbound.Guard` is already the type that says whether authentication is
  required, `config.InboundOAuth` is already a named mode that refuses, and the bind rule already
  reads `Guard.Required`. Adding a mode is filling in a branch that exists.
- **Testing**: an in-test authorization server — an RSA key pair, a JWKS endpoint, a token
  factory — so every refusal (wrong issuer, wrong audience, expired, unknown `kid`, bad
  signature, missing scope, `alg: none`) is a table row against the real middleware. Plus the
  canary, over HTTP, with two credentials in one process.

## Architecture

```text
internal/config         inboundAuth.oauth: issuer, audience, requiredScopes, algorithms,
                        jwksURI/jwksTTL, introspection{url, clientID, clientSecretRef}
internal/inbound        oauth.go:         the TokenVerifier — claims, then signature
                        jwks.go:   NEW:   fetch, cache, select by kid, bounded refetch
                        introspect.go: NEW: RFC 7662 for opaque tokens
                        metadata.go:   NEW: the PRM document and its route
internal/httpserver     one route outside the guard; everything else unchanged
internal/audit          the event gains the subject — who asked, never what they hold
```

The only structural change outside `internal/inbound` is the metadata route, and it is a single
exception with a single reason: a client needs that document before it has a token.

## Proposed decisions on the spec's open questions

Question 1 changes what enters the build and should be confirmed before tasks.md exists. The rest
stand as decisions unless someone objects.

1. **Use `golang-jwt/jwt/v5`.** It is already in `go.sum`, so nothing new is downloaded, built or
   audited — the dependency is a fact of the build today and this only makes it honest in
   `go.mod`. The alternative is hand-written JWS verification, and Constitution VII's target is
   frameworks that make a codebase someone else's, not the refusal to reimplement a signature
   format whose known failure modes (`alg: none`, RSA-key-as-HMAC-secret) are a library's job to
   have already got right. The allowlist of algorithms is still ours and still configuration.
2. **Introspection ships here, in its own checkpoint.** Deferring it would leave `mode: oauth`
   meaning "JWT only" while the requirement that named it says otherwise, and the checkpoint
   convention already handles "this is a lot": it goes last, and the feature is shippable without
   it.
3. **FR-82 is an advertisement, not a registration.** lotsman registers no clients, because it is
   not an authorization server (§8). What it can do is name the authorization servers in its
   metadata and let a client take the Client ID Metadata Document path with *them*. 005 implements
   the advertisement and records this reading in the ADR, so the requirement stops looking unmet
   for the wrong reason.
4. **The subject is recorded, not enforced.** It goes into the audit event, and nowhere near
   `policy.Subject`. Per-subject authorization is RBAC, §8 defers RBAC to the gateway layer, and a
   half-built version of it is worse than none: an operator who saw `subject` in a rule would
   reasonably assume the rest.
5. **JWKS: a TTL, and one bounded refetch on an unknown `kid`.** Default TTL 15 minutes. An
   unknown `kid` triggers at most one refetch per cooldown window, which is what stops a forged
   `kid` from turning every request into an outbound fetch. A fetch that fails leaves the previous
   key set in force; if there is no previous key set, every token is refused.

## Checkpoints

1. **Metadata.** `/.well-known/oauth-protected-resource` served unauthenticated, the config that
   describes it, and 401s from the MCP endpoint naming it. A client can discover where to get a
   token before anything validates one.
2. **JWT validation.** `mode: oauth` with a JWKS: issuer, audience, `exp`/`nbf`, algorithm
   allowlist, signature, scopes. The bind rule sees `Guard.Required`, so a public bind is legal
   again — this is the checkpoint that makes M3's deployment real.
3. **JWKS lifecycle.** Cache, TTL, rotation, bounded refetch, and the fail-closed behaviour when
   the provider is unreachable.
4. **Introspection.** RFC 7662 for opaque tokens, its credential through the same secret
   references as everything else, and the canary extended to it.
5. **Docs and the criterion.** ADR, README, and M3's exit criterion in the release review moved
   from "only partly met" to met — or told plainly what is still missing.

## Constitution Check

- **I (exactness)**: nothing here touches translation. A token decides whether a caller reaches
  the catalog; it cannot change what the catalog contains. ✔
- **II (fail closed)**: every branch refuses — unreachable provider with no cached keys, unknown
  `kid`, algorithm outside the allowlist, absent `exp`, failed introspection. ✔
- **III (approval is not a boundary)**: unchanged. A validated subject does not skip approval, and
  approval does not authenticate. ✔
- **VI (secrets)**: the introspection client secret and any inbound token join the redaction
  registry and the canary gains a channel. A token is never logged, only its subject. ✔
- **VII (dependencies)**: no module enters the build; one already in `go.sum` becomes direct,
  pending the operator's confirmation. ✔
- **IX (no premature abstraction)**: no "identity provider" interface with one implementation —
  `auth.TokenVerifier` is the seam and the SDK already defined it. ✔
- **X (main stays green)**: five checkpoints, each a runnable server.

## Risks

1. **This is authentication code, and authentication code fails open when it fails.** Mitigated by
   making every negative a table row against the real middleware rather than a unit test of a
   helper: the thing under test is what a request actually meets.
2. **A cached JWKS is a decision about how long a revoked key keeps working.** Mitigated by a
   documented TTL, a bounded refetch on unknown `kid`, and saying the number out loud in the ADR
   rather than leaving it in a constant.
3. **The metadata route is the first hole in the guard, and holes grow.** Mitigated by it being
   one exact path, registered in one place, asserted by a test that the MCP endpoint and
   `/metrics` on the same bind still refuse without a token.
4. **`aud` is where resource servers get this wrong.** A token minted for a different resource of
   the same issuer must not work here. That is its own table row, not an afterthought inside
   "validation works".
