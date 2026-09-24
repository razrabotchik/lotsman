# ADR-0017: Minting upstream tokens with client credentials

- **Status**: Accepted
- **Date**: 2026-09-24
- **Decision owner**: feature 006 (M4a), tasks T501–T512; FR-35, FR-58, FR-63, FR-66, FR-67

## Context

Every credential lotsman could present was a constant: a value read from a reference and put on a
request. That excludes every API whose access token is minted rather than issued once, and it left
an operator holding a perfectly good client id and secret with nowhere to put them — the document
declaring `oauth2`/`clientCredentials` parsed fine and was then refused at binding, honestly and
uselessly.

FR-58 named the deadline and the shape; FR-63 calls this `service` mode. FR-35 has existed since
001 and had never meant anything, because no provider in the build could refresh a token.

## Decision

1. **A library mints the token.** `golang.org/x/oauth2/clientcredentials` was already in `go.sum`
   via the MCP SDK, so this changed one line of `go.mod` and nothing in the build. The flow is
   small and its details are not: `expires_in` arriving as a JSON string, `client_secret_basic`
   versus `client_secret_post`, the skew a token source needs so a token is not presented in the
   second it expires. Those are bugs found one provider at a time.

2. **The parser decides which flows are satisfiable, not the binder.** A requirement now records
   the flows its scheme declares. `clientCredentials` is satisfiable; `authorizationCode` and
   `implicit` are not, because obtaining those needs a browser and a person (FR-64, M4b). Deciding
   it in the parser keeps the reason in the report: the operation is rejected for what the
   document asks, not for what the operator forgot to configure.

3. **The token endpoint is egress.** The token source runs on the same client every API call runs
   on, so the origin allowlist, the dial guard and the redirect refusal apply. A credential
   provider is not a hole in the floor. The operator allowlists it explicitly; deriving it from
   the profile would let a configuration file widen egress by naming a URL, which is exactly what
   `--base-url` exists to deny the *document*.

4. **One token per credential, not per operation.** A single minter, keyed by profile name, shared
   across the catalog: two hundred tools bound to one profile hold one token between them. Twenty
   concurrent calls with nothing cached cost one mint, because oauth2's reusing source serialises
   them behind a single fetch.

5. **Nothing is minted at startup.** A process that cannot reach a token endpoint should still
   come up, still answer `inspect`, and refuse only the calls that actually need a token.

6. **No token store, and no cross-replica refresh coordination.** FR-66 and FR-67 were written for
   delegated mode and inherited by this one. In service mode the token is derived from a secret
   the process already holds, so persisting it saves one round trip and costs a bearer token at
   rest; and client credentials has no refresh token to rotate (RFC 6749 §4.4.3), so two replicas
   minting their own is the normal case rather than a race. Both requirements are met here in the
   only way that makes sense — an in-memory cache, one mint per expiry — and the rest arrives with
   M4b, which is the feature that needs more than one store.

7. **One refresh and one retry, on a 401, for a credential that can be re-minted** (FR-35). A 401
   is not a generic transport error: the API answered, and it answered that the credential was not
   accepted, so the request provably did not take effect. That is the only reason repeating a
   mutation is defensible at all (§7.5). The retry rebuilds the request from scratch — the body of
   a sent request has been read, and an unrepeatable request is not one to repeat. A second 401 is
   the API's own answer and goes to the caller unchanged.

8. **"Can provably refresh" is a property of the scheme.** An API key read from a file is not
   renewed by asking anyone. The runner looks in its own binding for a minting credential and
   finds none, which is why no branch had to be added for the other schemes.

9. **Scopes are configuration, not a per-operation check.** The profile requests what the operator
   configured. An operation declaring a scope the profile did not request is published and
   callable: refusing would be lotsman overruling the provider about a token it has already been
   handed, and providers differ on whether scopes are enforced at all. The report shows both, so
   the mismatch is visible without being fatal.

10. **The token endpoint must be HTTPS, including on loopback.** A client secret posted over plain
    HTTP is a client secret somebody has. Same rule as 005's, for the same reason: a development
    shortcut in a credential path is a production configuration eventually.

## Consequences

- M4a's exit criterion — *e2e с реальным провайдером* — is answered by a token endpoint that
  actually implements the flow: it checks the grant type, accepts either client-authentication
  style, and refuses the wrong secret. Over HTTPS, with the subprocess pointed at the certificate
  through `SSL_CERT_FILE`.
- Two secrets now live in one process — the client secret and the token it mints — and both are in
  the redaction registry. The canary covers both through every channel including the audit record.
- The audit event gained `refreshed`. It says that a credential was re-minted; it does not say
  what the credential is.
- `inspect` gained a `credentials` section. Writing the end-to-end test is what revealed it was
  missing: the report carried it and `inspect` built its own document from selected fields, so the
  section existed and never reached an operator.
- `go.mod` gained one direct require and `go.sum` gained nothing, for the second feature running.
