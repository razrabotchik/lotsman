package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpusManifest mirrors testdata/corpus/MANIFEST.json.
type corpusManifest struct {
	Specs []corpusSpec `json:"specs"`
}

type corpusSpec struct {
	Name   string `json:"name"`
	Vendor string `json:"vendor"`
	Digest string `json:"digest"`
	Expect struct {
		Outcome   string   `json:"outcome"` // reported | refused
		Found     int      `json:"found"`
		Supported int      `json:"supported"`
		Rejected  int      `json:"rejected"`
		ByReason  []string `json:"byReason"`
		Message   string   `json:"message"`
	} `json:"expect"`
}

// TestCorpus runs the real binary over the vendor corpus and checks that
// lotsman still makes of each document what it made of it at the M0 gate.
//
// The documents are not committed (see testdata/corpus/MANIFEST.json), so the
// test skips unless `make corpus` has fetched them. A diff here is the signal
// the corpus exists for: support moved, and someone has to say whether that
// was the intention.
func TestCorpus(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "corpus")
	raw, err := os.ReadFile(filepath.Join(root, "MANIFEST.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest corpusManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Specs) == 0 {
		t.Fatal("the manifest lists no specs")
	}

	for _, spec := range manifest.Specs {
		t.Run(spec.Name, func(t *testing.T) {
			path := filepath.Join(root, spec.Name)
			if _, err := os.Stat(path); err != nil {
				t.Skipf("corpus document not fetched; run `make corpus`")
			}

			stdout, stderr, code := runCLI(t, "inspect", path, "--json")

			if spec.Expect.Outcome == "refused" {
				if code == 0 {
					t.Fatalf("%s was accepted; the manifest records a refusal", spec.Vendor)
				}
				if !strings.Contains(stderr, spec.Expect.Message) {
					t.Errorf("refusal = %q, want it to mention %q", strings.TrimSpace(stderr), spec.Expect.Message)
				}
				return
			}

			if code != 0 {
				t.Fatalf("exit = %d, want a report\n%s", code, stderr)
			}
			report := decodeReport(t, stdout)

			if report.Spec.Digest != spec.Digest {
				t.Fatalf("digest = %s, want the pinned %s (the fetched document is not the one measured)",
					report.Spec.Digest, spec.Digest)
			}
			if report.Totals.Found != spec.Expect.Found {
				t.Errorf("found = %d, want %d", report.Totals.Found, spec.Expect.Found)
			}
			if report.Totals.Supported != spec.Expect.Supported {
				t.Errorf("supported = %d, want %d", report.Totals.Supported, spec.Expect.Supported)
			}
			if report.Totals.Rejected != spec.Expect.Rejected {
				t.Errorf("rejected = %d, want %d", report.Totals.Rejected, spec.Expect.Rejected)
			}
			for _, reason := range spec.Expect.ByReason {
				if report.ByReason[reason] == 0 {
					t.Errorf("byReason has no %s; the manifest records it as a known cause", reason)
				}
			}
		})
	}
}

// The corpus is also the determinism test at scale: the biggest document in
// it must produce the same report twice, byte for byte (Constitution IV).
func TestCorpusReportsAreDeterministic(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "corpus", "stripe.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("corpus document not fetched; run `make corpus`")
	}

	first, _, _ := runCLI(t, "inspect", path, "--json")
	second, _, _ := runCLI(t, "inspect", path, "--json")
	if first != second {
		t.Error("two runs over the same 5 MB document produced different reports")
	}
}
