package openapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// explodedSpec writes a small multi-file specification into a fresh directory
// and returns its resolved root.
func explodedSpec(t *testing.T) (root, spec string) {
	t.Helper()
	root = t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return root, writeExplodedSpec(t, root)
}

// writeExplodedSpec writes the referenced files into root and returns the root
// document, so a test can choose the path by which the root arrives.
func writeExplodedSpec(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "resources"), 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("resources/pet.yaml", `type: object
required: [name]
properties:
  name: { type: string }
  owner: { $ref: '../shared/person.yaml' }
`)
	if err := os.MkdirAll(filepath.Join(root, "shared"), 0o750); err != nil {
		t.Fatal(err)
	}
	write("shared/person.yaml", `type: object
properties:
  name: { type: string }
`)

	return `openapi: 3.0.3
info: { title: Exploded, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: './resources/pet.yaml' }
      responses: { "201": { description: created } }
`
}

// A spec split across files is translated, as long as every file it reaches
// for is inside the directory the document came from.
func TestFileReferencesInsideTheRootAreResolved(t *testing.T) {
	root, spec := explodedSpec(t)

	doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: root})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.HasErrors() {
		t.Fatalf("confined file references must resolve: %+v", doc.Diagnostics)
	}
	op := doc.Operations[0]
	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v", op.Support)
	}
	properties, _ := op.Input.Body.Schema["properties"].(map[string]any)
	if _, ok := properties["name"]; !ok {
		t.Errorf("body schema did not pick up the referenced file: %+v", op.Input.Body.Schema)
	}
	// The transitive reference (pet.yaml -> ../shared/person.yaml) resolved too.
	owner, _ := properties["owner"].(map[string]any)
	if owner == nil {
		t.Fatalf("transitive reference missing: %+v", properties)
	}
}

