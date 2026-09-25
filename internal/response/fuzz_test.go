package response

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// NFR-6 names response truncation as a surface to fuzz, and it was the one area
// on the list with no target. The body is the most attacker-shaped input the
// runtime handles: it comes from an API a model asked to call, at whatever size
// and encoding that API chose, and everything downstream treats the result as
// lotsman's own statement about it.
//
// The invariants are the promises the rest of the pipeline is built on:
//
//   - the result never exceeds the byte cap, whatever arrives;
//   - `truncated` is exactly "we cut it", so a caller can trust it as licence to
//     ask for less rather than a hint;
//   - a cut never leaves half a rune behind, because half a rune is how invalid
//     UTF-8 reaches a JSON encoder that then refuses the whole result;
//   - and shaping either succeeds or errors. A panic here is reachable from any
//     upstream that answers oddly.
func FuzzFromHTTPBoundsTheBody(f *testing.F) {
	f.Add("", 16)
	f.Add("{\"ok\":true}", 64)
	f.Add(strings.Repeat("a", 100), 10)
	f.Add("héllo wörld", 6)      // a cut that lands inside a two-byte rune
	f.Add("👋👋👋", 5)              // and inside a four-byte one
	f.Add("\x00\x01\xff\xfe", 3) // bytes that are not text at all
	f.Add(strings.Repeat("ю", 50), 0)

	f.Fuzz(func(t *testing.T, body string, limit int) {
		// A limit is a byte count an operator configured; the shape of the
		// input, not its sign, is what this is about.
		if limit < 0 {
			limit = -limit
		}
		if limit > 1<<16 {
			limit %= 1 << 16
		}

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       http.NoBody,
		}
		resp.Body = readerOf(body)

		result, err := FromHTTP(resp, limit)
		if err != nil {
			return // refusing is an acceptable answer; panicking is not
		}

		effective := limit
		if effective <= 0 {
			effective = MaxBodyBytes
		}
		if len(result.Body) > effective {
			t.Fatalf("body is %d bytes, cap is %d", len(result.Body), effective)
		}
		if result.Truncated != (len(body) > effective) {
			t.Fatalf("truncated = %v for a %d-byte body under a %d-byte cap",
				result.Truncated, len(body), effective)
		}
		if !result.Truncated && result.Body != body {
			t.Fatalf("an untruncated body was altered: %q -> %q", body, result.Body)
		}
		// A truncated result that started as valid text stays valid text: the
		// cut is moved back to a rune boundary rather than through one.
		if result.Truncated && utf8.ValidString(body) && !utf8.ValidString(result.Body) {
			t.Fatalf("truncation produced invalid UTF-8 from valid input: %q", result.Body)
		}
		if result.ReceivedBytes != len(result.Body) {
			t.Fatalf("receivedBytes = %d, body is %d bytes", result.ReceivedBytes, len(result.Body))
		}
	})
}

// readerOf is a body that reads a string once, the way an upstream does.
func readerOf(body string) *stringBody { return &stringBody{reader: strings.NewReader(body)} }

type stringBody struct{ reader *strings.Reader }

func (b *stringBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *stringBody) Close() error               { return nil }
