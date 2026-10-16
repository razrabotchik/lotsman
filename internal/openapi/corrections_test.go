package openapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

func parseCorrected(t *testing.T, spec string, c Corrections) *Document {
	t.Helper()
	doc, err := Parse(t.Context(), []byte(spec), Options{Corrections: c})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func opByID(t *testing.T, doc *Document, id string) domain.Operation {
	t.Helper()
	for i := range doc.Operations {
		if doc.Operations[i].SourceOperationID == id {
			return doc.Operations[i]
		}
	}
	t.Fatalf("no operation %q", id)
	return domain.Operation{}
}

// oneProblem asserts exactly one correction problem, in section at index,
// whose message contains every one of want.
func oneProblem(t *testing.T, doc *Document, section string, index int, want ...string) {
	t.Helper()
	if len(doc.CorrectionProblems) != 1 {
		t.Fatalf("problems = %v, want exactly one", doc.CorrectionProblems)
	}
	p := doc.CorrectionProblems[0]
	if p.Section != section || p.Index != index {
		t.Fatalf("problem = %+v, want %s[%d]", p, section, index)
	}
	for _, w := range want {
		if !strings.Contains(p.Message, w) {
			t.Fatalf("problem %q does not mention %q", p.Message, w)
		}
	}
}

func noProblems(t *testing.T, doc *Document) {
	t.Helper()
	if len(doc.CorrectionProblems) != 0 {
		t.Fatalf("unexpected problems %v", doc.CorrectionProblems)
	}
}

func object(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("value %v is %T, want map[string]any", v, v)
	}
	return m
}

const twoMediaTypes = `openapi: 3.0.3
info: { title: M, version: "1" }
paths:
  /x:
    patch:
      operationId: patchX
      requestBody:
        content:
          application/merge-patch+json: { schema: { type: object } }
          application/strategic-merge-patch+json: { schema: { type: object } }
          application/apply-patch+yaml: { schema: { type: object } }
      responses: { "200": { description: ok } }
`

func TestMediaTypePin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, pin, wantMedia, wantProblem string
		wantLevel                         domain.SupportLevel
	}{
		{"no pin is refused", "", "", "", domain.SupportRejected},
		{"declared json type is used", "application/merge-patch+json", "application/merge-patch+json", "", domain.SupportSupported},
		{"pin is normalized like the document", "Application/Merge-Patch+JSON; charset=utf-8", "application/merge-patch+json", "", domain.SupportSupported},
		{"undeclared type is a problem", "application/json-patch+json", "", "is not one", domain.SupportRejected},
		{"declared non-json type is a problem", "application/apply-patch+yaml", "", "sends JSON bodies only", domain.SupportRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var c Corrections
			if tc.pin != "" {
				c.Operations = []OperationCorrection{{OperationID: "patchX", MediaType: tc.pin}}
			}
			doc := parseCorrected(t, twoMediaTypes, c)
			op := opByID(t, doc, "patchX")
			if op.Support.Level != tc.wantLevel {
				t.Fatalf("support = %+v", op.Support)
			}
			// Without a pin the document offers a choice lotsman will not
			// make, and it says which refusal that is.
			if tc.pin == "" && !slices.Contains(op.Support.Reasons, domain.ReasonUnsupportedMediaType) {
				t.Fatalf("reasons = %v, want %s", op.Support.Reasons, domain.ReasonUnsupportedMediaType)
			}
			if tc.wantMedia != "" && op.Input.Body.MediaType != tc.wantMedia {
				t.Fatalf("media = %q", op.Input.Body.MediaType)
			}
			if tc.wantProblem == "" {
				noProblems(t, doc)
			} else {
				oneProblem(t, doc, "operationOverrides", 0, tc.wantProblem)
			}
			if tc.wantMedia != "" {
				want := []domain.Correction{{Source: domain.EffectSourceLocalOverride, Target: "mediaType " + tc.wantMedia}}
				if !reflect.DeepEqual(op.Corrections, want) {
					t.Fatalf("corrections = %v", op.Corrections)
				}
			}
		})
	}
}

// The pin never makes an unsupported type supported, and never overrides a
// document that already has one JSON type: it only chooses among declared ones.
func TestMediaTypePinChoosesAmongDeclaredTypesOnly(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: M, version: "1" }
paths:
  /x:
    post:
      operationId: postX
      requestBody:
        content:
          application/json: { schema: { type: object } }
          application/vnd.api+json: { schema: { type: object, properties: { a: { type: string } } } }
      responses: { "200": { description: ok } }
