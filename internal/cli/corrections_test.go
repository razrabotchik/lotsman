package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/openapi"
)

func TestReloadAcceptsCatalogChangesAndRefusesPolicyChanges(t *testing.T) {
	t.Parallel()
	running := config.Runtime{AllowMutations: false}

	catalogOnly := running
	catalogOnly.Overrides = []config.OperationOverride{{Match: config.Match{OperationID: "x"}, MediaType: "application/json"}}
	catalogOnly.ComponentOverrides = []config.ComponentOverride{{Match: config.ComponentMatch{Schema: "Pet"}, Patch: config.Patch{"type": "object"}}}
	catalogOnly.Overlays = []string{"/tmp/fix.yaml"}
	catalogOnly.IncludeTags = []string{"pets"}
	catalogOnly.Mode = "search"
	got, mode, err := reloadRuntime(running, "cfg.yaml", func() (config.Runtime, error) { return catalogOnly, nil })
	if err != nil || len(got.Overrides) != 1 || len(got.ComponentOverrides) != 1 || mode != "search" {
		t.Fatalf("catalog change: got %+v, mode %q, err %v", got, mode, err)
	}

	policy := catalogOnly
	policy.AllowMutations = true
	policy.BaseURL = "https://example.test"
	_, _, err = reloadRuntime(running, "cfg.yaml", func() (config.Runtime, error) { return policy, nil })
	if err == nil || !strings.Contains(err.Error(), "changed in execution.allowMutations, execution.baseURL;") {
		t.Fatalf("policy change: err = %v", err)
	}
}

// Every Runtime field outside the catalog sections has a configuration
// spelling, so a refusal names a line the operator can find.
func TestEveryNonCatalogRuntimeFieldHasAConfigurationSpelling(t *testing.T) {
	t.Parallel()
	fields := reflect.TypeOf(config.Runtime{})
	catalogOnly := map[string]bool{
		"Overrides": true, "ComponentOverrides": true, "Overlays": true, "IncludeTags": true,
		"Mode": true, "MaxSerializedBytes": true, "DescriptionBytesPerTool": true,
		"ConfigDir": true, // the same file is the same directory; it cannot change on reload
	}
	for i := range fields.NumField() {
		name := fields.Field(i).Name
		if !catalogOnly[name] && configSpelling(name) == name {
			t.Errorf("Runtime.%s has no configuration spelling", name)
		}
	}
}

func TestReloadRefusesAConfigurationThatFailsToResolve(t *testing.T) {
	t.Parallel()
	_, _, err := reloadRuntime(config.Runtime{}, "cfg.yaml", func() (config.Runtime, error) {
		return config.Runtime{}, os.ErrNotExist
	})
	if err == nil {
		t.Fatal("a configuration that fails to resolve was accepted")
	}
}

func TestReloadWithoutAConfigurationKeepsTheRunningOne(t *testing.T) {
	t.Parallel()
	running := config.Runtime{Mode: "search"}
	got, mode, err := reloadRuntime(running, "", func() (config.Runtime, error) {
		t.Error("resolve must not be called without a configuration file")
		return config.Runtime{}, nil
	})
	if err != nil || got.Mode != "search" || mode != "search" {
		t.Fatalf("got %+v %q %v", got, mode, err)
	}
}

