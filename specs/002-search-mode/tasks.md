# Tasks: Search Mode (M2 slice)

**Input**: plan.md, spec.md, docs/spec.md FR-47–54
**Convention**: [P] = parallelizable. Every task leaves main green. Checkpoints are points at
which the work could stop and still be worth shipping.

## Step 1: Mode selection  ✅ CHECKPOINT: an operator can see and choose the mode

- [x] T101 catalog: `Mode` (tools|search) with selection from the measured serialized estimate;
      `--mode` flag on serve/inspect; the report states the chosen mode and why
      → The report carries both `mode` (in force) and `recommendedMode` (what the measurement
        says), because search mode does not exist yet and hiding that gap would be the actual
        failure. `--mode=search` is refused as a capability rather than silently downgraded:
        exit 4, naming feature 002. A bad mode is exit 2.
      → `serve` warns once at startup when the catalog is over budget, with the numbers.
- [x] T102 [P] Threshold as configuration with a documented default derived from the corpus
      (docs/corpus.md numbers, not intuition); `inspect` shows estimate vs threshold
      → The default is 120 000 bytes — `catalog.maxSerializedBytes` from the frozen spec's own
        example configuration, not a new invention. Against it: the mini fixture fits (2.3 KB),
        DigitalOcean does not (743 KB), Kubernetes does not by a factor of eighteen (2.23 MB).
      → `inspect` prints the estimate, the budget, and by how much a catalog exceeds it.

## Step 2: The index  ✅ CHECKPOINT: search finds the right operation on the corpus

- [x] T103 searchindex: tokenization (identifier/path splitting shared with the effect scanner's
      rules), postings, document lengths; deterministic order everywhere
      → The tokenizer moved to `internal/textnorm`, shared with the effect scanner. Two callers
        needing the same answer must not disagree: an operation whose effect was raised by a verb
        the index cannot find would be a contradiction with a straight face.
      → `domain.Operation.Tags` added (the document's grouping vocabulary, sanitized and bounded
        in the catalog like every other piece of spec-authored text); `list_tags` needs it and so
        do filters.
      → `searchindex.FromCatalog` indexes published tools only: an operation lotsman refused to
        translate is not something an agent should be able to find, because finding it could only
        lead to a refusal.
- [x] T104 searchindex: BM25 scoring with a documented tie-break (score, then operation key)
      → BM25 with the literature defaults (k1=1.2, b=0.75), deliberately untuned, and field
        weights as term-frequency multipliers (name 3, path 2, tag 2, summary 1) — the API's own
        vocabulary says more about what an operation *is* than its prose does. The method is
        indexed as a word, so "delete pet" finds a DELETE whose name lacks the verb.
      → IDF is floored at zero: a term in most documents carries no information, and a negative
        weight would push real matches *down*. Scores are rounded to six places so a golden does
        not depend on float addition order.
      → A query that matches nothing returns nothing. No "closest" operation — that is how an
        agent calls the wrong endpoint confidently.
      → Filters exclude rather than demote: an operation a filter removed cannot be scored back in.
- [x] T105 [P] Golden test: same catalog ⇒ byte-identical index and scores
      → The golden pins postings, document lengths and scores for seven queries, not just a
        result order: two different indexes can agree on one query and disagree on the next.
- [x] T106 Query benchmark with committed expectations: Recall@5 and MRR over hand-written tasks
      for the corpus documents (FR-54), *before* any tuning
      → 22 tasks phrased the way a person types them, not the way the documents spell them.
        Baseline: **Kubernetes Recall@5 1.00 / MRR 0.53**, **DigitalOcean Recall@5 0.75 / MRR
        0.65**. Thresholds are per corpus, set from the measurement minus a margin.
      → The two fail in opposite directions and the numbers say why. Kubernetes: everything is
        findable, little is first, because its operations are near-duplicates (cluster-wide vs
        namespaced vs watch differ by one path segment) — ranking cannot fix an ambiguity that is
        in the question. DigitalOcean: usually first when found, and the three misses are
        **morphology** ("droplet" does not match "droplets") and **intent no word carries**
        ("power off" is a POST to an actions endpoint, and nothing lexical says so).
      → Tuning backlog, with evidence rather than intuition: light stemming for plurals is the
        first candidate, and the benchmark now exists to judge it.

## Step 3: The meta-tools  ✅ CHECKPOINT: an agent uses a 600-operation API through five tools

- [ ] T107 mcpserver: `search_operations` and `list_tags` (no schemas in results)
- [ ] T108 mcpserver: `describe_operation` with a per-response budget; `$defs` by reference
- [ ] T109 mcpserver: `call_read_operation` — re-validates identity, effect, args, auth, policy;
      refuses any effect that is not `read` (FR-49, FR-50)
- [ ] T110 mcpserver: `call_mutating_operation`, published only when mutations are enabled;
      conservative annotations (FR-51)
- [ ] T111 e2e: the same scenarios as tools mode, run in search mode, including the blocked and
      allowed mutation

## Step 4: Honesty at scale

- [ ] T112 Corpus: search-mode catalog size recorded alongside the tools-mode numbers
- [ ] T113 [P] Fuzz: query parsing and filters never panic and never return an operation the
      filters exclude
- [ ] T114 README + `--help`: when to use which mode, with the measured reason

## Dependencies

T101→T102; T103→T104→T105/T106; T101+T104→T107→T108/T109; T109→T110; everything → T111.
