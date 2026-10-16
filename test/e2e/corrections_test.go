package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// US-1: picking merge-patch for the twelve PATCH operations of Kubernetes
// apps/v1 publishes all 77.
func TestKubernetesMediaTypeCorrectionsPublishEveryOperation(t *testing.T) {
	t.Parallel()
	spec := filepath.Join("..", "testdata", "corpus", "kubernetes-apps-v1.json")
	raw, err := os.ReadFile(spec)
	if err != nil {
		t.Skipf("corpus not fetched (make corpus): %v", err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range doc.Paths {
		patch, ok := item["patch"]
		if !ok {
			continue
		}
		var op struct {
			OperationID string `json:"operationId"`
		}
		if err := json.Unmarshal(patch, &op); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, op.OperationID)
	}
	sort.Strings(ids)
	if len(ids) != 12 {
		t.Fatalf("found %d PATCH operations, want 12", len(ids))
	}

	before, stderr, code := runCLI(t, "inspect", spec, "--json")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, stderr)
	}
	if report := decodeReport(t, before); report.Totals.Found != 77 || report.Totals.Supported != 65 {
		t.Fatalf("uncorrected totals = %+v, want 65 of 77", report.Totals)
	}

	var cfg strings.Builder
	cfg.WriteString("apiVersion: lotsman.dev/v1alpha1\noperationOverrides:\n")
	for _, id := range ids {
		fmt.Fprintf(&cfg, "  - match: { operationId: %s }\n    mediaType: application/merge-patch+json\n", id)
	}
	cfgPath := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t, "inspect", spec, "--config", cfgPath, "--json")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, stderr)
	}
	report := decodeReport(t, stdout)
	if report.Totals.Found != 77 || report.Totals.Supported != 77 {
		t.Fatalf("totals = %+v, want 77/77", report.Totals)
	}
}

// US-2: a correction whose parameter is gone stops serve and names itself.
func TestAStaleCorrectionStopsServe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	cfg := filepath.Join(dir, "lotsman.yaml")
	writeE2EFile(t, spec, correctedSpec)
	writeE2EFile(t, cfg, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: offset, in: query, schema: { type: string } }]
`)
	_, stderr, code := runCLI(t, "serve", spec, "--config", cfg, "--base-url", "http://127.0.0.1:1")
	if code != 2 || !strings.Contains(stderr, "offset") || !strings.Contains(stderr, "operationOverrides[0]") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

const correctedSpec = `openapi: 3.0.3
info: { title: S, version: "1" }
paths:
  /items:
    get:
      operationId: listItems
      parameters: [{ name: limit, in: query, schema: { type: integer } }]
      responses: { "200": { description: ok } }