// A reload of an unchanged configuration file is not a change: resolving the
// same file twice must compare equal everywhere, or every SIGHUP would be
// refused with a spurious "changed in".
func TestReloadOfAnUnchangedConfigurationIsAccepted(t *testing.T) {
	t.Parallel()
	running := config.Runtime{
		AllowMutations: true, BaseURL: "https://api.example.test",
		AuthProfiles: map[string]config.Profile{"main": {Scheme: "bearer"}},
		Overrides:    []config.OperationOverride{{Match: config.Match{OperationID: "x"}, MediaType: "application/json"}},
	}
	if _, _, err := reloadRuntime(running, "cfg.yaml", func() (config.Runtime, error) { return running, nil }); err != nil {
		t.Fatalf("identical runtime: %v", err)
	}

	path := filepath.Join(t.TempDir(), "lotsman.yaml")
	writeTestFile(t, path, `apiVersion: lotsman.dev/v1alpha1
execution:
  allowMutations: true
  baseURL: https://api.example.test
  allowRules: [{ tag: pets }]
authProfiles:
  main: { scheme: bearer, tokenRef: "env:TOKEN" }
server:
  transport: http
  allowedHosts: [api.example.test]
componentOverrides:
  - { match: { schema: Pet }, patch: { properties: { age: { type: string } } } }
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: limit, in: query, schema: { type: string } }]
`)
	resolve := func() (config.Runtime, error) {
		file, err := config.Load(path)
		if err != nil {
			return config.Runtime{}, err
		}
		return config.Resolve(file, config.Environment{}, config.Overrides{}), nil
	}
	first, err := resolve()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reloadRuntime(first, path, resolve); err != nil {
		t.Fatalf("same file resolved twice: %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const twoPatchTypes = `openapi: 3.0.3
info: { title: M, version: "1" }
paths:
  /x:
    patch:
      operationId: patchX
      requestBody:
        content:
          application/merge-patch+json: { schema: { type: object } }
          application/strategic-merge-patch+json: { schema: { type: object } }
      responses: { "200": { description: ok } }
`

// A media type written the way an operator types it is the type the document
// declares: configuration and document share one normalizer.
func TestCorrectionsFromPinsAMediaTypeAsWritten(t *testing.T) {
	t.Parallel()
	file, err := config.Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: patchX }
    mediaType: "APPLICATION/Merge-Patch+JSON; charset=utf-8"
    note: merge patch is what the API takes
`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	runtime := config.Resolve(file, config.Environment{}, config.Overrides{})
	doc, err := openapi.Parse(t.Context(), []byte(twoPatchTypes), openapi.Options{Corrections: correctionsFrom(runtime)})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.CorrectionProblems) != 0 {
		t.Fatalf("problems = %v", doc.CorrectionProblems)
	}
	op := doc.Operations[0]
	if op.Support.Level != domain.SupportSupported || op.Input.Body.MediaType != "application/merge-patch+json" {
		t.Fatalf("support = %+v, media = %q", op.Support, op.Input.Body.MediaType)
	}
	if len(op.Corrections) != 1 || op.Corrections[0].Note != "merge patch is what the API takes" {
		t.Fatalf("corrections = %v", op.Corrections)
	}
}

// Decisions alone (effect, enabled, authProfile) are the catalog's and never
// reach the adapter; indices are the configuration's, not the filtered list's.
func TestCorrectionsFromKeepsConfigurationIndices(t *testing.T) {
	t.Parallel()
	yes := true
	c := correctionsFrom(config.Runtime{
		Overrides: []config.OperationOverride{
			{Match: config.Match{OperationID: "a"}, Effect: "read"},
			{Match: config.Match{OperationID: "b"}, Enabled: &yes},
			{Match: config.Match{Method: "GET", Path: "/c"}, RequestBody: &config.BodyOverride{Patch: config.Patch{"required": []any{"x"}}}},
		},
		ComponentOverrides: []config.ComponentOverride{{Match: config.ComponentMatch{Schema: "Pet"}, Patch: config.Patch{"type": "object"}, Note: "n"}},
	})
	if len(c.Operations) != 1 || c.Operations[0].Index != 2 || c.Operations[0].Method != http.MethodGet ||
		c.Operations[0].Path != "/c" || c.Operations[0].BodyPatch == nil {
		t.Fatalf("operations = %+v", c.Operations)
	}
	if len(c.Components) != 1 || c.Components[0].Index != 0 || c.Components[0].Schema != "Pet" || c.Components[0].Note != "n" {
		t.Fatalf("components = %+v", c.Components)
	}
}

// runInspect runs `inspect` over a spec and a configuration written to a
// fresh directory.
func runInspect(t *testing.T, spec, cfg string) (stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	specPath, cfgPath := filepath.Join(dir, "openapi.yaml"), filepath.Join(dir, "lotsman.yaml")
	writeTestFile(t, specPath, spec)
	writeTestFile(t, cfgPath, cfg)
	var out, errOut strings.Builder
	code = inspect(context.Background(), []string{"--config", cfgPath, specPath}, &out, &errOut)
	return out.String(), errOut.String(), code
}

const listItemsSpec = `openapi: 3.0.3
info: { title: S, version: "1" }
paths:
  /items:
    get:
      operationId: listItems
      parameters: [{ name: limit, in: query, schema: { type: integer } }]
      responses: { "200": { description: ok } }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
`

// A corrected schema that does not compile is the operator's error, reported
// before anything is served -- not at the first call.
func TestInspectRefusesACorrectedSchemaThatDoesNotCompile(t *testing.T) {
	t.Parallel()
	_, stderr, code := runInspect(t, listItemsSpec, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: limit, in: query, schema: { type: string, pattern: "(" } }]
`)
	if code != exitUsage || !strings.Contains(stderr, "does not compile") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

// Every correction problem is reported, not the first.
func TestInspectReportsEveryCorrectionProblem(t *testing.T) {
	t.Parallel()
	_, stderr, code := runInspect(t, listItemsSpec, `apiVersion: lotsman.dev/v1alpha1
componentOverrides:
  - { match: { schema: Pet }, patch: { required: [age] } }
  - { match: { schema: Dog }, patch: { type: object } }
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: offset, in: query, schema: { type: string } }]
`)
	if code != exitUsage {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	for _, want := range []string{"componentOverrides[1]", `"Dog"`, "operationOverrides[0]", `"offset"`} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr does not mention %s:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "componentOverrides[0]") {
		t.Fatalf("a correction that applied was reported:\n%s", stderr)
	}
}

// A correction that applies is reported with its provenance.
func TestInspectCountsCorrectedOperations(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := runInspect(t, listItemsSpec, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: limit, in: query, schema: { type: string } }]
    note: the API takes strings
`)
	if code != exitOK {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if !strings.Contains(strings.ToLower(stdout), "correct") {
		t.Fatalf("report does not mention the correction:\n%s", stdout)
	}
}

