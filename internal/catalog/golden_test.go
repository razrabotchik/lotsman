package catalog_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/openapi"
)

// update regenerates the golden catalog from the current output:
// `go test ./internal/catalog/... -run Golden -update`.
var update = flag.Bool("update", false, "update golden files")

// TestCatalogMiniSpecGolden is golden test #2 (T018): it pins the full
// published tool surface -- names, descriptions, grouped input schemas,
// executability and the catalog digest -- for the shared fixture spec.
//
// Golden #1 (internal/openapi) pins what lotsman understood about the
// document; this one pins what an MCP client is actually shown, which is the
// artefact a model reasons over and therefore the one that must not drift
// silently.
func TestCatalogMiniSpecGolden(t *testing.T) {
	cat := buildMiniCatalog(t)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cat); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()

	goldenPath := filepath.Join("testdata", "basic-catalog.golden.json")
	if *update {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("catalog does not match %s (run with -update to refresh it if intentional):\ngot:\n%s\nwant:\n%s",
			goldenPath, got, want)
	}
}

// TestCatalogMiniSpecDeterministic checks the invariant the golden file
// cannot: that two builds of the same spec agree with each other, not merely
// with a file someone regenerated.
func TestCatalogMiniSpecDeterministic(t *testing.T) {
	first := buildMiniCatalog(t)
	second := buildMiniCatalog(t)

	if first.Digest != second.Digest {
		t.Fatalf("digest differs across builds: %q != %q", first.Digest, second.Digest)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Error("two builds of the same spec produced different catalogs")
	}
}

func buildMiniCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	spec, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mini", "basic.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := openapi.Parse(t.Context(), spec, openapi.Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// A fixed digest keeps the golden about the catalog, not about the
	// fixture's bytes.
	return catalog.Build("sha256:fixture", doc.Operations, catalog.Options{})
}
