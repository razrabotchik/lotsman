# Feature 004: Production HTTP profile

**Status**: opened at the close of feature 003. This is M3 — *MCP 2026 stateless HTTP, inbound
auth, SSRF policy, Docker, metrics/audit metadata, hot reload subscriptions* (docs/spec.md §12).
The roadmap states its exit criterion in the same line: **≥2 replicas behind round-robin;
conformance and auth/egress security tests green.**

## Why this exists

Everything lotsman does today happens in one process that one person started by hand, next to the
document it serves. Four gaps follow from that, each traceable to something already written down
rather than to an intuition.

1. **There is one transport.** `serve` speaks stdio and only stdio; `--transport` is not a flag.
   FR-69–71 describe a stateless Streamable HTTP profile, §7.7 draws the deployment it enables
   (`clients → LB → lotsman × N`), and nothing implements either. The MCP `2026-07-28` profile has
   no protocol sessions, which is exactly why that deployment needs no sticky routing — an
   advantage that is currently theoretical.
2. **Three acceptance criteria are unmet, and all three are M3's.** Criterion 9 (hot reload does
   not drop a working catalog) is *out of scope*, 10 (conformance) and 11 (container runs nonroot)
   are *partial* — docs/release-v0.1.0-alpha.md. No fourth criterion is outstanding.
3. **`internal/audit` is a package comment and nothing else.** 003 deferred it on purpose
   ("approval logs, it does not audit" — specs/003/plan.md, decision 3), on the stated grounds
   that inventing an event format with one consumer and no reader is an abstraction, and that M3
   is where a sink arrives. FR-35 and FR-39 already say what belongs in the event.
4. **A catalog is built once per process and never again.** FR-72 requires a reload that builds a
   candidate, validates it *completely*, and only then swaps a pointer — the failure mode it names
   is a broken candidate destroying a working catalog, which is a thing that can only happen to a
   server nobody is watching.

One roadmap word is already mostly delivered and should not be re-scoped: **SSRF policy** exists
for *outbound* traffic — `internal/egress` carries the origin allowlist, the dial guard against DNS
rebinding and the redirect denial, with criterion 8 met. What M3 adds is the *inbound* side, which
is a different threat: until now nothing could reach lotsman that was not already on the machine.

## Scope

In: `--transport=http` serving the stateless Streamable HTTP profile (FR-69), bind and header
guards (FR-70, FR-71), inbound authorization (FR-79–85), hot reload with copy-on-write publication
and list-changed notification (FR-72–75), audit events with a stderr sink plus the metrics the HTTP
profile is allowed to carry (§7.4), a distroless nonroot image (criterion 11), and the SDK
conformance suite run against the real binary (criterion 10).

Out: upstream OAuth in either mode (FR-63–67 are M4a/M4b and stay there), recipes (M5), multi-API
namespaces, RBAC and admin (M6), distributed rate limiting and shared token stores (§7.7 names the
four reasons a shared service appears; a service-mode deployment triggers none of them), and remote
spec sources — HTTPS `$ref` and `--spec https://…` need `SpecFetchPolicy` (FR-3, FR-5), which is
its own trust question and not a transport question.

## User scenarios

- **US-1 (P1)**: As an operator, I run `lotsman serve --transport=http SPEC` and an MCP client
  reaches it over Streamable HTTP with the same catalog, the same refusals and the same report as
  over stdio. *Acceptance*: the official Go client drives the real binary over HTTP through the
  same end-to-end assertions that exist for stdio today, including the zero-RoundTrip ones.
- **US-2 (P1)**: As a security engineer, an HTTP endpoint is not reachable by accident.
  *Acceptance*: the default bind is loopback; a non-loopback bind with `inboundAuth: none` is
  refused at startup with an exit code, not a warning; a request with a foreign `Origin` or an
  unexpected `Host` is rejected before any handler runs.
- **US-3 (P1)**: As an operator of a deployed instance, I put a bearer token or an OAuth resource
  server in front of it and unauthenticated calls never reach the catalog. *Acceptance*: a missing
  or invalid token yields 401 with the metadata and scope hints FR-84 requires and nothing about
  internal policy; an inbound token is never forwarded upstream (FR-83), asserted by a canary.
- **US-4 (P1)**: As an operator, I replace the specification under a running server and no client
  sees a broken catalog. *Acceptance*: a candidate that fails to parse leaves the working catalog
  serving, reports the failure, and exits non-zero from nothing; a valid candidate with a different
  digest is published atomically and calls in flight finish against the snapshot they started on.
- **US-5 (P2)**: As an operator, two replicas behind a round-robin load balancer are
  indistinguishable from one. *Acceptance*: an e2e that alternates requests across two processes
  serving the same document completes an initialize/list/call sequence with no sticky routing.
