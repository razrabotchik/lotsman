package openapi

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

const basicSpec = `
openapi: 3.0.3
info:
  title: Mini
  version: "1.0"
paths:
  /pets/{petId}:
    parameters:
      - name: petId
        in: path
        required: true
        schema:
          type: string
    get:
      operationId: getPet
      responses:
        "200": { description: ok }
    delete:
      operationId: deletePet
      responses:
        "200": { description: ok }
  /pets:
    get:
      operationId: listPets
      responses:
        "200": { description: ok }
    post:
      operationId: createPet
      responses:
        "200": { description: ok }
`

func TestParseEnumeratesInDeterministicOrder(t *testing.T) {
	doc, err := Parse([]byte(basicSpec), "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Diagnostics) != 0 {
		t.Fatalf("unexpected document diagnostics: %+v", doc.Diagnostics)
	}

	want := []struct {
		method, path, opID string
	}{
		{"GET", "/pets", "listPets"},
		{"POST", "/pets", "createPet"},
		{"GET", "/pets/{petId}", "getPet"},
		{"DELETE", "/pets/{petId}", "deletePet"},
	}
	if len(doc.Operations) != len(want) {
		t.Fatalf("len(Operations) = %d, want %d: %+v", len(doc.Operations), len(want), doc.Operations)
	}
	for i, w := range want {
		got := doc.Operations[i]
		if got.Method != w.method || got.PathTemplate != w.path || got.SourceOperationID != w.opID {
			t.Errorf("Operations[%d] = %+v, want method=%s path=%s opID=%s", i, got, w.method, w.path, w.opID)
		}
		if got.Support.Level != domain.SupportSupported {
			t.Errorf("Operations[%d] Support = %+v, want supported", i, got.Support)
		}
		wantKey := domain.NewOperationKey("", w.method, w.path)
		if got.Key != wantKey {
			t.Errorf("Operations[%d].Key = %q, want %q", i, got.Key, wantKey)
		}
	}
}

func TestParseNamespaceScopesKey(t *testing.T) {
	doc, err := Parse([]byte(basicSpec), "petstore", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, op := range doc.Operations {
		if !strings.HasPrefix(string(op.Key), "petstore:") {
			t.Errorf("Key %q does not carry namespace prefix", op.Key)
		}
	}
}

func TestParsePathParameterMismatchRejected(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: Bad, version: "1.0" }
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      responses:
        "200": { description: ok }
`
	doc, err := Parse([]byte(spec), "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) != 1 {
		t.Fatalf("len(Operations) = %d, want 1", len(doc.Operations))
	}
	op := doc.Operations[0]
	if op.Support.Level != domain.SupportRejected {
		t.Fatalf("Support.Level = %q, want rejected", op.Support.Level)
	}
	if len(op.Support.Reasons) != 1 || op.Support.Reasons[0] != domain.ReasonPathParameterMismatch {
		t.Fatalf("Support.Reasons = %+v, want [path_parameter_mismatch]", op.Support.Reasons)
	}
	if len(op.Diagnostics) != 1 {
		t.Fatalf("len(Diagnostics) = %d, want 1: %+v", len(op.Diagnostics), op.Diagnostics)
	}
	diag := op.Diagnostics[0]
	if diag.Code != domain.ReasonPathParameterMismatch {
		t.Errorf("Diagnostics[0].Code = %q, want path_parameter_mismatch", diag.Code)
	}
	if diag.Severity != domain.SeverityError {
		t.Errorf("Diagnostics[0].Severity = %q, want error", diag.Severity)
	}
	wantPointer := "#/paths/~1pets~1{petId}/get"
	if diag.Pointer != wantPointer {
		t.Errorf("Diagnostics[0].Pointer = %q, want %q", diag.Pointer, wantPointer)
	}
}

func TestParsePathParameterNotRequiredIsMismatch(t *testing.T) {
	// A parameter named after the placeholder exists but is not required:
	// the OpenAPI spec mandates required:true for `in: path`, so this is as
	// broken as a missing parameter.
	const spec = `
openapi: 3.0.3
info: { title: Bad, version: "1.0" }
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      parameters:
        - name: petId
          in: path
          required: false
          schema: { type: string }
      responses:
        "200": { description: ok }
`
	doc, err := Parse([]byte(spec), "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Operations[0].Support.Level != domain.SupportRejected {
		t.Fatalf("Support.Level = %q, want rejected", doc.Operations[0].Support.Level)
	}
}

func TestParseOperationParameterOverridesPathParameter(t *testing.T) {
	// Path-level declares petId as not required; the operation overrides it
	// to required:true for the same (name, in) key. Merge must prefer the
	// operation-level entry (pipeline.md 3.1).
	const spec = `
openapi: 3.0.3
info: { title: Override, version: "1.0" }
paths:
  /pets/{petId}:
    parameters:
      - name: petId
        in: path
        required: false
        schema: { type: string }
    get:
      operationId: getPet
      parameters:
        - name: petId
          in: path
          required: true
          schema: { type: string }
      responses:
        "200": { description: ok }
`
	doc, err := Parse([]byte(spec), "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Operations[0].Support.Level != domain.SupportSupported {
		t.Fatalf("Support = %+v, want supported", doc.Operations[0].Support)
	}
}

func TestParseNoPaths(t *testing.T) {
	const spec = "openapi: 3.0.3\ninfo: { title: x, version: '1.0' }\n"
	doc, err := Parse([]byte(spec), "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) != 0 {
		t.Fatalf("len(Operations) = %d, want 0", len(doc.Operations))
	}
}

func TestParseInvalidBytesReturnsError(t *testing.T) {
	tests := []struct {
		name string
		spec string
	}{
		{"empty", ""},
		{"garbage", "not valid: yaml: : :\n\tbad indent"},
		{"unsupported version", "info:\n  title: x\n  version: '1.0'\npaths: {}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.spec), "", nil)
			if err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestParseCollectsDocumentDiagnosticsWithoutFailing(t *testing.T) {
	// An unresolved local $ref is a structural error libopenapi reports via
	// BuildV3Model's joined error, but it still returns a usable partial
	// model — pipeline.md 3.1.2 wants every error surfaced, not just the
	// first, and wants enumeration to proceed regardless.
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /widgets:
    get:
      operationId: getWidget
      parameters:
        - name: filter
          in: query
          schema:
            $ref: '#/components/schemas/Missing'
      responses:
        "200": { description: ok }
`
	doc, err := Parse([]byte(spec), "", nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Diagnostics) == 0 {
		t.Fatal("want at least one document-level diagnostic for the unresolved $ref")
	}
	for _, d := range doc.Diagnostics {
		if d.Severity != domain.SeverityError {
			t.Errorf("diagnostic severity = %q, want error: %+v", d.Severity, d)
		}
	}
	if len(doc.Operations) != 1 || doc.Operations[0].SourceOperationID != "getWidget" {
		t.Fatalf("Operations = %+v, want getWidget still enumerated", doc.Operations)
	}
}
