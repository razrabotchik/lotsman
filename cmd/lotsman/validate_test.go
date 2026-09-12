package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSpec(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// FR-77: the exit code is the contract. A pipeline has to be able to tell "the
// document is broken" from "the document is fine but nothing is usable" from
// "you called me wrong" without parsing prose.
func TestValidateExitCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec string
		want int
	}{
		{
			name: "usable document",
			spec: `openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      responses: { "200": { description: ok } }
`,
			want: 0,
		},
		{
			name: "not an OpenAPI document",
			spec: "hello: world\n",
			want: 3,
		},
		{
			name: "Swagger 2.0",
			spec: "swagger: \"2.0\"\ninfo: {title: X, version: \"1\"}\npaths: {}\n",
			want: 4,
		},
		{
			name: "valid but nothing publishable",
			spec: `openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /orders/{orderId}:
    get:
      operationId: getOrder
      responses: { "200": { description: ok } }
`,
			want: 4,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, code := runCLI(t, "validate", writeSpec(t, tt.spec))
			if code != tt.want {
				t.Errorf("exit = %d, want %d\n%s", code, tt.want, stderr)
			}
		})
	}

	if _, _, code := runCLI(t, "validate"); code != 2 {
		t.Errorf("missing SPEC exit = %d, want 2", code)
	}
}

func TestValidateQuietPrintsNothing(t *testing.T) {
	stdout, stderr, code := runCLI(t, "validate", writeSpec(t, "hello: world\n"), "--quiet")
	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("--quiet printed stdout=%q stderr=%q", stdout, stderr)
	}
}

// A document with a document-level error is unusable however many operations
// it enumerates.
func TestValidateRejectsDocumentLevelErrors(t *testing.T) {
	spec := writeSpec(t, `openapi: 3.0.3
info: { title: X, version: "1.0" }
paths:
  /pets:
    get:
      operationId: listPets
      parameters:
        - $ref: './missing.yaml#/Limit'
      responses: { "200": { description: ok } }
`)
	_, stderr, code := runCLI(t, "validate", spec)
	if code != 3 {
		t.Errorf("exit = %d, want 3\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "not usable") {
		t.Errorf("stderr = %q", stderr)
	}
}
