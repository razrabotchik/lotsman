package openapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NFR-6 names refs as a surface to fuzz, and it had no target -- which is the
// one on the list with a file system behind it. A `$ref` is a string in an
// untrusted document that asks lotsman to open a file, so the question is not
// whether the answer is useful but whether it is ever a file the operator did
// not offer.
//
// The canary lives outside the spec root. No reference may reach it, by any
// spelling: `..`, an absolute path, a URL, percent-encoding, a null byte, a
// symlink, or a path that resolves back out through one.
func FuzzRefConfinement(f *testing.F) {
	for _, seed := range []string{
		"./inside.yaml",
		"../secret.yaml",
		"../../secret.yaml",
		"/etc/passwd",
		"secret.yaml",
		"link/../../secret.yaml",
		"%2e%2e%2fsecret.yaml",
		"..\\secret.yaml",
		"inside.yaml#/properties/name",
		"https://example.com/schema.yaml",
		"file:///etc/passwd",
		"\x00",
		"./" + strings.Repeat("a/../", 40) + "secret.yaml",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, ref string) {
		base := t.TempDir()
		if resolved, err := filepath.EvalSymlinks(base); err == nil {
			base = resolved
		}
		root := filepath.Join(base, "spec")
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatal(err)
		}
		// Outside the root, and the only place this string exists.
		const canary = "CANARY-ref-confinement-4d19-DO-NOT-READ"
		if err := os.WriteFile(filepath.Join(base, "secret.yaml"),
			[]byte("type: string\ntitle: "+canary+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "inside.yaml"),
			[]byte("type: object\nproperties:\n  name: { type: string }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A symlink pointing out of the root, so "resolve then compare" is
		// exercised rather than only "reject `..`".
		_ = os.Symlink(base, filepath.Join(root, "link"))

		document := []byte(`openapi: 3.0.3
info: { title: Fuzz, version: "1.0" }
paths:
  /things:
    post:
      operationId: createThing
      requestBody:
        content:
          application/json:
            schema: { $ref: ` + quoteYAML(ref) + ` }
      responses: { "201": { description: created } }
`)

		doc, err := Parse(t.Context(), document, Options{RootPath: root})
		if err != nil {
			return // refusing the whole document is always allowed
		}

		// Nothing outside the root was read: not into a schema, not into a
		// diagnostic, not into a title.
		if found := findCanary(doc, canary); found != "" {
			t.Fatalf("a reference reached outside the spec root: %s", found)
		}
		// And the manifest only ever names documents inside it.
		for _, reference := range doc.References {
			if strings.HasPrefix(reference.Path, "..") || strings.HasPrefix(reference.Path, "/") ||
				strings.Contains(reference.Path, "..") {
				t.Fatalf("the manifest names a document outside the root: %q", reference.Path)
			}
		}
	})
}

// quoteYAML renders a fuzzed string as a YAML scalar that survives the parser.
func quoteYAML(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case 0:
			out.WriteString(`\0`)
		default:
			if r < 0x20 {
				continue // a control character in a $ref is not what this tests
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// findCanary reports where the canary surfaced, or empty.
func findCanary(doc *Document, canary string) string {
	for i := range doc.Diagnostics {
		if strings.Contains(doc.Diagnostics[i].Message, canary) {
			return "diagnostic: " + doc.Diagnostics[i].Message
		}
	}
	for i := range doc.Operations {
		op := &doc.Operations[i]
		if op.Input.Body == nil {
			continue
		}
		if rendered := renderSchema(op.Input.Body.Schema); strings.Contains(rendered, canary) {
			return "body schema: " + rendered
		}
	}
	return ""
}

// renderSchema flattens a schema to text for the search above.
func renderSchema(schema map[string]any) string {
	var out strings.Builder
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				out.WriteString(key)
				out.WriteByte(' ')
				walk(nested)
			}
		case []any:
			for _, nested := range typed {
				walk(nested)
			}
		case string:
			out.WriteString(typed)
			out.WriteByte(' ')
		}
	}
	walk(schema)
	return out.String()
}