// A correction naming no operation, or more than one, is refused by name
// rather than applied to fewer or more operations than the operator meant.
func TestInspectRefusesAnOperationCorrectionThatMatchesNoneOrMany(t *testing.T) {
	t.Parallel()
	duplicated := `openapi: 3.0.3
info: { title: S, version: "1" }
paths:
  /a:
    get:
      operationId: dup
      parameters: [{ name: limit, in: query, schema: { type: integer } }]
      responses: { "200": { description: ok } }
  /b:
    get:
      operationId: dup
      parameters: [{ name: limit, in: query, schema: { type: integer } }]
      responses: { "200": { description: ok } }
`
	cases := []struct{ name, operationID, want string }{
		{"none", "nosuch", "matches no operation"},
		{"many", "dup", "matches 2 operations"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, stderr, code := runInspect(t, duplicated, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: `+tc.operationID+` }
    parameters: [{ name: limit, in: query, schema: { type: string } }]
`)
			if code != exitUsage || !strings.Contains(stderr, "operationOverrides[0]") || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit %d, stderr %s", code, stderr)
			}
		})
	}
}

const nestedComponentSpec = `openapi: 3.0.3
info: { title: N, version: "1" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "201": { description: ok } }
components:
  schemas:
    Pet:
      type: object
      properties:
        age: { type: integer }
        owner:
          type: object
          properties:
            name: { type: string }
            phone: { type: integer }
`

// The whole path a component correction takes -- YAML, config.Parse,
// correctionsFrom, openapi.Parse -- with a patch nested two objects deep: the
// leaf changes, its siblings stay, and the published schema says so.
func TestANestedComponentPatchReachesThePublishedSchema(t *testing.T) {
	t.Parallel()
	file, err := config.Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
componentOverrides:
  - match: { schema: Pet }
    patch:
      properties:
        owner:
          properties:
            phone: { type: string, pattern: "^[0-9+]+$" }
          required: [name]
    note: phones have a plus
`))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	runtime := config.Resolve(file, config.Environment{}, config.Overrides{})
	doc, err := openapi.Parse(t.Context(), []byte(nestedComponentSpec), openapi.Options{Corrections: correctionsFrom(runtime)})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.CorrectionProblems) != 0 {
		t.Fatalf("problems = %v", doc.CorrectionProblems)
	}
	op := doc.Operations[0]
	pet := op.Input.Defs["Pet"]
	props, _ := pet["properties"].(map[string]any)
	owner, _ := props["owner"].(map[string]any)
	ownerProps, _ := owner["properties"].(map[string]any)
	phone, _ := ownerProps["phone"].(map[string]any)
	name, _ := ownerProps["name"].(map[string]any)
	age, _ := props["age"].(map[string]any)
	if phone["type"] != "string" || phone["pattern"] != "^[0-9+]+$" {
		t.Fatalf("phone = %v (Pet = %v)", phone, pet)
	}
	if name["type"] != "string" || age["type"] != "integer" {
		t.Fatalf("siblings were replaced rather than merged: Pet = %v", pet)
	}
	if req, _ := owner["required"].([]any); len(req) != 1 || req[0] != "name" {
		t.Fatalf("owner.required = %v", owner["required"])
	}
	if len(op.Corrections) != 1 || op.Corrections[0].Target != "component Pet" || op.Corrections[0].Note != "phones have a plus" {
		t.Fatalf("corrections = %v", op.Corrections)
	}
}

