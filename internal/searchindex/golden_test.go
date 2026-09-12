package searchindex

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "update golden files")

// goldenQueries exercise the parts of scoring a single query would not: an
// exact name, a partial path, a method as a word, a term in most documents, and
// one that matches nothing.
var goldenQueries = []string{
	"list pets",
	"pet",
	"delete",
	"orders refund",
	"customer",
	"kubernetes",
	"",
}

// TestIndexGolden pins the index and its scores byte for byte (T105). A change
// here is either a deliberate ranking change -- which the recall benchmark then
// has to justify -- or a determinism regression.
func TestIndexGolden(t *testing.T) {
	index := Build(petStore())

	payload := struct {
		Index   Dump                `json:"index"`
		Results map[string][]Result `json:"results"`
	}{
		Index:   index.DumpForTest(),
		Results: map[string][]Result{},
	}
	for _, query := range goldenQueries {
		payload.Results[query] = index.Search(query, Filters{}, 10)
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()

	goldenPath := filepath.Join("testdata", "petstore-index.golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("index does not match %s (run with -update if intentional):\ngot:\n%s", goldenPath, got)
	}
}
