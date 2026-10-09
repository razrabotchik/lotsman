// Package jsonnum carries JSON numbers as the literals they were written as.
//
// A float64 holds integers exactly only up to 2^53, and decimals almost never:
// an id of 9007199254740993 or a bound of 9223372036854775807 becomes a
// neighbouring number the moment it is decoded into one, without an error. The
// literal is the value, so lotsman keeps it -- in arguments, in schema bounds
// -- and this package is what it may safely do with one.
package jsonnum

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Bounds on a number lotsman will carry as the literal it arrived as. Far
// beyond any API's integers (int64 is nineteen digits, a SQL decimal
// thirty-eight) and beyond float64's range, and small enough that exact
// arithmetic on a literal costs nothing: "1e-100000000" is a short string and
// a forty-megabyte power of ten.
const (
	MaxLength   = 100
	MaxExponent = 400
)

// ErrNotCarried is a literal outside the bounds, or not a JSON number.
var ErrNotCarried = errors.New("not a JSON number within the carried bounds")

// Carried reports whether a literal is within the bounds.
func Carried(n json.Number) bool {
	literal := string(n)
	if len(literal) > MaxLength {
		return false
	}
	i := strings.IndexAny(literal, "eE")
	if i < 0 {
		return true
	}
	exponent, err := strconv.Atoi(literal[i+1:])
	return err == nil && exponent >= -MaxExponent && exponent <= MaxExponent
}

// Plain writes a literal as the exact decimal it denotes, in the one spelling
// every parser reads: no exponent, and no zeros that carry no value. JSON
// spells one number many ways -- "1e3", "1000.0" and "1000" are the same
// integer to a schema, and only the last is an integer to an API reading a
// query string. The decimal point is moved by the exponent as text, so there
// is no arithmetic and nothing to round.
func Plain(n json.Number) (string, error) {
	literal := string(n)
	if !Carried(n) {
		return "", ErrNotCarried
	}
	sign := ""
	if strings.HasPrefix(literal, "-") {
		sign, literal = "-", literal[1:]
	}
	mantissa, exponent := literal, 0
	if i := strings.IndexAny(literal, "eE"); i >= 0 {
		parsed, err := strconv.Atoi(literal[i+1:])
		if err != nil {
			return "", ErrNotCarried
		}
		mantissa, exponent = literal[:i], parsed
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	if whole == "" || strings.Trim(whole+fraction, "0123456789") != "" {
		return "", ErrNotCarried
	}

	digits := whole + fraction
	point := len(whole) + exponent
	if point < 0 {
		digits = strings.Repeat("0", -point) + digits
		point = 0
	}
	if point > len(digits) {
		digits += strings.Repeat("0", point-len(digits))
	}
	whole = strings.TrimLeft(digits[:point], "0")
	fraction = strings.TrimRight(digits[point:], "0")
	if whole == "" {
		whole = "0"
	}
	if fraction != "" {
		return sign + whole + "." + fraction, nil
	}
	if whole == "0" {
		// Negative zero is zero to every reader.
		return "0", nil
	}
	return sign + whole, nil
}

// Exact returns the value a literal denotes, given what a decoder already
// made of it: the float64 itself when that is the same number, so nothing
// changes where nothing was lost, and the literal where the float64 is a
// neighbour. A literal that is not a carried JSON number -- YAML's 0x1F, .inf
// -- leaves the float64 as it was.
func Exact(literal string, decoded float64) any {
	if !json.Valid([]byte(literal)) {
		// Plain is lenient about what it is never handed by a JSON decoder
		// ("01"); a YAML scalar can be anything, and a json.Number that is
		// not JSON fails every later Marshal.
		return decoded
	}
	exact, err := Plain(json.Number(literal))
	if err != nil {
		return decoded
	}
	shortest, err := Plain(json.Number(strconv.FormatFloat(decoded, 'g', -1, 64)))
	if err != nil || shortest == exact {
		return decoded
	}
	return json.Number(literal)
}
