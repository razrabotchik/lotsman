package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCorrectionValidation(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, yaml, want string }{
		{"path parameter cannot be optional", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: path, required: false }]`, "always required"},
		{"parameter states nothing", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: query }]`, "states nothing"},
		{"parameter without a name", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ in: query, required: true }]`, "name is required"},
		{"unknown location", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: body, required: true }]`, "is not path"},
		{"duplicate parameter", `
operationOverrides:
  - match: { operationId: x }
    parameters:
      - { name: id, in: query, required: true }
      - { name: id, in: query, schema: { type: string } }`, "corrected twice"},
		{"empty parameter schema", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: query, schema: {} }]`, "schema is empty"},
		{"ref in a parameter schema", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: query, schema: { type: array, items: { $ref: '#/x' } } }]`, `"$ref" is not allowed`},
		{"ref in a patch", `
componentOverrides:
  - match: { schema: Pet }
    patch: { properties: { a: { $ref: '#/x' } } }`, `"$ref" is not allowed`},
		{"defs in a body patch", `
operationOverrides:
  - match: { operationId: x }
    requestBody: { patch: { allOf: [{ $defs: {} }] } }`, `"$defs" is not allowed`},
		{"empty body patch", `
operationOverrides:
  - match: { operationId: x }
    requestBody: { patch: {} }`, "requestBody states nothing"},
		{"component without a name", `
componentOverrides:
  - { match: {}, patch: { type: object } }`, "needs a schema name"},
		{"component without a patch", `
componentOverrides:
  - { match: { schema: Pet } }`, "states nothing"},
		{"duplicate component", `
componentOverrides:
  - { match: { schema: Pet }, patch: { type: object } }
  - { match: { schema: Pet }, patch: { type: object } }`, "both patch"},
		{"media type shape", `
operationOverrides:
  - match: { operationId: x }
    mediaType: json`, "type/subtype"},
		{"empty overlay entry", `
spec: { overlays: [""] }`, "must name a file"},
		{"stdin overlay entry", `
spec: { overlays: ["-"] }`, "must name a file"},
		{"unknown correction field", `
operationOverrides:
  - match: { operationId: x }
    requestBody: { replace: { type: object } }`, "replace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte("apiVersion: " + APIVersion + "\n" + strings.TrimPrefix(tc.yaml, "\n")))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestACorrectionAloneIsNotAnEmptyOverride(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		"    mediaType: application/merge-patch+json\n",
		"    parameters: [{ name: limit, in: query, schema: { type: string } }]\n",
		"    requestBody: { patch: { required: [a] } }\n",
		"    note: why\n    effect: read\n",
	} {
		if _, err := Parse([]byte("apiVersion: " + APIVersion + "\noperationOverrides:\n  - match: { operationId: x }\n" + body)); err != nil {
			t.Fatalf("%q: err = %v", body, err)
		}
	}
}

// A note alone states nothing about the operation.
func TestANoteAloneIsAnEmptyOverride(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("apiVersion: " + APIVersion + `
operationOverrides:
  - match: { operationId: x }
    note: just a note
`))
	if err == nil || !strings.Contains(err.Error(), "states nothing") {
		t.Fatalf("err = %v", err)
	}
}

// The same parameter name in two locations is two parameters.
func TestTheSameNameInTwoLocationsIsNotADuplicate(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("apiVersion: " + APIVersion + `
operationOverrides:
  - match: { operationId: x }
    parameters:
      - { name: id, in: query, required: true }
      - { name: id, in: header, required: true }
`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}

// Corrections reach the runtime in file order, and overlays are resolved
// relative to the configuration file like spec.source.
func TestCorrectionsReachTheRuntime(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lotsman.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: "+APIVersion+`
spec:
  overlays: [./fixes/one.yaml, two.yaml]
componentOverrides:
  - { match: { schema: B }, patch: { type: object }, note: second }
  - { match: { schema: A }, patch: { type: object } }
operationOverrides:
  - match: { operationId: x }
    mediaType: application/merge-patch+json
`), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	runtime := Resolve(file, Environment{}, Overrides{})
	want := []string{filepath.Join(dir, "fixes", "one.yaml"), filepath.Join(dir, "two.yaml")}
	if len(runtime.Overlays) != 2 || runtime.Overlays[0] != want[0] || runtime.Overlays[1] != want[1] {
		t.Fatalf("overlays = %v, want %v", runtime.Overlays, want)
	}
	if len(runtime.ComponentOverrides) != 2 || runtime.ComponentOverrides[0].Match.Schema != "B" ||
		runtime.ComponentOverrides[0].Note != "second" {
		t.Fatalf("componentOverrides = %+v", runtime.ComponentOverrides)
	}
	if len(runtime.Overrides) != 1 || runtime.Overrides[0].MediaType != "application/merge-patch+json" {
		t.Fatalf("overrides = %+v", runtime.Overrides)
	}
}
