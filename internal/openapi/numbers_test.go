package openapi

import (
	"encoding/json"
	"testing"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
)

// A bound is the number the document wrote. As a float64, int64's own limit
// is 2^63 -- one past it -- and an argument of 9223372036854775808 passes a
// maximum the API will reject it for.
func TestNumericConstraintsKeepTheDocumentsNumbers(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          application/json:
            schema:
              type: object
              properties:
                id: { type: integer, minimum: -9223372036854775808, maximum: 9223372036854775807 }
                ratio: { type: number, maximum: 0.1000000000000000055511151231257827, exclusiveMaximum: true }
                kind: { type: integer, enum: [18446744073709551617] }
                page: { type: integer, minimum: 1, maximum: 100 }`))

	properties, _ := op.Input.Body.Schema["properties"].(map[string]any)
	field := func(name, keyword string) any {
		schema, _ := properties[name].(map[string]any)
		return schema[keyword]
	}
	for _, tc := range []struct {
		name, keyword string
		want          any
	}{
		{"id", "minimum", json.Number("-9223372036854775808")},
		{"id", "maximum", json.Number("9223372036854775807")},
		{"ratio", "exclusiveMaximum", json.Number("0.1000000000000000055511151231257827")},
		// A bound a float64 holds exactly is published exactly as before.
		{"page", "minimum", float64(1)},
		{"page", "maximum", float64(100)},
	} {
		if got := field(tc.name, tc.keyword); got != tc.want {
			t.Errorf("%s.%s = %#v, want %#v", tc.name, tc.keyword, got, tc.want)
		}
	}
	if got, _ := field("kind", "enum").([]any); len(got) != 1 || got[0] != json.Number("18446744073709551617") {
		t.Errorf("kind.enum = %#v, want the literal", field("kind", "enum"))
	}

	validator, err := argvalidate.Compile("t", map[string]any{
		"type":       "object",
		"properties": map[string]any{"body": op.Input.Body.Schema},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for literal, valid := range map[string]bool{
		`{"body":{"id":9223372036854775807}}`:    true,
		`{"body":{"id":9223372036854775808}}`:    false,
		`{"body":{"id":-9223372036854775809}}`:   false,
		`{"body":{"kind":18446744073709551617}}`: true,
		`{"body":{"kind":18446744073709551616}}`: false,
	} {
		args, err := argvalidate.Decode([]byte(literal))
		if err != nil {
			t.Fatalf("decode %s: %v", literal, err)
		}
		if err := validator.Validate(args); (err == nil) != valid {
			t.Errorf("%s: valid = %v, want %v (%v)", literal, err == nil, valid, err)
		}
	}
}