`, Corrections{Operations: []OperationCorrection{{OperationID: "postX", MediaType: "application/vnd.api+json"}}})
	noProblems(t, doc)
	op := opByID(t, doc, "postX")
	if op.Input.Body.MediaType != "application/vnd.api+json" {
		t.Fatalf("media = %q, want the pinned one over the default application/json", op.Input.Body.MediaType)
	}
	if _, ok := op.Input.Body.Schema["properties"]; !ok {
		t.Fatalf("body schema is not the pinned type's: %v", op.Input.Body.Schema)
	}
}

const sharedPathParameter = `openapi: 3.0.3
info: { title: P, version: "1" }
paths:
  /items:
    parameters:
      - { name: limit, in: query, schema: { type: integer } }
    get:
      operationId: listItems
      responses: { "200": { description: ok } }
    delete:
      operationId: deleteItems
      responses: { "204": { description: gone } }
`

// Review focus 1: a path-level parameter is shared; a correction names one operation.
func TestParameterCorrectionDoesNotLeakToSiblingOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems", Note: "API takes strings",
		Parameters: []ParameterCorrection{{Name: "limit", In: "query", Schema: domain.Schema{"type": "string"}}},
	}}})
	noProblems(t, doc)
	list, del := opByID(t, doc, "listItems"), opByID(t, doc, "deleteItems")
	if got := findParam(t, &list, "limit").Schema["type"]; got != "string" {
		t.Fatalf("listItems limit type = %v", got)
	}
	if got := findParam(t, &del, "limit").Schema["type"]; got != "integer" {
		t.Fatalf("deleteItems limit type = %v, want the document's integer", got)
	}
	want := []domain.Correction{{Source: domain.EffectSourceLocalOverride, Target: "parameter query/limit", Note: "API takes strings"}}
	if !reflect.DeepEqual(list.Corrections, want) || len(del.Corrections) != 0 {
		t.Fatalf("corrections: list=%v delete=%v", list.Corrections, del.Corrections)
	}
}

func TestParameterRequiredAndExplodeCorrections(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: P, version: "1" }
paths:
  /items:
    get:
      operationId: listItems
      parameters:
        - { name: ids, in: query, schema: { type: array, items: { type: string } } }
      responses: { "200": { description: ok } }
`, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems",
		Parameters:  []ParameterCorrection{{Name: "ids", In: "query", Required: &yes, Explode: &no}},
	}}})
	noProblems(t, doc)
	op := opByID(t, doc, "listItems")
	p := findParam(t, &op, "ids")
	if !p.Required || p.Explode {
		t.Fatalf("ids = %+v, want required and not exploded", p)
	}
}

func TestParameterStyleCorrectionCanRejectAnOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems",
		Parameters:  []ParameterCorrection{{Name: "limit", In: "query", Style: "deepObject"}},
	}}})
	op := opByID(t, doc, "listItems")
	if op.Support.Level != domain.SupportRejected ||
		!reflect.DeepEqual(op.Support.Reasons, []domain.ReasonCode{domain.ReasonUnsupportedParameterStyle}) {
		t.Fatalf("support = %+v", op.Support)
	}
	if len(doc.CorrectionProblems) != 0 {
		t.Fatalf("a correction that breaks an operation is a consequence, not a problem: %v", doc.CorrectionProblems)
	}
}

// Review focus 3: an object in a query value, and every other shape a URL
// value cannot carry, rejects the operation; an array of scalars does not.
func TestParameterSchemaReplacementMustStayCarriable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		schema domain.Schema
		ok     bool
	}{
		{"object", domain.Schema{"type": "object"}, false},
		{"array of objects", domain.Schema{"type": "array", "items": map[string]any{"type": "object"}}, false},
		{"array of arrays", domain.Schema{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, false},
		{"composition", domain.Schema{"oneOf": []any{map[string]any{"type": "string"}}}, false},
		{"array without items", domain.Schema{"type": "array"}, false},
		{"typeless", domain.Schema{"description": "anything"}, false},
		{"array of strings", domain.Schema{"type": "array", "items": map[string]any{"type": "string"}}, true},
		{"nullable string", domain.Schema{"type": []any{"string", "null"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
				OperationID: "listItems",
				Parameters:  []ParameterCorrection{{Name: "limit", In: "query", Schema: tc.schema}},
			}}})
			noProblems(t, doc)
			op := opByID(t, doc, "listItems")
			if tc.ok {
				if op.Support.Level != domain.SupportSupported {
					t.Fatalf("support = %+v", op.Support)
				}
				return
			}
			if op.Support.Level != domain.SupportRejected ||
				!reflect.DeepEqual(op.Support.Reasons, []domain.ReasonCode{domain.ReasonUnsupportedParameterSchema}) {
				t.Fatalf("support = %+v, want rejected unsupported_parameter_schema", op.Support)
			}
		})
	}
}

