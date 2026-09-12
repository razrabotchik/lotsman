package catalog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/openapi"
)

// BenchmarkBuildCorpus measures catalog derivation -- tool names, grouped
// schemas, the report -- separately from parsing, because they scale with
// different things: parsing with the document's size, this with the number of
// operations and the size of their schemas.
func BenchmarkBuildCorpus(b *testing.B) {
	for _, name := range []string{"kubernetes-apps-v1.json", "stripe.yaml"} {
		b.Run(name, func(b *testing.B) {
			path := filepath.Join("..", "..", "testdata", "corpus", name)
			spec, err := os.ReadFile(path)
			if err != nil {
				b.Skipf("corpus document not fetched; run `make corpus`")
			}
			doc, err := openapi.Parse(b.Context(), spec, openapi.Options{})
			if err != nil {
				b.Fatalf("Parse: %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()

			var built catalog.Catalog
			for b.Loop() {
				built = catalog.Build("sha256:bench", doc.Operations, catalog.Options{})
			}
			b.ReportMetric(float64(built.Report.Estimate.SerializedBytes), "catalog_bytes")
			b.ReportMetric(float64(len(built.Tools)), "tools")
		})
	}
}
