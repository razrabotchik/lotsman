package openapi

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

func TestOverlayUpdatesTheDocument(t *testing.T) {
	t.Parallel()
	out, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{{Path: "fix.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: fix, version: "1" }
actions:
  - target: $.paths['/items'].parameters[0].schema
    update: { type: string }
`)}})
	if err != nil {
		t.Fatalf("ApplyOverlays: %v", err)
	}
	doc, err := Parse(t.Context(), out, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	list := opByID(t, doc, "listItems")
	if got := findParam(t, &list, "limit").Schema["type"]; got != "string" {
		t.Fatalf("limit type = %v", got)
	}
}

// Overlays apply in order: the second sees what the first wrote.
func TestOverlaysApplyInOrder(t *testing.T) {
	t.Parallel()
	out, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{
		{Path: "one.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: one, version: "1" }
actions:
  - target: $.paths['/items'].parameters[0].schema
    update: { type: string }
`)},
		{Path: "two.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: two, version: "1" }
actions:
  - target: $.paths['/items'].parameters[0].schema
    update: { maxLength: 5 }
`)},
	})
	if err != nil {
		t.Fatalf("ApplyOverlays: %v", err)
	}
	doc, err := Parse(t.Context(), out, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	list := opByID(t, doc, "listItems")
	schema := findParam(t, &list, "limit").Schema
	if schema["type"] != "string" || schema["maxLength"] == nil {
		t.Fatalf("limit schema = %v", schema)
	}
}

func TestOverlayRemoveAction(t *testing.T) {
	t.Parallel()
	out, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{{Path: "rm.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: rm, version: "1" }
actions:
  - target: $.paths['/items'].delete
    remove: true
`)}})
	if err != nil {
		t.Fatalf("ApplyOverlays: %v", err)
	}
	doc, err := Parse(t.Context(), out, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) != 1 || doc.Operations[0].SourceOperationID != "listItems" {
		t.Fatalf("operations = %v", doc.Operations)
	}
}

func TestOverlayErrorsNameTheFile(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, overlay, want string }{
		{"target matching nothing", `overlay: 1.0.0
info: { title: fix, version: "1" }
actions:
  - target: $.paths['/nothing']
    update: { description: x }
`, "/nothing"},
		{"action without target", `overlay: 1.0.0
info: { title: fix, version: "1" }
actions:
  - target: $.paths['/items']
    update: { description: x }
  - update: { description: y }
`, "actions[1]"},
		{"no actions", `overlay: 1.0.0
info: { title: fix, version: "1" }
actions: []
`, "no actions"},
		{"not an overlay", `openapi: 3.0.3
`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{{Path: "fix.yaml", Bytes: []byte(tc.overlay)}})
			if err == nil || !strings.Contains(err.Error(), "fix.yaml") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming fix.yaml and %q", err, tc.want)
			}
		})
	}
}

// Review focus 5: what an overlay writes is confined like what the vendor wrote.
func TestOverlayRefIsConfinedToTheSpecRoot(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "spec")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.yaml"), []byte("type: string\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{{Path: "evil.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: evil, version: "1" }
actions:
  - target: $.paths['/items'].parameters[0]
    update: { schema: { $ref: '../secret.yaml' } }
`)}})
	if err != nil {
		t.Fatalf("ApplyOverlays: %v", err)
	}
	if !strings.Contains(string(out), "../secret.yaml") {
		t.Fatalf("overlay did not write the reference: %s", out)
	}
	// The document parses; confinement is what refuses the reference, and it
	// says so -- not some other failure that would make this test pass for
	// the wrong reason.
	doc, err := Parse(t.Context(), out, Options{RootPath: root})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) == 0 {
		t.Fatal("no operations parsed")
	}
	for _, op := range doc.Operations {
		if op.Support.Level != domain.SupportRejected || !slices.Contains(op.Support.Reasons, domain.ReasonRefOutsideRoot) {
			t.Fatalf("%s: support = %+v, want rejected for ref_outside_root", op.SourceOperationID, op.Support)
		}
	}
	confined := false
	for _, d := range doc.Diagnostics {
		if d.Code == domain.ReasonRefOutsideRoot && strings.Contains(d.Message, "../secret.yaml") {
			confined = true
		}
	}
	if !confined {
		t.Fatalf("no ref_outside_root diagnostic for ../secret.yaml: %+v", doc.Diagnostics)
	}
	if len(doc.References) != 0 {
		t.Fatalf("a file outside the root was read: %v", doc.References)
	}
}