`

func writeE2EFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A corrected operation carries its provenance into the JSON report, and the
// catalog digest follows what the correction changed (Constitution IV).
func TestCorrectionsAreVisibleInTheReportAndTheDigest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	cfg := filepath.Join(dir, "lotsman.yaml")
	writeE2EFile(t, spec, correctedSpec)
	writeE2EFile(t, cfg, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: limit, in: query, schema: { type: string } }]
    note: the API takes strings
`)

	plain, stderr, code := runCLI(t, "inspect", spec, "--json")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, stderr)
	}
	first, stderr, code := runCLI(t, "inspect", spec, "--config", cfg, "--json")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, stderr)
	}
	second, _, _ := runCLI(t, "inspect", spec, "--config", cfg, "--json")

	var corrected struct {
		Operations []struct {
			Corrections []struct {
				Source string `json:"source"`
				Target string `json:"target"`
				Note   string `json:"note"`
			} `json:"corrections"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(first), &corrected); err != nil {
		t.Fatal(err)
	}
	if len(corrected.Operations) != 1 || len(corrected.Operations[0].Corrections) != 1 {
		t.Fatalf("operations = %+v", corrected.Operations)
	}
	got := corrected.Operations[0].Corrections[0]
	if got.Source != "local_override" || got.Target != "parameter query/limit" || got.Note != "the API takes strings" {
		t.Fatalf("correction = %+v", got)
	}

	plainDigest, firstDigest := decodeReport(t, plain).Catalog.Digest, decodeReport(t, first).Catalog.Digest
	if plainDigest == firstDigest {
		t.Fatal("a correction that changes a published schema left the catalog digest unchanged")
	}
	if firstDigest != decodeReport(t, second).Catalog.Digest || first != second {
		t.Fatal("the same spec and configuration produced two reports")
	}
}

// spec.overlays are read relative to the configuration and applied before
// parsing; a broken one is a startup error naming the file.
func TestOverlaysFromTheConfiguration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	cfg := filepath.Join(dir, "lotsman.yaml")
	writeE2EFile(t, spec, correctedSpec)
	if err := os.Mkdir(filepath.Join(dir, "fixes"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeE2EFile(t, filepath.Join(dir, "fixes", "limit.yaml"), `overlay: 1.0.0
info: { title: limit, version: "1" }
actions:
  - target: $.paths['/items'].get.parameters[0].schema
    update: { type: string }
`)
	writeE2EFile(t, filepath.Join(dir, "fixes", "stale.yaml"), `overlay: 1.0.0
info: { title: stale, version: "1" }
actions:
  - target: $.paths['/gone'].get
    update: { summary: x }
`)
	writeE2EFile(t, cfg, "apiVersion: lotsman.dev/v1alpha1\nspec:\n  overlays: [./fixes/limit.yaml]\n")
	plain, _, _ := runCLI(t, "inspect", spec, "--json")
	overlaid, stderr, code := runCLI(t, "inspect", spec, "--config", cfg, "--json")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, stderr)
	}
	if decodeReport(t, plain).Catalog.Digest == decodeReport(t, overlaid).Catalog.Digest {
		t.Fatal("the overlay changed nothing in the catalog")
	}

	writeE2EFile(t, cfg, "apiVersion: lotsman.dev/v1alpha1\nspec:\n  overlays: [./fixes/stale.yaml]\n")
	_, stderr, code = runCLI(t, "inspect", spec, "--config", cfg)
	if code != 2 || !strings.Contains(stderr, "stale.yaml") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

// US-4 and spec section 2: --watch polls the configuration as it polls the
// specification. A catalog-shaping change is republished; a policy change is
// refused and the running catalog stays.
func TestWatchReappliesCatalogConfigurationAndRefusesPolicy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	cfgPath := filepath.Join(dir, "lotsman.yaml")
	replaceSpec(t, specPath, specWith("shown", "hidden"))
	replaceSpec(t, cfgPath, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - { match: { operationId: hidden }, enabled: false }
`)
	endpoint, stderr := serveHTTPEnv(t, nil, "--listen", "127.0.0.1:0", "--config", cfgPath, specPath,
		"--watch", "--log-level", "debug")
	if got := toolNames(t, endpoint); !slices.Contains(got, "shown") || slices.Contains(got, "hidden") {
		t.Fatalf("published at startup: %v\nstderr:\n%s", got, stderr.String())
	}

	replaceSpec(t, cfgPath, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - { match: { operationId: hidden }, enabled: true, note: reviewed }
`)
	if !waitForTool(t, endpoint, "hidden", 30*time.Second) {
		t.Fatalf("the configuration change was never published\nstderr:\n%s", stderr.String())
	}

	replaceSpec(t, cfgPath, `apiVersion: lotsman.dev/v1alpha1
execution: { allowMutations: true }
operationOverrides:
  - { match: { operationId: hidden }, enabled: false }
`)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stderr.String(), "AllowMutations") {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "reload refused") || !strings.Contains(stderr.String(), "AllowMutations") {
		t.Fatalf("the policy change was not refused by name\nstderr:\n%s", stderr.String())
	}
	if got := toolNames(t, endpoint); !slices.Contains(got, "hidden") {
		t.Fatalf("the running catalog changed after a refused reload: %v", got)
	}
}
