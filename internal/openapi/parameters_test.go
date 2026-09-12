package openapi

import (
	"reflect"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// paramSpec wraps parameter YAML in the smallest valid document.
func paramSpec(parameters string) []byte {
	return []byte(`openapi: 3.0.3
info: { title: Params, version: "1.0" }
paths:
  /widgets/{widgetId}:
    get:
      operationId: getWidget
      parameters:
        - { name: widgetId, in: path, required: true, schema: { type: string } }
` + parameters + `
      responses:
        "200": { description: ok }
`)
}

func parseOne(t *testing.T, spec []byte) domain.Operation {
	t.Helper()
	doc, err := Parse(t.Context(), spec, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Operations) != 1 {
		t.Fatalf("got %d operations, want 1", len(doc.Operations))
	}
	return doc.Operations[0]
}

func findParam(t *testing.T, op *domain.Operation, name string) domain.Parameter {
	t.Helper()
	for _, p := range op.Input.Parameters {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("parameter %q not in %+v", name, op.Input.Parameters)
	return domain.Parameter{}
}

// The location-dependent defaults are the detail conversions get wrong most
// often: a query array must default to explode=true (pitfall #5).
func TestParameterStyleAndExplodeDefaults(t *testing.T) {
	op := parseOne(t, paramSpec(`        - { name: tags, in: query, schema: { type: array, items: { type: string } } }
        - { name: X-Tenant, in: header, schema: { type: string } }
        - { name: session, in: cookie, schema: { type: string } }`))

	for _, tt := range []struct {
		name    string
		style   domain.ParameterStyle
		explode bool
	}{
		{"widgetId", domain.StyleSimple, false},
		{"tags", domain.StyleForm, true},
		{"X-Tenant", domain.StyleSimple, false},
		{"session", domain.StyleForm, true},
	} {
		p := findParam(t, &op, tt.name)
		if p.Style != tt.style || p.Explode != tt.explode {
			t.Errorf("%s: style/explode = %q/%t, want %q/%t", tt.name, p.Style, p.Explode, tt.style, tt.explode)
		}
	}
}

func TestParameterExplicitExplodeWins(t *testing.T) {
	op := parseOne(t, paramSpec(`        - { name: ids, in: query, explode: false, schema: { type: array, items: { type: integer } } }`))
	if p := findParam(t, &op, "ids"); p.Explode {
		t.Error("explode: false on a query parameter was not honoured")
	}
}

func TestParameterOrderIsDeterministic(t *testing.T) {
	op := parseOne(t, paramSpec(`        - { name: zebra, in: query, schema: { type: string } }
        - { name: alpha, in: query, schema: { type: string } }
        - { name: X-Trace, in: header, schema: { type: string } }`))

	var got []string
	for _, p := range op.Input.Parameters {
		got = append(got, p.Name)
	}
	want := []string{"widgetId", "alpha", "zebra", "X-Trace"} // path, query (a-z), header
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parameter order = %v, want %v", got, want)
	}
}

func TestUnsupportedStylesRejectTheOperation(t *testing.T) {
	for _, tt := range []struct {
		name  string
		param string
	}{
		{"query deepObject", `        - { name: filter, in: query, style: deepObject, schema: { type: string } }`},
		{"query spaceDelimited", `        - { name: ids, in: query, style: spaceDelimited, schema: { type: array, items: { type: string } } }`},
		{"query pipeDelimited", `        - { name: ids, in: query, style: pipeDelimited, schema: { type: array, items: { type: string } } }`},
		{"path label", `        - { name: extra, in: path, required: true, style: label, schema: { type: string } }`},
		{"path matrix", `        - { name: extra, in: path, required: true, style: matrix, schema: { type: string } }`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			op := parseOne(t, paramSpec(tt.param))
			if op.Support.Level != domain.SupportRejected {
				t.Fatalf("support = %q, want rejected", op.Support.Level)
			}
			if !hasReason(op.Support.Reasons, domain.ReasonUnsupportedParameterStyle) {
				t.Errorf("reasons = %v, want %s", op.Support.Reasons, domain.ReasonUnsupportedParameterStyle)
			}
		})
	}
}