// A schema replacement can rescue what normalization refused, because it
// replaces rather than patches (spec section 1).
func TestParameterSchemaReplacementRescuesARefusedSchema(t *testing.T) {
	t.Parallel()
	spec := `openapi: 3.0.3
info: { title: P, version: "1" }
paths:
  /items:
    get:
      operationId: listItems
      parameters:
        - { name: filter, in: query, schema: { type: object } }
      responses: { "200": { description: ok } }
`
	if op := opByID(t, parseCorrected(t, spec, Corrections{}), "listItems"); op.Support.Level != domain.SupportRejected {
		t.Fatalf("precondition: uncorrected support = %+v", op.Support)
	}
	doc := parseCorrected(t, spec, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems",
		Parameters:  []ParameterCorrection{{Name: "filter", In: "query", Schema: domain.Schema{"type": "string"}}},
	}}})
	if op := opByID(t, doc, "listItems"); op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v", op.Support)
	}
}

func TestUndeclaredParameterIsAProblem(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		Index: 3, OperationID: "listItems",
		Parameters: []ParameterCorrection{{Name: "offset", In: "query", Schema: domain.Schema{"type": "string"}}},
	}}})
	oneProblem(t, doc, "operationOverrides", 3, `"offset"`, `"query"`)
}

// A parameter is matched by name and location together.
func TestParameterInTheWrongLocationIsAProblem(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems",
		Parameters:  []ParameterCorrection{{Name: "limit", In: "header", Schema: domain.Schema{"type": "string"}}},
	}}})
	oneProblem(t, doc, "operationOverrides", 0, `"limit"`, `"header"`)
}

func TestOperationMatchedByMethodAndPath(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		Method: "delete", Path: "/items",
		Parameters: []ParameterCorrection{{Name: "limit", In: "query", Schema: domain.Schema{"type": "string"}}},
	}}})
	noProblems(t, doc)
	list, del := opByID(t, doc, "listItems"), opByID(t, doc, "deleteItems")
	if findParam(t, &del, "limit").Schema["type"] != "string" || findParam(t, &list, "limit").Schema["type"] != "integer" {
		t.Fatalf("delete=%v list=%v", del.Input.Parameters, list.Input.Parameters)
	}
}

const sharedBody = `openapi: 3.0.3
info: { title: C, version: "1" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "201": { description: ok } }
    put:
      operationId: replacePet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "200": { description: ok } }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
`

// Review focus 2: a body patch on a $ref must not change the shared definition.
func TestBodyPatchIsLocalToTheOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedBody, Corrections{Operations: []OperationCorrection{{
		OperationID: "createPet", BodyPatch: domain.Schema{"required": []any{"age"}},
	}}})
	noProblems(t, doc)
	create, replace := opByID(t, doc, "createPet"), opByID(t, doc, "replacePet")
	if _, ok := create.Input.Body.Schema["required"]; !ok {
		t.Fatalf("createPet body = %v", create.Input.Body.Schema)
	}
	if _, ok := create.Input.Body.Schema["$ref"]; ok {
		t.Fatalf("createPet body kept a $ref beside the patch: %v", create.Input.Body.Schema)
	}
	if _, ok := replace.Input.Body.Schema["required"]; ok {
		t.Fatalf("replacePet body changed: %v", replace.Input.Body.Schema)
	}
	if _, ok := create.Input.Defs["Pet"]["required"]; ok {
		t.Fatalf("createPet's $defs entry was patched: %v", create.Input.Defs["Pet"])
	}
	want := []domain.Correction{{Source: domain.EffectSourceLocalOverride, Target: "requestBody"}}
	if !reflect.DeepEqual(create.Corrections, want) || len(replace.Corrections) != 0 {
		t.Fatalf("corrections: create=%v replace=%v", create.Corrections, replace.Corrections)
	}
}

const nestedReference = `openapi: 3.0.3
info: { title: N, version: "1" }
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
                owner: { $ref: '#/components/schemas/Owner', description: the owner }
      responses: { "201": { description: ok } }
    put:
      operationId: replacePet
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                owner: { $ref: '#/components/schemas/Owner' }
      responses: { "200": { description: ok } }
components:
  schemas:
    Owner: { type: object, properties: { name: { type: string }, phone: { type: string } } }
`

