# ADR-0014: The HTTP transport, its guards, and inbound authorization

- **Status**: Accepted
- **Date**: 2026-09-24
- **Decision owner**: feature 004 (M3), tasks T301–T309; FR-69–71, FR-79–85

## Context

Until this feature, nothing could reach lotsman that was not already on the machine. Every
security decision in the codebase was taken under that assumption: the egress policy guards what
lotsman calls, the mutation gate guards what a client may ask for, and the client was, by
construction, a process the same person started.

FR-69 asks for the stateless Streamable HTTP profile of MCP `2026-07-28`, §7.7 draws the
deployment it enables (`clients → LB → lotsman × N`, no sticky routing), and FR-70/71 and §8
describe what has to be true before that is safe.

## Decision

1. **The transport decides nothing.** Both transports are handed the same `mcpserver.Options` and
   serve a server built by the same `mcpserver.New`. There is nowhere to write a behaviour that
   applies to one and not the other, and the end-to-end scenarios run from a table over both —
   including the zero-RoundTrip refusals. A refusal that held over stdio and not over HTTP would
   be a bug in the server rather than a property of the protocol.

2. **The listener and its guards ship together.** `--transport=http` arrived in the same
   checkpoint as the bind rule, the header checks and the drain. An endpoint that becomes
   reachable before it can refuse a stranger is the one mistake this feature could make that the
   rest of the codebase cannot undo.

3. **A public bind that nothing authenticates does not start** (FR-70). Not a warning, not a 403
   on the first call: exit 2, before the socket is opened. A deployment that comes up and then
   denies everything looks like an outage; one that will not come up looks like what it is. The
   dangerous opt-in FR-70 permits is spelled `--allow-unauthenticated-public-bind` and warns on
   every start.

4. **A hostname is public unless it is literally `localhost`.** Resolving names at startup would
   make the safety of a configuration depend on a DNS answer, and the answer can change afterwards
   without the process noticing. The cost is that an operator writes an address; the alternative
   cost is the guard.

5. **Two of the three header guards are somebody else's implementation.** The SDK already rejects
   a request that arrived over loopback carrying a non-loopback `Host`, which is the DNS-rebinding
   case, and `net/http.CrossOriginProtection` already implements cross-origin rejection. A second
   opinion about the same attack is a second thing to keep correct, and the one that drifts is the
   one nobody is looking at. Only the explicit `Host` allowlist — which neither of them offers —
   is ours.

6. **Inbound authorization is split.** `none` and `static-bearer` ship here over the SDK's
   `auth.RequireBearerToken`; the OAuth resource server of FR-80–82 is feature 005. A deployment
   behind an ingress that terminates OAuth is a real deployment and static-bearer serves it
   honestly. `mode: oauth` is therefore refused as a capability this build lacks — exit 4, naming
   the feature — rather than read as `none`: the difference is between an operator who knows their
   endpoint is unauthenticated and one who believes the opposite.

7. **Tokens are compared as digests.** Constant-time comparison of raw values still leaks their
   length, and a length is a useful thing to learn about a secret you are guessing at. Hashing
   both sides costs one SHA-256 and leaks nothing.

8. **A challenge says how, never why.** `WWW-Authenticate: Bearer` and nothing more: no realm
   naming the deployment, no description distinguishing "no token" from "wrong token". A refusal
   that explains itself is a policy oracle for anyone who can reach the port (FR-84). The SDK
   emits no challenge at all without metadata or scopes to advertise, so the bare 401 had to be
   given the scheme it was missing.

9. **Forwarding headers are removed, not ignored** (FR-85). "Ignore them" is a rule every future
   reader of the request has to know and obey; deleting them from an untrusted peer means a later
   feature that reads `X-Forwarded-For` gets the truth without having been warned.

10. **An inbound token is never an upstream credential** (FR-83). The two live in one process and
    both travel in an `Authorization` header, which is exactly why it is a canary test rather than
    a comment.

## Consequences

- The `2026-07-28` sessionless profile is real and now demonstrated: `server/discover` answers
  with `2026-07-28` among its supported versions, and `tools/list` succeeds with no prior
  handshake. Two processes behind an alternating proxy serve one session's worth of traffic.
- A **legacy `initialize` handshake negotiates `2025-11-25`**, and that is correct rather than a
  shortfall: `initialize` is the handshake the new revision removed, so a client using it is by
  definition not speaking `2026-07-28`. `buildinfo.MCPProtocolVersion` is the revision this build
  targets and serves, not the one every client will get.
- Approval survives statelessness unchanged, because 003 never stored anything between round
  trips. That is asserted rather than assumed, and the assertion checks the prompt actually ran.
- M3's exit criterion *auth tests green* is only fully met at the end of feature 005. Said out
  loud here rather than quietly read down to what shipped.
- In a container the documented loopback default means nothing outside can connect. Reaching a
  containerised endpoint is a deliberate `--listen 0.0.0.0:8080`, which then requires inbound
  authentication — the container boundary is not a boundary lotsman can see.
