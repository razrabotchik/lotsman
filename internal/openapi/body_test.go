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

// A self-referential schema is published, not refused: references are kept as
// references, so the cycle closes in $defs exactly as the document wrote it.
// Inlining is what used to make this impossible (T025).
func TestRequestBodyCycleBecomesARecursiveDefinition(t *testing.T) {
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
        label: { type: string }
        child: { $ref: "#/components/schemas/Node" }
`)
	doc, err := Parse(t.Context(), spec, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) != 1 {
		t.Fatalf("got %d operations, want 1", len(doc.Operations))
	}
	op := doc.Operations[0]
	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v, want supported", op.Support)
	}
	if got := op.Input.Body.Schema["$ref"]; got != "#/$defs/Node" {
		t.Errorf("body schema = %#v, want a reference into $defs", op.Input.Body.Schema)
	}
	node, ok := op.Input.Defs["Node"]
	if !ok {
		t.Fatalf("Node missing from $defs: %+v", op.Input.Defs)
	}
	properties, _ := node["properties"].(map[string]any)
	child, _ := properties["child"].(map[string]any)
	if child["$ref"] != "#/$defs/Node" {
		t.Errorf("recursive member = %#v, want it to point back at Node", properties["child"])
	}
}

// A component used twice is defined once: this is the difference between a
// publishable catalog and megabytes of repetition (docs/corpus.md).
func TestSharedComponentIsDefinedOnce(t *testing.T) {
	spec := []byte(`openapi: 3.0.3
info: { title: Shared, version: "1.0" }
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
                owner: { $ref: "#/components/schemas/Person" }
                vet: { $ref: "#/components/schemas/Person" }
      responses:
        "201": { description: created }
components:
  schemas:
    Person:
      type: object
      properties:
        name: { type: string }
        address: { type: string }
`)
	doc, err := Parse(t.Context(), spec, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	op := doc.Operations[0]
	if len(op.Input.Defs) != 1 {
		t.Fatalf("defs = %+v, want exactly one Person", op.Input.Defs)
	}
	properties, _ := op.Input.Body.Schema["properties"].(map[string]any)
	for _, member := range []string{"owner", "vet"} {
		schema, _ := properties[member].(map[string]any)
		if schema["$ref"] != "#/$defs/Person" {
			t.Errorf("%s = %#v, want a reference", member, properties[member])
		}
	}
}

// A reference to a scalar is inlined: an indirection for {"type":"integer"}
// costs a reader more than it saves.
func TestScalarReferencesAreInlined(t *testing.T) {
	spec := []byte(`openapi: 3.0.3
info: { title: Scalars, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      parameters:
        - name: limit
          in: query
          schema: { $ref: "#/components/schemas/Limit" }
      responses:
        "200": { description: ok }
components:
  schemas:
    Limit:
      type: integer
      maximum: 100
`)
	doc, err := Parse(t.Context(), spec, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	op := doc.Operations[0]
	if len(op.Input.Defs) != 0 {
		t.Errorf("defs = %+v, want none for a scalar", op.Input.Defs)
	}
	schema := op.Input.Parameters[0].Schema
	if schema["type"] != "integer" || schema["maximum"] != float64(100) {
		t.Errorf("parameter schema = %#v, want the scalar inlined", schema)
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

// Pitfall #7: a readOnly property is sent by the server, not by the client.
// Publishing it as an input invites a model to invent an id for a resource
// that does not exist yet -- and `required: [id]` alongside `readOnly: true`
// means required *in the response*.
func TestReadOnlyPropertiesAreExcludedFromInput(t *testing.T) {
	op := parseOne(t, bodySpec(`        required: true
        content:
          application/json:
            schema:
              type: object
              required: [id, name]
              properties:
                id: { type: string, readOnly: true }
                name: { type: string }
                secret: { type: string, writeOnly: true }
                nested:
                  type: object
                  required: [createdAt]
                  properties:
                    createdAt: { type: string, readOnly: true }
                    label: { type: string }`))

	schema := op.Input.Body.Schema
	properties, _ := schema["properties"].(map[string]any)
	if _, present := properties["id"]; present {
		t.Error("a readOnly property was published as an input")
	}
	if _, present := properties["name"]; !present {
		t.Error("an ordinary property went missing")
	}
	// writeOnly is the opposite case: the client sends it, the server does not
	// return it, which is exactly what an input schema describes.
	secret, _ := properties["secret"].(map[string]any)
	if secret == nil || secret["writeOnly"] != true {
		t.Errorf("writeOnly property = %#v, want it kept and marked", properties["secret"])
	}

	required, _ := schema["required"].([]any)
	for _, name := range required {
		if name == "id" {
			t.Error("a readOnly property is still required of the caller")
		}
	}
	if len(required) != 1 || required[0] != "name" {
		t.Errorf("required = %#v, want only name", required)
	}

	// The exclusion applies at every level, not just the top.
	nested, _ := properties["nested"].(map[string]any)
	nestedProperties, _ := nested["properties"].(map[string]any)
	if _, present := nestedProperties["createdAt"]; present {
		t.Error("a nested readOnly property was published as an input")
	}
	if _, present := nested["required"]; present {
		t.Error("a nested required list that held only readOnly names must disappear")
	}
}

// OAS spells examples in the singular; JSON Schema 2020-12 in the plural.
func TestExamplesAreNormalizedToThePluralForm(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          application/json:
            schema:
              type: object
              properties:
                name: { type: string, example: Murka }`))

	properties, _ := op.Input.Body.Schema["properties"].(map[string]any)
	name, _ := properties["name"].(map[string]any)
	examples, _ := name["examples"].([]any)
	if len(examples) != 1 || examples[0] != "Murka" {
		t.Errorf("examples = %#v, want [Murka]", name["examples"])
	}
	if _, present := name["example"]; present {
		t.Error("the OAS singular spelling was republished as a keyword")
	}
}

// Keywords that exist only in OAS say nothing about whether an argument is
// valid, so they do not reach the validation schema.
func TestOASOnlyKeywordsAreNotPublished(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          application/json:
            schema:
              type: object
              externalDocs: { url: https://example.com }
              xml: { name: pet }
              properties:
                name: { type: string }`))

	for _, keyword := range []string{"xml", "externalDocs", "discriminator", "deprecated"} {
		if _, present := op.Input.Body.Schema[keyword]; present {
			t.Errorf("%q reached the validation schema", keyword)
		}
	}
}
