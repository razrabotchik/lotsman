package searchindex_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/openapi"
	"github.com/razrabotchik/lotsman/internal/searchindex"
)

// NFR-10: search over a thousand operations answers in under 50 ms at the
// 95th percentile, cold build excluded.
//
// It had never been measured. A budget nobody has taken a number against is
// not a budget, and the failure it is meant to catch -- a ranking change that
// quietly makes every query linear in something it was not -- is invisible
// until somebody looks.
const searchBudget = 50 * time.Millisecond

// searchRegressionThreshold is the number that will actually fail.
//
// The measured p95 is around a quarter of a millisecond, so NFR-10's budget
// is roughly two hundred times the truth and would not notice a change that
// made search twenty times slower. This threshold is the measured baseline
// with a wide margin for platform variance (NFR-11's wording asks for exactly
// that), on the same principle as the recall thresholds: a bound nobody can
// meet gets commented out, and a bound nothing can breach is decoration.
const searchRegressionThreshold = 5 * time.Millisecond

// operationsAtScale is the size NFR-10 states. The corpus is smaller than
// that (DigitalOcean publishes 631), so the index is grown to the stated
// scale rather than the budget being measured somewhere easier.
const operationsAtScale = 1000

// TestSearchLatency measures the percentile the requirement names.
func TestSearchLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping the corpus latency measurement")
	}
	index := scaledIndex(t)

	// The queries are the recall tasks plus the shapes that cost the most:
	// a term that matches almost everything, a long query, and an empty one
	// that browses the whole filtered set.
	queries := make([]string, 0, len(digitalOceanTasks)+4)
	for _, item := range digitalOceanTasks {
		queries = append(queries, item.query)
	}
	queries = append(queries,
		"list", // a term half the corpus carries
		"",     // browse: no ranking, every document
		"create a droplet with a volume and a firewall in a region with backups enabled",
		"zzzz-nothing-matches-this-at-all", // the miss path, which still scans
	)

	const repetitions = 40
	timings := make([]time.Duration, 0, len(queries)*repetitions)
	for range repetitions {
		for _, query := range queries {
			started := time.Now()
			results := index.Search(query, searchindex.Filters{}, 10)
			timings = append(timings, time.Since(started))
			// Keep the compiler from deciding the call is dead.
			if len(results) > 10 {
				t.Fatalf("Search returned %d results for a limit of 10", len(results))
			}
		}
	}

	p50, p95, worst := percentiles(timings)
	t.Logf("search over %d operations: p50 %s, p95 %s, max %s (%d samples)",
		operationsAtScale, p50, p95, worst, len(timings))

	if p95 > searchBudget {
		t.Errorf("p95 = %s, want at most %s (NFR-10)", p95, searchBudget)
	}
	if p95 > searchRegressionThreshold {
		t.Errorf("p95 = %s, over the regression threshold of %s: NFR-10 still holds, "+
			"but something has changed by an order of magnitude", p95, searchRegressionThreshold)
	}
}

// TestTagVocabularyLatency: `list_tags` is the other call an agent makes
// before it knows what to search for, and it walks the whole index.
func TestTagVocabularyLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping the corpus latency measurement")
	}
	index := scaledIndex(t)

	timings := make([]time.Duration, 0, 40)
	for range 40 {
		started := time.Now()
		tags := index.Tags()
		timings = append(timings, time.Since(started))
		if len(tags) == 0 {
			t.Fatal("the index has no tag vocabulary, so this measures nothing")
		}
	}
	p50, p95, worst := percentiles(timings)
	t.Logf("list_tags over %d operations: p50 %s, p95 %s, max %s", operationsAtScale, p50, p95, worst)
	if p95 > searchRegressionThreshold {
		t.Errorf("p95 = %s, want at most %s", p95, searchRegressionThreshold)
	}
}

// BenchmarkSearch is the form CI can track for a regression, alongside the
// test that states the budget.
func BenchmarkSearch(b *testing.B) {
	index := scaledIndexFor(b)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		index.Search(digitalOceanTasks[i%len(digitalOceanTasks)].query, searchindex.Filters{}, 10)
	}
}

// scaledIndex builds an index of at least operationsAtScale documents from
// the corpus.
func scaledIndex(t *testing.T) *searchindex.Index {
	t.Helper()
	return scaledIndexFor(t)
}

// scaledIndexFor works for both a test and a benchmark.
func scaledIndexFor(tb testing.TB) *searchindex.Index {
	tb.Helper()
	documents := corpusDocuments(tb)
	if len(documents) == 0 {
		tb.Skip("corpus document not fetched; run `make corpus`")
	}

	// Grown by repeating the corpus under distinct keys. Duplication makes
	// each term's posting list longer, which is the thing search cost scales
	// with -- so measuring here is conservative rather than flattering.
	grown := make([]searchindex.Document, 0, operationsAtScale)
	for round := 0; len(grown) < operationsAtScale; round++ {
		for i := range documents {
			if len(grown) == operationsAtScale {
				break
			}
			copied := documents[i]
			if round > 0 {
				suffix := fmt.Sprintf("-r%d", round)
				copied.Key = domain.OperationKey(string(copied.Key) + suffix)
				copied.ToolName += suffix
			}
			grown = append(grown, copied)
		}
	}
	return searchindex.Build(grown)
}

// corpusDocuments reads the DigitalOcean corpus into index documents, or
// returns nothing when it has not been fetched.
//
// The mapping repeats FromCatalog's rather than exporting an accessor for it:
// a production API added for a benchmark is a production API somebody uses.
func corpusDocuments(tb testing.TB) []searchindex.Document {
	tb.Helper()
	root := filepath.Join("..", "..", "testdata", "corpus")
	path := filepath.Join(root, "digitalocean-exploded", "specification", "DigitalOcean-public.v2.yaml")
	spec, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	doc, err := openapi.Parse(context.Background(), spec, openapi.Options{RootPath: filepath.Dir(path)})
	if err != nil {
		tb.Fatalf("Parse: %v", err)
	}
	cat := catalog.Build("sha256:latency", doc.Operations, catalog.Options{})

	documents := make([]searchindex.Document, 0, len(cat.Tools))
	for i := range cat.Tools {
		tool := &cat.Tools[i]
		documents = append(documents, searchindex.Document{
			Key:      tool.OperationKey,
			ToolName: tool.Name,
			Method:   tool.Method,
			Path:     tool.PathTemplate,
			Summary:  tool.Description,
			Tags:     tool.Tags,
			Effect:   tool.Effect.Effect,
		})
	}
	return documents
}

// percentiles reports p50, p95 and the worst sample.
func percentiles(timings []time.Duration) (p50, p95, worst time.Duration) {
	sorted := append([]time.Duration(nil), timings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(fraction float64) time.Duration {
		index := int(float64(len(sorted)-1) * fraction)
		return sorted[index]
	}
	return at(0.50), at(0.95), sorted[len(sorted)-1]
}