// A body patch that descends into a property written as a $ref patches a copy
// of the definition in place of the reference: never a sibling of $ref (a
// conjunction in 2020-12), never the shared $defs entry.
func TestBodyPatchDescendsIntoANestedReferenceByCopy(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, nestedReference, Corrections{Operations: []OperationCorrection{{
		OperationID: "createPet",
		BodyPatch: domain.Schema{"properties": map[string]any{"owner": map[string]any{
			"required": []any{"name"}, "properties": map[string]any{"phone": nil}}}},
	}}})
	noProblems(t, doc)
	create, replace := opByID(t, doc, "createPet"), opByID(t, doc, "replacePet")

	owner := object(t, object(t, create.Input.Body.Schema["properties"])["owner"])
	if _, ok := owner["$ref"]; ok {
		t.Fatalf("patched owner kept its $ref: %v", owner)
	}
	if !reflect.DeepEqual(owner["required"], []any{"name"}) || owner["description"] != "the owner" {
		t.Fatalf("patched owner = %v, want required [name] and the reference's description", owner)
	}
	props := object(t, owner["properties"])
	if _, ok := props["phone"]; ok {
		t.Fatalf("phone not removed: %v", owner)
	}
	if _, ok := props["name"]; !ok {
		t.Fatalf("name lost: %v", owner)
	}

	replaceOwner := object(t, object(t, replace.Input.Body.Schema["properties"])["owner"])
	if replaceOwner["$ref"] != "#/$defs/Owner" || len(replaceOwner) != 1 {
		t.Fatalf("replacePet owner changed: %v", replaceOwner)
	}
	for _, op := range []domain.Operation{create, replace} {
		def := op.Input.Defs["Owner"]
		if def == nil {
			continue
		}
		if _, ok := def["required"]; ok {
			t.Fatalf("%s $defs/Owner was patched: %v", op.SourceOperationID, def)
		}
		if _, ok := object(t, def["properties"])["phone"]; !ok {
			t.Fatalf("%s $defs/Owner lost phone: %v", op.SourceOperationID, def)
		}
	}
	if replace.Input.Defs["Owner"] == nil {
		t.Fatal("replacePet lost $defs/Owner")
	}
}

func TestComponentPatchAppliesEverywhereWithProvenance(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedBody, Corrections{Components: []ComponentCorrection{{
		Schema: "Pet", Note: "API takes strings",
		Patch: domain.Schema{"properties": map[string]any{"age": map[string]any{"type": "string"}}},
	}}})
	noProblems(t, doc)
	for _, id := range []string{"createPet", "replacePet"} {
		op := opByID(t, doc, id)
		age := object(t, object(t, op.Input.Defs["Pet"]["properties"])["age"])
		if age["type"] != "string" {
			t.Fatalf("%s age = %v", id, age)
		}
		want := []domain.Correction{{Source: domain.EffectSourceLocalOverride, Target: "component Pet", Note: "API takes strings"}}
		if !reflect.DeepEqual(op.Corrections, want) {
			t.Fatalf("%s corrections = %v", id, op.Corrections)
		}
	}
}

// A component the bundle inlines (a scalar) is patched where it is used.
func TestComponentPatchReachesAnInlinedScalarComponent(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: S, version: "1" }
paths:
  /items:
    get:
      operationId: listItems
      parameters:
        - { name: limit, in: query, schema: { $ref: '#/components/schemas/Limit' } }
      responses: { "200": { description: ok } }
components:
  schemas:
    Limit: { type: integer }
`, Corrections{Components: []ComponentCorrection{{Schema: "Limit", Patch: domain.Schema{"type": "string"}}}})
	noProblems(t, doc)
	op := opByID(t, doc, "listItems")
	if got := findParam(t, &op, "limit").Schema["type"]; got != "string" {
		t.Fatalf("limit type = %v", got)
	}
	if len(op.Corrections) != 1 || op.Corrections[0].Target != "component Limit" {
		t.Fatalf("corrections = %v", op.Corrections)
	}
}

// A component patch whose result a URL value cannot carry rejects the
// operation for its usual reason; it is a consequence, not a problem.
func TestComponentPatchCanRejectAnOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: S, version: "1" }
paths:
  /items:
    get:
      operationId: listItems
      parameters:
        - { name: limit, in: query, schema: { $ref: '#/components/schemas/Limit' } }
      responses: { "200": { description: ok } }
components:
  schemas:
    Limit: { type: integer }
`, Corrections{Components: []ComponentCorrection{{Schema: "Limit", Patch: domain.Schema{"type": "object"}}}})
	noProblems(t, doc)
	op := opByID(t, doc, "listItems")
	if op.Support.Level != domain.SupportRejected ||
		!reflect.DeepEqual(op.Support.Reasons, []domain.ReasonCode{domain.ReasonUnsupportedParameterSchema}) {
		t.Fatalf("support = %+v", op.Support)
	}
}

