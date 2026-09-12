# ADR-0007: Parameter translation and serialization

- **Status**: Accepted
- **Date**: 2026-09-12
- **Decision owner**: M0 implementation review (T014, T016)

## Context

Step 5 turns declared OAS parameters into published tool arguments and then into bytes on the
wire. Both halves are places where "almost right" is indistinguishable from wrong: a query array
serialized at the wrong explode default silently queries the wrong thing, and a path argument
that keeps its slashes silently changes which resource is addressed.

## Decision

1. **Only scalars and arrays of scalars become parameters.** An object, a composition
   (`allOf`/`oneOf`/`anyOf`/`not`/`if`), a nested array or a schema with no declared type rejects
   the operation with `unsupported_parameter_schema`. A path or query value carries text; a shape
   that has no single honest text encoding must not be guessed at.
2. **Style and explode are always resolved in the IR**, from the location-dependent defaults
   (path/header → simple, false; query/cookie → form, **true**). No consumer re-derives them.
   Any style outside the supported matrix rejects the operation with `unsupported_parameter_style`
   rather than falling back to one that "looks close".
3. **Header and cookie parameters translate and publish but block execution.** They are in the M1
   matrix, but their serialization -- including the CRLF defence -- lands with T020, and the
   request builder refuses them explicitly instead of dropping them.
4. **Path values are percent-encoded down to RFC 3986 unreserved characters**, and the escaped
   path is assembled as a string. `url.URL.Path` holds the *decoded* form, so assigning to it
   would undo exactly the encoding that keeps `../` and `/` inside a single path segment
   (pitfall #9). A fuzz target pins both invariants: the value round-trips, and it never escapes
   its segment.
5. **The query string is built by hand.** `url.Values.Encode` sorts keys, always percent-encodes
   and cannot express a non-exploded array, so it is the wrong tool three times over (FR-26).
   Query order follows the IR's deterministic parameter order.
6. **`allowReserved` keeps reserved characters literal except `&`, `=` and `#`.** This is a
   deliberate deviation from a literal reading of RFC 3986's reserved set. `allowReserved` exists
   so values like dates and paths survive intact; leaving the query's own separators literal would
   let an argument append parameters to the request, which no spec author enables on purpose.
7. **Schema `default` is never published as a keyword.** The MCP SDK fills missing arguments
   from a schema's `default` before the handler runs, so publishing one would put a query
   parameter on the wire that the model never sent -- the exact silent default FR-24 forbids.
   The default is stated in the parameter's description instead ("The API uses "name" when this
   is omitted."), so the model keeps the information and nothing acts on it behind lotsman's
   back. Found by running the real binary against the development API fixture, where a call with
   no arguments arrived upstream as `/pets?sort=name`.
8. **Numbers render as JSON meant them.** Arguments arrive as `float64`, so an integral value is
   written `42`, never `42.0`; `null` is refused outright, because a URL has no representation for
   it and an empty value would be a guess.

## Consequences

- Operations whose parameters lotsman cannot serialize exactly are rejected by name and show up in
  the capability report, which is the intended outcome, not a gap.
- `requestbuild.Build` now takes the operation's parameters and the validated grouped arguments;
  the old parameterless signature is gone.
- The OAS 3.0 spellings that would emit invalid 2020-12 (`nullable`, boolean exclusive bounds) are
  translated for the parameter subset now; T023 does the same for body and response schemas.
- `parameters_not_implemented` no longer blocks an operation whose parameters are all path/query
  (ADR-0005's condition -- validation and serialization tests passing -- is met).
