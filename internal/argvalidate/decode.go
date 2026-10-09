package argvalidate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// Bounds on a number lotsman will carry as the literal it arrived as. Far
// beyond any API's integers (int64 is nineteen digits, a SQL decimal
// thirty-eight) and beyond float64's range, and small enough that the exact
// arithmetic the validator does on a literal costs nothing: "1e-100000000" is
// a short string and a forty-megabyte power of ten.
const (
	maxNumberLength   = 100
	maxNumberExponent = 400
)

// Decode reads tool arguments from the JSON the client sent, keeping every
// number as the literal it was.
//
// The obvious decoding, into map[string]any, turns every number into a
// float64, and a float64 holds integers exactly only up to 2^53: an id of
// 9007199254740993 reached the API as 9007199254740992 -- a different
// resource, sent without an error. Decoding with UseNumber keeps the
// literal, which the validator checks exactly and the request builder writes
// back out unchanged.
//
// No arguments at all is an empty argument object, as it is to the SDK.
func Decode(raw []byte) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "argvalidate: arguments are not JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errs.Errorf(errs.ClassUsage, "argvalidate: arguments are followed by more JSON")
	}
	args, ok := value.(map[string]any)
	if !ok {
		return nil, errs.Errorf(errs.ClassUsage, "argvalidate: arguments must be a JSON object")
	}
	if err := checkNumbers(args); err != nil {
		return nil, err
	}
	return args, nil
}

// checkNumbers refuses a number whose exact value would be expensive to
// compute, before anything computes it.
func checkNumbers(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for _, item := range v {
			if err := checkNumbers(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := checkNumbers(item); err != nil {
				return err
			}
		}
	case json.Number:
		if !Carried(v) {
			return errs.Errorf(errs.ClassUsage,
				"argvalidate: a number argument is longer than %d characters or has an exponent beyond ±%d",
				maxNumberLength, maxNumberExponent)
		}
	}
	return nil
}

// Carried reports whether a number literal is within the bounds lotsman
// carries exactly.
func Carried(n json.Number) bool {
	literal := string(n)
	if len(literal) > maxNumberLength {
		return false
	}
	i := strings.IndexAny(literal, "eE")
	if i < 0 {
		return true
	}
	exponent, err := strconv.Atoi(literal[i+1:])
	return err == nil && exponent >= -maxNumberExponent && exponent <= maxNumberExponent
}
