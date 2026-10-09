package argvalidate

import (
	"encoding/json"
	"strings"
	"testing"
)

// A number comes out as the literal that went in, and the validator checks
// that literal rather than its float64 neighbour.
func TestDecodeKeepsNumbersExact(t *testing.T) {
	args, err := Decode([]byte(`{"path":{"id":9007199254740993},"body":{"amounts":[0.1000000000000000055511151231257827]}}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Re-encoding writes a json.Number as its literal and a float64 as its
	// shortest form, so the bytes say which one came out.
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, want := range []string{`"id":9007199254740993`, `"amounts":[0.1000000000000000055511151231257827]`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("decoded %s, want it to carry %s", encoded, want)
		}
	}

	validator, err := Compile("t", map[string]any{
		"type": "object",
		"properties": map[string]any{"path": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "integer", "maximum": json.Number("9007199254740992")}},
		}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// As a float64 the id equals the maximum; as the number it is, it is one
	// past it.
	if err := validator.Validate(args); err == nil {
		t.Error("9007199254740993 passed a maximum of 9007199254740992")
	}
}

func TestDecodeRefusesWhatIsNotAnArgumentObject(t *testing.T) {
	for name, raw := range map[string]string{
		"an array":               `[1]`,
		"a scalar":               `1`,
		"trailing data":          `{} {}`,
		"not JSON":               `{`,
		"a long number":          `{"n":` + strings.Repeat("1", 101) + `}`,
		"a large exponent":       `{"n":1e401}`,
		"a small exponent":       `{"n":1e-100000000}`,
		"a nested large literal": `{"q":{"list":[1,2,1e999]}}`,
	} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// No arguments is an empty argument object, as it is to the SDK.
func TestDecodeReadsNothingAsEmpty(t *testing.T) {
	for _, raw := range []string{"", "  ", "null"} {
		args, err := Decode([]byte(raw))
		if err != nil || args == nil || len(args) != 0 {
			t.Errorf("%q decoded to %#v, %v", raw, args, err)
		}
	}
}
