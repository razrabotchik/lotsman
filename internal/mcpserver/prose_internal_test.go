package mcpserver

import (
	"reflect"
	"testing"
)

// The 24 KB budget in search mode is paid by dropping prose, and this is what
// does the dropping (feature 002). It had no test.
//
// Two properties matter. Everything that is not prose survives byte for byte,
// because a schema with a constraint missing is a schema that validates the
// wrong thing — and the caller is told `descriptionsOmitted` rather than
// `constraintsOmitted`. And the input is not touched, because the catalog it
// came from is shared and immutable: pruning in place would strip the
// published schema for every later caller, including the ones in tools mode
// who never asked for a budget.
func TestWithoutProseDropsOnlyProse(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"title":       "Pet",
		"description": "a pet",
		"examples":    []any{map[string]any{"name": "Murka"}},
		"required":    []any{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "the pet's name",
				"minLength":   float64(1),
				"pattern":     "^[a-z]+$",
			},
			"tags": map[string]any{
				"type":  "array",
				"title": "Tags",
				"items": map[string]any{
					"type":        "string",
					"description": "one tag",
					"enum":        []any{"a", "b"},
				},
			},
			"owner": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "null", "description": "nobody"},
					map[string]any{"$ref": "#/$defs/Person", "title": "Person"},
				},
			},
		},
		"additionalProperties": false,
	}
	want := map[string]any{
		"type":     "object",
		"required": []any{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":      "string",
				"minLength": float64(1),
				"pattern":   "^[a-z]+$",
			},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": []any{"a", "b"}},
			},
			"owner": map[string]any{
				"oneOf": []any{
					map[string]any{"type": "null"},
					map[string]any{"$ref": "#/$defs/Person"},
				},
			},
		},
		"additionalProperties": false,
	}

	// A copy to compare against afterwards: the original must be untouched.
	before := deepCopy(schema)

	got := withoutProse(schema)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pruned schema =\n%#v\nwant\n%#v", got, want)
	}
	if !reflect.DeepEqual(schema, before) {
		t.Error("withoutProse mutated the schema it was given; the catalog it came from is shared")
	}
}

// A schema carrying no prose comes back equal to itself, so the budget path
// cannot quietly change a schema it had nothing to remove from.
func TestWithoutProseLeavesALeanSchemaAlone(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
	}
	if got := withoutProse(schema); !reflect.DeepEqual(got, schema) {
		t.Errorf("a schema with no prose changed:\n%#v", got)
	}
}

// The shapes that are easy to get wrong in a recursive prune.
func TestWithoutProseHandlesAwkwardShapes(t *testing.T) {
	cases := map[string]struct{ in, want map[string]any }{
		"an empty schema": {
			in:   map[string]any{},
			want: map[string]any{},
		},
		"prose inside a composed schema": {
			in: map[string]any{"allOf": []any{
				map[string]any{"type": "string", "title": "inner"},
				map[string]any{"minLength": float64(1)},
			}},
			want: map[string]any{"allOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"minLength": float64(1)},
			}},
		},
		"a malformed composition is left as it was found": {
			// `allOf` holding something that is not a schema is not a shape
			// lotsman can improve by guessing, and a prune is not the place
			// to start.
			in:   map[string]any{"allOf": []any{[]any{"not a schema"}}},
			want: map[string]any{"allOf": []any{[]any{"not a schema"}}},
		},
		"instance data that happens to look like prose": {
			// `enum`, `const` and `default` hold the API's own values. A
			// value with a `description` field is data, and walking into it
			// looking for keywords would edit the API.
			in: map[string]any{
				"title": "Status",
				"enum": []any{
					map[string]any{"code": "ok", "description": "everything is fine"},
				},
				"default": map[string]any{"code": "ok", "title": "the usual"},
				"const":   map[string]any{"description": "fixed"},
			},
			want: map[string]any{
				"enum": []any{
					map[string]any{"code": "ok", "description": "everything is fine"},
				},
				"default": map[string]any{"code": "ok", "title": "the usual"},
				"const":   map[string]any{"description": "fixed"},
			},
		},
		"a vendor extension is not something to guess about": {
			in: map[string]any{
				"type":          "string",
				"x-vendor-hint": map[string]any{"title": "keep me", "weight": float64(3)},
			},
			want: map[string]any{
				"type":          "string",
				"x-vendor-hint": map[string]any{"title": "keep me", "weight": float64(3)},
			},
		},
		"a boolean additionalProperties is not a schema": {
			in:   map[string]any{"additionalProperties": false, "title": "x"},
			want: map[string]any{"additionalProperties": false},
		},
		"a property legitimately named description": {
			// `properties.description` is a field of the *data*, not prose
			// about the schema. Dropping it would delete part of the API.
			in: map[string]any{"properties": map[string]any{
				"description": map[string]any{"type": "string", "description": "what it is"},
			}},
			want: map[string]any{"properties": map[string]any{
				"description": map[string]any{"type": "string"},
			}},
		},
		"a null value": {
			in:   map[string]any{"default": nil, "title": "x"},
			want: map[string]any{"default": nil},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := withoutProse(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got\n%#v\nwant\n%#v", got, tc.want)
			}
		})
	}
}

// deepCopy is the "before" picture a mutation test needs.
func deepCopy(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, deepCopy(item))
		}
		return out
	default:
		return value
	}
}
