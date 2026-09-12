# Tasks: Core Runtime (M0–M1)

**Input**: plan.md, spec.md, docs/pipeline.md
**Convention**: [P] = parallelizable (different files, no dependency). Every task leaves main green.
Checkpoints match the tracer-bullet steps; stop at any checkpoint with working software.

## Phase A — M0 spike (gate: ADR-0001…0005)

### Step 0: Skeleton
- [x] T001 Init module `github.com/razrabotchik/lotsman`; create package dirs with doc.go; Makefile (test/lint/run/fuzz); .golangci.yml
- [x] T002 [P] CI workflow: lint + test + race on push/PR (.github/workflows/ci.yaml); NOTICE file; SECURITY.md stub

### Step 1: Ping over stdio  ✅ CHECKPOINT: agent calls a tool
- [x] T003 mcpserver: stdio server with hardcoded `ping` tool (go-sdk v1.7); logs to stderr only
- [x] T004 Manual e2e: register in Claude Desktop config, verify call; document quirks in docs/adr/0004-mcp-client-notes.md (draft)
      → ADR drafted; stdio e2e automated (cmd/lotsman/e2e_test.go) and `claude mcp list` reports Connected.
      → Desktop confirmed 2026-09-11: `ping` round trip via Claude Desktop returned `pong`, version
        `5d9117d-dirty`, correct echo, and server timestamp `2026-09-11T17:51:12Z`.
      → Remaining: the detailed client-behavior table in ADR-0004 ("Open — to confirm on a desktop
        client") needs more than one tool/larger schemas to exercise — revisit at the M0 gate (T022).

### Step 2: Operations enumeration
- [x] T005 specsource: file/stdin loader with byte/time limits; sha256 digest
- [x] T006 domain: minimal IR — OperationKey, Method, PathTemplate, Diagnostic, SupportStatus
- [x] T007 openapi adapter: parse via libopenapi, enumerate paths×methods sorted, param inheritance merge (name,in); emit IR
      → Found and fixed: libopenapi's default DocumentConfiguration logs errors to
        *stdout* as JSON, which would corrupt the MCP JSON-RPC stream on stdio transport.
        Parse now requires a caller logger (nil falls back to stderr, not upstream's default).
- [x] T008 CLI `lotsman operations SPEC`: table output. Test spec: testdata/mini/basic.yaml (5 ops, handwritten)
      → Found and fixed: `--rejected` placed after SPEC (the exact form documented in
        quickstart.md) was silently ignored — Go's flag.Parse stops at the first
        positional argument. operations now splits flags from positionals before parsing.
- [x] T009 [P] Golden test #1: mini spec → expected IR dump

### Step 3: Static tools  ✅ CHECKPOINT: real spec's GETs visible in Claude
- [x] T010 catalog: toolName generation (operationId→snake→charset→64, collision hash), deterministic order, digest
- [x] T011 mcpserver: serve catalog GET ops as tools (no params yet); description sanitize+budget v0
      → `lotsman serve SPEC` is wired end to end: specsource → openapi → catalog → mcpserver.
        Only GET operations are published (mutation policy gate is T019); each publishes with a
        handler that returns an explicit "not executable yet" error rather than approximating a
        call (T012 wires real execution). Verified against Claude Code via `claude mcp add/list`
        (✔ Connected) in addition to the e2e test.
      → domain.Operation gained Summary/Description (raw, untrusted) so catalog has a text
        source for FR-18/19; golden fixture (T009) updated accordingly.

### Step 4: First real call  ✅ CHECKPOINT: agent hits a live API through lotsman
- [x] T012 requestbuild+response: execute parameterless GET; servers resolution v0 (+--base-url); bounded reader; {status, contentType, body} result; isError on 4xx/5xx
      → domain.Operation/catalog.Tool gained Servers (operation→path→root inheritance, first
        non-empty wins; no server-variable substitution yet). After ADR-0005, mcpserver executes
        only a published GET with `executable=true` and explicit `--base-url`; params/body/auth
        produce blockers and never reach the network until their stages are complete.
- [x] T013 Integration test vs httptest; then manual run against a public API
      → httptest coverage in internal/requestbuild, internal/response, internal/mcpserver
        (success, upstream 4xx/5xx, large-body truncation).
      → Manual run 2026-09-11 against https://httpbin.org through the real stdio MCP transport:
        `getHttpbin` (GET /get) → status 200, real httpbin JSON body, isError=false;
        `getNotFound` (GET /status/404) → status 404, isError=true. Confirms the Step 4
        checkpoint end to end, not just at the package level.

### Step 4a: Pre-execution security correction  ✅ CHECKPOINT: every refusal is zero-network
- [x] T013a domain+catalog+mcpserver: separate support/publication/executability; params/body/auth add machine-readable execution blockers until their stages are complete
- [x] T013b egress: redirects deny and proxy-env inheritance off from the first real call; require explicit `--base-url` before full allowedOrigins policy; validate http(s), host, userinfo, query and fragment without echoing secrets; zero-RoundTrip tests
- [x] T013c strict/lax: document-level errors fail both modes; strict fails on rejected/partial operations, lax serves only supported operations; record decision in ADR-0005
- [x] T013d specsource+openapi: context-bound parse timeout, YAML node/alias budget, max operations, ref depth/document/aggregate-byte limits; source metadata carries root path into ref resolution
      → stage 0 measures alias expansion instead of performing it (memoized, saturating):
        a 9x9 anchor bomb, 400 bytes and far under every byte limit, is refused in
        microseconds. Budgets: nodes, aliases, nesting depth, expanded nodes.
      → Found and fixed: handing libopenapi the document's directory as `BasePath` — the
        obvious way to "carry the root path into ref resolution" — switches its rolodex
        into indexing every YAML/JSON file under that directory, i.e. the arbitrary-file
        read that root confinement exists to prevent. RootPath is carried as lotsman's own
        confinement root instead, with file/remote refs off; see ADR-0006.
      → `$ref` closure audited on the raw node tree before resolution: distinct documents,
        aggregate bytes, longest local chain; cycles cut (T025 owns truncation policy).
        Every ref leaving the root document is refused with `external_ref_unsupported`
        plus pointer and line/column.
      → `openapi.Parse` now takes a context and an Options value; the parse deadline bounds
        waiting, not libopenapi's work (it has no cancellation hook) — the byte/node/ref
        budgets are what keep that work finite.
- [x] T013e introduce stable typed error classes before validation/policy layers; T034 maps them to final CLI exit codes and MCP prefixes
      → internal/errs: internal|usage|spec_invalid|unsupported|policy|auth|upstream. Closed
        set, unexported carrier interface, wrapping preserves the class, outer re-tag wins,
        unclassified reads as `internal` (a missing annotation degrades to "our fault",
        never to "safe").
      → Applied across specsource/openapi/cmd; the class is logged (`class=` on serve,
        `[class]` on operations) but exit codes stay 0/1/2 — T034 owns the
        contracts/cli.md mapping, so that contract breaks once, deliberately.

### Step 5: Parameters  ✅ CHECKPOINT: agent calls a parameterized GET for real
- [x] T014 domain+openapi: params with location/style/explode (per-location defaults!), grouped InputModel
      → domain gained Parameter/InputModel/Schema and the argument-group vocabulary (FR-22);
        style/explode are resolved in the IR from the location-dependent defaults so no
        consumer re-derives them. catalog builds the grouped input schema with
        additionalProperties:false at every level and sanitizes parameter prose (it reaches
        an LLM context exactly like a tool description does).
      → Only scalars and arrays of scalars are accepted; objects, compositions, nested arrays
        and untyped schemas reject the operation (`unsupported_parameter_schema`), as do
        styles outside the matrix (`unsupported_parameter_style`). ADR-0007.
      → The two OAS 3.0 spellings that are not valid 2020-12 (`nullable`, boolean exclusive
        bounds) are translated for the parameter subset now; T023 still owns body/response.
- [x] T015 Argument validation: santhosh-tekuri/jsonschema v6 on grouped schema, additionalProperties:false at every level; SPIKE: compare with libopenapi-validator → note in ADR-0002
      → internal/argvalidate compiles the published schema (re-read through JSON so the
        compiled document is byte-for-byte the published one) and asserts formats.
      → SPIKE result in ADR-0002: libopenapi-validator rejected on three independent grounds
        (validates an already-built request, needs libopenapi types outside the adapter,
        does not validate the grouped argument object).
      → Found: the MCP SDK validates tool arguments itself, against our published schema, and
        usually rejects first. Ours still earns its place — it enforces `format`, which JSON
        Schema treats as an annotation and the SDK's validator accordingly ignores. Pinned by
        TestCallCatalogToolEnforcesFormatsTheSDKTreatsAsAnnotations.
      → A schema that does not compile publishes the tool as not executable rather than
        unvalidated.
- [x] T016 requestbuild: path/simple + query/form serialization, scalars+arrays, explode matrix, percent-encoding (pitfall #9), no url.Values.Encode for allowReserved
      → Found and fixed: the obvious `u.Path = base + expanded` assignment undoes the very
        encoding that keeps a path argument inside its segment — url.URL.Path holds the
        *decoded* form. The escaped path is assembled as a string instead (ADR-0007).
      → allowReserved deliberately keeps `&`, `=` and `#` encoded: it exists so dates and
        paths survive, not so an argument can append parameters to the request.
      → Header/cookie parameters are refused explicitly by the builder, never dropped.
- [x] T017 [P] Serialization test table: pitfalls #5, #9; fuzz seed for serializer
      → 15-row table over the documented rules plus 7 refusal rows; FuzzParameterEncoding pins
        round-trip fidelity and segment containment (6.1M execs clean in 30s).
      → Found and fixed (live run against cmd/api-gateway): the MCP SDK applies JSON Schema
        `default`s to tool arguments before the handler runs, so a call with no arguments
        reached the API as `/pets?sort=name`. Defaults are no longer published as a keyword;
        they are stated in the parameter description instead (FR-24). Pinned by the e2e.
      → Found and fixed (same run): a tool whose parameters are all optional could not be
        called with no arguments at all — absent arguments marshalled to JSON `null`, which
        every object schema rejects. Only the real transport reproduces it, so the guard
        lives in the stdio e2e.
- [x] T018 Golden test #2: mini spec → full tool schemas
      → internal/catalog/testdata/basic-catalog.golden.json: names, descriptions, grouped
        schemas, executability and digest. The fixture spec grew parameters covering the
        implemented matrix (path scalar, query scalar, query array at the default explode,
        enum) without changing its operation count.

### Step 6: POST + security floor  ✅ CHECKPOINT: mutation blocked by default, works when allowed
- [x] T019 policy: EffectDecision(effect, source, confidence) from method heuristics; read-only default gate BEFORE requestbuild supports POST; suspicious mutation-like GET/HEAD/OPTIONS becomes `unknown` until explicit override
      → internal/policy: Decide (method heuristics + suspicious-verb scanner) and the read-only
        gate. POST/PUT/PATCH infer `unknown`, never `write`: the method cannot tell a draft save
        from a payment. An unset effect counts as unknown, so forgetting to classify fails closed.
      → The scanner matches whole tokens, not substrings (`GET /updates` is a listing,
        `GET /cache/refresh` is not), splitting camelCase and punctuation. It never reads the
        summary: untrusted prose must not get a vote in policy. ADR-0008.
      → Publication is not permission: every supported operation is now published with
        annotations derived conservatively from the effect (FR-42), and a policy-blocked call
        refuses before the network naming the code and the remedy. Rejected operations stay
        unpublished.
      → Policy blockers are carried separately from execution blockers, because "not implemented
        yet" and "the operator said no" call for different actions (T021 groups them).
      → The catalog digest depends on the policy config, per data-model invariant 1 (spec+config).
- [x] T020 requestbuild: JSON body; allowMutations config path; header serialization with CRLF defense (pitfall #10)
      → openapi: request body → IR. One media type chosen deterministically (exact
        application/json, else a single +json type; otherwise `unsupported_media_type`, FR-29).
        The body converter handles objects/arrays/compositions with the OAS 3.0 fixes applied at
        every level; conditional subschemas, unconstrained schemas and resolved reference cycles
        are refused (`unsupported_body_schema`).
      → requestbuild: JSON body with Content-Type, plus header serialization. Header values are
        printable US-ASCII only and are **refused, not sanitized** — a header lotsman rewrote is
        not the header the caller asked for (pitfall #10).
      → Found: a spec can declare a header parameter named `Authorization`, `Host` or
        `Content-Type` — handing a model lotsman's own credentials, the transport framing, or a
        contradiction of the body being sent. Protected header names are now rejected at
        translation and refused again in the builder (domain.IsProtectedHeader).
      → `--allow-mutations` / `--read-only` on serve; `lotsman operations` reports the effect and
        applies the same default policy, so its EXECUTABLE column answers "would this run?".
      → `request_body_not_implemented` retired: a JSON body is implemented and any other media
        type is a rejection, not a temporary gap. Cookie parameters still block execution.
      → Known gap left to T024: readOnly properties are still published as inputs.

### Step 7: Inspect + corpus = M0 GATE
- [x] T021 catalog: capability report (found/supported/partial/rejected + published/executable/policy-blocked + reason codes); determinism test: two runs byte-identical
      → catalog.Report accounts for every enumerated operation, not just the published ones, and
        keeps the four outcomes apart: rejected (lotsman will not translate), capabilityBlocked
        (not implemented yet), policyBlocked (the operator said no), executable. Each points at a
        different remedy, so collapsing them would hide which one a reader can act on.
      → Reasons carry code + detail + JSON Pointer; byReason counts them; the catalog estimate
        measures the real tools/list payload, which is what decides tools vs search mode.
      → Determinism pinned twice: byte-identical reports across runs in a unit test, and across
        two `inspect --json` runs over 5.2 MB of Stripe in the corpus test.
- [x] T022 CLI `lotsman inspect SPEC [--json]` (versioned JSON schema v1); vendor corpus (GitLab, DigitalOcean, Kubernetes) with DIGESTS; run, record numbers
      → `inspect` in both forms (human + schemaVersion 1 JSON), exits 0 with rejected operations
        because the report is the product; `--fail-on-rejected` is the CI helper (exit 4);
        `--allow-mutations` previews the other policy. Document-level errors are reported rather
        than fatal — a document `serve` refuses is exactly what `inspect` exists to explain.
      → Corpus pinned in testdata/corpus/MANIFEST.json (immutable refs + sha256, documents not
        committed), fetched by `make corpus`, checked by a skipping smoke test. Numbers recorded
        in docs/corpus.md: Kubernetes apps/v1 65/77 supported, Stripe 0/559 (form bodies +
        deepObject, both documented non-goals), DigitalOcean refused (662-document closure),
        GitLab refused (Swagger 2.0).
      → Found and fixed at the gate: Kubernetes declares DELETE bodies as `*/*` and lotsman
        refused all of them — 27 operations lost to a wildcard that already includes JSON. A sole
        `*/*` or `application/*` now sends JSON; a wildcard beside a concrete type is still
        refused. Supported went 38 → 65.
      → Found and fixed at the gate: GitLab publishes Swagger 2.0, and lotsman passed libopenapi's
        "Try 'BuildV2Model()'" message to the operator. The version is now read from the root
        before the parser sees the bytes (pipeline.md 1.1) and the refusal names Swagger 2.0.
      → Found, recorded for T025: a real exploded spec spans 662 documents; the default
        MaxRefDocuments of 64 is an order of magnitude too low.
      → Measured: 65 Kubernetes tools serialize to 4.26 MB of tools/list, delivered over stdio in
        146 ms. The transport is not the limit — the context window is. Search mode (002) is a
        prerequisite for specs of this shape, not an optimization.
      → ADR-0001 (libopenapi verdict, with the three sharp edges that cost a session each),
        ADR-0002 (validator choice, written with T015), ADR-0003 (IR viability + gate verdict),
        ADR-0004 (client notes: server-side profile accepted; the GUI table stays open and does
        not block the gate, because every assumption there is conservative).
      → **GATE: PASSED.** The IR held without rework. Fields were added during M0 (Input, Effect,
        Servers, ExecutionBlockers) and none had to be reinterpreted or removed; the three-axis
        support/publication/executability model absorbed policy as a fourth axis without touching
        the first three. Every corpus refusal traces to an unwritten feature with an existing
        task, not to a shape the IR cannot express (ADR-0003).

## Phase B — M1 hardening

### Step 8: Schema normalization for real
- [x] T023 openapi: OAS 3.0 branch — nullable→type array, bool exclusiveMin/Max→numeric, example→examples, strip OAS-only keys to metadata (pitfall #6)
      → One normalizer with two targets (parameters, bodies) replaces the two code paths that
        had been growing separate bugs. OAS 3.0's three near-misses are translated: nullable →
        union type, boolean exclusive bounds → numeric keyword, `example` → `examples`.
        OAS-only keys (xml, discriminator, externalDocs) are not copied: they say nothing about
        whether an argument is valid, and a validation schema that carries them invites a client
        to act on them.
- [x] T024 openapi: readOnly excluded from input, writeOnly handling (pitfall #7); empty `security: []` = public (pitfall #4)
      → readOnly properties are dropped from the input schema at every nesting level, and from
        `required` with them — in OAS, required+readOnly means required *in the response*.
        writeOnly survives and is marked. The empty `security: []` case was already covered by
        the security work in T013a; T026 rebuilt it on a real model.
- [x] T025 openapi: local $ref resolution policy — root confinement, cycle cut at depth N → partial + diagnostic (pitfall #8); keep $ref/$defs bundle for validator (no full inline)
      → References are published as references into the tool's `$defs`, verified end to end:
        the SDK resolves them, lotsman's validator compiles the same document, and a recursive
        value is validated at depth. Kubernetes: 4.25 MB → 2.23 MB of tools/list (−47%).
      → The planned "cycle cut at depth N → partial" is obsolete and was not implemented: a
        cycle now closes in $defs exactly as the author wrote it. The cut existed to work around
        inlining, and nothing needs working around (ADR-0009).
      → File references are followed under confinement: each hop resolved relative to the
        document containing it, symlinks expanded, checked to be inside the spec root, walked
        transitively under document and byte budgets. The verified list becomes the parser's
        allowlist, so an unreferenced file next to the spec is never read. Remote refs are still
        never fetched; stdin has no root and so resolves nothing.
      → **DigitalOcean went from "refused before parsing" to 631 of 659 operations in 0.3 s**,
        743 KB catalog. MaxRefDocuments raised 64 → 4,096 from the measured closure (~2,900
        documents), not from intuition.
      → Found and fixed: libopenapi logs one line per unresolvable reference, so one exploded
        spec produced 662 log lines next to our own 662 diagnostics. The parser's log is now
        discarded by default (Options.ParserLog reinstates it) because everything in it reaches
        us as a returned error with better provenance; serve/inspect also cap logged diagnostics.
- [x] T026 [P] domain: security OR/AND alternatives (FR-55–57); ambiguous_security → rejected
      → Alternatives are OR, requirements inside one are AND, each carrying the scheme definition
        it names (type/in/name/http scheme/scopes) so an auth provider can act on it. Inheritance
        replaces rather than merges; an explicit `security: []` is public.
      → An operation whose every alternative needs a credential the core providers cannot supply
        (OAuth2-only, or an undefined scheme) is rejected with `unsupported_security_scheme`
        rather than published as a tool that always fails.
      → `ambiguous_security` is defined in the vocabulary but not yet emitted: ambiguity means
        *several satisfiable alternatives with no policy to choose between them*, and there are
        no auth profiles to be satisfiable against until T029/T030. Noted rather than faked.
- [x] T027 [P] Fuzz: YAML alias bomb budget (pitfall #1), name normalization, path template mismatch (pitfall #3), duplicate operationId (pitfall #2)
      → FuzzLoadBudget (stage 0 refuses or accepts in bounded time, always classified),
        FuzzToolName (portable charset and length for any operationId, deterministic),
        FuzzDuplicateOperationIDs (two operations never collapse into one tool, and the catalog
        does not depend on input order), FuzzPathTemplate (an operation is executable only if
        every placeholder has a required path parameter). All four run in CI at 20s each.
- [x] T028 Corpus golden set: per-corpus expected report counts committed; CI smoke on corpus
      → Expected counts live in testdata/corpus/MANIFEST.json next to the pin, so a change to
        translation support has to answer for the diff. Five entries now, including the exploded
        DigitalOcean checkout pinned by commit (`make corpus` fetches it via a shallow clone).
      → CI gained two jobs: corpus (fetch + smoke) and a short fuzz run.

### Step 9: Auth
- [x] T029 config: secretRef type (env:/file:), precedence model; literal-secret rejection in flags (`keyring:` deferred to 004 portability spike)
      → internal/config: SecretRef (env:/file:), a strictly-decoded configuration file with a
        required apiVersion, and the documented precedence (defaults < file < environment <
        flags) with pointer overrides so "not given" never overrides the file with a zero value.
      → A literal secret is refused at load time and **the refusal does not echo it** — what was
        given may be the secret; the message says how many characters were supplied.
      → Deviation from plan.md: no koanf. The file lotsman needs today is a hundred lines of
        typed YAML decode, and a YAML parser is already a dependency; Constitution VII's bar is
        not met yet (ADR-0010). Direct dependencies stay at four.
      → `lotsman config check|export` (FR-11 CLI list) is not implemented; it belongs with the
        larger config surface.
- [x] T030 auth: apikey(header/query/cookie)/basic/bearer providers as RoundTripper, applied last before wire; auth profile selection per security alternatives
      → Credentials are applied by the *innermost* round tripper: every other layer has already
        seen the request without the credential in it, so a token cannot reach a trace by
        accident. The request is cloned first (the RoundTripper contract, and a credential
        written onto a shared request outlives its call).
      → Secrets resolve per call, not at startup: a rotated token takes effect on the next
        request, and an idle process holds nothing.
      → Selection matches scheme *type* and placement: an API key configured for a header cannot
        satisfy a scheme the API reads from the query string. Ambiguity (FR-57) is now a real
        refusal — and `ambiguous_security` fired on DigitalOcean, which turned out to be the same
        credential written two ways, so identical bindings are collapsed rather than refused.
      → The auth verdict moved from the adapter to the catalog: whether a credential is available
        is a fact about the configuration, not about the document.
      → **DigitalOcean with one bearer profile: 0 → 328 executable reads, 617 of 631 with
        mutations.** The 14 that remain ask for a different scheme (`inference_bearer_auth`) and
        are refused rather than given the wrong token.
- [x] T031 slog redaction handler + canary secret test suite (stdout, stderr, results, errors, report — pitfall #14)
      → internal/redact: a registry of resolved values, an slog handler wrapping the outermost
        layer (messages, attributes, groups, errors), and error redaction that keeps the chain
        intact so errors.Is and the class survive.
      → Found by the canary test, not by reasoning: an upstream that echoes the credential back
        in its response body puts it straight into a model's context. Response bodies, content
        types and handler errors are redacted.
      → The canary suite is end-to-end (a real credential, a real call) across four channels:
        tool result, stderr at debug level, the JSON report, and an error path. Every unit-level
        version of it would have passed while the response body leaked.

### Step 10: Limits & errors
- [x] T032 requestbuild/egress: expand the T013b floor with allowedOrigins/CIDR/DNS-rebinding checks, full timeout budgets and retry=off scaffolding per FR-32–34
      → egress.Policy: an origin allowlist checked before the call, **and a dial guard checked on
        every connection**. A name can resolve to anything, including 169.254.169.254; the only
        place to catch rebinding is the dial.
      → Naming a private address is intent (127.0.0.1, localhost, [::1] pass without ceremony); a
        *hostname* that resolves into a private/loopback/link-local/CGNAT/ULA range is refused
        unless allowPrivateNetworks says otherwise. Naming [::1] does not permit 127.0.0.1.
      → Per-phase budgets (connect, TLS, response header, idle) alongside the total: one timeout
        is not enough against a server that accepts and then dribbles.
      → Retry is **absent, not present-and-disabled**: FR-34's rules are not expressible yet, and
        a field that could be set to true before they exist would be a hole with a name (ADR-0011).
- [x] T033 response: truncation never yields broken-JSON-as-JSON (pitfall #11); header allowlist; receivedBytes/truncated fields
      → The body is always text, deliberately against FR-37's example object: a truncated JSON
        document handed over as structured content is the pitfall itself. `truncated` and
        `receivedBytes` report what happened, and a cut inside a multi-byte character is trimmed
        back to a rune boundary.
      → Header allowlist (FR-38) with the values redacted like everything else. Verified live:
        512 KB response → truncated:true with receivedBytes at the limit; a 302 comes back with
        its location header and is not followed.
- [x] T034 Distinct error classes + CLI exit codes (FR-77); `lotsman explain-call OP --args FILE` (FR-31)
      → The mapping lives at the CLI boundary, not on the class: internal/errs knows nothing about
        processes. usage→2, spec_invalid→3, unsupported/policy→4, auth→5, rest→1.
      → `explain-call` computes rather than describes: every line comes from the path a real call
        takes (same serializer, policy, auth and egress decisions), then it stops. The auth line
        names the profile and the *reference*, never a value — pinned by a canary test.
      → Found and fixed: flag.Parse stops at a leading positional, so `explain-call OP --spec ...`
        silently ignored every flag — the same trap as `operations SPEC --rejected` in T008.
- [x] T035 [P] `lotsman validate` command; relative servers handling (pitfall #12: file source requires --base-url)
      → `validate` answers with an exit code (3 unusable, 4 nothing publishable, 0 otherwise) and
        a `--quiet` for pipelines; `inspect` remains the command that explains.
      → A relative `servers` URL is refused by name: "/api/v2 is relative, and a specification read
        from a file has no origin to resolve it against; pass --base-url".

### Step 11: Package & release  ✅ CHECKPOINT: v0.1.0-alpha public
- [x] T036 README + CLI `--help`: safe 5-minute quickstart (Claude Desktop), honest support matrix table, "lotsman doesn't guess" positioning (FR-76)
      → README leads with the safe default (read-only, one authorized origin, no credentials) and
        makes everything else opt-in; the support matrix names what is refused *and why that is
        the product*, with the corpus numbers behind it. `--help` carries the same quickstart and
        the exit-code contract.
- [x] T037 [P] .goreleaser.yaml: platform matrix, checksums, SBOM; release workflow on tag
      → Static binaries (CGO off, -trimpath), linux/darwin amd64+arm64 and windows/amd64
        (windows/arm64 is deliberately not shipped: untested is worse than absent), sha256
        checksums, an SBOM per archive, release drafted on a tag. Verified with `goreleaser check`
        and a snapshot build whose binary reports the injected version.
      → The release workflow runs the race tests before publishing: a release that was never
        tested is a promise nobody made.
- [x] T038 [P] Benchmarks: parse+normalize on corpus (SC-3), RSS check; record baseline in docs/benchmarks.md
      → NFR-9 met with room: Stripe (5.2 MB, 559 operations) parses and normalizes in **279 ms**
        against a 2 s budget; Kubernetes in 61 ms.
      → **NFR-11 does not hold for exploded specifications**: DigitalOcean peaks at 262 MB RSS
        against a 200 MiB budget, because the ~2,900-document closure is parsed in full before any
        operation is enumerated. Recorded rather than fixed — the remedy is lazy per-operation ref
        resolution, which is a structural change, and an operator deserves the number now.
- [x] T039 e2e suite via official MCP client over stdio: list, GET call, blocked POST, allowed POST
      → The suite is written against the feature's user scenarios rather than against a list of
        verbs: US-1 (a GET with parameters and a JSON POST in one live session, plus the
        repeated-key array default), US-5 (`--lax` serves the subset, strict refuses the document
        and says why), and the exit-code contract. US-2/3/4 are pinned by the inspect, mutation
        and canary suites and are named in the file rather than duplicated.
- [x] T040 Tag v0.1.0-alpha; verify acceptance criteria list in spec.md; open 002-search-mode spec
      → docs/release-v0.1.0-alpha.md reviews all eleven criteria of docs/spec.md §13 against
        evidence: 7 met with the test that would fail if they stopped being true, 3 out of scope
        for an alpha (search mode, interactive approval, hot reload), 2 partial (MCP conformance
        suite not run; no container image). Known limits are stated before someone finds them.
      → specs/002-search-mode/spec.md opened, and it opens with the measurement rather than an
        intuition: 2.23 MB of tools/list for 65 Kubernetes tools is why search mode is the only
        workable mode for that shape, not an optimization of this one.
      → **Tagging is not done here.** The work is uncommitted, and cutting a release is the
        maintainer's call, not the implementation's.

## Dependencies

T003→T004; T005–T007→T008; T010→T011→T012→T013a/T013b/T013c; T013d/T013e BEFORE T014; T014→T015/T016; T019 BEFORE T020 (mutation security floor);
T021→T022 (gate blocks Phase B); T029→T030→T031; everything → T040.
