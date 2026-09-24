package openapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// A path template that cannot become the request the document describes is
// refused while the document is being read, rather than producing a request
// that quietly differs or an error that blames the operator's configuration.
func TestUnrequestablePathsRejectTheOperation(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{
			// net/url refuses these anyway -- but at call time, with a
			// message about the base URL, sending an operator to check a
			// setting that is fine.
			name: "a control character",
			path: "/pets/A\rB",
		},
		{
			name: "a newline",
			path: "/pets/A\nB",
		},
		{
			// Everything after it silently disappears from the request, and
			// lotsman does not approximate a call.
			name: "a query separator",
			path: "/pets/A?x=1",
		},
		{
			name: "a fragment separator",
			path: "/pets/A#frag",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := parseOne(t, pathSpec(tc.path))
			if op.Support.Level != domain.SupportRejected {
				t.Fatalf("support = %q, want rejected", op.Support.Level)
			}
			if !hasReason(op.Support.Reasons, domain.ReasonInvalidPath) {
				t.Errorf("reasons = %v, want %s", op.Support.Reasons, domain.ReasonInvalidPath)
			}
		})
	}
}

// The diagnostic does not quote the template: it is the untrusted half, and
// it travels into a log and a report from here.
func TestThePathRefusalDoesNotQuoteTheTemplate(t *testing.T) {
	const canary = "SYSTEM-IGNORE-PREVIOUS"
	op := parseOne(t, pathSpec("/pets/"+canary+"?x=1"))
	if len(op.Diagnostics) == 0 {
		t.Fatal("no diagnostic was produced, so this asserts nothing")
	}
	var saw bool
	for _, d := range op.Diagnostics {
		if d.Code != domain.ReasonInvalidPath {
			continue
		}
		saw = true
		if strings.Contains(d.Message, canary) {
			t.Errorf("the diagnostic quotes the template it is refusing: %s", d.Message)
		}
	}
	if !saw {
		t.Errorf("no %s diagnostic among %d", domain.ReasonInvalidPath, len(op.Diagnostics))
	}
}

// Markup is a display problem, not a request problem: it is cleaned where it
// is shown (catalog.Tool.SafePath) and does not stop the operation, because
// the API really does have that path.
func TestAPathIsNotRejectedForBeingUglyToDisplay(t *testing.T) {
	for _, path := range []string{
		"/pets/<b>x</b>",
		"/v2/droplets/{droplet_id}/actions",
		"/pets/a b",
		"/файлы/{id}",
	} {
		op := parseOne(t, pathSpec(path))
		if op.Support.Level != domain.SupportSupported {
			t.Errorf("path %q: support = %q, want supported", path, op.Support.Level)
		}
	}
}

// pathSpec is a document with one operation at the given path.
func pathSpec(path string) []byte {
	encoded, err := json.Marshal(path)
	if err != nil {
		panic(err)
	}
	placeholders := strings.Count(path, "{")
	parameters := ""
	if placeholders > 0 {
		name := path[strings.Index(path, "{")+1 : strings.Index(path, "}")]
		parameters = `
      parameters:
        - name: ` + name + `
          in: path
          required: true
          schema: { type: string }`
	}
	return []byte(`openapi: 3.0.3
info: { title: Paths, version: "1.0" }
paths:
  ` + string(encoded) + `:
    get:
      operationId: listPets` + parameters + `
      responses: { "200": { description: ok } }
`)
}
