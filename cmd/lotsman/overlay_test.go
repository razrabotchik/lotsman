package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/catalog"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: lotsman.dev/v1alpha1\n"+content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An override the operator believes is in force has to be in force: a match on
// nothing exits with a usage error rather than serving a slightly different
// catalog.
func TestOverrideThatMatchesNothingIsAStartupError(t *testing.T) {
	cfg := writeConfig(t, `operationOverrides:
  - match: { operationId: listPetz }
    effect: read
`)
	for _, command := range [][]string{
		{"inspect", miniSpecPath(t), "--config", cfg},
		{"validate", miniSpecPath(t), "--config", cfg},
		{"operations", miniSpecPath(t), "--config", cfg},
	} {
		t.Run(command[0], func(t *testing.T) {
			_, stderr, code := runCLI(t, command...)
			if code != 2 {
				t.Errorf("exit = %d, want 2 (usage); stderr: %s", code, stderr)
			}
			if !strings.Contains(stderr, "listPetz") {
				t.Errorf("stderr = %q, want it to name the override", stderr)
			}
		})
	}
}

// The overlay reaches the published catalog, and the report accounts for what
// it removed: an operator who cannot see what a filter did cannot trust it.
func TestOverlayIsAppliedAndReported(t *testing.T) {
	cfg := writeConfig(t, `operationOverrides:
  - match: { operationId: deletePet }
    enabled: false
  - match: { operationId: createPet }
    effect: read
`)
	stdout, stderr, code := runCLI(t, "inspect", miniSpecPath(t), "--config", cfg, "--json")
	if code != 0 {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}

	var report struct {
		Totals     catalog.Totals             `json:"totals"`
		Operations []catalog.OperationVerdict `json:"operations"`
		ByReason   map[string]int             `json:"byReason"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode inspect --json: %v", err)
	}
	if report.Totals.Excluded != 1 || report.ByReason["disabled_by_override"] != 1 {
		t.Errorf("totals = %+v, byReason = %v, want one disabled operation", report.Totals, report.ByReason)
	}

	for _, op := range report.Operations {
		switch {
		case strings.HasSuffix(string(op.Key), "DELETE:/pets/{petId}"):
			if op.Published || !op.Excluded {
				t.Errorf("deletePet = %+v, want excluded and unpublished", op)
			}
		case strings.HasSuffix(string(op.Key), "POST:/pets"):
			// A reviewed override is explicit, and an explicit read runs
			// under the default read-only policy.
			if op.Effect.Source != "local_override" || op.Effect.Confidence != "explicit" {
				t.Errorf("createPet effect = %+v, want the operator's provenance", op.Effect)
			}
			if len(op.PolicyBlockers) != 0 {
				t.Errorf("createPet = %+v, want no policy blocker after being declared a read", op)
			}
		}
	}
}

// A tag filter that matches nothing leaves a valid document with nothing to
// publish, which `validate` reports as exit 4 -- and says whose decision it was.
func TestSelectionCanEmptyTheCatalogAndValidateSaysSo(t *testing.T) {
	cfg := writeConfig(t, "catalog:\n  includeTags: [nothing-carries-this]\n")
	_, stderr, code := runCLI(t, "validate", miniSpecPath(t), "--config", cfg)
	if code != 4 {
		t.Fatalf("exit = %d, want 4; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "excluded by configuration") {
		t.Errorf("stderr = %q, want it to separate the operator's exclusions from lotsman's refusals", stderr)
	}
}
