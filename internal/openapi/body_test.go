package openapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

func bodySpec(body string) []byte {
	return []byte(`openapi: 3.0.3
info: { title: Bodies, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
` + body + `
      responses:
        "201": { description: created }
`)
}

func TestRequestBodyJSONIsTranslated(t *testing.T) {
	op := parseOne(t, bodySpec(`        required: true
        description: The pet to create.
        content:
          application/json:
            schema:
              type: object
              required: [name]
              additionalProperties: false
              properties:
                name: { type: string, maxLength: 40 }
                tags:
                  type: array
                  items: { type: string }`))

	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v", op.Support)
	}
	body := op.Input.Body
	if body == nil {
		t.Fatal("no body in the IR")
	}
	if body.MediaType != "application/json" || !body.Required {
		t.Errorf("body = %+v, want a required application/json body", body)
	}
	if got := body.Schema["type"]; got != "object" {
		t.Errorf("type = %v, want object", got)
	}
	if got, want := body.Schema["required"], []any{"name"}; !reflect.DeepEqual(got, want) {
		t.Errorf("required = %#v, want %#v", got, want)
	}
	if got := body.Schema["additionalProperties"]; got != false {
		t.Errorf("additionalProperties = %v, want false (as authored)", got)
	}
	properties, ok := body.Schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", body.Schema["properties"])
	}
	name, _ := properties["name"].(map[string]any)
	if name["type"] != "string" || name["maxLength"] != int64(40) {
		t.Errorf("name property = %#v", name)
	}
	tags, _ := properties["tags"].(map[string]any)
	items, _ := tags["items"].(map[string]any)
	if items["type"] != "string" {
		t.Errorf("tags items = %#v", tags["items"])
	}
}

// A body may be a composition: lotsman republishes the shape rather than
// flattening it into something the API never promised.
func TestRequestBodyCompositionIsPreserved(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          application/json:
            schema:
              oneOf:
                - type: object
                  properties: { kind: { type: string } }
                - type: array
                  items: { type: integer }`))

	branches, ok := op.Input.Body.Schema["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("oneOf = %#v", op.Input.Body.Schema["oneOf"])
	}
	first, _ := branches[0].(map[string]any)
	if first["type"] != "object" {
		t.Errorf("first branch = %#v", first)
	}
}

// FR-29: with several media types the choice must be deterministic, and no
// safe choice means the operation is not published.
func TestRequestBodyMediaTypeSelection(t *testing.T) {
	for _, tt := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "json wins over others",
			content: `          application/xml:
            schema: { type: object }
          application/json:
            schema: { type: object }`,
			want: "application/json",
		},
		{
			name: "json with parameters is still json",
			content: `          application/json; charset=utf-8:
            schema: { type: object }`,
			want: "application/json",
		},
		{
			name: "a single +json structured type is unambiguous",
			content: `          application/merge-patch+json:
            schema: { type: object }`,
			want: "application/merge-patch+json",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			op := parseOne(t, bodySpec("        content:\n"+tt.content))
			if op.Support.Level != domain.SupportSupported {
				t.Fatalf("support = %+v", op.Support)
			}
			if op.Input.Body.MediaType != tt.want {
				t.Errorf("media type = %q, want %q", op.Input.Body.MediaType, tt.want)
			}
		})
	}
}

func TestRequestBodyRejections(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		reason  domain.ReasonCode
		mention string
	}{
		{
			name: "no json media type",
			body: `        content:
          multipart/form-data:
            schema: { type: object }`,
			reason:  domain.ReasonUnsupportedMediaType,
			mention: "multipart/form-data",
		},
		{
			name: "two competing +json types",
			body: `        content:
          application/merge-patch+json:
            schema: { type: object }
          application/json-patch+json:
            schema: { type: object }`,
			reason: domain.ReasonUnsupportedMediaType,
		},
		{
			name: "no schema",
			body: `        content:
          application/json: {}`,
			reason: domain.ReasonUnsupportedBodySchema,
		},
		{
			name: "schema that accepts anything",
			body: `        content:
          application/json:
            schema: {}`,
			reason: domain.ReasonUnsupportedBodySchema,
		},
		{
			name: "conditional subschema",
			body: `        content:
          application/json:
            schema:
              type: object
              not: { type: string }`,
			reason: domain.ReasonUnsupportedBodySchema,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			op := parseOne(t, bodySpec(tt.body))
			if op.Support.Level != domain.SupportRejected {
				t.Fatalf("support = %q, want rejected", op.Support.Level)
			}
			if !hasReason(op.Support.Reasons, tt.reason) {
				t.Errorf("reasons = %v, want %s", op.Support.Reasons, tt.reason)
			}
			if op.Executable() {
				t.Error("a rejected operation must never be executable")
			}
			if tt.mention != "" {
				var found bool
				for _, d := range op.Diagnostics {
					if strings.Contains(d.Message, tt.mention) {
						found = true
					}
				}
				if !found {
					t.Errorf("diagnostics %+v do not mention %q", op.Diagnostics, tt.mention)
				}
			}
		})
	}
}

// A body schema that implies its own type by declaring properties is very
// common; stating the implied type is not a guess, since properties on a
// non-object mean nothing.
func TestRequestBodyInfersObjectFromProperties(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          application/json:
            schema:
              properties:
                name: { type: string }`))
	if got := op.Input.Body.Schema["type"]; got != "object" {
		t.Errorf("type = %v, want object", got)
	}
}