// A mapping with a non-string key once hid a $ref from every check, and the
// schema compiler then read the file it named. The configuration refuses it.
func TestInspectRefusesAPatchWithANonStringKey(t *testing.T) {
	t.Parallel()
	secret := filepath.Join(t.TempDir(), "secret.json")
	writeTestFile(t, secret, `{"type":"string"}`)
	_, stderr, code := runInspect(t, listItemsSpec, `apiVersion: lotsman.dev/v1alpha1
componentOverrides:
  - match: { schema: Pet }
    patch:
      properties:
        1: { type: string }
        age: { $ref: 'file://`+secret+`' }
`)
	if code != exitUsage || !strings.Contains(stderr, "key 1 is not a string") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}

// Overlays are named relative to the configuration's directory, `..` and all:
// two files called fix.yaml are two names, and none of them is a host path.
func TestOverlaysAreNamedRelativeToTheConfigurationAndInTheManifest(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "cfg")
	for _, d := range []string{filepath.Join(dir, "fixes"), filepath.Join(parent, "shared")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	overlay := `overlay: 1.0.0
info: { title: fix, version: "1" }
actions:
  - target: $.info
    update: { description: fixed }
`
	writeTestFile(t, filepath.Join(dir, "fixes", "fix.yaml"), overlay)
	writeTestFile(t, filepath.Join(parent, "shared", "fix.yaml"), overlay)
	specPath := filepath.Join(dir, "openapi.yaml")
	writeTestFile(t, specPath, listItemsSpec)

	report := func(cfg string) inspectDocument {
		t.Helper()
		cfgPath := filepath.Join(dir, "lotsman.yaml")
		writeTestFile(t, cfgPath, "apiVersion: lotsman.dev/v1alpha1\n"+cfg)
		var out, errOut strings.Builder
		if code := inspect(context.Background(), []string{"--config", cfgPath, "--json", specPath}, &out, &errOut); code != exitOK {
			t.Fatalf("exit %d: %s", code, errOut.String())
		}
		var doc inspectDocument
		if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		return doc
	}
	plain := report("")
	both := report("spec:\n  overlays: [./fixes/fix.yaml, ../shared/fix.yaml]\n")
	names := []string{}
	for _, o := range both.Spec.Overlays {
		names = append(names, o.Path)
	}
	if want := []string{"fixes/fix.yaml", "../shared/fix.yaml"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("overlay names = %v, want %v", names, want)
	}
	if strings.Contains(strings.Join(names, " "), parent) {
		t.Fatalf("an overlay name leaks the host path: %v", names)
	}
	one := report("spec:\n  overlays: [./fixes/fix.yaml]\n")
	if plain.Spec.ManifestDigest == one.Spec.ManifestDigest || one.Spec.ManifestDigest == both.Spec.ManifestDigest {
		t.Fatalf("manifest digests: plain %s, one %s, both %s", plain.Spec.ManifestDigest, one.Spec.ManifestDigest, both.Spec.ManifestDigest)
	}
	if plain.Spec.Digest != one.Spec.Digest {
		t.Fatal("the root document's own digest moved with an overlay")
	}
}

// `operations` refuses a corrected schema that does not compile, as every
// other command reading the configuration does.
func TestOperationsRefusesACorrectedSchemaThatDoesNotCompile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	specPath, cfgPath := filepath.Join(dir, "openapi.yaml"), filepath.Join(dir, "lotsman.yaml")
	writeTestFile(t, specPath, listItemsSpec)
	writeTestFile(t, cfgPath, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: limit, in: query, schema: { type: string, pattern: "(" } }]
`)
	var out, errOut strings.Builder
	code := operations(context.Background(), []string{"--config", cfgPath, specPath}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "does not compile") {
		t.Fatalf("exit %d, stderr %s", code, errOut.String())
	}
}

// A reload that fails on a new, broken overlay still watches it: fixing that
// file is the edit --watch must notice.
func TestAFailedLoadWatchesTheConfiguredOverlays(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	specPath, broken := filepath.Join(dir, "openapi.yaml"), filepath.Join(dir, "broken.yaml")
	writeTestFile(t, specPath, listItemsSpec)
	writeTestFile(t, broken, "overlay: 1.0.0\ninfo: { title: b, version: \"1\" }\nactions:\n  - target: $.paths['/gone'].get\n    update: { summary: x }\n")
	watched := &watchSet{config: filepath.Join(dir, "lotsman.yaml")}
	earlier := filepath.Join(dir, "earlier.yaml")
	watched.paths.Store(&[]string{earlier})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := loadCatalog(t.Context(), specPath, logger, config.Runtime{Overlays: []string{broken}}, "tools", watched)
	if err == nil {
		t.Fatal("a broken overlay loaded")
	}
	got := watched.documents()
	for _, want := range []string{earlier, broken, watched.config} {
		if !slices.Contains(got, want) {
			t.Fatalf("watched = %v, want %s among them", got, want)
		}
	}
}
