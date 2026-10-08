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

// FR-13b: an exploded specification is identified by everything it read, not by
// its index. Editing a referenced document leaves the root byte-identical, so
// `specDigest` alone reports two different specifications as the same one --
// which is the wrong answer to "am I serving what I think I am serving".
func TestTheManifestIdentifiesEveryDocumentThatWasRead(t *testing.T) {
	dir := t.TempDir()
	referenced := filepath.Join(dir, "pet.yaml")
	writeFile := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(referenced, "type: object\nproperties:\n  name: { type: string }\n")
	specPath := filepath.Join(dir, "openapi.yaml")
	writeFile(specPath, `openapi: 3.0.3
info: { title: Exploded, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: './pet.yaml' }
      responses: { "201": { description: created } }
`)

	first := specIdentityOf(t, specPath)
	if first.ManifestDigest == "" {
		t.Fatal("the report carries no manifest digest")
	}
	if len(first.References) != 1 || first.References[0].Path != "pet.yaml" {
		t.Fatalf("references = %+v, want the one document that was read", first.References)
	}
	if first.References[0].Digest == first.Digest {
		t.Error("the referenced document carries the root's digest")
	}
	if first.ManifestDigest == first.Digest {
		t.Error("the manifest digest is the root digest, so it identifies the index rather than the specification")
	}

	// Change only the referenced document.
	writeFile(referenced, "type: object\nproperties:\n  name: { type: string }\n  colour: { type: string }\n")
	second := specIdentityOf(t, specPath)

	if second.Digest != first.Digest {
		t.Errorf("the root digest moved, so this test is not about what it claims")
	}
	if second.ManifestDigest == first.ManifestDigest {
		t.Error("editing a referenced document did not change the manifest digest")
	}
	if second.References[0].Digest == first.References[0].Digest {
		t.Error("the referenced document's own digest did not change")
	}

	// And it is an identity, not a timestamp: reading the same bytes twice
	// produces the same manifest.
	if again := specIdentityOf(t, specPath); again.ManifestDigest != second.ManifestDigest {
		t.Error("two reads of the same specification produced different manifests")
	}
}

// reportedSpec is the identity half of an `inspect` report: the root document's
// digest, the manifest of everything it read, and the documents themselves.
//
// A named type rather than the anonymous struct this used to return: a seven
// line type in a signature is one nobody can read at the call site, and it
// cannot be reused by the next test that needs the same thing.
type reportedSpec struct {
	Digest         string `json:"digest"`
	ManifestDigest string `json:"manifestDigest"`
	References     []struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
	} `json:"references"`
}

// specIdentityOf runs `inspect --json` and returns the spec identity.
func specIdentityOf(t *testing.T, specPath string) reportedSpec {
	t.Helper()
	stdout, stderr, code := runCLI(t, "inspect", specPath, "--json")
	if code != 0 {
		t.Fatalf("inspect failed (%d): %s", code, stderr)
	}
	var report struct {
		Spec reportedSpec `json:"spec"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	return report.Spec
}
