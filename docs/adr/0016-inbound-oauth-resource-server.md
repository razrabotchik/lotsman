# ADR-0016: lotsman as an OAuth resource server

- **Status**: Accepted
- **Date**: 2026-09-24
- **Decision owner**: feature 005 (M3 tail), tasks T401–T415; FR-79–85, §8

## Context

004 shipped two inbound modes: `none`, and a shared secret. FR-79 calls the second one what it is
— *«`static-bearer` для ограниченного deployment»* — and it has no expiry, no audience, no subject
and no revocation. `mode: oauth` existed as a named mode that refused to start, which was the
right answer to shipping a split feature and not an answer anyone can deploy.

§8 fixes the frame: *«`lotsman` является OAuth resource server, а не authorization server»*.
Everything below follows from that sentence.

## Decision

1. **A library verifies the signature.** `github.com/golang-jwt/jwt/v5` was already in `go.sum` —
   the MCP SDK depends on it — so this changed one line of `go.mod` and nothing in the build.
   Constitution VII targets frameworks that make a codebase somebody else's; it is not a reason to
   reimplement a signature format whose famous failure modes (`alg: none`, an RSA public key
   presented as an HMAC secret) are exactly what a library has already got right.

2. **The algorithm allowlist is configuration, and asymmetric only.** `jwt.WithValidMethods` is
   the defence that matters: without it a token nominates its own algorithm. A configuration
   naming `HS256` or `none` is refused at load, because a resource server that accepts a symmetric
   algorithm accepts a token signed with the key it verifies with.

3. **Every refusal is the same refusal from outside.** The SDK writes the verifier's error message
   into the 401 body, so the verifier returns the bare sentinel and the reason goes to the log. A
   caller who could tell "unknown issuer" from "wrong audience" would be reading the configuration
   one guess at a time (FR-84). Four spoiled tokens produce one byte-identical response, and a
   test says so.

4. **The metadata document is the one route outside the guard.** A client reads
   `/.well-known/oauth-protected-resource` in order to find out how to authenticate, so requiring
   authentication to read it is a loop with no way in. It skips authentication and *only*
   authentication — it is mounted inside the Host and Origin checks, not in front of them — and
   its content is thin on purpose: the resource, the authorization servers, the scopes. Nothing an
   operator would mind a stranger reading; in particular, not the JWKS URI.

5. **One way to validate a token, never two.** `jwksURI` and `introspection.url` are refused
   together. Two ways to decide whether a token is good are two answers waiting to disagree, and
   the disagreement would be settled by whichever code path happened to run first.

6. **Introspection checks `iss` and `aud` itself.** RFC 7662 §2.2 says the authorization server
   *may* return them. "May" is not a validation: anything present is checked, and an absent `aud`
   is a refusal — reading the omission as "for me" would accept every token that server ever
   issued, for any of its resources.

7. **A cached key set has a stated lifetime.** 15 minutes by default, which is a decision about
   how long a revoked key keeps working and therefore belongs in a named constant and in this
   document rather than inside a function. An unknown `kid` provokes at most one refetch per
   minute: without that bound a forged `kid` turns every request into an outbound fetch, and the
   identity provider finds out about it before we do. The cooldown does not share a clock with the
   ordinary TTL refresh — it did at first, and a rotation happening just after a routine refresh
   went unnoticed for a whole window.

8. **A provider outage does not open the door and does not close it either.** A refresh that fails
   with a key set already in force keeps serving it: an identity provider having a bad day is not
   a reason to stop accepting tokens it already signed. With nothing cached, every token is
   refused. "Could not check" is not "it is fine", which is the rule the effect gate has followed
   since 001.

9. **The subject is recorded, never enforced.** It goes into the audit event and nowhere near
   `policy.Subject`. Matching policy on a caller is RBAC, §8 defers RBAC to the gateway layer, and
   a half-built RBAC is worse than none — an operator who saw `subject` in a rule would reasonably
   assume the rest of it exists.

10. **FR-82 is an advertisement, not a registration.** Client ID Metadata Documents and Dynamic
    Client Registration are things an authorization server offers to clients. lotsman registers
    nobody. What it can do, and does, is name the authorization servers in its metadata so a
    client takes that path with *them*. The requirement is met in the only sense it can be by
    something that is not an authorization server; it is recorded here so it stops looking unmet
    for the wrong reason.

## Consequences

- M3's exit criterion — *conformance and auth/egress security tests green* — is answered. Inbound
  authorization now has a production mode, and the refusal table is asserted against the real
  middleware rather than against a helper.
- A deployment can distinguish its callers for the first time. Nothing acts on that yet, by
  decision 9, but the record exists — which is what M4b's delegated mode will eventually key on.
- `go.mod` gained one line and `go.sum` gained nothing. The dependency was always in the build;
  it had merely been understating itself.
- An operator with a private certificate authority points the process at it the ordinary way
  (`SSL_CERT_FILE`), which is also how the end-to-end test does it: a real HTTPS provider, a real
  key set, a real token.
- HTTP is refused for every OAuth URL, including loopback. A development shortcut in an
  authentication path is a production configuration eventually.
