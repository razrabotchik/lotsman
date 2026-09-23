# Feature 005: Inbound OAuth — lotsman as a resource server

**Status**: opened at the close of feature 004. It is the half of M3's inbound authorization that
004 deliberately split off (specs/004/plan.md decision 1, confirmed by the operator before
tasks.md existed), and it is what closes M3's exit criterion — *conformance and auth/egress
security tests green* — which the release review currently records as only partly met.

## Why this exists

1. **The only production-grade auth mode is a shared secret.** 004 shipped `none` and
   `static-bearer`. Static-bearer is honest for a limited deployment and is exactly what FR-79
   says it is: *«`static-bearer` для ограниченного deployment»*. It has no expiry, no audience, no
   subject and no revocation — one value that everyone who may call the endpoint holds, rotated by
   restarting every client at once.
2. **`mode: oauth` already exists and already refuses.** `config.InboundOAuth` is a named mode
   that fails at load with *"not implemented in this build (feature 005)"* and exit 4. That
   refusal was the right answer to shipping a split; it is not an answer anyone can deploy.
3. **FR-80–82 are written and unimplemented.** Protected Resource Metadata (RFC 9728), issuer /
   expiry / audience / scope / signature validation or introspection for opaque tokens, and the
   client registration discovery path. §8 states the frame the whole feature sits in: *«`lotsman`
   является OAuth resource server, а не authorization server»*.
4. **A deployment with more than one caller cannot distinguish them.** Every inbound-auth
   statement lotsman can make today is "the caller knows the secret". A token carries a subject,
   an audience and scopes, which is the difference between an endpoint that is protected and one
   that can say who asked — the thing M4b's delegated mode will eventually key on (FR-64).

## Scope

In: `inboundAuth.mode: oauth` — Protected Resource Metadata served unauthenticated (FR-80),
validation of issuer, `exp`/`nbf`, audience/resource binding, scopes and signature against a
cached JWKS (FR-81), token introspection (RFC 7662) as the path for opaque tokens (FR-81), and
challenges that name the metadata and the missing scope without describing policy (FR-84). The
`ambiguous_security` rule that inbound identity is never upstream authority (FR-83) is unchanged
and re-asserted against the new mode.

Out: being an authorization server, in any degree — no token endpoint, no authorization endpoint,
no consent. Upstream OAuth in either mode (FR-63–67 are M4a/M4b and stay there). Delegated
identity: a validated subject is recorded and refused as an upstream credential, never exchanged
for one. RBAC and per-subject catalog visibility (M6). DPoP and sender-constrained tokens — the
Go SDK does not implement them (its own conformance baseline excludes `auth/dpop`), so lotsman
claiming them would be a claim about somebody else's code.

## User scenarios

- **US-1 (P1)**: As an operator, I put lotsman behind my existing identity provider and an agent
  reaches it with a token that provider issued. *Acceptance*: a token signed by the configured
  issuer's key, carrying the configured audience and an unexpired `exp`, is admitted; the same
  token with any one of those wrong is refused before the catalog, with zero RoundTrips upstream.
- **US-2 (P1)**: As a security engineer, I can say which scope a caller needs. *Acceptance*: a
  token lacking a required scope is refused with 403 and a challenge naming the scope; the refusal
  says nothing about which operations exist or what policy would have allowed.
- **US-3 (P1)**: As an MCP client author, I discover where to get a token without being told out
  of band. *Acceptance*: `GET /.well-known/oauth-protected-resource` answers **without a token**
  with the resource identifier and the authorization servers; a 401 from the MCP endpoint names
  that document in `WWW-Authenticate` (RFC 9728 §5.1).
- **US-4 (P2)**: As an operator whose provider issues opaque tokens, I configure introspection
  instead of a JWKS. *Acceptance*: an active token is admitted, an inactive one refused, and the
  introspection credential never appears in any channel the canary test covers.
- **US-5 (P2)**: As an operator, a provider that is unreachable does not turn into an open door.
  *Acceptance*: a JWKS fetch that fails leaves the last good key set in force if it has one and
  refuses if it does not — never "could not check, therefore fine".

## Requirements carried from the frozen spec

FR-79 (`oauth` mode for production), FR-80 (publish Protected Resource Metadata naming an external
authorization server), FR-81 (issuer, expiry/not-before, audience/resource binding, scopes, and
signature — or introspection for opaque tokens), FR-82 (Client ID Metadata Documents are the
preferred discovery/registration path; pre-registration supported; DCR is a compatibility
fallback), FR-83 (no passthrough; inbound and upstream audiences differ), FR-84 (challenges carry
metadata and scope hints without leaking internal policy), FR-85 (trusted proxies — unchanged),
§8 (lotsman is a resource server, not an authorization server; RBAC waits for the gateway layer).

## Non-negotiables

- **Unverifiable is refused.** A signature that cannot be checked, an issuer that cannot be
  reached for the first time, an algorithm outside the allowlist, a token with no `exp` — each is
  a refusal. "We could not tell" and "it is fine" are different answers, and the gate has said so
  since 001.
- **The metadata document is public; everything else is not.** A client needs
  `/.well-known/oauth-protected-resource` *before* it has a token, so that one route sits outside
  the guard — and it is the only one that ever will. `/metrics` stays inside.
- **`alg` comes from the operator, never from the token.** An allowlist of signing algorithms is
  configuration. Trusting the header's `alg` is the oldest way to turn a public key into a shared
  secret.
- **Inbound identity is still not upstream authority** (FR-83). A subject and its scopes may
  decide whether a call is allowed; they may never *become* the credential the call carries.
- **The refusal says how, not why** (FR-84). A challenge names the scheme, the metadata URL and a
  missing scope. It never distinguishes "unknown issuer" from "wrong audience" to a caller who has
  neither.

## Open questions

1. **Which library verifies the signature?** `github.com/golang-jwt/jwt/v5` is already in
   `go.sum` — the MCP SDK depends on it — so using it promotes an existing module to a direct
   require rather than adding one to the build. The alternative is parsing and verifying JWS by
   hand against `crypto/rsa` and `crypto/ecdsa`: no dependency at all, and roughly 250 lines of
   the exact code where `alg: none` and algorithm-confusion bugs live. Constitution VII is about
   not dragging in frameworks; it is not obviously about reimplementing a signature format.
2. **Is introspection in this feature or the next?** It is a separate transport (an authenticated
   POST per request, with its own credential, timeout, cache and failure mode) serving a different
   token shape. Shipping it alongside JWT validation doubles the feature; deferring it means
   `mode: oauth` covers only providers that issue JWTs, which is most of them but not all.
3. **What does FR-82 mean for a resource server?** Client ID Metadata Documents and Dynamic Client
   Registration are things an *authorization server* offers to clients. lotsman registers nobody.
   The honest reading is that FR-82 constrains what its metadata advertises and what M4b will do
   as a client — in which case 005 implements the advertisement and says so, rather than leaving a
   requirement looking unmet.
4. **Does a validated subject reach the policy layer?** FR-41's rules match on namespace,
   operation, tag and effect — not on who is asking. Wiring the subject into `policy.Subject` now
   would be the first step of RBAC, which §8 explicitly defers to the gateway. Recording it in the
   audit event is not.
5. **How long may a JWKS be cached, and what happens at a rotation?** Too short and every key
   rotation becomes a fetch storm; too long and a revoked key keeps working. The answer is a TTL
   plus a bounded refetch on an unknown `kid`, and the bound is what stops an attacker with a
   forged `kid` from driving unlimited fetches.
