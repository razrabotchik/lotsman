# Tasks: Core Runtime (M0–M1)

**Input**: plan.md, spec.md, docs/pipeline.md
**Convention**: [P] = parallelizable (different files, no dependency). Every task leaves main green.
Checkpoints match the tracer-bullet steps; stop at any checkpoint with working software.

## Phase A — M0 spike (gate: ADR-0001…0004)

### Step 0: Skeleton
- [ ] T001 Init module `github.com/razrabotchik/lotsman`; create package dirs with doc.go; Makefile (test/lint/run/fuzz); .golangci.yml
- [ ] T002 [P] CI workflow: lint + test + race on push/PR (.github/workflows/ci.yaml); NOTICE file; SECURITY.md stub

### Step 1: Ping over stdio  ✅ CHECKPOINT: agent calls a tool
- [ ] T003 mcpserver: stdio server with hardcoded `ping` tool (go-sdk v1.7); logs to stderr only
- [ ] T004 Manual e2e: register in Claude Desktop config, verify call; document quirks in docs/adr/0004-mcp-client-notes.md (draft)

### Step 2: Operations enumeration
- [ ] T005 specsource: file/stdin loader with byte/time limits; sha256 digest
- [ ] T006 domain: minimal IR — OperationKey, Method, PathTemplate, Diagnostic, SupportStatus
- [ ] T007 openapi adapter: parse via libopenapi, enumerate paths×methods sorted, param inheritance merge (name,in); emit IR
- [ ] T008 CLI `lotsman operations SPEC`: table output. Test spec: testdata/mini/basic.yaml (5 ops, handwritten)
- [ ] T009 [P] Golden test #1: mini spec → expected IR dump

### Step 3: Static tools  ✅ CHECKPOINT: real spec's GETs visible in Claude
- [ ] T010 catalog: toolName generation (operationId→snake→charset→64, collision hash), deterministic order, digest
- [ ] T011 mcpserver: serve catalog GET ops as tools (no params yet); description sanitize+budget v0

### Step 4: First real call  ✅ CHECKPOINT: agent hits a live API through lotsman
- [ ] T012 requestbuild+response: execute parameterless GET; servers resolution v0 (+--base-url); bounded reader; {status, contentType, body} result; isError on 4xx/5xx
- [ ] T013 Integration test vs httptest; then manual run against a public API

### Step 5: Parameters
- [ ] T014 domain+openapi: params with location/style/explode (per-location defaults!), grouped InputModel
- [ ] T015 Argument validation: santhosh-tekuri/jsonschema v6 on grouped schema, additionalProperties:false at every level; SPIKE: compare with libopenapi-validator → note in ADR-0002
- [ ] T016 requestbuild: path/simple + query/form serialization, scalars+arrays, explode matrix, percent-encoding (pitfall #9), no url.Values.Encode for allowReserved
- [ ] T017 [P] Serialization test table: pitfalls #5, #9; fuzz seed for serializer
- [ ] T018 Golden test #2: mini spec → full tool schemas

### Step 6: POST + security floor  ✅ CHECKPOINT: mutation blocked by default, works when allowed
- [ ] T019 policy: EffectDecision(effect, source, confidence) from method heuristics; read-only default gate BEFORE requestbuild supports POST; suspicious-verb scanner (warning)
- [ ] T020 requestbuild: JSON body; allowMutations config path; header serialization with CRLF defense (pitfall #10)

### Step 7: Inspect + corpus = M0 GATE
- [ ] T021 catalog: capability report (found/supported/partial/rejected + reason codes); determinism test: two runs byte-identical
- [ ] T022 CLI `lotsman inspect SPEC [--json]` (versioned JSON schema v1); vendor corpus (GitLab, DigitalOcean, Kubernetes) with DIGESTS; run, record numbers
      → Write ADR-0001 (libopenapi verdict), ADR-0002 (validator choice), ADR-0003 (IR viability + changes made), ADR-0004 (client notes)
      → GATE: IR held without fundamental rework? proceed : redesign IR now (cheap here)

## Phase B — M1 hardening

### Step 8: Schema normalization for real
- [ ] T023 openapi: OAS 3.0 branch — nullable→type array, bool exclusiveMin/Max→numeric, example→examples, strip OAS-only keys to metadata (pitfall #6)
- [ ] T024 openapi: readOnly excluded from input, writeOnly handling (pitfall #7); empty `security: []` = public (pitfall #4)
- [ ] T025 openapi: local $ref resolution policy — root confinement, cycle cut at depth N → partial + diagnostic (pitfall #8); keep $ref/$defs bundle for validator (no full inline)
- [ ] T026 [P] domain: security OR/AND alternatives (FR-55–57); ambiguous_security → rejected
- [ ] T027 [P] Fuzz: YAML alias bomb budget (pitfall #1), name normalization, path template mismatch (pitfall #3), duplicate operationId (pitfall #2)
- [ ] T028 Corpus golden set: per-corpus expected report counts committed; CI smoke on corpus

### Step 9: Auth
- [ ] T029 config: secretRef type (env:/file:), precedence model; literal-secret rejection in flags
- [ ] T030 auth: apikey(header/query/cookie)/basic/bearer providers as RoundTripper, applied last before wire; auth profile selection per security alternatives
- [ ] T031 slog redaction handler + canary secret test suite (stdout, stderr, results, errors, report — pitfall #14)

### Step 10: Limits & errors
- [ ] T032 requestbuild/egress: full timeout budgets, redirects=deny, retry=off scaffolding per FR-32–34
- [ ] T033 response: truncation never yields broken-JSON-as-JSON (pitfall #11); header allowlist; receivedBytes/truncated fields
- [ ] T034 Distinct error classes + CLI exit codes (FR-77); `lotsman explain-call OP --args FILE` (FR-31)
- [ ] T035 [P] `lotsman validate` command; relative servers handling (pitfall #12: file source requires --base-url)

### Step 11: Package & release  ✅ CHECKPOINT: v0.1.0-alpha public
- [ ] T036 README: 5-minute quickstart (Claude Desktop), honest support matrix table, "lotsman doesn't guess" positioning
- [ ] T037 [P] .goreleaser.yaml: platform matrix, checksums, SBOM; release workflow on tag
- [ ] T038 [P] Benchmarks: parse+normalize on corpus (SC-3), RSS check; record baseline in docs/benchmarks.md
- [ ] T039 e2e suite via official MCP client over stdio: list, GET call, blocked POST, allowed POST
- [ ] T040 Tag v0.1.0-alpha; verify acceptance criteria list in spec.md; open 002-search-mode spec

## Dependencies

T003→T004; T005–T007→T008; T010→T011→T012; T014→T015/T016; T019 BEFORE T020 (security floor);
T021→T022 (gate blocks Phase B); T029→T030→T031; everything → T040.