func TestMissingComponentIsAProblem(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedBody, Corrections{Components: []ComponentCorrection{{
		Index: 1, Schema: "Dog", Patch: domain.Schema{"type": "object"},
	}}})
	oneProblem(t, doc, "componentOverrides", 1, `"Dog"`)
}

// A correction that names nothing is an error even where there is nothing to
// translate.
func TestMissingComponentIsAProblemWithoutPaths(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, `openapi: 3.1.0
info: { title: W, version: "1" }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
`, Corrections{Components: []ComponentCorrection{{Index: 2, Schema: "Dog", Patch: domain.Schema{"type": "object"}}}})
	if len(doc.Operations) != 0 {
		t.Fatalf("operations = %v", doc.Operations)
	}
	oneProblem(t, doc, "componentOverrides", 2, `"Dog"`)

	none := parseCorrected(t, `openapi: 3.1.0
info: { title: W, version: "1" }
`, Corrections{Components: []ComponentCorrection{{Schema: "Pet", Patch: domain.Schema{"type": "object"}}}})
	oneProblem(t, none, "componentOverrides", 0, `"Pet"`)
}

const aliasedComponent = `openapi: 3.0.3
info: { title: A, version: "1" }
paths:
  /animals:
    post:
      operationId: createAnimal
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Animal' } } }
      responses: { "201": { description: ok } }
  /described:
    post:
      operationId: createDescribed
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Described' } } }
      responses: { "201": { description: ok } }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
    Animal: { $ref: '#/components/schemas/Pet' }
    Described: { $ref: '#/components/schemas/Pet', description: an animal }
`

// A component that is itself a reference has no schema of its own to patch.
func TestComponentCorrectionOnAnAliasIsAProblem(t *testing.T) {
	t.Parallel()
	for _, alias := range []string{"Animal", "Described"} {
		t.Run(alias, func(t *testing.T) {
			t.Parallel()
			doc := parseCorrected(t, aliasedComponent, Corrections{Components: []ComponentCorrection{{
				Index: 4, Schema: alias, Patch: domain.Schema{"required": []any{"age"}},
			}}})
			oneProblem(t, doc, "componentOverrides", 4, `"`+alias+`"`, "is a reference to", "#/components/schemas/Pet")
		})
	}
}

// Patching the component an alias points to reaches operations that use the
// alias.
func TestComponentPatchReachesOperationsThroughAnAlias(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, aliasedComponent, Corrections{Components: []ComponentCorrection{{
		Schema: "Pet", Patch: domain.Schema{"properties": map[string]any{"age": map[string]any{"type": "string"}}},
	}}})
	noProblems(t, doc)
	for _, id := range []string{"createAnimal", "createDescribed"} {
		op := opByID(t, doc, id)
		if op.Support.Level != domain.SupportSupported {
			t.Fatalf("%s support = %+v", id, op.Support)
		}
		if !strings.Contains(renderSchemas(&op), `"age":{"type":"string"}`) {
			t.Fatalf("%s was not patched through the alias: %s", id, renderSchemas(&op))
		}
		if len(op.Corrections) != 1 || op.Corrections[0].Target != "component Pet" {
			t.Fatalf("%s corrections = %v", id, op.Corrections)
		}
	}
}

// A component patch that reaches through a reference to a definition still
// being built (here: itself) cannot copy it, and says so by index and path.
func TestComponentPatchThroughACycleIsAProblem(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: L, version: "1" }
paths:
  /nodes:
    post:
      operationId: createNode
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Node' } } }
      responses: { "201": { description: ok } }
components:
  schemas:
    Node:
      type: object
      properties:
        value: { type: string }
        next: { $ref: '#/components/schemas/Node' }
`, Corrections{Components: []ComponentCorrection{{
		Index: 5, Schema: "Node",
		Patch: domain.Schema{"properties": map[string]any{"next": map[string]any{"required": []any{"value"}}}},
	}}})
	oneProblem(t, doc, "componentOverrides", 5, "Node", "properties.next")
}

// A body patch runs after the operation's $defs are complete, so it can reach
// through a recursive component: it patches a copy, never the definition.
func TestBodyPatchThroughARecursiveComponentPatchesACopy(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: L, version: "1" }
paths:
  /nodes:
    post:
      operationId: createNode
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Node' } } }
      responses: { "201": { description: ok } }
components:
  schemas:
    Node:
      type: object
      properties:
        value: { type: string }
        next: { $ref: '#/components/schemas/Node' }
`, Corrections{Operations: []OperationCorrection{{
		Index: 1, OperationID: "createNode",
		BodyPatch: domain.Schema{"properties": map[string]any{"next": map[string]any{"required": []any{"value"}}}},
	}}})
	noProblems(t, doc)
	op := opByID(t, doc, "createNode")
	next := object(t, object(t, op.Input.Body.Schema["properties"])["next"])
	if !reflect.DeepEqual(next["required"], []any{"value"}) {
		t.Fatalf("next = %v", next)
	}
	if _, ok := object(t, object(t, op.Input.Defs["Node"]["properties"])["next"])["required"]; ok {
		t.Fatalf("$defs/Node was patched: %v", op.Input.Defs["Node"])
	}
}

