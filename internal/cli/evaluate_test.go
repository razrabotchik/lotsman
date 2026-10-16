package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/workbench"
)

const evaluateSpec = `openapi: 3.0.3
info: { title: T, version: "1" }
paths:
  /x:
    get: { operationId: getX, responses: { "200": { description: ok } } }
  /y:
    patch:
      operationId: patchY
      parameters:
        - { name: filter, in: query, style: deepObject, schema: { type: object } }
      requestBody:
        content:
          application/merge-patch+json: { schema: { type: object } }
          application/json-patch+json: { schema: { type: array } }
      responses: { "200": { description: ok } }
`

func evaluateDir(t *testing.T, spec string) (dir, cfg string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "lotsman.yaml")
}

func operation(t *testing.T, snap *workbench.Snapshot, id string) *workbench.Operation {
	t.Helper()
	for i := range snap.Operations {
		if snap.Operations[i].OperationID == id {
			return &snap.Operations[i]
		}
	}
	t.Fatalf("no operation %s in %+v", id, snap.Operations)
	return nil
}

// Review focus 5: the workbench must show a broken state, not exit on it.
func TestEvaluateReportsInsteadOfFailing(t *testing.T) {
	t.Parallel()
	dir, cfg := evaluateDir(t, "swagger: \"2.0\"\n")
	spec := filepath.Join(dir, "openapi.yaml")

	// A document that does not parse.
	snap, cat, _ := evaluate(t.Context(), []byte("apiVersion: lotsman.dev/v1alpha1\nspec: {source: ./openapi.yaml}\n"), pipelineInputs{configPath: cfg})
	if !strings.Contains(strings.ToLower(snap.SpecError), "swagger") || cat != nil {
		t.Fatalf("spec error = %q", snap.SpecError)
	}

	// No document at all.
	snap, _, _ = evaluate(t.Context(), nil, pipelineInputs{configPath: cfg})
	if snap.SpecError == "" {
		t.Fatal("no document, no error")
	}

	// A configuration that does not validate still shows the document.
	if err := os.WriteFile(spec, []byte(evaluateSpec), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, _, _ = evaluate(t.Context(), []byte("apiVersion: lotsman.dev/v1alpha1\nbogus: 1\n"), pipelineInputs{configPath: cfg, spec: spec})
	if snap.ConfigError == "" || len(snap.Operations) != 2 {
		t.Fatalf("config error = %q, ops = %d", snap.ConfigError, len(snap.Operations))
	}

	// A stale correction is a problem with its match, not an exit.
	snap, _, _ = evaluate(t.Context(), []byte(`apiVersion: lotsman.dev/v1alpha1
spec: {source: ./openapi.yaml}
operationOverrides:
  - match: { operationId: getX }
    parameters: [{ name: gone, in: query, schema: { type: string } }]
`), pipelineInputs{configPath: cfg})
	if len(snap.Problems) != 1 || snap.Problems[0].Match == nil || snap.Problems[0].Match.OperationID != "getX" {
		t.Fatalf("problems = %+v", snap.Problems)
	}
	if len(snap.Operations) != 2 {
		t.Fatalf("operations = %d", len(snap.Operations))
	}

	// An override that matches nothing is a problem too, and the tree still shows.
	snap, _, _ = evaluate(t.Context(), []byte(`apiVersion: lotsman.dev/v1alpha1
spec: {source: ./openapi.yaml}
operationOverrides:
  - match: { operationId: noSuchOperation }
    effect: write
`), pipelineInputs{configPath: cfg})
	if len(snap.Problems) != 1 || !strings.Contains(snap.Problems[0].Message, "noSuchOperation") || len(snap.Operations) != 2 {
		t.Fatalf("problems = %+v, ops = %d", snap.Problems, len(snap.Operations))
	}
}

// The snapshot carries what the form needs: the offered media types, every
// declared parameter with why it was refused, and the override in force.
func TestEvaluateSnapshotsTheFormInputs(t *testing.T) {
	t.Parallel()
	_, cfg := evaluateDir(t, evaluateSpec)
	data := []byte(`apiVersion: lotsman.dev/v1alpha1
spec: {source: ./openapi.yaml}
operationOverrides:
  - match: { method: get, path: /x }
    effect: read
`)
	snap, _, _ := evaluate(t.Context(), data, pipelineInputs{configPath: cfg})
	if snap.SpecError != "" || snap.ConfigError != "" {
		t.Fatalf("snapshot = %+v", snap)
	}
	y := operation(t, snap, "patchY")
	if strings.Join(y.MediaTypes, ",") != "application/json-patch+json,application/merge-patch+json" {
		t.Fatalf("media types = %v", y.MediaTypes)
	}
	if y.Status != workbench.StatusDecision || len(y.Parameters) != 1 || y.Parameters[0].Reason != "unsupported_parameter_style" {
		t.Fatalf("patchY = %+v", y)
	}
	x := operation(t, snap, "getX")
	if x.Override == nil || x.Override.Match.Path != "/x" || x.Status != workbench.StatusWorks {
		t.Fatalf("getX = %+v", x)
	}
}

// The workbench lists credentials by name and scheme, and never resolves the
// secret behind one: a reference to an unset variable is not an error here.
func TestEvaluateNeverResolvesASecret(t *testing.T) {
	t.Parallel()
	_, cfg := evaluateDir(t, evaluateSpec)
	snap, _, _ := evaluate(t.Context(), []byte(`apiVersion: lotsman.dev/v1alpha1
spec: {source: ./openapi.yaml}
authProfiles:
  main: { scheme: bearer, tokenRef: "env:LOTSMAN_TEST_SURELY_UNSET_TOKEN" }
`), pipelineInputs{configPath: cfg})
	if snap.ConfigError != "" || len(snap.Profiles) != 1 || snap.Profiles[0] != (workbench.Profile{Name: "main", Scheme: "bearer"}) {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestPipelineExplainUnderADraft(t *testing.T) {
	t.Parallel()
	_, cfg := evaluateDir(t, evaluateSpec)
	pipe := newPipeline(pipelineInputs{configPath: cfg})
	data := []byte("apiVersion: lotsman.dev/v1alpha1\nspec: {source: ./openapi.yaml}\n")
	snap, err := pipe.Evaluate(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	key := operation(t, snap, "getX").Key
	text, err := pipe.Explain(t.Context(), data, key, []byte(`{}`))
	if err != nil || !strings.Contains(text, "/x") {
		t.Fatalf("explain = %q, %v", text, err)
	}
	if _, err := pipe.Explain(t.Context(), data, "default:GET:/nowhere", nil); err == nil {
		t.Fatal("explained an operation that does not exist")
	}
}

// Spec §5: a document that does not parse still opens the workbench, which
// shows why instead of exiting.
func TestTheWorkbenchOpensOnABrokenDocument(t *testing.T) {
	t.Parallel()
	_, cfg := evaluateDir(t, "swagger: \"2.0\"\n")
	if err := os.WriteFile(cfg, []byte("apiVersion: lotsman.dev/v1alpha1\nspec: {source: ./openapi.yaml}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wb, err := workbench.New(t.Context(), cfg, "", newPipeline(pipelineInputs{configPath: cfg}))
	if err != nil {
		t.Fatalf("the workbench did not open: %v", err)
	}
	st, err := wb.State(t.Context())
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Draft == nil || st.Draft.SpecError == "" || len(st.Draft.Operations) != 0 {
		t.Fatalf("draft = %+v", st.Draft)
	}
}

const fixOverlay = "overlay: 1.0.0\ninfo: { title: fix, version: \"1\" }\nactions:\n  - target: $.info\n    update: { description: fixed }\n"

// The landing panel edits the overlays list by handing back the whole list:
// the snapshot spells each path as the file does, so adding one leaves the
// others written as they were.
func TestAddingAnOverlayKeepsTheOthersSpelling(t *testing.T) {
	t.Parallel()
	dir, cfg := evaluateDir(t, evaluateSpec)
	for _, name := range []string{"a.overlay.yaml", "sub/b.overlay.yaml", "c.overlay.yaml"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(fixOverlay), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	source := "apiVersion: lotsman.dev/v1alpha1\nspec:\n  source: ./openapi.yaml\n  overlays:\n    - ./a.overlay.yaml\n    - " + filepath.Join(dir, "sub", "b.overlay.yaml") + "\n"
	if err := os.WriteFile(cfg, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	wb, err := workbench.New(t.Context(), cfg, "", newPipeline(pipelineInputs{configPath: cfg}))
	if err != nil {
		t.Fatal(err)
	}
	st, err := wb.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Draft.SpecError != "" || len(st.Draft.Overlays) != 2 || st.Draft.Overlays[0] != "./a.overlay.yaml" {
		t.Fatalf("overlays = %q (%s)", st.Draft.Overlays, st.Draft.SpecError)
	}
	if _, err := wb.Apply(t.Context(), workbench.Entry{Kind: "overlays", Overlays: append(st.Draft.Overlays, "c.overlay.yaml")}); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(cfg)
	if want := source + "    - c.overlay.yaml\n"; string(out) != want {
		t.Fatalf("saved:\n%s\nwant:\n%s", out, want)
	}
}

// An overlay path typed in the browser that names a directory, a FIFO or a
// huge file is refused before anything reads it.
func TestOverlaysMustBeSmallRegularFiles(t *testing.T) {
	t.Parallel()
	dir, cfg := evaluateDir(t, evaluateSpec)
	huge := filepath.Join(dir, "huge.yaml")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxOverlayBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for name, want := range map[string]string{dir: "not a regular file", huge: "larger than"} {
		data := []byte("apiVersion: lotsman.dev/v1alpha1\nspec:\n  source: ./openapi.yaml\n  overlays: [" + name + "]\n")
		snap, cat, _ := evaluate(t.Context(), data, pipelineInputs{configPath: cfg})
		if cat != nil || !strings.Contains(snap.SpecError, want) || !strings.Contains(snap.SpecError, name) {
			t.Errorf("%s: spec error = %q", name, snap.SpecError)
		}
	}
}
