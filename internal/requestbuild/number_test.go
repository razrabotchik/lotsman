package requestbuild

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// A number literal reaches a URL as the exact decimal it denotes, in the one
// spelling a query-string parser reads: no exponent, no zeros that carry no
// value, no rounding anywhere.
func TestANumberIsWrittenAsItsExactDecimal(t *testing.T) {
	cases := map[string]string{
		"9007199254740993":                     "9007199254740993",
		"9223372036854775807":                  "9223372036854775807",
		"-9223372036854775808":                 "-9223372036854775808",
		"18446744073709551616":                 "18446744073709551616",
		"0.1000000000000000055511151231257827": "0.1000000000000000055511151231257827",
		"1e3":                                  "1000",
		"1E+2":                                 "100",
		"1000.0":                               "1000",
		"1.50":                                 "1.5",
		"12.345e1":                             "123.45",
		"1.5e-3":                               "0.0015",
		"-2.5e-1":                              "-0.25",
		"0":                                    "0",
		"0.000":                                "0",
		"-0":                                   "0",
		"-0.0e5":                               "0",
		"1e20":                                 "100000000000000000000",
	}
	for literal, want := range cases {
		got, err := scalarString("n", json.Number(literal))
		if err != nil {
			t.Errorf("%s: %v", literal, err)
			continue
		}
		if got != want {
			t.Errorf("%s was written as %s, want %s", literal, got, want)
		}
	}
}

// Expanding an exponent writes its zeros out, so the exponent is bounded
// before anything is written; and a literal that is not a JSON number is not
// guessed at.
func TestANumberLotsmanWillNotExpandIsRefused(t *testing.T) {
	for _, literal := range []string{
		"1e401", "1e-401", "1e999999999", strings.Repeat("9", 101),
		"", "-", "1.2.3", "0x1F", "1e", "+1",
	} {
		if got, err := scalarString("n", json.Number(literal)); err == nil {
			t.Errorf("%q was written as %q", literal, got)
		} else if class := errs.ClassOf(err); class != errs.ClassUsage {
			t.Errorf("%q: class = %s, want usage", literal, class)
		}
	}
}