// renderSchemas is an operation's body schema and $defs as one comparable
// string, for asserting what was published wherever it landed.
func renderSchemas(op *domain.Operation) string {
	var b strings.Builder
	if op.Input.Body != nil {
		b.WriteString(compactJSON(map[string]any(op.Input.Body.Schema)))
	}
	for _, name := range sortedKeys(op.Input.Defs) {
		b.WriteString(name + "=" + compactJSON(map[string]any(op.Input.Defs[name])))
	}
	return b.String()
}

func compactJSON(v map[string]any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(encoded)
}

// Components in an external file of an exploded specification are not matched
// (spec section 1): a root Pet patch does not reach a same-named Pet that
// another file defines, even through the same reference text.
func TestComponentPatchDoesNotReachASameNamedExternalComponent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.WriteFile(filepath.Join(root, "other.yaml"), []byte(`components:
  schemas:
    Other:
      type: object
      properties:
        pet: { $ref: '#/components/schemas/Pet' }
    Pet:
      type: object
      properties:
        name: { type: string }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := `openapi: 3.0.3
info: { title: X, version: "1" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "201": { description: ok } }
  /others:
    post:
      operationId: createOther
      requestBody:
        content: { application/json: { schema: { $ref: './other.yaml#/components/schemas/Other' } } }
      responses: { "201": { description: ok } }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: root, Corrections: Corrections{
		Components: []ComponentCorrection{{Schema: "Pet",
			Patch: domain.Schema{"properties": map[string]any{"patched": map[string]any{"type": "boolean"}}}}},
	}})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.HasErrors() {
		t.Fatalf("diagnostics: %+v", doc.Diagnostics)
	}
	noProblems(t, doc)

	pet := opByID(t, doc, "createPet")
	if !strings.Contains(renderSchemas(&pet), `"patched"`) {
		t.Fatalf("root Pet user not patched: %s", renderSchemas(&pet))
	}
	if len(pet.Corrections) != 1 || pet.Corrections[0].Target != "component Pet" {
		t.Fatalf("createPet corrections = %v", pet.Corrections)
	}

	other := opByID(t, doc, "createOther")
	if other.Support.Level != domain.SupportSupported {
		t.Fatalf("createOther support = %+v", other.Support)
	}
	rendered := renderSchemas(&other)
	if strings.Contains(rendered, `"patched"`) || !strings.Contains(rendered, `"name"`) {
		t.Fatalf("external Pet was patched or lost: %s", rendered)
	}
	if len(other.Corrections) != 0 {
		t.Fatalf("createOther corrections = %v, want none", other.Corrections)
	}
}

func TestBodyCorrectionsOnAnOperationWithoutABody(t *testing.T) {
	t.Parallel()
	specs := map[string]string{
		"no requestBody": `openapi: 3.0.3
info: { title: B, version: "1" }
paths:
  /x:
    post:
      operationId: postX
      responses: { "200": { description: ok } }
`,
		"empty content": `openapi: 3.0.3
info: { title: B, version: "1" }
paths:
  /x:
    post:
      operationId: postX
      requestBody: { content: {} }
      responses: { "200": { description: ok } }
`,
	}
	fixes := map[string]OperationCorrection{
		"patch":     {Index: 2, OperationID: "postX", BodyPatch: domain.Schema{"required": []any{"a"}}},
		"mediaType": {Index: 2, OperationID: "postX", MediaType: "application/json"},
	}
	for specName, spec := range specs {
		for fixName, fix := range fixes {
			t.Run(specName+"/"+fixName, func(t *testing.T) {
				t.Parallel()
				doc := parseCorrected(t, spec, Corrections{Operations: []OperationCorrection{fix}})
				oneProblem(t, doc, "operationOverrides", 2, "declares no request body")
			})
		}
	}
}

