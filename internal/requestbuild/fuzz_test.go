package requestbuild_test

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/requestbuild"
)

// A model's argument becomes part of a URL and a header, which makes this the
// one place where text chosen by the untrusted side of the conversation turns
// into the request lotsman actually sends. Everything else in the pipeline
// decides *whether* to send something; this decides what.
//
// So the invariants are about what can never come out, whatever goes in:
//
//   - a path value cannot add a segment, a query or a fragment: `../admin` and
//     `a/b` stay one segment, because the template is the contract with the API
//     and an argument may fill a slot, not restructure the path;
//   - a header value cannot carry CR or LF, which is header injection;
//   - the host is the one the operator authorized, whatever the argument says;
//   - and the builder either produces a request or refuses. It does not panic,
//     because a panic on the runtime path is a denial of service reachable from
//     a tool call.
func FuzzBuildKeepsArgumentsInTheirSlot(f *testing.F) {
	f.Add("ordinary", "value", "1")
	f.Add("../../admin", "x", "1")
	f.Add("a/b/c", "?q=1", "1")
	f.Add("%2e%2e%2f", "#frag", "1")
	f.Add("x\r\nX-Injected: yes", "y\r\nSet-Cookie: a=b", "1")
	f.Add("\x00", "\x7f", "1")
	f.Add("👋 space", "a b&c=d", "1")
	f.Add(strings.Repeat("z", 4096), "", "1")

	f.Fuzz(func(t *testing.T, pathValue, queryValue, headerValue string) {
		op := &requestbuild.Operation{
			Method:       "GET",
			PathTemplate: "/v1/things/{id}/children",
			Servers:      []string{"https://api.example.com/base"},
			Parameters: []domain.Parameter{
				{Name: "id", In: domain.LocationPath, Required: true,
					Style: domain.StyleSimple, Schema: domain.Schema{"type": "string"}},
				{Name: "q", In: domain.LocationQuery,
					Style: domain.StyleForm, Schema: domain.Schema{"type": "string"}},
				{Name: "X-Trace", In: domain.LocationHeader,
					Style: domain.StyleSimple, Schema: domain.Schema{"type": "string"}},
			},
		}
		args := requestbuild.Arguments{
			"path":   map[string]any{"id": pathValue},
			"query":  map[string]any{"q": queryValue},
			"header": map[string]any{"X-Trace": headerValue},
		}

		req, err := requestbuild.Build(t.Context(), op, args, requestbuild.Options{})
		if err != nil {
			return // refusing is always an acceptable answer
		}

		if req.URL.Host != "api.example.com" {
			t.Fatalf("an argument moved the request to %q", req.URL.Host)
		}
		if req.URL.Fragment != "" {
			t.Fatalf("an argument introduced a fragment: %q", req.URL.Fragment)
		}
		// EscapedPath is what goes on the wire: the server's own /base, then the
		// template's three segments with the parameter filled in. Five in total,
		// and an argument may not add a sixth.
		const (
			wantSegments = 5
			filledSlot   = 3
		)
		segments := strings.Split(strings.TrimPrefix(req.URL.EscapedPath(), "/"), "/")
		if len(segments) != wantSegments {
			t.Fatalf("the path gained or lost a segment: %q -> %v",
				req.URL.EscapedPath(), segments)
		}
		if strings.ContainsAny(segments[filledSlot], "/?#") {
			t.Fatalf("the filled path segment carries a separator unencoded: %q", segments[filledSlot])
		}
		for name, values := range req.Header {
			for _, value := range values {
				if strings.ContainsAny(value, "\r\n") {
					t.Fatalf("header %q carries CR or LF: %q", name, value)
				}
			}
		}
		if _, ok := req.Header["X-Injected"]; ok {
			t.Fatal("an argument added a header of its own")
		}
	})
}
