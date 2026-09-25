# Benchmarks

Baseline recorded at the v0.1.0-alpha preparation (T038) and re-measured since, most recently after
the diagnostic and reporting fixes. Reproduce with:

```bash
make corpus   # fetch the vendor documents (pinned, see testdata/corpus/MANIFEST.json)
make bench
```

Machine: linux/amd64, 32 logical CPUs, Go 1.27. These are *this machine's* numbers; CI runners
differ by a factor of two or three, which is why the budgets below are stated with headroom and
why a regression threshold belongs in CI rather than in a README.

## Parse and normalize (NFR-9: 5 MiB / 1000 operations, p95 < 2 s)

| Document | Size | Operations | Parse + normalize | Throughput | Allocations |
|---|---|---|---|---|---|
| Stripe | 5.2 MB | 559 | **239 ms** | 21.9 MB/s | 199 MB, 3.3 M allocs |
| Kubernetes `apps/v1` | 833 KB | 77 | **48 ms** | 17.2 MB/s | 137 MB, 0.66 M allocs |
| mini fixture | 1.5 KB | 5 | 0.3 ms | — | 0.32 MB, 4.7 K allocs |

The budget is met with room to spare: the document the NFR was written for takes 239 ms against a
2 s ceiling. Allocation volume is the number to watch rather than wall time — 199 MB allocated to
parse a 5 MB document is the parser's cost of keeping every YAML node addressable for diagnostics,
and it is what drives the memory figures below.

## Catalog derivation

| Document | Tools | Build | Published `tools/list` |
|---|---|---|---|
| Kubernetes `apps/v1` | 65 | 54 ms | 2.23 MB |
| Stripe | 0 (all refused) | 0.13 ms | — |

Derivation is measured separately from parsing because the two scale with different things:
parsing with the document's size, derivation with the number of operations and the size of their
schemas.

## Search latency (NFR-10: p95 < 50 ms over 1000 operations, cold build excluded)

| Call | Operations | p50 | p95 | max |
|---|---|---|---|---|
| `search_operations` | 1,000 | 86 µs | **257 µs** | 359 µs |
| `list_tags` | 1,000 | 40 ns | 451 ns | 691 ns |

Measured by `TestSearchLatency` over the DigitalOcean corpus grown to the size the requirement
states — the corpus publishes 631 operations, so the index is repeated under distinct keys until
it reaches a thousand. Duplication lengthens every term's posting list, which is what search cost
scales with, so this is a conservative measurement rather than a flattering one. The query set is
the recall tasks plus the shapes that cost most: a term half the corpus carries, an empty query
that browses everything, a long query, and one that matches nothing but still scans.

**The budget is met by a factor of about two hundred.** That is worth stating plainly, because it
means NFR-10 cannot catch a regression: search could become twenty times slower and still pass.
The test therefore asserts a second threshold of 5 ms — the measured baseline with wide margin for
platform variance — so that an order-of-magnitude change fails somewhere rather than nowhere.

`BenchmarkSearch` reports the same thing in the form CI can track: 86 µs/op.

## End-to-end `inspect` and NFR-11

Re-measured after the `$ref` resolution cache (below). Peak RSS varies by a few percent between
runs; the figures are the median of three.

| Document | Wall | Peak RSS |
|---|---|---|
| Kubernetes `apps/v1` (833 KB) | 0.14 s | 87 MB |
| Stripe (5.2 MB) | 0.36 s | 146 MB |
| DigitalOcean, exploded (~2,900 documents, 14 MB) | 0.31 s | **263 MB** |

**NFR-11 still does not hold for the exploded case**, and the remedy is still the structural one
described below rather than a knob. Two things were tried and are recorded so nobody tries them
again expecting more:

- **The Go runtime's own limit.** `GOMEMLIMIT=180MiB` brings peak RSS to 215 MB — still over the
  200 MiB budget — and doubles wall time to 0.80 s. `GOGC=50` gives 250 MB for a small slowdown.
  Neither reaches the budget, because the peak is driven by allocation churn against a live heap
  that is already 103 MB: the runtime cannot collect what is still in use.
- **Skipping the index's diagnostic metadata.** libopenapi collects descriptions, summaries,
  enums and JSONPath values for tools that lint documents; lotsman reads the high-level model and
  never touches an index accessor. `SkipMetadataCollection` takes 5-7 MB off each document in the
  corpus — 269 MB to 265 MB on the exploded one — with every report byte-identical across all five
  corpus documents. Free, and not nearly enough.
- **A `$ref` resolution cache**, which *was* worth doing on its own merits. Confinement resolves
  symlinks on every reference, and an exploded specification asks the same question thousands of
  times over paths that share their leading components. Caching the answer for one closure walk
  cut wall time from 0.39 s to 0.32 s (~18%) and peak RSS by about 8 MB, with the report
  byte-identical across 2,900 documents. It does not change the confinement decision: identical
  input, identical answer.

The live heap after parsing the exploded corpus is 103 MB, against 448 MB allocated in total. The
allocation is where the peak comes from, and it is libopenapi's: `lookupRolodex` and
`ExtractComponentsFromRefs` account for roughly two thirds of what is still resident. Nothing in
lotsman's own code is a meaningful share of it.

### The remedy is larger than this document used to say

The earlier wording was "parsing referenced documents lazily … which changes how the adapter is
structured". Looking for the lever showed that understated it.

libopenapi has one option for not resolving external references —
`SkipExternalRefResolution` — and its own documentation says what it costs: schema proxies keep
the reference string and `Schema()` returns **nil**. lotsman derives its IR from those schemas, so
with that option there is nothing to derive from. There is no setting that resolves a reference
the first time an operation needs it.

So the remedy is not "restructure the adapter" but "stop using libopenapi's reference resolution
and do it here" — resolving `$ref` against the closure lotsman already walks, on demand, and
handing libopenapi one document at a time. That is a parser-level change with its own correctness
surface (circularity, pointer semantics, `$ref` siblings), and it is not something to start
because a benchmark is 25% over.

The alternative is to say NFR-11 does not apply to exploded specifications and amend it, which is
a decision about the requirement rather than about the code. Either way it is a decision, not a
task, and it is recorded here as one.

Until then, an operator serving a large exploded specification should expect a quarter of a
gigabyte of resident memory, and the number is here so the decision is theirs rather than a
surprise.

## What is not benchmarked yet

- Per-call latency is dominated by the upstream API and is not a useful lotsman metric until the
  runtime does something expensive per call. Validation and serialization are microseconds against
  a network round trip.
