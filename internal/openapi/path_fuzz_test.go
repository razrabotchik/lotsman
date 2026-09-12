package openapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// FuzzPathTemplate covers pitfall #3: a path template whose placeholders do
// not match the declared parameters. The invariant is not "lotsman parses it"
// but "lotsman never calls it": an operation may only be executable when every
// placeholder in its template has a required path parameter to fill it.
func FuzzPathTemplate(f *testing.F) {
	for _, seed := range []struct{ template, param string }{
		{"/pets", "petId"},
		{"/pets/{petId}", "petId"},
		{"/pets/{petId}", "other"},
		{"/pets/{petId}/{petId}", "petId"},
		{"/pets/{}", ""},
		{"/pets/{pet id}", "pet id"},
		{"/{a}/{b}", "a"},
		{"/pets/{petId", "petId"},
		{"/pets/}", "petId"},
		{"/{петId}", "петId"},
		{"/pets/{petId}.json", "petId"},
	} {
		f.Add(seed.template, seed.param)
	}

	f.Fuzz(func(t *testing.T, template, parameter string) {
		// Keep the fuzzer on the question being asked: YAML-safe, path-shaped
		// inputs, not arbitrary documents (stage 0 has its own target).
		if !safeForYAML(template) || !safeForYAML(parameter) || !strings.HasPrefix(template, "/") {
			t.Skip()
		}

		spec := fmt.Sprintf(`openapi: 3.0.3
info: { title: Paths, version: "1.0" }
paths:
  %q:
    get:
      operationId: probe
      parameters:
        - { name: %q, in: path, required: true, schema: { type: string } }
      responses: { "200": { description: ok } }
`, template, parameter)

		doc, err := Parse(t.Context(), []byte(spec), Options{})
		if err != nil {
			return // a document lotsman refuses outright is a fine outcome
		}
		for i := range doc.Operations {
			op := &doc.Operations[i]
			if !op.Executable() {
				continue
			}
			// Executable means the request builder will be asked to fill this
			// template, so every placeholder must have a parameter.
			for _, match := range pathPlaceholder.FindAllStringSubmatch(op.PathTemplate, -1) {
				var found bool
				for _, p := range op.Input.Parameters {
					if p.In == domain.LocationPath && p.Name == match[1] && p.Required {
						found = true
					}
				}
				if !found {
					t.Fatalf("executable operation %q has placeholder %q with no required path parameter (declared %q)",
						op.PathTemplate, match[1], parameter)
				}
			}
		}
	})
}

// safeForYAML keeps the fuzzer from spending its time proving that arbitrary
// bytes are not a YAML document.
func safeForYAML(s string) bool {
	if len(s) > 120 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}
