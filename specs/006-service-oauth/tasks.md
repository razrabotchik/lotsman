# Tasks: Service OAuth (M4a)

**Input**: plan.md, spec.md, docs/spec.md §4.9, FR-35, FR-63
**Convention**: [P] = parallelizable. Every task leaves main green. Checkpoints are points at
which the work could stop and still be worth shipping.
**Dependency decision**: `golang.org/x/oauth2/clientcredentials` mints the token. The module is
already in `go.sum` — the MCP SDK pulls it — so nothing new enters the build, on the same
precedent as `golang-jwt` in 005.

## Step 1: Binding  ✅ CHECKPOINT: an oauth2 document stops being refused for the wrong reason

- [ ] T501 config: the `oauth2-client-credentials` scheme — `tokenURL`, `clientID`,
      `clientSecretRef`, `scopes`, `audience`, `authStyle`; strict decoding and no inferred fields
      → `tokenURL` must be https, like every other authentication URL: a development shortcut in
        a credential path is a production configuration eventually (the rule 005 set).
      → `authStyle` is `basic` or `body` with `auto` the default, because providers disagree and
        the disagreement is not something an operator should have to discover from a 401.
- [ ] T502 auth/select: an `oauth2` security scheme with a `clientCredentials` flow is satisfiable
      by such a profile (FR-55–57 unchanged around it)
      → The AND/OR semantics do not move. What changes is that one more scheme type has a
        compatible profile kind, so `ambiguous_security` and `no_compatible_credential` keep
        meaning exactly what they meant.
      → Scopes are configuration, not a per-operation check (plan decision 3). An operation naming
        a scope the profile did not request is published and callable; the report shows both, so
        the mismatch is visible without lotsman overruling the provider.
- [ ] T503 [P] the report and `inspect` say a profile mints rather than presents, and which scopes
      it requests
      → The report is the product (FR-12a). "This operation is satisfied by a credential that will
        be fetched" is a different fact from "satisfied by one you configured", and an operator
        reading the report before serving should see which.

## Step 2: Minting  ✅ CHECKPOINT: a call reaches an API with a token lotsman obtained

- [ ] T504 auth/oauth2: the provider — mint over the egress-governed client, cache until expiry,
      one mint under concurrency
      → Over `egress.Policy`, not `http.DefaultClient`. The token endpoint is an origin lotsman
        calls, so the allowlist, the dial guard and the redirect refusal apply. A credential
        provider is not a hole in the floor the last five features closed.
      → Nothing is minted at startup. A process that cannot reach a token endpoint should still
        come up, still answer `inspect`, and refuse the calls that need it.
- [ ] T505 transport: the new scheme applies the minted bearer; the minted token joins the
      redaction registry the moment it exists
- [ ] T506 [P] a token endpoint that is unreachable, slow or refusing is a credential failure
      naming the profile and the origin — with zero requests to the API itself
      → Exit code 5 and `ClassAuth`, not `ClassUpstream`: the API did not fail, and an operator
        reading a failure should be sent to the right system.

## Step 3: Expiry  ✅ CHECKPOINT: FR-35, exercised for the first time

- [ ] T507 runner: one refresh-and-retry on `401`, only for a credential that can be re-minted
      → In the runner, not in a `RoundTripper`: §7.5 puts the retry coordinator above auth
        application, and a retry decided inside a transport would be invisible to the audit record
        that has to mention it.
      → A 401 is not a generic transport error — the request provably did not take effect — which
        is why this is allowed for a mutation at all. That distinction lives in the code, not only
        in this note.
      → The retried request is rebuilt and re-authenticated from scratch, never the old request
        with a header patched.
- [ ] T508 audit: the event says a credential was refreshed (FR-35's *audit metadata*) — that it
      happened, never what the token is
- [ ] T509 [P] no retry for any other status, any other scheme, or a second 401
      → A second 401 is the API's own answer and goes to the caller as FR-39 requires. lotsman
        adding "and this credential cannot be refreshed" would be commentary on a message that
        belongs to the API.

## Step 4: Secrets and the criterion  ✅ CHECKPOINT: M4a's exit criterion

- [ ] T510 the canary covers both secrets at once: the client secret and the token it mints,
      through every channel the existing test checks plus the audit record
- [ ] T511 [P] end to end against a real OAuth2 token endpoint — the exit criterion M4a states
      → "Real" means a token endpoint that actually implements the flow, not a stub returning a
        fixed string: the mint is a form POST whose client authentication style, `expires_in` and
        error shape are the parts that go wrong.

## Step 5: Docs  ✅ CHECKPOINT: the support matrix stops being out of date

- [ ] T512 [P] docs: an ADR, README, and the support matrix — `oauth2` client credentials moves
      out of the refused column, with `authorizationCode` and `implicit` staying in it and saying
      why (M4b, and it starts only when a real user needs it)
