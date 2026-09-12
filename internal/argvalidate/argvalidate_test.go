package argvalidate

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// widgetSchema mirrors what the catalog publishes: grouped, with
// additionalProperties:false at every level.
func widgetSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"widgetId": map[string]any{"type": "string", "format": "uuid"},
				},
				"required":             []any{"widgetId"},
				"additionalProperties": false,
			},
			"query": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
					"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"additionalProperties": false,
			},
		},
		"required":             []any{"path"},
		"additionalProperties": false,
	}
}

func mustCompile(t *testing.T) *Validator {
	t.Helper()
	v, err := Compile("getWidget", widgetSchema())
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return v
}

func TestValidateAcceptsWellFormedArguments(t *testing.T) {
	v := mustCompile(t)
	args := map[string]any{
		"path":  map[string]any{"widgetId": "3fa85f64-5717-4562-b3fc-2c963f66afa6"},
		"query": map[string]any{"limit": float64(10), "tags": []any{"a", "b"}},
	}
	if err := v.Validate(args); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// The whole point of additionalProperties:false: an argument the model
// invented must be an error, not something quietly dropped -- or smuggled
// into the query string.
func TestValidateRejectsUnknownArguments(t *testing.T) {
	v := mustCompile(t)
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"unknown group", map[string]any{
			"path": map[string]any{"widgetId": "3fa85f64-5717-4562-b3fc-2c963f66afa6"},
			"body": map[string]any{"anything": true},
		}},
		{"unknown query parameter", map[string]any{
			"path":  map[string]any{"widgetId": "3fa85f64-5717-4562-b3fc-2c963f66afa6"},
			"query": map[string]any{"admin": true},
		}},
		{"unknown path parameter", map[string]any{
			"path": map[string]any{"widgetId": "3fa85f64-5717-4562-b3fc-2c963f66afa6", "sneaky": "x"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(tt.args)
			if err == nil {
				t.Fatal("want a validation error")
			}
			if got := errs.ClassOf(err); got != errs.ClassUsage {
				t.Errorf("class = %q, want %q", got, errs.ClassUsage)
			}
		})
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	v := mustCompile(t)
	valid := "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"missing required group", map[string]any{"query": map[string]any{"limit": float64(1)}}},
		{"missing required member", map[string]any{"path": map[string]any{}}},
		{"wrong type", map[string]any{"path": map[string]any{"widgetId": float64(7)}}},
		{"bad format", map[string]any{"path": map[string]any{"widgetId": "not-a-uuid"}}},
		{"out of range", map[string]any{
			"path": map[string]any{"widgetId": valid}, "query": map[string]any{"limit": float64(1000)}}},
		{"non-integer number", map[string]any{
			"path": map[string]any{"widgetId": valid}, "query": map[string]any{"limit": 1.5}}},
		{"wrong item type", map[string]any{
			"path": map[string]any{"widgetId": valid}, "query": map[string]any{"tags": []any{1, 2}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.Validate(tt.args); err == nil {
				t.Fatal("want a validation error")
			}
		})
	}
}

func TestValidateMessageNamesTheLocation(t *testing.T) {
	v := mustCompile(t)
	err := v.Validate(map[string]any{"path": map[string]any{"widgetId": float64(7)}})
	if err == nil {
		t.Fatal("want a validation error")
	}
	if !strings.Contains(err.Error(), "/path/widgetId") {
		t.Errorf("error = %q, want it to point at /path/widgetId", err)
	}
	if strings.Count(err.Error(), "\n") != 0 {
		t.Errorf("error spans multiple lines, which a tool result should not: %q", err)
	}
}

func TestValidateMessageIsDeterministic(t *testing.T) {
	v := mustCompile(t)
	args := map[string]any{"path": map[string]any{"widgetId": float64(7), "nope": 1}}
	first := v.Validate(args)
	second := v.Validate(args)
	if first == nil || second == nil {
		t.Fatal("want validation errors")
	}
	if first.Error() != second.Error() {
		t.Errorf("messages differ across runs:\n%q\n%q", first, second)
	}
}

func TestCompileRejectsAnInvalidSchema(t *testing.T) {
	_, err := Compile("broken", map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "object", "pattern": "([a-z"},
	}})
	if err == nil {
		t.Fatal("want a compile error for an invalid pattern")
	}
	if got := errs.ClassOf(err); got != errs.ClassSpecInvalid {
		t.Errorf("class = %q, want %q", got, errs.ClassSpecInvalid)
	}
}

// A tool whose operation declares no parameters must still be callable with
// no arguments: the MCP client sends nothing, which is an empty argument
// object, not a JSON null.
func TestValidateAcceptsAbsentArgumentsForAParameterlessTool(t *testing.T) {
	v, err := Compile("getHealth", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		"additionalProperties": false,
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if err := v.Validate(nil); err != nil {
		t.Errorf("Validate(nil): %v", err)
	}
	if err := v.Validate(map[string]any{}); err != nil {
		t.Errorf("Validate(empty): %v", err)
	}
}