- **US-6 (P2)**: As a security team, every executed call leaves an event I can read, and no event
  contains a secret. *Acceptance*: the canary test grows a fifth channel — the audit sink — and
  the event carries operation key, effect, decision, origin without query, status, timing and
  sizes, exactly as §7.4 lists them.
- **US-7 (P2)**: As an operator, the published container runs as nonroot on a read-only root
  filesystem and its provenance is checkable. *Acceptance*: an image built in CI reports its
  injected version, runs `inspect` as a non-root UID, and ships with the checksums and SBOM the
  release already produces.

## Requirements carried from the frozen spec

FR-68 (stdout stays protocol-only — unchanged, and the reason the HTTP profile must not become an
excuse to log there), FR-69 (stateless/sessionless `2026-07-28`), FR-70 (loopback default;
non-loopback without inbound auth refused absent an explicit dangerous opt-in), FR-71 (Origin, Host
and MCP headers), FR-72 (copy-on-write reload, a failed candidate never destroys the working
catalog), FR-73 (list-changed through the revision's opt-in subscription mechanism), FR-74
(`tools/list` cache hints; the digest changes only on a substantive change), FR-75 (graceful
shutdown drains rather than cuts), FR-79–85 (inbound authorization modes, Protected Resource
Metadata, token validation, no passthrough, challenge hygiene, trusted proxies), §7.4 (metrics only
in the HTTP profile), §7.5 (audit completion is the last stage of the execution pipeline), §7.6
(the reload algorithm, step by step), §7.7 (what may and may not require a shared service).

## Non-negotiables

- **The transport changes nothing about the gate.** Effect policy, rules, overrides and the
  approval prompt are decided before a transport exists and are not re-litigated per protocol. An
  assertion that holds over stdio and not over HTTP is a bug in the server, not a property of HTTP.
- **Inbound identity is not upstream authority.** An inbound token authorizes talking to lotsman.
  It is never forwarded, never reused as an upstream credential, and never widens what the operator
  configured (FR-83). The audiences differ by construction.
- **A failed reload is a non-event for clients.** The working catalog keeps serving, the failure is
  reported, and nothing in flight observes a torn state (FR-72). A reload that can half-apply is
  worse than no reload.
- **Statelessness is a property, not an aspiration.** No request may depend on having been preceded
  by another on the same process. This is what makes §7.7's round-robin true, and it is testable:
  two processes, alternating requests.
- **Determinism survives replication.** Same spec + same config ⇒ same catalog and same digest on
  every replica (Principle IV). A digest that differs per process would make cache hints (FR-74)
  actively wrong.
- **Nothing lands on stdout.** Adding an HTTP transport does not retire FR-68; a single-binary
  server that may still be launched over stdio cannot have a code path that prints.

## Open questions

1. **How much of inbound auth belongs in 004?** `none` and `static-bearer` are a day's work over
   `auth.RequireBearerToken`, which the SDK already ships. Full `oauth` resource-server mode
   (FR-80–82: Protected Resource Metadata, issuer/audience/expiry validation, JWKS or introspection,
   Client ID Metadata Documents) is a feature in its own right. Splitting means shipping a profile
   whose only production-grade auth mode is a shared static secret; not splitting means M3 lands as
   one large step, against the checkpoint convention.
2. **What triggers a reload?** A filesystem watch, a signal (`SIGHUP`), an authenticated admin
   endpoint, or a poll on the source digest. §7.6 specifies the *algorithm* and is silent on the
   trigger. The answer differs by deployment: a watch is right for a config volume, a signal for a
   sidecar, an endpoint for a control plane — and an endpoint is a new authenticated surface.
3. **What does FR-73 mean without sessions?** `notifications/tools/list_changed` presumes something
   to notify. In the stateless profile a server has no connection to push to between requests, so
   either the notification is only meaningful for stdio and legacy connections, or the HTTP profile
   answers the question entirely through FR-74's cache hints. The two readings imply different work.
4. **Do metrics justify a dependency?** §7.4 permits Prometheus/OTel *in the HTTP profile only* and
   the constitution is hostile to dependencies. An expvar or a hand-rolled `/metrics` in the
   Prometheus text format costs nothing and satisfies "aggregated statistics" (U13); a client
   library costs a dependency and buys conventions. The choice should be made once and written down.
5. **Does the audit event get a schema version?** The report has one (`ReportSchemaVersion`) because
   it is a product. An audit event that anything ever parses becomes the same kind of promise, and
   the cheapest moment to decide that is before the first event is written.
6. **Is `--transport=http` allowed to serve no spec?** `serve` today can start without one. Over
   HTTP that means a reachable endpoint with an empty catalog, which is either a useful hot-reload
   starting state or a misconfiguration that should fail loudly.
