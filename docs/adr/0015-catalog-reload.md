# ADR-0015: Reload by swapping the server, not by editing it

- **Status**: Accepted
- **Date**: 2026-09-24
- **Decision owner**: feature 004 (M3), tasks T310–T313; FR-72–75, acceptance criterion 9

## Context

FR-72 names its own failure mode: a broken candidate must never destroy a working catalog. §7.6
gives the algorithm — load, validate, build, digest, publish atomically, notify, let in-flight
calls finish on the snapshot they started on — and acceptance criterion 9 is the test.

Until now a catalog was built once per process, which is the right answer for a stdio session
somebody started by hand and the wrong one for a deployment where nobody is watching.

## Decision

1. **A reload is not an edit.** The candidate is built and validated in full beside the catalog
   already serving. Only when it is complete does one pointer move.

2. **What moves is the whole server, not its tool list.** `AddTool` and `RemoveTools` on a live
   server are two critical sections, and a call arriving in the gap between them finds no tool.
   That is a half-applied reload, which is worse than none. Swapping the value a transport
   resolves *per request* has no such gap.

3. **Which decides where reload is available.** The stateless HTTP handler asks for a server on
   every POST; a stdio session resolves one for its whole life and cannot be handed another. So
   reload is a property of the HTTP transport, and `--watch` over stdio is a usage error rather
   than a flag that quietly does nothing.

4. **An identical candidate publishes nothing.** FR-74's cache hints promise that a digest moves
   only when the catalog does, and republishing an identical catalog would break that for no gain.

5. **Triggers are `SIGHUP` and `--watch`, not an endpoint.** A signal costs nothing, has no
   surface, and is what a sidecar or a config-map reloader already sends. A reload endpoint is a
   new authenticated write surface and belongs to the control-plane discussion (M6), not to a
   transport.

6. **Watching polls.** A filesystem notification API would be a dependency (Constitution VII) for
   the job of noticing a file every couple of seconds, and a poll behaves the same through every
   kind of mount — including the container volumes this is actually for. The poll waits for the
   file to stop changing before acting: a document mid-write parses as a broken document, and
   reporting that failure would be reporting something that was never true.

7. **FR-73 is deferred with its reason written down.** `tools/list_changed` presumes a connection
   to push to. The sessionless profile has none between requests, and the transport that does have
   one — stdio — is the one that cannot reload. There is therefore no case in this build where the
   notification is both possible and meaningful, so it is not implemented. FR-74 carries the
   weight instead, and it is two things: `ttlMs` says when to ask again, and the catalog digest in
   `_meta` says whether the answer changed.

## Consequences

- Acceptance criterion 9 is met and asserted end to end: a running server is handed a document
  that is not a document, the failure is reported, and the next call is served by the catalog that
  was already working.
- `internal/mcpserver` needed no change at all. A `Catalog` was already immutable and its runners
  already hold the tools they were built with, so §7.6 step 7 was satisfied before it was asked
  for. The task that planned this expected to edit the wrong package.
- A reload that changes the digest changes what every subsequent `tools/list` reports, including
  the cache hint's identity. Clients that cached on the old digest discover the change on their
  next scrape rather than being told.
- The watcher compares size and modification time. An edit that changes neither is missed; the
  operator who needs to cover that has `SIGHUP`.
