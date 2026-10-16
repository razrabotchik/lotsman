package specsource

import (
	"bytes"
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

// readableAsYAML returns the bytes the YAML parser is given for data.
//
// JSON is read as YAML everywhere downstream, and the two disagree on one
// thing: JSON spells a character outside the Basic Multilingual Plane as a
// surrogate pair of \u escapes (RFC 8259 §7), and the YAML parser refuses each
// half as an invalid code point. A valid JSON document with an escaped emoji
// was refused as malformed. Writing the pair as the character it spells is
// what a JSON parser would read; nothing else in the document changes.
//
// Only a document that is valid JSON is touched, so a YAML document -- whose
// quoting rules are not JSON's -- is never reinterpreted.
func readableAsYAML(data []byte) []byte {
	if !bytes.Contains(data, []byte(`\u`)) || !json.Valid(data) {
		return data
	}
	return joinSurrogateEscapes(data)
}

// joinSurrogateEscapes rewrites each \uD8xx\uDCxx escape inside a JSON string
// as the UTF-8 character it encodes. Everything else is copied: a BMP escape,
// an escaped backslash, and a lone or reversed surrogate, which is not text and
// is left for the parser to refuse.
func joinSurrogateEscapes(data []byte) []byte {
	var out []byte // allocated on the first pair; most documents have none
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			if out != nil {
				out = append(out, c)
			}
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			if r, ok := surrogatePairAt(data, i); ok {
				if out == nil {
					out = append(make([]byte, 0, len(data)), data[:i]...)
				}
				out = utf8.AppendRune(out, r)
				i += 11 // two \uXXXX escapes are 12 bytes; the loop takes the last step
				continue
			}
			// Any other escape is two bytes as far as string state goes: the
			// escaped character -- a quote, a backslash -- ends nothing.
			if out != nil {
				out = append(out, data[i:min(i+2, len(data))]...)
			}
			i++
			continue
		}
		if out != nil {
			out = append(out, c)
		}
	}
	if out == nil {
		return data
	}
	return out
}

// surrogatePairAt decodes a high and a low surrogate escape starting at i.
func surrogatePairAt(data []byte, i int) (rune, bool) {
	high, ok := escapeAt(data, i)
	if !ok || !utf16.IsSurrogate(high) || high >= 0xDC00 {
		return 0, false
	}
	low, ok := escapeAt(data, i+6)
	if !ok || low < 0xDC00 || low > 0xDFFF {
		return 0, false
	}
	return utf16.DecodeRune(high, low), true
}

// escapeAt reads a \uXXXX escape starting at i.
func escapeAt(data []byte, i int) (rune, bool) {
	if i+6 > len(data) || data[i] != '\\' || data[i+1] != 'u' {
		return 0, false
	}
	var r rune
	for _, h := range data[i+2 : i+6] {
		var digit byte
		switch {
		case h >= '0' && h <= '9':
			digit = h - '0'
		case h >= 'a' && h <= 'f':
			digit = h - 'a' + 10
		case h >= 'A' && h <= 'F':
			digit = h - 'A' + 10
		default:
			return 0, false
		}
		r = r<<4 | rune(digit)
	}
	return r, true
}