func TestBodyPatchThatLeavesNoShapeRejectsTheOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedBody, Corrections{Operations: []OperationCorrection{{
		OperationID: "createPet", BodyPatch: domain.Schema{"type": nil, "properties": nil},
	}}})
	noProblems(t, doc)
	op := opByID(t, doc, "createPet")
	if op.Support.Level != domain.SupportRejected ||
		!reflect.DeepEqual(op.Support.Reasons, []domain.ReasonCode{domain.ReasonUnsupportedBodySchema}) {
		t.Fatalf("support = %+v", op.Support)
	}
}

func TestMergePatch(t *testing.T) {
	t.Parallel()
	target := domain.Schema{"type": "object", "properties": map[string]any{
		"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "string"}}}
	patched := newBundle().mergePatch(target, domain.Schema{"properties": map[string]any{
		"a": map[string]any{"type": "string"}, "b": nil}, "required": []any{"a"}}, "",
		func(def, at string) { t.Fatalf("blocked at %s by %s", at, def) })

	props := object(t, patched["properties"])
	if object(t, props["a"])["type"] != "string" {
		t.Fatalf("patched = %v", patched)
	}
	if _, ok := props["b"]; ok {
		t.Fatalf("null did not remove b: %v", patched)
	}
	if patched["type"] != "object" || !reflect.DeepEqual(patched["required"], []any{"a"}) {
		t.Fatalf("patched = %v", patched)
	}
	if target["properties"].(map[string]any)["a"].(map[string]any)["type"] != "integer" {
		t.Fatal("mergePatch wrote into its target")
	}
	if _, ok := target["properties"].(map[string]any)["b"]; !ok {
		t.Fatal("mergePatch deleted from its target")
	}
	// Nested values must be plain maps: catalog walkers assert map[string]any.
	if _, ok := patched["properties"].(map[string]any); !ok {
		t.Fatalf("nested type = %T", patched["properties"])
	}
	// An array is replaced, not merged; a scalar replaces an object.
	again := newBundle().mergePatch(domain.Schema{"enum": []any{"a", "b"}, "items": map[string]any{"type": "string"}},
		domain.Schema{"enum": []any{"c"}, "items": "x"}, "", func(string, string) {})
	if !reflect.DeepEqual(again["enum"], []any{"c"}) || again["items"] != "x" {
		t.Fatalf("again = %v", again)
	}
}

func TestCorrectionsAreDeterministic(t *testing.T) {
	t.Parallel()
	c := Corrections{
		Components: []ComponentCorrection{{Schema: "Pet",
			Patch: domain.Schema{"properties": map[string]any{"age": map[string]any{"type": "string"}}}}},
		Operations: []OperationCorrection{{OperationID: "createPet",
			BodyPatch: domain.Schema{"required": []any{"age"}, "properties": map[string]any{"b": map[string]any{"type": "string"}, "a": map[string]any{"type": "string"}}}}},
	}
	first := parseCorrected(t, sharedBody, c)
	for range 5 {
		if again := parseCorrected(t, sharedBody, c); !reflect.DeepEqual(first.Operations, again.Operations) {
			t.Fatal("two parses with the same corrections differ")
		}
	}
}

// Parse never writes into the caller's corrections (the media type is
// normalized on a copy).
func TestParseDoesNotModifyTheCorrections(t *testing.T) {
	t.Parallel()
	c := Corrections{Operations: []OperationCorrection{{OperationID: "patchX", MediaType: "Application/Merge-Patch+JSON"}}}
	parseCorrected(t, twoMediaTypes, c)
	if c.Operations[0].MediaType != "Application/Merge-Patch+JSON" {
		t.Fatalf("caller's media type rewritten to %q", c.Operations[0].MediaType)
	}
}

func FuzzMergePatchNeverWritesItsTarget(f *testing.F) {
	f.Add("type", "string", "required")
	f.Add("$ref", "#/$defs/X", "$ref")
	f.Add("$ref", "#/$defs/", "$ref") // once a nil-map write: an empty definition name
	f.Fuzz(func(t *testing.T, key, value, other string) {
		target := domain.Schema{key: map[string]any{other: value}, "keep": []any{value}}
		before := cloneSchema(target)
		_ = newBundle().mergePatch(target, domain.Schema{key: nil, other: map[string]any{key: value}}, "",
			func(string, string) {})
		if !reflect.DeepEqual(target, before) {
			t.Fatalf("target changed: %v -> %v", before, target)
		}
	})
}

// Configuration refuses a patch with a non-string key or a `$` keyword; the
// adapter refuses it again, and never copies what it could not read into a
// published schema.
func TestAnUnreadablePatchIsRefusedNotCopied(t *testing.T) {
	t.Parallel()
	secret := map[string]any{"$ref": "file:///tmp/x/secret.json"}
	cases := []struct {
		name string
		c    Corrections
		want string
	}{
		{"non-string key in a component patch", Corrections{Components: []ComponentCorrection{{
			Schema: "Pet", Patch: domain.Schema{"properties": map[any]any{1: map[string]any{"type": "string"}, "age": secret}},
		}}}, "patch.properties: a mapping whose keys are not all strings"},
		{"ref in a body patch", Corrections{Operations: []OperationCorrection{{
			OperationID: "createPet", BodyPatch: domain.Schema{"properties": map[string]any{"age": secret}},
		}}}, `requestBody.patch.properties.age: "$ref" is not allowed`},
		{"non-string key in a body patch list", Corrections{Operations: []OperationCorrection{{
			OperationID: "createPet", BodyPatch: domain.Schema{"allOf": []any{map[any]any{true: secret}}},
		}}}, "requestBody.patch.allOf[0]: a mapping"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := parseCorrected(t, sharedBody, tc.c)
			section := "componentOverrides"
			if len(tc.c.Operations) > 0 {
				section = "operationOverrides"
			}
			oneProblem(t, doc, section, 0, tc.want)
			raw, err := json.Marshal(doc.Operations)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "secret.json") {
				t.Fatalf("the unread $ref reached the catalog: %s", raw)
			}
		})
	}
}

