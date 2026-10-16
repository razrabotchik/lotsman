package specsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// JSON spells a character outside the Basic Multilingual Plane as a pair of
// \u escapes (RFC 8259 §7) -- the way every JSON encoder writes an emoji when
// asked for ASCII output. The YAML parser that reads JSON here refuses each
// half as an invalid code point, so a valid document (Todoist's) was refused
// as malformed.
func TestLoadAcceptsASurrogatePairEscapeInJSON(t *testing.T) {
	data := []byte(`{"openapi":"3.0.3","info":{"title":"Tasks \ud83d\ude00","version":"1"},"paths":{}}`)

	src, err := load(context.Background(), "test", "", bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Contains(src.Bytes, []byte("Tasks \U0001F600")) {
		t.Errorf("Bytes = %s, want the pair read as the one character it spells", src.Bytes)
	}
	// The digest identifies the document as authored: it is what an operator
	// compares against `sha256sum` of the file.
	sum := sha256.Sum256(data)
	if want := "sha256:" + hex.EncodeToString(sum[:]); src.Digest != want {
		t.Errorf("Digest = %q, want the authored bytes' %q", src.Digest, want)
	}
}

func TestJoinSurrogateEscapesTouchesOnlyPairsInsideStrings(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a pair", `{"a":"\ud83d\ude00"}`, "{\"a\":\"\U0001F600\"}"},
		{"upper-case hex", `{"a":"x\uD83D\uDE00y"}`, "{\"a\":\"x\U0001F600y\"}"},
		{"an escaped backslash is not an escape", `{"a":"\\ud83d\\ude00"}`, `{"a":"\\ud83d\\ude00"}`},
		{"a BMP escape is left alone", `{"a":"\u00e9"}`, `{"a":"\u00e9"}`},
		{"a lone high surrogate is left for the parser to refuse", `{"a":"\ud83d"}`, `{"a":"\ud83d"}`},
		{"a reversed pair is not a pair", `{"a":"\ude00\ud83d"}`, `{"a":"\ude00\ud83d"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(joinSurrogateEscapes([]byte(tc.in))); got != tc.want {
				t.Errorf("joinSurrogateEscapes(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// A lone surrogate is not text in any encoding; the document stays refused.
func TestLoadStillRefusesALoneSurrogateEscape(t *testing.T) {
	data := []byte(`{"openapi":"3.0.3","info":{"title":"\ud83d","version":"1"},"paths":{}}`)
	_, err := load(context.Background(), "test", "", bytes.NewReader(data), Options{})
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

// Whatever the document, the rewrite must not change what it says: a JSON
// parser reads the same value before and after.
func FuzzJoinSurrogateEscapesPreservesTheJSONValue(f *testing.F) {
	for _, seed := range []string{
		`{"a":"\ud83d\ude00"}`, `["x\uD83D\uDE00y", "\\ud83d"]`, `{"\ud83d\ude00":1}`,
		`"\ude00\ud83d"`, `{"a":"\"\ud83d\ude00\""}`, `{}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if !json.Valid(data) {
			return
		}
		var before, after any
		if err := json.Unmarshal(data, &before); err != nil {
			return
		}
		rewritten := joinSurrogateEscapes(data)
		if err := json.Unmarshal(rewritten, &after); err != nil {
			t.Fatalf("rewrite of %q is not JSON: %q (%v)", data, rewritten, err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("rewrite of %q changed its value: %#v -> %#v", data, before, after)
		}
	})
}