// The confinement check is the point: a reference that climbs out of the root
// is refused, whichever way it climbs.
func TestFileReferencesOutsideTheRootAreRefused(t *testing.T) {
	root, _ := explodedSpec(t)
	outside := filepath.Join(filepath.Dir(root), "outside.yaml")
	if err := os.WriteFile(outside, []byte("type: object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, tt := range []struct {
		name string
		ref  string
	}{
		{"parent directory", "'../outside.yaml'"},
		{"absolute path", "'" + outside + "'"},
		{"absolute path to a system file", "'/etc/passwd'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := `openapi: 3.0.3
info: { title: Escape, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: ` + tt.ref + ` }
      responses: { "201": { description: created } }
`
			doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: root})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !doc.HasErrors() {
				t.Fatal("a reference outside the root was accepted")
			}
			var found bool
			for _, d := range doc.Diagnostics {
				if d.Code == domain.ReasonRefOutsideRoot {
					found = true
					if !strings.Contains(d.Message, root) {
						t.Errorf("message = %q, want it to name the root", d.Message)
					}
				}
			}
			if !found {
				t.Errorf("diagnostics = %+v, want %s", doc.Diagnostics, domain.ReasonRefOutsideRoot)
			}
			if doc.Operations[0].Support.Level != domain.SupportRejected {
				t.Error("the operation holding the escaping reference must be rejected")
			}
		})
	}
}

// The root a caller hands in is not always the path the filesystem reports for
// the files under it: a macOS temporary directory reaches /private/var through
// /var, and a Windows one through an 8.3 short name. Confinement resolves
// symlinks on the candidate, so it has to resolve the root as well. Comparing a
// resolved candidate against an unresolved root refuses every legitimate $ref --
// safe, and wrong -- and resolving the root must not buy that back by letting a
// reference out of it.
func TestRootReachedThroughASymlink(t *testing.T) {
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	inside := filepath.Join(base, "spec")
	if err := os.MkdirAll(inside, 0o750); err != nil {
		t.Fatal(err)
	}
	spec := writeExplodedSpec(t, inside)
	root := filepath.Join(base, "link")
	if err := os.Symlink(inside, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	t.Run("its references still resolve", func(t *testing.T) {
		doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: root})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if doc.HasErrors() {
			t.Fatalf("a root reached through a symlink refused its own files: %+v", doc.Diagnostics)
		}
		properties, _ := doc.Operations[0].Input.Body.Schema["properties"].(map[string]any)
		if _, ok := properties["owner"]; !ok {
			t.Errorf("the transitive reference did not resolve: %+v", doc.Operations[0].Input.Body.Schema)
		}
	})

	t.Run("an escape from it is still refused", func(t *testing.T) {
		outside := filepath.Join(base, "outside.yaml")
		if err := os.WriteFile(outside, []byte("type: object\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		escaping := `openapi: 3.0.3
info: { title: Symlinked root, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: '../outside.yaml' }
      responses: { "201": { description: created } }
`
		doc, err := Parse(t.Context(), []byte(escaping), Options{RootPath: root})
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		var found bool
		for _, d := range doc.Diagnostics {
			if d.Code == domain.ReasonRefOutsideRoot {
				found = true
			}
		}
		if !found {
			t.Errorf("resolving the root let a reference out of it: %+v", doc.Diagnostics)
		}
	})
}

// A symlink is the oldest way out of a directory check, so the check resolves
// symlinks before comparing.
func TestSymlinkEscapeIsRefused(t *testing.T) {
	root, _ := explodedSpec(t)
	outside := filepath.Join(filepath.Dir(root), "symlinked.yaml")
	if err := os.WriteFile(outside, []byte("type: object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	link := filepath.Join(root, "innocent.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	spec := `openapi: 3.0.3
info: { title: Symlink, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: './innocent.yaml' }
      responses: { "201": { description: created } }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: root})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var found bool
	for _, d := range doc.Diagnostics {
		if d.Code == domain.ReasonRefOutsideRoot {
			found = true
		}
	}
	if !found {
		t.Errorf("a symlink out of the root was followed: %+v", doc.Diagnostics)
	}
}

// A document read from stdin has no directory, so no file reference can ever
// be resolved against it -- and the refusal says exactly that.
func TestFileReferencesFromStdinAreRefused(t *testing.T) {
	spec := `openapi: 3.0.3
info: { title: Stdin, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: './pet.yaml' }
      responses: { "201": { description: created } }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var found bool
	for _, d := range doc.Diagnostics {
		if d.Code == domain.ReasonExternalRefUnsupported && strings.Contains(d.Message, "stdin") {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %+v, want the stdin explanation", doc.Diagnostics)
	}
}

// Remote references are never fetched: a document must not be able to make
// lotsman issue a request of its choosing.
func TestRemoteReferencesAreNeverFetched(t *testing.T) {
	root, _ := explodedSpec(t)
	spec := `openapi: 3.0.3
info: { title: Remote, version: "1.0" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content:
          application/json:
            schema: { $ref: 'https://evil.example.com/schema.yaml#/Pet' }
      responses: { "201": { description: created } }
`
	doc, err := Parse(t.Context(), []byte(spec), Options{RootPath: root})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var found bool
	for _, d := range doc.Diagnostics {
		if d.Code == domain.ReasonExternalRefUnsupported && strings.Contains(d.Message, "remote") {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %+v, want a remote refusal", doc.Diagnostics)
	}
}

// Only referenced files are read. A secret sitting next to the spec is not
// part of the closure and is never opened, which is why the parser gets an
// allowlist instead of a directory.
func TestUnreferencedFilesAreNotInTheClosure(t *testing.T) {
	root, spec := explodedSpec(t)
	if err := os.WriteFile(filepath.Join(root, "secrets.yaml"), []byte("token: hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	scan, err := scanRefs([]byte(spec), root, Limits{})
	if err != nil {
		t.Fatalf("scanRefs: %v", err)
	}
	for _, file := range scan.files {
		if strings.Contains(file, "secrets") {
			t.Fatalf("an unreferenced file entered the closure: %v", scan.files)
		}
	}
	if len(scan.files) != 2 {
		t.Errorf("closure = %v, want exactly the two referenced documents", scan.files)
	}
}