func TestUnsupportedParameterSchemasRejectTheOperation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		param  string
		reason domain.ReasonCode
	}{
		{"object", `        - { name: filter, in: query, schema: { type: object } }`, domain.ReasonUnsupportedParameterSchema},
		{"array of objects", `        - { name: filters, in: query, schema: { type: array, items: { type: object } } }`, domain.ReasonUnsupportedParameterSchema},
		{"nested array", `        - { name: matrix, in: query, schema: { type: array, items: { type: array, items: { type: string } } } }`, domain.ReasonUnsupportedParameterSchema},
		{"composition", `        - { name: either, in: query, schema: { type: string, oneOf: [{ type: string }, { type: integer }] } }`, domain.ReasonUnsupportedParameterSchema},
		{"no type", `        - { name: anything, in: query, schema: { description: whatever } }`, domain.ReasonUnsupportedParameterSchema},
		{"content instead of schema", `        - { name: filter, in: query, content: { application/json: { schema: { type: string } } } }`, domain.ReasonUnsupportedMediaType},
		{"neither schema nor content", `        - { name: bare, in: query }`, domain.ReasonInvalidParameter},
	} {
		t.Run(tt.name, func(t *testing.T) {
			op := parseOne(t, paramSpec(tt.param))
			if op.Support.Level != domain.SupportRejected {
				t.Fatalf("support = %q, want rejected", op.Support.Level)
			}
			if !hasReason(op.Support.Reasons, tt.reason) {
				t.Errorf("reasons = %v, want %s", op.Support.Reasons, tt.reason)
			}
			if op.Executable() {
				t.Error("a rejected operation must never be executable")
			}
		})
	}
}

// OAS 3.0 spells two things in a way that is not valid JSON Schema 2020-12.
// Publishing them unchanged would mean publishing a schema no validator reads
// the way the spec author meant (pitfall #6).
func TestOAS30SchemaConstructsAreTranslated(t *testing.T) {
	op := parseOne(t, paramSpec(`        - { name: nickname, in: query, schema: { type: string, nullable: true } }
        - { name: age, in: query, schema: { type: integer, minimum: 18, exclusiveMinimum: true } }
        - { name: score, in: query, schema: { type: number, maximum: 100 } }`))

	nickname := findParam(t, &op, "nickname").Schema
	if got, want := nickname["type"], []any{"string", "null"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nullable type = %#v, want %#v", got, want)
	}

	age := findParam(t, &op, "age").Schema
	if got, ok := age["exclusiveMinimum"].(float64); !ok || got != 18 {
		t.Errorf("exclusiveMinimum = %#v, want 18", age["exclusiveMinimum"])
	}
	if _, present := age["minimum"]; present {
		t.Error("the boolean 3.0 form must not leave a duplicate inclusive minimum behind")
	}

	score := findParam(t, &op, "score").Schema
	if got, ok := score["maximum"].(float64); !ok || got != 100 {
		t.Errorf("maximum = %#v, want 100", score["maximum"])
	}
}

func TestSchema31ExclusiveBoundsPassThrough(t *testing.T) {
	spec := []byte(`openapi: 3.1.0
info: { title: Params, version: "1.0" }
paths:
  /widgets:
    get:
      operationId: listWidgets
      parameters:
        - { name: page, in: query, schema: { type: integer, exclusiveMinimum: 0 } }
      responses:
        "200": { description: ok }
`)
	op := parseOne(t, spec)
	page := findParam(t, &op, "page").Schema
	if got, ok := page["exclusiveMinimum"].(float64); !ok || got != 0 {
		t.Errorf("exclusiveMinimum = %#v, want 0", page["exclusiveMinimum"])
	}
}

func TestParameterSchemaKeywordsAreCopied(t *testing.T) {
	op := parseOne(t, paramSpec(`        - name: status
          in: query
          description: Filter by status
          schema:
            type: string
            enum: [open, closed]
            default: open
            pattern: "^[a-z]+$"
            minLength: 3
            maxLength: 10`))

	p := findParam(t, &op, "status")
	if p.Description != "Filter by status" {
		t.Errorf("Description = %q", p.Description)
	}
	want := domain.Schema{
		"type":      "string",
		"enum":      []any{"open", "closed"},
		"default":   "open",
		"pattern":   "^[a-z]+$",
		"minLength": int64(3),
		"maxLength": int64(10),
	}
	if !reflect.DeepEqual(map[string]any(p.Schema), map[string]any(want)) {
		t.Errorf("schema = %#v, want %#v", p.Schema, want)
	}
}

func TestOperationParametersOverridePathLevel(t *testing.T) {
	// FR-28: the override key is (name, in), and the operation wins.
	spec := []byte(`openapi: 3.0.3
info: { title: Params, version: "1.0" }
paths:
  /widgets:
    parameters:
      - { name: limit, in: query, schema: { type: string } }
      - { name: shared, in: query, schema: { type: string } }
    get:
      operationId: listWidgets
      parameters:
        - { name: limit, in: query, schema: { type: integer } }
      responses:
        "200": { description: ok }
`)
	op := parseOne(t, spec)
	if len(op.Input.Parameters) != 2 {
		t.Fatalf("parameters = %+v, want limit and shared", op.Input.Parameters)
	}
	if got := findParam(t, &op, "limit").Schema["type"]; got != "integer" {
		t.Errorf("limit type = %v, want integer (operation level wins)", got)
	}
	if got := findParam(t, &op, "shared").Schema["type"]; got != "string" {
		t.Errorf("shared type = %v, want string (path level survives)", got)
	}
}

func hasReason(reasons []domain.ReasonCode, want domain.ReasonCode) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
