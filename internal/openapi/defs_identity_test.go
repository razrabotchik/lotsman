package openapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// A reference written inside another file of an exploded specification can
// read exactly like one written in the root ("#/components/schemas/Pet") and
// still name a different component. The two must not share a $defs slot: one
// would silently replace the other in the published schema.
func TestSameReferenceTextInAnotherFileGetsItsOwnDef(t *testing.T) {
	t.Parallel()
	petRoot := `{"properties":{"age":{"type":"integer"}},"type":"object"}`
	petOther := `{"properties":{"x":{"type":"string"}},"type":"object"}`
	for _, tc := range []struct {
		name, thing, wantB string
		wantDefs           map[string]string
	}{
		// The alias is reached through a reference into the other file, which
		// is inlined; the parser hands over other.yaml's Pet itself.
		{"alias", `Thing: { $ref: '#/components/schemas/Pet' }`, petOther,
			map[string]string{"Pet": petRoot}},
		{"property", `Thing: { type: object, properties: { pet: { $ref: '#/components/schemas/Pet' } } }`,
			`{"properties":{"pet":{"$ref":"#/$defs/Pet_2"}},"type":"object"}`,
			map[string]string{"Pet": petRoot, "Pet_2": petOther}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if resolved, err := filepath.EvalSymlinks(root); err == nil {
				root = resolved
			}
			other := "components:\n  schemas:\n    " + tc.thing + `
    Pet: { type: object, properties: { x: { type: string } } }
`
			if err := os.WriteFile(filepath.Join(root, "other.yaml"), []byte(other), 0o600); err != nil {
				t.Fatal(err)
			}
			spec := `openapi: 3.0.3
info: { title: X, version: "1" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                a: { $ref: '#/components/schemas/Pet' }
                b: { $ref: './other.yaml#/components/schemas/Thing' }
      responses: { "201": { description: ok } }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
`
			doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: root})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if doc.HasErrors() {
				t.Fatalf("diagnostics: %+v", doc.Diagnostics)
			}
			op := opByID(t, doc, "createPet")
			rendered := renderSchemas(&op)
			want := tc.wantDefs
			if len(op.Input.Defs) != len(want) {
				t.Fatalf("defs = %s", rendered)
			}
			for name, schema := range want {
				if got := compactJSON(op.Input.Defs[name]); got != schema {
					t.Fatalf("$defs/%s = %s, want %s (all: %s)", name, got, schema, rendered)
				}
			}
			props := asMap(op.Input.Body.Schema["properties"])
			if got := compactJSON(asMap(props["a"])); got != `{"$ref":"#/$defs/Pet"}` {
				t.Fatalf("a = %s (all: %s)", got, rendered)
			}
			b := compactJSON(asMap(props["b"]))
			if b != tc.wantB {
				t.Fatalf("b = %s, want %s (all: %s)", b, tc.wantB, rendered)
			}
		})
	}
}

func asMap(v any) map[string]any {
	switch m := v.(type) {
	case map[string]any:
		return m
	case domain.Schema:
		return m
	}
	return nil
}
