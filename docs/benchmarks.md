# Benchmarks

Baseline recorded at the v0.1.0-alpha preparation (T038). Reproduce with:

```bash
make corpus   # fetch the vendor documents (pinned, see testdata/corpus/MANIFEST.json)
make bench
```

Machine: linux/amd64, 32 logical CPUs, Go 1.25. These are *this machine's* numbers; CI runners
differ by a factor of two or three, which is why the budgets below are stated with headroom and
why a regression threshold belongs in CI rather than in a README.

## Parse and normalize (NFR-9: 5 MiB / 1000 operations, p95 < 2 s)

| Document | Size | Operations | Parse + normalize | Throughput | Allocations |
|---|---|---|---|---|---|
| Stripe | 5.2 MB | 559 | **279 ms** | 18.7 MB/s | 210 MB, 3.4 M allocs |
| Kubernetes `apps/v1` | 833 KB | 77 | **61 ms** | 13.8 MB/s | 138 MB, 0.67 M allocs |
| mini fixture | 1.5 KB | 5 | 0.5 ms | — | 0.37 MB, 4.8 K allocs |

The budget is met with room to spare: the document the NFR was written for takes 279 ms against a
2 s ceiling. Allocation volume is the number to watch rather than wall time — 210 MB allocated to
parse a 5 MB document is the parser's cost of keeping every YAML node addressable for diagnostics,
and it is what drives the memory figures below.

## Catalog derivation

| Document | Tools | Build | Published `tools/list` |
|---|---|---|---|
| Kubernetes `apps/v1` | 65 | 60 ms | 2.23 MB |
| Stripe | 0 (all refused) | 0.2 ms | — |

Derivation is measured separately from parsing because the two scale with different things:
parsing with the document's size, derivation with the number of operations and the size of their
schemas.

## End-to-end `inspect` (process wall time and peak RSS)

| Document | Wall | Peak RSS |
|---|---|---|
| Kubernetes `apps/v1` (833 KB) | 0.15 s | 84 MB |
| Stripe (5.2 MB) | 0.40 s | 147 MB |
| DigitalOcean, exploded (~2,900 documents, 14 MB) | 0.39 s | **262 MB** |

**NFR-11 (core RSS < 200 MiB after loading a reference specification) holds for single-document
specifications and does not hold for the exploded case.** DigitalOcean's closure is parsed and
indexed in full before a single operation is enumerated, and 262 MB is the honest cost of that.

This is recorded rather than fixed. The remedy is not a tuning knob: it is parsing referenced
documents lazily, on the path from an operation to the schemas it actually uses, which changes how
the adapter is structured. Until then, an operator serving a large exploded specification should
expect a quarter of a gigabyte of resident memory, and the number is here so the decision is
theirs rather than a surprise.

## What is not benchmarked yet

- Search mode (NFR-10) does not exist; it arrives with feature 002.
- Per-call latency is dominated by the upstream API and is not a useful lotsman metric until the
  runtime does something expensive per call. Validation and serialization are microseconds against
  a network round trip.
