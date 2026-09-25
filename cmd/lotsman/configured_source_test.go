package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One file describing a deployment: the configuration names the document, and
// a relative path in it means the same thing from any directory.
//
// The second half is the part worth testing. A path resolved against the
// working directory would make a deployment's file work from the directory it
// happens to live in and fail from anywhere else — including from wherever a
// service manager starts the process.
func TestAConfigurationCanNameTheDocument(t *testing.T) {
	dir := t.TempDir()
	document, err := os.ReadFile(miniSpecPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), document, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "lotsman.yaml")
	if err := os.WriteFile(configPath, []byte(`apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./openapi.yaml
  strict: false
catalog:
  mode: tools
  descriptionBytesPerTool: 40
execution:
  timeout: 5s
  maxResponseBytes: 4096
  redirects: deny
  defaultPolicy: read-only
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Run from somewhere that is not the configuration's directory: the path
	// inside it resolves against the file, not the caller.
	elsewhere := t.TempDir()
	stdout, stderr, code := runCLIIn(t, elsewhere, "inspect", "--config", configPath, "--json")
	if code != 0 {
		t.Fatalf("inspect failed (%d): %s", code, stderr)
	}

	var report struct {
		Spec struct {
			Source string `json:"source"`
		} `json:"spec"`
		Catalog struct {
			Mode                    string `json:"mode"`
			DescriptionBytesPerTool int    `json:"descriptionBytesPerTool"`
		} `json:"catalog"`
		Operations []struct {
			ToolName string `json:"toolName"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}

	if !strings.HasSuffix(report.Spec.Source, filepath.Join(dir, "openapi.yaml")) {
		t.Errorf("the report names %q, want the document beside the configuration", report.Spec.Source)
	}
	if report.Catalog.Mode != "tools" {
		t.Errorf("mode = %q, want the configured tools", report.Catalog.Mode)
	}
	if report.Catalog.DescriptionBytesPerTool != 40 {
		t.Errorf("descriptionBytesPerTool = %d, want the configured 40",
			report.Catalog.DescriptionBytesPerTool)
	}
	if len(report.Operations) == 0 {
		t.Error("the document was read but produced no operations")
	}
}

// The command line still wins (FR-62): a configuration that could override an
// argument would make a one-off `inspect OTHER.yaml` unreliable.
func TestTheArgumentWinsOverTheConfiguredSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lotsman.yaml"), []byte(`apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./does-not-exist.yaml
`), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t, "inspect", miniSpecPath(t),
		"--config", filepath.Join(dir, "lotsman.yaml"), "--json")
	if code != 0 {
		t.Fatalf("inspect failed (%d): %s", code, stderr)
	}
	if strings.Contains(stdout, "does-not-exist") {
		t.Error("the configuration's source overrode the argument")
	}
}

// runCLIIn runs the binary with a working directory of its own.
func runCLIIn(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runCLIEnvDir(t, dir, nil, args...)
}
