package openapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A remote reference is refused by the scan, and the parser is configured never
// to fetch one either. Two layers, and until this test only the first was
// observable: a mutation that turned the parser's own switch back on was caught
// by nothing, because the scan makes it unreachable.
//
// That is what defence in depth looks like when it works, and it is also how a
// backstop rots. This test watches the wire instead of the diagnostics: whatever
// spelling arrives, no request leaves the process. It would fail if either layer
// went, not only the outer one.
//
// The spellings matter. The scan's test is a case-sensitive prefix, so `HTTPS://`
// takes a different path through the code than `https://` -- it becomes a file
// path, is confined, and is refused for being unreadable. Different reason, same
// answer, and the point is that the answer is the same.
func TestNoSpellingOfARemoteReferenceReachesTheNetwork(t *testing.T) {
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte("type: object\nproperties:\n  owned: { type: string }\n"))
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	for _, reference := range []string{
		upstream.URL + "/schema.yaml",
		strings.ToUpper("HTTP://") + host + "/schema.yaml",
		"HtTp://" + host + "/schema.yaml",
		"//" + host + "/schema.yaml",
		"http://user:token@" + host + "/schema.yaml",
		upstream.URL + "/schema.yaml#/properties/owned",
	} {
		t.Run(reference, func(t *testing.T) {
			root, _ := explodedSpec(t)
			document := []byte(`openapi: 3.0.3
info: { title: Remote, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: '` + reference + `' }
      responses: { "201": { description: created } }
`)
			before := requests.Load()
			doc, err := Parse(t.Context(), document, Options{RootPath: root})
			if got := requests.Load() - before; got != 0 {
				t.Fatalf("%d request(s) were made for %q", got, reference)
			}
			if err != nil {
				return // refused outright, which is an answer
			}
			// And it was refused rather than quietly resolved to something.
			if !doc.HasErrors() {
				t.Errorf("a remote reference was accepted without a diagnostic: %+v", doc.Diagnostics)
			}
		})
	}
}