// A self-referential schema must not spin the converter. Truncation policy
// for cycles is T025's; until then the honest answer is a refusal.
func TestRequestBodyCycleIsRefusedNotHung(t *testing.T) {
	spec := []byte(`openapi: 3.0.3
info: { title: Cycles, version: "1.0" }
paths:
  /nodes:
    post:
      operationId: createNode
      requestBody:
        content:
          application/json:
            schema: { $ref: "#/components/schemas/Node" }
      responses:
        "201": { description: created }
components:
  schemas:
    Node:
      type: object
      properties:
        child: { $ref: "#/components/schemas/Node" }
`)
	doc, err := Parse(t.Context(), spec, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) == 0 {
		return // libopenapi refused the circular document outright, which is also fail-closed
	}
	op := doc.Operations[0]
	if op.Executable() || op.Support.Level != domain.SupportRejected {
		t.Fatalf("an operation with a cyclic body schema must be rejected: %+v", op.Support)
	}
	if !hasReason(op.Support.Reasons, domain.ReasonUnsupportedBodySchema) {
		t.Errorf("reasons = %v, want %s", op.Support.Reasons, domain.ReasonUnsupportedBodySchema)
	}
}

func TestRequestBodyNullableIsTranslatedInsideProperties(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          application/json:
            schema:
              type: object
              properties:
                nickname: { type: string, nullable: true }
                age: { type: integer, minimum: 0, exclusiveMinimum: true }`))

	properties, _ := op.Input.Body.Schema["properties"].(map[string]any)
	nickname, _ := properties["nickname"].(map[string]any)
	if got, want := nickname["type"], []any{"string", "null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nullable nested type = %#v, want %#v", got, want)
	}
	age, _ := properties["age"].(map[string]any)
	if got, ok := age["exclusiveMinimum"].(float64); !ok || got != 0 {
		t.Errorf("exclusiveMinimum = %#v, want 0", age["exclusiveMinimum"])
	}
	if _, present := age["minimum"]; present {
		t.Error("the boolean 3.0 form must not leave a duplicate inclusive minimum behind")
	}
}

// A sole wildcard range says "any media type": JSON is inside that set, and
// the schema beside it applies. Kubernetes declares its DELETE bodies this
// way -- found by running the M0 corpus (T022).
func TestRequestBodyWildcardMediaTypeSendsJSON(t *testing.T) {
	for _, wildcard := range []string{"*/*", "application/*"} {
		t.Run(wildcard, func(t *testing.T) {
			op := parseOne(t, bodySpec("        content:\n          \""+wildcard+"\":\n            schema: { type: object }"))
			if op.Support.Level != domain.SupportSupported {
				t.Fatalf("support = %+v", op.Support)
			}
			if op.Input.Body.MediaType != "application/json" {
				t.Errorf("media type = %q, want the concrete type lotsman sends", op.Input.Body.MediaType)
			}
		})
	}
}

// A wildcard next to a concrete type leaves two schemas in play, and picking
// one would be the guess the wildcard rule exists to avoid.
func TestRequestBodyWildcardBesideAnotherTypeIsRefused(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          "*/*":
            schema: { type: object }
          application/xml:
            schema: { type: string }`))
	if op.Support.Level != domain.SupportRejected {
		t.Fatalf("support = %q, want rejected", op.Support.Level)
	}
	if !hasReason(op.Support.Reasons, domain.ReasonUnsupportedMediaType) {
		t.Errorf("reasons = %v", op.Support.Reasons)
	}
}
