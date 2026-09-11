package openapi

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// update regenerates testdata/basic.golden.json from the current Parse
// output: `go test ./internal/openapi/... -run Golden -update`.
var update = flag.Bool("update", false, "update golden files")

// TestParseMiniSpecGolden pins the IR produced for the shared fixture spec
// (testdata/mini/basic.yaml, T008) byte for byte. Any change here is either
// a deliberate IR/normalization change (re-run with -update) or a
// determinism regression (data-model.md invariant 1).
func TestParseMiniSpecGolden(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mini", "basic.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	doc, err := Parse(spec, "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Diagnostics) != 0 {
		t.Fatalf("unexpected document diagnostics: %+v", doc.Diagnostics)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()

	goldenPath := filepath.Join("testdata", "basic.golden.json")
	if *update {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("IR dump does not match %s (run with -update to refresh it if intentional):\ngot:\n%s\nwant:\n%s",
			goldenPath, got, want)
	}
}

// TestParseMiniSpecDeterministic guards Constitution invariant 1 directly:
// two loads of the same spec produce byte-identical output, independent of
// the golden fixture above.
func TestParseMiniSpecDeterministic(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mini", "basic.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	first, err := Parse(spec, "", nil)
	if err != nil {
		t.Fatalf("Parse (1st): %v", err)
	}
	second, err := Parse(spec, "", nil)
	if err != nil {
		t.Fatalf("Parse (2nd): %v", err)
	}

	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Errorf("two Parse calls on the same spec disagree:\n1st: %s\n2nd: %s", a, b)
	}
}
