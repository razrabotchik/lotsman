# ADR-0010: Credentials, references and redaction

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M1 implementation review (T029–T031)

## Context

Step 9 is where lotsman starts holding other people's credentials. Every decision here is about
keeping a secret in as few places, for as little time, and in as few code paths as possible —
because the failure mode is not a bug report, it is a token in someone's log aggregator.

## Decision

1. **A configuration holds references, never values.** `env:NAME` and `file:/path` are the two
   forms; `keyring:` is named in the error message as *not yet* rather than *never*, because those
   are different things to an operator. A literal value is refused at load time and **the refusal
   does not echo what was given**: what was given may be the secret. The message says how many
   characters were supplied, which is enough to recognise a paste without reproducing it.
2. **A reference is resolved per call, inside the provider.** A token rotated in the environment or
   on disk takes effect on the next request, a process that is not calling anything is not holding
   credentials in a long-lived structure, and `config check` works on a machine that has none of
   the secrets.
3. **The credential is applied by the innermost round tripper.** Every other layer — policy,
   egress, logging, whatever arrives later — has already seen and recorded the request *without*
   it. A token cannot appear in a trace by accident, because nothing that traces ever holds one.
   The request is cloned first: the RoundTripper contract forbids modifying the caller's request,
   and a credential written onto a shared request is a credential that outlives its call.
4. **The document decides where a credential goes; the profile decides its value.** An API key
   configured for a header cannot satisfy a scheme the API reads from the query string — sending it
   anyway would deliver the credential to a place nobody is guarding. Scheme *types* must match
   too: bearer credentials never satisfy an API key.
5. **Two satisfiable alternatives with nothing to choose between them is a refusal**
   (`ambiguous_security`, FR-57). But two alternatives that resolve to the *same* credentials are
   not a choice: DigitalOcean writes `bearer_auth: []` and `bearer_auth: [scope]` on one operation,
   and refusing that would be pedantry with an outage attached. Bindings are compared by the
   credentials they present, not by how the document spelled them.
6. **Whether an operation can be authenticated is a fact about the configuration, not the
   document.** The IR states what the document asks for; the catalog — which knows the configured
   profiles — decides whether it can be met. This moved `authentication_not_implemented` out of the
   adapter, where it had been a hardcoded "not yet".
7. **Redaction is the second line, not the first.** A registry holds the values this process has
   resolved (the same values it already holds from the environment), and an slog handler wraps the
   outermost logging layer so that whatever any package logs passes through it. Errors are redacted
   while keeping the chain intact, so `errors.Is` and the error class survive.
8. **Upstream responses are redacted too.** An API is entitled to echo a credential back — some
   return the token they were given, and a 401 body often quotes the header it rejected. A response
   body flows into a model's context, so a value lotsman resolved is removed from it. This was
   found by the canary test, not by reasoning: the first version of that test failed on exactly
   this path.

## Consequences

- `internal/config` parses its own YAML with strict field checking instead of adopting koanf: the
  file lotsman needs today is a hundred lines of typed decode, and the project already depends on
  a YAML parser. Constitution VII's bar — "a reason a few hundred lines of our own code cannot
  provide" — is not met yet. It will be when environment binding and file merging grow.
- Direct dependencies stay at four.
- DigitalOcean with one bearer profile: 0 → **328 executable** reads, and 617 of 631 with mutations
  enabled. The 14 that remain ask for a *different* scheme (`inference_bearer_auth`), and lotsman
  refuses to present the wrong token for them — which is the behaviour, not a gap.
- The canary suite checks four channels for a real credential used in a real call: the tool result,
  stderr at debug level, the JSON report, and an error path. It is an end-to-end test on purpose;
  every unit-level version of it would have passed while the response body leaked.