func TestCloneValueNeverCopiesAForeignMap(t *testing.T) {
	t.Parallel()
	got := cloneSchema(domain.Schema{"properties": map[any]any{1: map[string]any{"$ref": "file:///x"}}})
	if got["properties"] != nil {
		t.Fatalf("properties = %#v, want nil", got["properties"])
	}
}

// The form needs what the document declares, not only what lotsman
// translated: every parameter (a refused one with its reason), every offered
// media type in a stable order, and the component a body is named after.
func TestDeclaredParametersAndOfferedMediaTypes(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: X, version: "1" }
paths:
  /x:
    patch:
      operationId: patchX
      parameters:
        - { name: filter, in: query, style: deepObject, schema: { type: object } }
        - { name: limit, in: query, required: true, schema: { type: integer } }
      requestBody:
        content:
          application/strategic-merge-patch+json: { schema: { type: object } }
          application/merge-patch+json: { schema: { type: object } }
          application/apply-patch+yaml: { schema: { type: object } }
          application/json-patch+json: { schema: { type: array } }
      responses: { "200": { description: ok } }
`, Corrections{})
	op := opByID(t, doc, "patchX")
	want := []string{"application/apply-patch+yaml", "application/json-patch+json",
		"application/merge-patch+json", "application/strategic-merge-patch+json"}
	if !reflect.DeepEqual(op.RequestMediaTypes, want) {
		t.Fatalf("media types = %v, want %v", op.RequestMediaTypes, want)
	}
	byName := map[string]domain.DeclaredParameter{}
	for _, p := range op.DeclaredParameters {
		byName[p.Name] = p
	}
	if len(op.DeclaredParameters) != 2 {
		t.Fatalf("declared = %+v", op.DeclaredParameters)
	}
	if f := byName["filter"]; f.Reason != domain.ReasonUnsupportedParameterStyle || f.In != "query" || f.Style != "deepObject" {
		t.Fatalf("filter = %+v", f)
	}
	if l := byName["limit"]; l.Reason != "" || !l.Required {
		t.Fatalf("limit = %+v", l)
	}
}

// BodyComponent names a root component by the node the body references, so a
// same-named component in another file of an exploded document is not it.
func TestBodyComponentIsTheRootComponentOnly(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.WriteFile(filepath.Join(root, "other.yaml"), []byte(`components:
  schemas:
    Pet:
      type: object
      properties:
        name: { type: string }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := `openapi: 3.0.3
info: { title: X, version: "1" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "201": { description: ok } }
  /others:
    post:
      operationId: createOtherPet
      requestBody:
        content: { application/json: { schema: { $ref: './other.yaml#/components/schemas/Pet' } } }
      responses: { "201": { description: ok } }
  /inline:
    post:
      operationId: createInline
      requestBody:
        content: { application/json: { schema: { type: object } } }
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
	if got := opByID(t, doc, "createPet").BodyComponent; got != "Pet" {
		t.Fatalf("createPet body component = %q, want Pet", got)
	}
	if got := opByID(t, doc, "createOtherPet").BodyComponent; got != "" {
		t.Fatalf("createOtherPet body component = %q, want none (another file's Pet)", got)
	}
	if got := opByID(t, doc, "createInline").BodyComponent; got != "" {
		t.Fatalf("createInline body component = %q", got)
	}
}
