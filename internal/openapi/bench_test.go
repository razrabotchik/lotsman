package openapi

import (
	"os"
	"path/filepath"
	"testing"
)

// corpusDocument loads a vendor corpus document, skipping when the corpus has
// not been fetched (`make corpus`).
func corpusDocument(b *testing.B, name string) []byte {
	b.Helper()
	path := filepath.Join("..", "..", "testdata", "corpus", name)
	data, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("corpus document not fetched; run `make corpus`")
	}
	return data
}

// BenchmarkParseCorpus measures NFR-9: parse plus normalize on a real
// specification. The 5 MB Stripe document is the one the budget was written
// for (5 MiB / 1000 operations, p95 under 2 s).
func BenchmarkParseCorpus(b *testing.B) {
	for _, name := range []string{"stripe.yaml", "kubernetes-apps-v1.json"} {
		b.Run(name, func(b *testing.B) {
			spec := corpusDocument(b, name)
			b.SetBytes(int64(len(spec)))
			b.ReportAllocs()
			b.ResetTimer()

			for b.Loop() {
				doc, err := Parse(b.Context(), spec, Options{})
				if err != nil {
					b.Fatalf("Parse: %v", err)
				}
				if len(doc.Operations) == 0 {
					b.Fatal("no operations parsed")
				}
			}
		})
	}
}

// BenchmarkParseMiniSpec is the floor: what the pipeline costs when the
// document is not the bottleneck.
func BenchmarkParseMiniSpec(b *testing.B) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mini", "basic.yaml"))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(b.Context(), spec, Options{}); err != nil {
			b.Fatalf("Parse: %v", err)
		}
	}
}
