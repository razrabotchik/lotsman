package openapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// A refused reference is one problem, and it is reported once.
//
// The parser is handed the confined allowlist, so a document lotsman refused is
// one it never receives -- and it reports the reference missing again, in its
// own vocabulary ("component `X` does not exist in the specification"), with no
// reason code and pointing at where the reference was used rather than where it
// was written. Passing that through doubled every list: the DigitalOcean index
// alone produced 1,322 diagnostics for 661 missing files, every second one a
// worse restatement of the one above it.
func TestARefusedReferenceIsReportedOnce(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	spec := []byte(`openapi: 3.0.3
info: { title: Missing, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      parameters:
        - $ref: './resources/limit.yaml'
      responses: { "200": { description: ok } }
`)

	doc, err := Parse(t.Context(), spec, Options{RootPath: root})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(doc.Diagnostics) != 1 {
		t.Fatalf("one missing document produced %d diagnostics, want 1:\n%+v",
			len(doc.Diagnostics), doc.Diagnostics)
	}
	only := doc.Diagnostics[0]
	if only.Code != domain.ReasonRefUnresolvable {
		t.Errorf("code = %q, want %q", only.Code, domain.ReasonRefUnresolvable)
	}
	// lotsman's own diagnostic is the one that survives, and it is the better
	// one: it points at the place the reference was written.
	if only.Pointer == "" {
		t.Errorf("the surviving diagnostic has no pointer: %+v", only)
	}
}

// The suppression is targeted, not a blanket: a reference the scan never
// refused, because it is internal and there is nothing to confine, is still the
// parser's to report -- and it arrives with a code now.
func TestADanglingInternalReferenceIsStillReported(t *testing.T) {
	spec := []byte(`openapi: 3.0.3
info: { title: Dangling, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: { $ref: '#/components/schemas/Absent' }
`)

	doc, err := Parse(t.Context(), spec, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Diagnostics) == 0 {
		t.Fatal("a reference to a schema that does not exist was not reported at all")
	}
	for _, d := range doc.Diagnostics {
		if d.Code == "" {
			t.Errorf("diagnostic without a code: %+v", d)
		}
	}
}

// Every diagnostic carries a code, including the ones lotsman has no narrow
// word for: `code` is the field a consumer branches on (FR-11), and an entry
// without one is not machine-readable however good its prose.
func TestEveryDocumentDiagnosticHasACode(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	// A referenced document that exists and is not a document: the read
	// succeeds, so this travels further into the parser than a missing file.
	if err := os.WriteFile(filepath.Join(root, "broken.yaml"), []byte("::not yaml::\n\t- ["), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := []byte(`openapi: 3.0.3
info: { title: Broken, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      parameters:
        - $ref: './broken.yaml'
      responses: { "200": { description: ok } }
`)

	doc, err := Parse(t.Context(), spec, Options{RootPath: root})
	if err != nil {
		return // refused outright is a fine answer; the codes are what this checks
	}
	if len(doc.Diagnostics) == 0 {
		t.Fatal("a reference into a file that is not a document was not reported")
	}
	for _, d := range doc.Diagnostics {
		if d.Code == "" {
			t.Errorf("diagnostic without a code: %+v", d)
		}
	}
}
