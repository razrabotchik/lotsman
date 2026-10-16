package openapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/domain"
)

// siblingRefSpec is a document whose parameter and body property each put an
// annotation beside a `$ref` -- the most common way an author documents one use
// of a shared component.
func siblingRefSpec(version string) []byte {
	return []byte(`openapi: ` + version + `
info: { title: Siblings, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      parameters:
        - { name: mode, in: query, schema: { $ref: '#/components/schemas/Mode', default: fast, description: How hard to try. } }
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                owner: { $ref: '#/components/schemas/Owner', description: Who keeps the pet. }
      responses:
        "201": { description: created }
components:
  schemas:
    Mode: { type: string, enum: [fast, slow] }
    Owner:
      type: object
      required: [name]
      properties:
        name: { type: string, maxLength: 40 }
`)
}

// An annotation beside a `$ref` constrains nothing, so the operation is exactly
// as translatable as it would be without it. It used to be refused: the parser
// turns the siblings into an allOf branch of their own (3.0), or hands back the
// siblings alone (3.1), and a schema carrying only annotations looked like one
// that accepts anything. Both versions are checked because they arrive in
// different shapes.
func TestAnnotationsBesideARefDoNotHideTheReferencedSchema(t *testing.T) {
	for _, version := range []string{"3.0.3", "3.1.0"} {
		t.Run(version, func(t *testing.T) {
			op := parseOne(t, siblingRefSpec(version))
			if op.Support.Level != domain.SupportSupported {
				t.Fatalf("support = %+v, diagnostics = %+v", op.Support, op.Diagnostics)
			}

			mode := findParam(t, &op, "mode")
			if got := mode.Schema["type"]; got != "string" {
				t.Errorf("mode type = %v, want the referenced string", got)
			}
			if got, want := mode.Schema["enum"], []any{"fast", "slow"}; !reflect.DeepEqual(got, want) {
				t.Errorf("mode enum = %#v, want %#v", got, want)
			}
			if got := mode.Schema["default"]; got != "fast" {
				t.Errorf("mode default = %v, want the sibling's fast", got)
			}
			if got := mode.Schema["description"]; got != "How hard to try." {
				t.Errorf("mode description = %v, want the sibling's", got)
			}

			properties, _ := op.Input.Body.Schema["properties"].(map[string]any)
			owner, _ := properties["owner"].(map[string]any)
			if got := owner["$ref"]; got != "#/$defs/Owner" {
				t.Errorf("owner = %#v, want a reference into $defs", owner)
			}
			if got := owner["description"]; got != "Who keeps the pet." {
				t.Errorf("owner description = %v, want the sibling's", got)
			}
			// The annotation belongs to this use, not to the component: a
			// second use of Owner must not inherit it.
			if got := op.Input.Defs["Owner"]["description"]; got != nil {
				t.Errorf("$defs.Owner description = %v, want none: the sibling leaked into the shared definition", got)
			}
			if got := op.Input.Defs["Owner"]["required"]; !reflect.DeepEqual(got, []any{"name"}) {
				t.Errorf("$defs.Owner required = %#v, want the component's", got)
			}

			// And the annotation weakens nothing: the component's own
			// constraints still decide what a caller may send.
			defs := map[string]any{}
			for name, schema := range op.Input.Defs {
				defs[name] = map[string]any(schema)
			}
			validator, err := argvalidate.Compile("createPet", map[string]any{
				"type":       "object",
				"properties": map[string]any{"body": map[string]any(op.Input.Body.Schema)},
				"$defs":      defs,
			})
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			for literal, valid := range map[string]bool{
				`{"body":{"owner":{"name":"Ann"}}}`:                             true,
				`{"body":{"owner":{}}}`:                                         false,
				`{"body":{"owner":{"name":"` + strings.Repeat("n", 41) + `"}}}`: false,
			} {
				args, err := argvalidate.Decode([]byte(literal))
				if err != nil {
					t.Fatalf("decode %s: %v", literal, err)
				}
				if err := validator.Validate(args); (err == nil) != valid {
					t.Errorf("%s: valid = %v, want %v (%v)", literal, err == nil, valid, err)
				}
			}
		})
	}
}
