package jsonnum

import (
	"encoding/json"
	"testing"
)

// Where a float64 is the number, it stays: a schema that was right does not
// change by a byte. Where it is a neighbour, the literal takes its place.
func TestExactReplacesOnlyWhatAFloatLost(t *testing.T) {
	cases := []struct {
		literal string
		want    any
	}{
		{"10", float64(10)},
		{"0.1", float64(0.1)},
		{"1e3", float64(1000)},
		{"1.50", float64(1.5)},
		{"9007199254740992", float64(9007199254740992)},
		{"9007199254740993", json.Number("9007199254740993")},
		{"9223372036854775807", json.Number("9223372036854775807")},
		{"0.1000000000000000055511151231257827", json.Number("0.1000000000000000055511151231257827")},
		// Not JSON: the decoder's reading stands.
		{"0x1F", float64(31)},
		{"+12", float64(12)},
		{"01", float64(1)},
		{".inf", float64(0)},
	}
	for _, tc := range cases {
		var decoded float64
		switch want := tc.want.(type) {
		case float64:
			decoded = want
		case json.Number:
			decoded, _ = want.Float64()
		}
		if got := Exact(tc.literal, decoded); got != tc.want {
			t.Errorf("Exact(%q) = %#v, want %#v", tc.literal, got, tc.want)
		}
	}
}
