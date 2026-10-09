# ADR-0019: What the record says when no answer came back

- **Status**: Accepted
- **Date**: 2026-10-09
- **Decision owner**: docs/improvements.md P1 items 3 and 5; FR-34, FR-44, §7.4, §7.5

## Context

The audit event had three decisions — `executed`, `refused`, `input_required` — and every Go error
from the call path that was not an approval became `refused`. That included a transport error after
the request had been written: the API received a mutation, the connection dropped before the
answer, and the record said lotsman had stopped the call. A refusal is a promise that nothing
reached the upstream, and here it was made about a call that may well have taken effect.

The caller was told the same thing in different words. An agent that reads "refused" or a bare
transport error after a `POST` has every reason to try again, and for a mutation without an
idempotency key that is how a payment is made twice. §7.5 already forbids lotsman itself from
repeating such a call; it said nothing about telling the caller not to.

Two neighbouring defects had the same root — the record describing something other than what
happened:

- A credential that could not be applied (an unreadable secret, a failed mint) fails inside the
  round tripper, and `send` re-wrapped every transport error as `upstream`. The record and the
  caller named the API for a problem in the operator's secret.
- The approval prompt and the record's `origin` read the document's first server. With
  `--base-url` pointing elsewhere, a human approved a call to staging, the request went to
  production, and the record said staging. The field's own comment promised scheme, host and port;
  it carried the server URL with its base path.

## Decision

1. **Five decisions, each a statement about the upstream.**

   | decision          | what reached the upstream             | Reason                     |
   |-------------------|---------------------------------------|----------------------------|
   | `executed`        | the request, and an answer came back  | set if the body was lost   |
   | `refused`         | nothing; a control stopped it         | the control's class        |
   | `input_required`  | nothing; waiting for a human          | —                          |
   | `failed`          | nothing; the network failed first     | `upstream`                 |
   | `outcome_unknown` | the request; no answer came back      | `upstream`                 |

2. **"Nothing was sent" needs evidence; "it may have been" does not.** The runner traces the request
   through `net/http`: a transport that asked for a connection and never wrote the request headers
   provably sent nothing. Every other failure — including one from a transport that does not report
   its progress — is `outcome_unknown`. The safe mistake is to doubt a call that did not happen,
   never to deny one that did.

3. **The caller hears it too.** A mutation whose outcome is unknown says that the API may have
   acted on it and to check its state before calling again. A read says only that no response came
   back: repeating a read is what reads are for. A call that `failed` says that nothing reached the
   API.

4. **An answer with a lost body is an execution.** The status arrived, so the API acted and said
   how; the record keeps the status and sets `reason`, and the caller is told the status.

5. **A control keeps its own class through `send`.** The dial guard's denial is `policy`, a
   credential that could not be applied is `auth`, and both are refusals.

6. **The prompt and the record ask the request builder where the call goes.**
   `requestbuild.BaseURL` is the function `Build` uses; the prompt shows that base and the record
   reduces it to scheme, host and port. There is no second derivation to disagree with the first.
   The old reason for showing the document's server — that `--base-url` might still be refused at
   the egress check — stopped applying when the prompt moved after the check.

## Consequences

- `audit.SchemaVersion` is 2. The shape is unchanged; the meaning of `refused` narrowed and two
  values were added, which is a change a reader filtering on `decision` has to know about.
- `lotsman_tool_calls_total` gains the two decisions as label values. `lotsman_refusals_total` now
  counts refusals only, which is what its help text always said.
- Egress denials from `CheckTarget` classify as `policy`. They were unclassified and reported as
  `internal` — lotsman's own fault — in the record and in the error a caller read.
