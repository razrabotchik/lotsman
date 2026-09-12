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
	doc, err := Parse(t.Context(), []byte(basicSpec), Options{})
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
	doc, err := Parse(t.Context(), []byte(basicSpec), Options{Namespace: "petstore"})
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
	doc, err := Parse(t.Context(), []byte(spec), Options{})
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
	doc, err := Parse(t.Context(), []byte(spec), Options{})
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
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Operations[0].Support.Level != domain.SupportSupported {
		t.Fatalf("Support = %+v, want supported", doc.Operations[0].Support)
	}
}

func TestParseNoPaths(t *testing.T) {
	const spec = "openapi: 3.0.3\ninfo: { title: x, version: '1.0' }\n"
	doc, err := Parse(t.Context(), []byte(spec), Options{})
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
			_, err := Parse(t.Context(), []byte(tt.spec), Options{})
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
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Diagnostics) == 0 {
		t.Fatal("want at least one document-level diagnostic for the unresolved $ref")
	}
	if !doc.HasErrors() {
		t.Fatal("document-level error diagnostics were not reflected by HasErrors")
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

// Cookie parameters enumerate and publish a schema but cannot be written to
// the wire yet, so they block execution; path, query and header parameters do
// not. Authentication is still unimplemented and blocks on its own.
func TestParseMarksUnsupportedRuntimeCapabilitiesAsExecutionBlockers(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
security:
  - bearerAuth: []
paths:
  /widgets:
    get:
      operationId: listWidgets
      parameters:
        - { name: session, in: cookie, schema: { type: string } }
      responses:
        "200": { description: ok }
components:
  securitySchemes:
    bearerAuth: { type: http, scheme: bearer }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	op := doc.Operations[0]
	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("Support = %+v", op.Support)
	}
	if op.Executable() {
		t.Fatal("operation with unimplemented parameters/body/auth is executable")
	}
	want := []domain.ReasonCode{
		domain.ReasonParametersNotImplemented,
		domain.ReasonAuthenticationNotImplemented,
	}
	if len(op.ExecutionBlockers) != len(want) {
		t.Fatalf("ExecutionBlockers = %v, want %v", op.ExecutionBlockers, want)
	}
	for i := range want {
		if op.ExecutionBlockers[i] != want[i] {
			t.Fatalf("ExecutionBlockers = %v, want %v", op.ExecutionBlockers, want)
		}
	}
}

func TestParseExplicitEmptySecurityOverridesRoot(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
security:
  - bearerAuth: []
paths:
  /public:
    get:
      operationId: public
      security: []
      responses:
        "200": { description: ok }
components:
  securitySchemes:
    bearerAuth: { type: http, scheme: bearer }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !doc.Operations[0].Executable() {
		t.Fatalf("explicit public operation blockers = %v", doc.Operations[0].ExecutionBlockers)
	}
}

func TestParseServersRootOnly(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
servers:
  - url: https://api.example.com
paths:
  /widgets:
    get:
      operationId: getWidget
      responses:
        "200": { description: ok }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"https://api.example.com"}
	if got := doc.Operations[0].Servers; !equalStrings(got, want) {
		t.Errorf("Servers = %v, want %v", got, want)
	}
}

func TestParseServersPathOverridesRoot(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
servers:
  - url: https://root.example.com
paths:
  /widgets:
    servers:
      - url: https://path.example.com
    get:
      operationId: getWidget
      responses:
        "200": { description: ok }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"https://path.example.com"}
	if got := doc.Operations[0].Servers; !equalStrings(got, want) {
		t.Errorf("Servers = %v, want %v", got, want)
	}
}

func TestParseServersOperationOverridesPath(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
servers:
  - url: https://root.example.com
paths:
  /widgets:
    servers:
      - url: https://path.example.com
    get:
      operationId: getWidget
      servers:
        - url: https://op.example.com
      responses:
        "200": { description: ok }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"https://op.example.com"}
	if got := doc.Operations[0].Servers; !equalStrings(got, want) {
		t.Errorf("Servers = %v, want %v", got, want)
	}
}

func TestParseServersNoneDeclared(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /widgets:
    get:
      operationId: getWidget
      responses:
        "200": { description: ok }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := doc.Operations[0].Servers; len(got) != 0 {
		t.Errorf("Servers = %v, want none", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseImplementedParametersDoNotBlockExecution(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /widgets/{widgetId}:
    get:
      operationId: getWidget
      parameters:
        - { name: widgetId, in: path, required: true, schema: { type: string } }
        - { name: tags, in: query, schema: { type: array, items: { type: string } } }
      responses:
        "200": { description: ok }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	op := doc.Operations[0]
	if !op.Executable() {
		t.Fatalf("path/query parameters must not block execution: %v", op.ExecutionBlockers)
	}
	if len(op.Input.Parameters) != 2 {
		t.Fatalf("Input.Parameters = %+v, want 2", op.Input.Parameters)
	}
}

// The IR carries an effect for every operation, with its provenance, because
// the runtime gate reads it and a report has to explain it (spec 4.7).
func TestParseAssignsEffectWithProvenance(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      responses: { "200": { description: ok } }
    post:
      operationId: createPet
      responses: { "201": { description: created } }
  /pets/{petId}:
    parameters:
      - { name: petId, in: path, required: true, schema: { type: string } }
    delete:
      operationId: deletePet
      responses: { "204": { description: gone } }
  /cache/rebuild:
    get:
      operationId: rebuildCache
      responses: { "200": { description: ok } }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byID := map[string]domain.Operation{}
	for _, op := range doc.Operations {
		byID[op.SourceOperationID] = op
	}
	for id, want := range map[string]domain.Effect{
		"listPets":  domain.EffectRead,
		"createPet": domain.EffectUnknown,
		"deletePet": domain.EffectDestructive,
		// A read-looking method with a mutation-like verb is not a read.
		"rebuildCache": domain.EffectUnknown,
	} {
		op, ok := byID[id]
		if !ok {
			t.Fatalf("%s missing from %+v", id, byID)
		}
		if op.Effect.Effect != want {
			t.Errorf("%s effect = %q, want %q", id, op.Effect.Effect, want)
		}
		if op.Effect.Source != domain.EffectSourceHTTPMethod || op.Effect.Confidence != domain.ConfidenceInferred {
			t.Errorf("%s provenance = %+v", id, op.Effect)
		}
	}
	if len(byID["rebuildCache"].Effect.Warnings) == 0 {
		t.Error("a suspicious GET must carry a warning explaining the raised effect")
	}
}

// A spec may not hand a model the headers that carry credentials or control
// the transport.
func TestParseRejectsProtectedHeaderParameters(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /widgets:
    get:
      operationId: listWidgets
      parameters:
        - { name: Authorization, in: header, schema: { type: string } }
      responses: { "200": { description: ok } }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	op := doc.Operations[0]
	if op.Support.Level != domain.SupportRejected {
		t.Fatalf("support = %q, want rejected", op.Support.Level)
	}
	if !hasReason(op.Support.Reasons, domain.ReasonInvalidParameter) {
		t.Errorf("reasons = %v, want %s", op.Support.Reasons, domain.ReasonInvalidParameter)
	}
}

// A header parameter is serializable now, so it must not block execution.
func TestParseHeaderParametersDoNotBlockExecution(t *testing.T) {
	const spec = `
openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /widgets:
    get:
      operationId: listWidgets
      parameters:
        - { name: X-Tenant, in: header, schema: { type: string } }
      responses: { "200": { description: ok } }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !doc.Operations[0].Executable() {
		t.Fatalf("header parameters blocked execution: %v", doc.Operations[0].ExecutionBlockers)
	}
}
