# Tasks: Selection, overrides and interactive approval (M2 tail)

**Input**: plan.md, spec.md, docs/spec.md FR-41, FR-44–46, §4.7, §5.1–5.2
**Convention**: [P] = parallelizable. Every task leaves main green. Checkpoints are points at
which the work could stop and still be worth shipping.

## Step 1: Overrides  ✅ CHECKPOINT: an operator can correct an effect, disable an operation and pin a credential

- [x] T201 config: `operationOverrides` with `match: {operationId | method+path}` and
      `effect`, `enabled`, `authProfile`; strict decoding; a match on nothing is an error, not a
      shrug (a typo'd override is a security control that silently did not apply)
      → Half a coordinate is refused too: `match: {method: GET}` without a path would match by
        accident. So is an override that states nothing, and one naming an authProfile that is
        not configured.
      → `enabled` is `*bool`: "not stated" and "stated false" are different, and the whole
        precedence model rests on being able to tell them apart.
- [x] T202 catalog: apply overrides before tools are built — effect becomes
      `source=local_override, confidence=explicit`; `enabled: false` removes the operation from
      publication and records it in the report; `authProfile` pins the binding
      → The overlay runs on a copy, so `Build` never mutates the caller's IR and a catalog built
        twice from one document is the same catalog.
      → An override cannot publish an operation lotsman refused to translate. It classifies,
        hides or binds; support is not a matter of opinion.
      → The scanner's warnings survive an override: "this GET is named rebuild" stays worth
        reading after someone decided it is a read anyway.
- [x] T203 [P] report + `explain-call`: override provenance is visible for every affected operation
      → `Totals.Excluded` is its own number, and every excluded operation keeps its line in the
        report with `disabled_by_override` or `excluded_by_selection`.
      → Pinning `authProfile` turned up a real gap: `auth.bind` took the *first* compatible
        profile when two satisfied one scheme — the arbitrary choice FR-57 forbids, deterministic
        only because the index is sorted. Two now refuse as `ambiguous_security`, and the refusal
        names them and says how to choose (ADR-0012 §9).

## Step 2: Selection  ✅ CHECKPOINT: a 631-tool catalog can be narrowed to the tags in use

- [x] T204 config + catalog: `catalog.includeTags` filters publication; excluded operations are
      accounted for in the report with a reason code, never silently absent
      → Matching is against the *sanitized* tags, the vocabulary the report and `list_tags` show.
        A filter written against the raw document would be one nobody could read off lotsman's
        own output.
      → An untagged operation is out when a filter is configured: the operator named a surface,
        and "untagged" is not on it.
- [x] T205 [P] the search index and `list_tags` see exactly what was published — a filtered-out
      operation must not be findable, since finding it could only lead to a refusal
      → Free by construction (`FromCatalog` indexes published tools) and asserted anyway, tag
        vocabulary included: a `list_tags` entry no published operation carries is a dead end.

## Step 3: Mutation rules  ✅ CHECKPOINT: enabling mutations no longer means enabling all of them

- [x] T206 policy: `Subject` (namespace, key, tags, effect) and `Rules` (allow/deny);
      deny wins; a non-empty allow list refuses the unmatched; `Evaluate` takes the subject
      → Two lists rather than one ordered one: first-match-wins would make the safety of a
        configuration depend on the order somebody pasted it in.
      → Deny applies to reads too. FR-41 gates mutations with the allow list, but hiding an
        endpoint must not require enabling mutations first — making things worse to make them
        better is not a workflow.
- [x] T207 config + wiring: `execution.allowRules` / `denyRules`; new reason codes; every caller
      of `Evaluate` updated; `explain-call` names the rule that decided
      → An empty rule is refused at load time rather than interpreted as "everything".
      → `explain-call` gained an `approval` line as well: whether a human will be asked is a fact
        an operator needs before the call, not after it.
- [x] T208 [P] zero-RoundTrip tests in both modes: a denied tag, an unmatched allow list
      → A denied operation stays discoverable — `describe_operation` still answers. A deny rule
        says "you may not call this", not "this does not exist".

## Step 4: Interactive approval  ✅ CHECKPOINT: acceptance criterion 6

- [x] T209 policy: `interactiveApproval` mode (always | client-capability | never, default always)
      and the rule for when a call requires approval — mutations only; reads never prompt
      → The default is applied in `config.Resolve`, so no downstream caller has to decide what an
        unset mode means, and a zero `mcpserver.Options` asks rather than not asking.
- [x] T210 mcpserver: elicitation prompt with the FR-46 payload (title, effect, origin, path
      template, redacted argument summary); accept executes, decline/cancel/timeout refuse
      → `ServerSession.Elicit` inside a handler is refused by the SDK on protocol 2026-07-28:
        the answer is a multi-round-trip input request (SEP-2322), which is what FR-44 said all
        along. There is therefore no wait to bound and no timeout to configure: the call returns
        input-required, and the client makes it again with the answer (ADR-0013).
      → Approval runs *last*, after validation, request build and the egress check: a prompt is
        only ever shown for a call that would otherwise happen.
      → Path and query values are shown (approving the deletion of "a droplet" is not a
        decision); header and cookie names without values (that is where a credential travels);
        the body by field names only.
- [x] T211 fail-closed: `always` + no client elicitation capability = refusal before the network,
      asserted end to end with zero RoundTrips in tools mode and in search mode
      → And once against the real binary over stdio, which is what criterion 6 is worth citing:
        `TestServeMutationPolicyEndToEnd/fails_closed_when_the_client_cannot_be_asked`.
      → `--approval` mirrors the config setting for an operator who wants to state it on the
        command line, and the same e2e proves the call goes through with `--approval never`.
- [x] T212 [P] docs: ADR-0012 (config shapes for selection, overrides and rules), ADR-0013
      (approval over elicitation, and why it is not a boundary), README, and criterion 6 in
      docs/release-v0.1.0-alpha.md
