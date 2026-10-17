package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
)

// fakePipeline turns a configuration into a snapshot by reading it: one
// operation, whose effect is whatever the first override with an effect
// says. It reports the override naming the operation the way the real
// pipeline does, so the workbench's entry identity can be exercised.
func fakePipeline() Pipeline {
	return fakePipelineWith(nil)
}

// fakePipelineWith calls hook (when set) at the start of every evaluation.
func fakePipelineWith(hook func()) Pipeline {
	return Pipeline{
		Evaluate: func(_ context.Context, data []byte) (*Snapshot, error) {
			if hook != nil {
				hook()
			}
			op := Operation{Key: "default:POST:/pets", Method: "POST", Path: "/pets", OperationID: "createPet",
				Support: "supported", Effect: "unknown", Executable: false}
			if file, err := config.Parse(data); err == nil {
				for i := range file.OperationOverrides {
					o := &file.OperationOverrides[i]
					if o.Match.OperationID == "createPet" || (o.Match.Method == http.MethodPost && o.Match.Path == "/pets") {
						entry := file.OperationOverrides[i]
						op.Override = &entry
					}
					if o.Effect != "" {
						op.Effect = o.Effect
					}
					if o.MediaType == "bad/type" {
						return &Snapshot{Operations: []Operation{op}, Problems: []Problem{{Section: "operationOverrides", Message: "bad media type"}}}, nil
					}
				}
			} else if strings.TrimSpace(string(data)) != "" {
				return &Snapshot{ConfigError: err.Error()}, nil
			}
			Classify(&op)
			return &Snapshot{Operations: []Operation{op}}, nil
		},
		Explain: func(context.Context, []byte, string, []byte) (string, error) { return "explained", nil },
	}
}

const withComment = `# keep this comment
apiVersion: lotsman.dev/v1alpha1
spec:
  source: ./openapi.yaml # and this one
operationOverrides:
  # an unrelated override
  - match: { operationId: other }
    enabled: false
`

func newBench(t *testing.T, content string) (wb *Workbench, path string) {
	t.Helper()
	return newBenchWith(t, content, fakePipeline())
}

func newBenchWith(t *testing.T, content string, pipe Pipeline) (wb *Workbench, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "lotsman.yaml")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wb, err := New(t.Context(), path, "./openapi.yaml", pipe)
	if err != nil {
		t.Fatal(err)
	}
	return wb, path
}

func createPetEffect(effect string) Entry {
	return Entry{Kind: "operation", OperationKey: "default:POST:/pets",
		Operation: &config.OperationOverride{Match: config.Match{OperationID: "createPet"}, Effect: effect}}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// Review focus 1: comments next to unrelated entries survive edit and save.
func TestSaveKeepsCommentsAndUnrelatedEntries(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, withComment)
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	for _, want := range []string{"# keep this comment", "# and this one", "# an unrelated override", "operationId: other", "operationId: createPet", "effect: write"} {
		if !strings.Contains(out, want) {
			t.Errorf("saved file lost %q:\n%s", want, out)
		}
	}
}

func TestANewConfigurationNamesTheDocument(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, "")
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	if !strings.Contains(out, "source: ./openapi.yaml") || !strings.Contains(out, "apiVersion: lotsman.dev/v1alpha1") {
		t.Fatalf("new file:\n%s", out)
	}
}

// A file of comments only is an empty configuration the person has started:
// it evaluates as it is, and the comments stay on the first save.
func TestACommentOnlyFileKeepsItsComment(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, "# my notes about this API\n")
	st, err := wb.State(t.Context())
	if err != nil {
		t.Fatalf("state of a comment-only file: %v", err)
	}
	if len(st.Entries) != 0 || st.Draft == nil {
		t.Fatalf("state = %+v", st)
	}
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	for _, want := range []string{"# my notes about this API", "apiVersion: lotsman.dev/v1alpha1", "effect: write"} {
		if !strings.Contains(out, want) {
			t.Errorf("saved file lacks %q:\n%s", want, out)
		}
	}
}

// A file that is not a mapping cannot be edited, but it can be looked at: the
// state reports it rather than failing, and an edit is refused, not written.
func TestAListFileIsShownButNotEdited(t *testing.T) {
	t.Parallel()
	const list = "- a\n- b\n"
	wb, path := newBench(t, list)
	st, err := wb.State(t.Context())
	if err != nil {
		t.Fatalf("state of a list file: %v", err)
	}
	if st.Draft == nil || st.Draft.ConfigError == "" {
		t.Fatalf("the snapshot does not report the broken configuration: %+v", st.Draft)
	}
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); !errors.Is(err, errNotMapping) {
		t.Fatalf("err = %v, want errNotMapping", err)
	}
	if got := readFile(t, path); got != list {
		t.Fatalf("file changed:\n%s", got)
	}
}

// Review focus 2: a hand edit while the UI is open.
func TestSaveRefusesAChangedFileAndKeepsTheDraft(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, withComment)
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(withComment+"# edited by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	st, err := wb.Reload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 1 {
		t.Fatalf("draft lost: %+v", st.Entries)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	if !strings.Contains(out, "# edited by hand") || !strings.Contains(out, "effect: write") {
		t.Fatalf("saved:\n%s", out)
	}
}

// The pipeline runs between the first hash check and the write; a hand edit
// in that window must still be caught.
func TestSaveChecksTheDiskAgainRightBeforeWriting(t *testing.T) {
	t.Parallel()
	var path string
	var edit atomic.Bool
	pipe := fakePipelineWith(func() {
		if edit.CompareAndSwap(true, false) {
			_ = os.WriteFile(path, []byte(withComment+"# edited during save\n"), 0o600)
		}
	})
	wb, p := newBenchWith(t, withComment, pipe)
	path = p
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	edit.Store(true)
	if _, err := wb.Save(t.Context(), nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
	out := readFile(t, path)
	if !strings.Contains(out, "# edited during save") || strings.Contains(out, "effect: write") {
		t.Fatalf("the hand edit was overwritten:\n%s", out)
	}
	st, err := wb.State(t.Context())
	if err != nil || len(st.Entries) != 1 {
		t.Fatalf("draft lost: %v %+v", err, st)
	}
}

// Review focus 4: widening needs an acknowledgement, enforced here, not in the browser.
func TestWideningNeedsAcknowledgement(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, withComment)
	st, err := wb.Apply(t.Context(), createPetEffect("read"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Comparison.Widening) != 1 || st.Comparison.Widening[0].Kind != "effect-read" {
		t.Fatalf("widening = %+v", st.Comparison.Widening)
	}
	if _, err := wb.Save(t.Context(), nil); !errors.Is(err, ErrUnacknowledged) {
		t.Fatalf("err = %v, want ErrUnacknowledged", err)
	}
	if _, err := wb.Save(t.Context(), []string{"something-else"}); !errors.Is(err, ErrUnacknowledged) {
		t.Fatalf("err = %v, want ErrUnacknowledged for the wrong acknowledgement", err)
	}
	if strings.Contains(readFile(t, path), "effect: read") {
		t.Fatal("an unacknowledged widening was written")
	}
	if _, err := wb.Save(t.Context(), []string{st.Comparison.Widening[0].ID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, path), "effect: read") {
		t.Fatal("the acknowledged change was not written")
	}
}

func TestAnEntryThatAddsAProblemIsNotTaken(t *testing.T) {
	t.Parallel()
	wb, _ := newBench(t, withComment)
	e := createPetEffect("")
	e.Operation.MediaType = "bad/type"
	if _, err := wb.Apply(t.Context(), e); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	st, _ := wb.State(t.Context())
	if len(st.Entries) != 0 {
		t.Fatalf("draft took a rejected entry: %+v", st.Entries)
	}
}

// An entry names its operation once: a match beside the override that says
// something else is refused, not guessed at.
func TestAnEntryWhoseMatchesDisagreeIsRejected(t *testing.T) {
	t.Parallel()
	wb, _ := newBench(t, withComment)
	e := createPetEffect("write")
	e.Match = &config.Match{OperationID: "other"}
	if _, err := wb.Apply(t.Context(), e); !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
	if _, err := wb.Apply(t.Context(), Entry{Kind: "operation", Operation: &config.OperationOverride{Effect: "write"}}); !errors.Is(err, ErrRejected) {
		t.Fatalf("an entry without a match: err = %v, want ErrRejected", err)
	}
	st, _ := wb.State(t.Context())
	if len(st.Entries) != 0 {
		t.Fatalf("draft took a rejected entry: %+v", st.Entries)
	}
}

// An operation already corrected by method and path is edited in place when
// the form names it by operationId: one entry, not a second override.
func TestEditingAnOverrideWrittenByMethodAndPathUpdatesIt(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, `apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  # by coordinates
  - match: { method: POST, path: /pets }
    effect: write
`)
	for _, effect := range []string{"destructive", "write"} {
		st, err := wb.Apply(t.Context(), createPetEffect(effect))
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Entries) != 1 || st.Entries[0].Match == nil || st.Entries[0].Match.Method != http.MethodPost {
			t.Fatalf("entries = %+v, want one matched by method and path", st.Entries)
		}
		if _, err := wb.Save(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		out := readFile(t, path)
		if strings.Count(out, "match:") != 1 || strings.Contains(out, "operationId: createPet") ||
			!strings.Contains(out, "effect: "+effect) || !strings.Contains(out, "# by coordinates") {
			t.Fatalf("saved:\n%s", out)
		}
	}
}

func TestRevertAndRemove(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, withComment)
	st, err := wb.Apply(t.Context(), createPetEffect("write"))
	if err != nil {
		t.Fatal(err)
	}
	st, err = wb.Revert(t.Context(), st.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 0 {
		t.Fatal("revert left the entry")
	}
	if _, err := wb.Apply(t.Context(), Entry{Kind: "operation", Match: &config.Match{OperationID: "other"}, Remove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	if strings.Contains(out, "operationId: other") {
		t.Fatalf("remove did not remove:\n%s", out)
	}
	if strings.Contains(out, "operationOverrides") {
		t.Fatalf("an emptied list was left behind:\n%s", out)
	}
	if !strings.Contains(out, "# keep this comment") {
		t.Fatalf("remove lost an unrelated comment:\n%s", out)
	}
}

// A person's own empty list is theirs: a remove that removes nothing leaves it.
func TestANoOpRemoveKeepsTheUsersEmptyList(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, "apiVersion: lotsman.dev/v1alpha1\noperationOverrides: []\n")
	if _, err := wb.Apply(t.Context(), Entry{Kind: "operation", Match: &config.Match{OperationID: "nothing"}, Remove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if out := readFile(t, path); !strings.Contains(out, "operationOverrides: []") {
		t.Fatalf("the user's empty list is gone:\n%s", out)
	}
}

func TestDiffShowsThePendingChange(t *testing.T) {
	t.Parallel()
	wb, _ := newBench(t, withComment)
	if d, err := wb.Diff(); err != nil || d != "" {
		t.Fatalf("diff with nothing pending = %q, %v", d, err)
	}
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	d, err := wb.Diff()
	if err != nil || !strings.Contains(d, "+    effect: write") || !strings.Contains(d, "lotsman.yaml (on disk)") {
		t.Fatalf("diff = %q, %v", d, err)
	}
}

// The configuration types travel to the browser in the same camelCase the
// YAML uses, so the form reads and writes one vocabulary.
func TestCorrectionsSerializeInCamelCase(t *testing.T) {
	t.Parallel()
	enabled := true
	e := Entry{Kind: "operation", OperationKey: "k", Operation: &config.OperationOverride{
		Match:     config.Match{OperationID: "createPet"},
		MediaType: "application/merge-patch+json",
		Enabled:   &enabled, AuthProfile: "main",
		Parameters:  []config.ParameterOverride{{Name: "limit", In: "query", Schema: config.Patch{"type": "string"}}},
		RequestBody: &config.BodyOverride{Patch: config.Patch{"required": []any{"name"}}},
	}}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"operationKey":"k"`, `"match":{"operationId":"createPet"}`, `"mediaType":"application/merge-patch+json"`,
		`"authProfile":"main"`, `"parameters":[{"name":"limit","in":"query","schema":{"type":"string"}}]`,
		`"requestBody":{"patch":{"required":["name"]}}`} {
		if !strings.Contains(text, want) {
			t.Errorf("JSON lacks %s:\n%s", want, text)
		}
	}
	var back Entry
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Operation.Match.OperationID != "createPet" || back.Operation.RequestBody == nil || back.Operation.MediaType == "" {
		t.Fatalf("round trip = %+v", back.Operation)
	}
	c, _ := json.Marshal(config.ComponentOverride{Match: config.ComponentMatch{Schema: "Pet"}, Patch: config.Patch{"type": "object"}})
	if string(c) != `{"match":{"schema":"Pet"},"patch":{"type":"object"}}` {
		t.Errorf("component JSON = %s", c)
	}
}

func TestCompareCountsAndWarnings(t *testing.T) {
	t.Parallel()
	before := &Snapshot{Operations: []Operation{
		{Key: "a", Support: "rejected", Reasons: []Reason{{Code: "unsupported_media_type"}}},
		{Key: "b", Support: "supported", Effect: "read"},
		{Key: "c", Support: "supported", Effect: "read", Parameters: []Parameter{{Schema: map[string]any{"type": "integer"}}}},
	}}
	before.Operations[2].Parameters[0].Name, before.Operations[2].Parameters[0].In = "limit", "query"
	after := &Snapshot{Operations: []Operation{
		{Key: "a", Support: "supported"},
		{Key: "b", Support: "rejected", Reasons: []Reason{{Code: "unsupported_parameter_style", Pointer: "#/paths/~1x/get/parameters/query/f"}}},
		{Key: "c", Support: "supported", Effect: "read", Parameters: []Parameter{{Schema: map[string]any{"type": "string"}}}},
	}}
	after.Operations[2].Parameters[0].Name, after.Operations[2].Parameters[0].In = "limit", "query"
	cmp := Compare(before, after)
	if cmp.Summary != (Summary{Fixed: 1, Broken: 1, Altered: 1}) {
		t.Fatalf("summary = %+v", cmp.Summary)
	}
	if w := cmp.Changes[1].Warnings[0]; w.Field != "parameter query/f" || w.Level != "error" {
		t.Fatalf("broken warning = %+v", w)
	}
	if w := cmp.Changes[2].Warnings[0]; w.Field != "parameter query/limit" || !strings.Contains(w.Message, "integer → string") {
		t.Fatalf("altered warning = %+v", w)
	}
}

func TestCompareWidening(t *testing.T) {
	t.Parallel()
	before := &Snapshot{Operations: []Operation{
		{Key: "post", Method: "POST", Path: "/p", Effect: "write"},
		{Key: "hidden", Method: "GET", Path: "/h", Effect: "read", Excluded: true},
		{Key: "auth", Method: "GET", Path: "/a", Effect: "read"},
		{Key: "same", Method: "GET", Path: "/s", Effect: "read"},
	}}
	after := &Snapshot{Operations: []Operation{
		{Key: "post", Method: "POST", Path: "/p", Effect: "read"},
		{Key: "hidden", Method: "GET", Path: "/h", Effect: "read"},
		{Key: "auth", Method: "GET", Path: "/a", Effect: "read", AuthProfiles: []string{"admin"},
			Override: &config.OperationOverride{AuthProfile: "admin"}},
		{Key: "same", Method: "GET", Path: "/s", Effect: "read"},
	}}
	cmp := Compare(before, after)
	SortWidening(cmp.Widening)
	var kinds []string
	for _, w := range cmp.Widening {
		kinds = append(kinds, w.ID)
	}
	if strings.Join(kinds, ",") != "auth#auth-profile,hidden#enabled,post#effect-read" {
		t.Fatalf("widening = %v", kinds)
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	cases := []struct {
		op   Operation
		want Status
	}{
		{Operation{Support: "rejected", Reasons: []Reason{{Code: "unsupported_media_type"}}, MediaTypes: []string{"application/merge-patch+json", "application/json-patch+json"}}, StatusDecision},
		{Operation{Support: "rejected", Reasons: []Reason{{Code: "unsupported_media_type"}}, MediaTypes: []string{"multipart/form-data"}}, StatusUnsupported},
		{Operation{Support: "rejected", Reasons: []Reason{{Code: "unsupported_body_schema"}}}, StatusUnsupported},
		{Operation{Support: "rejected", Reasons: []Reason{{Code: "unsupported_parameter_style"}}}, StatusDecision},
		{Operation{Support: "supported", Executable: false}, StatusBlocked},
		{Operation{Support: "supported", Executable: true, Excluded: true}, StatusBlocked},
		{Operation{Support: "supported", Executable: true}, StatusWorks},
		{Operation{Support: "supported", Executable: false, Blockers: []string{"ambiguous_security"}}, StatusDecision},
	}
	for i, tc := range cases {
		Classify(&tc.op)
		if tc.op.Status != tc.want {
			t.Errorf("case %d: %s, want %s", i, tc.op.Status, tc.want)
		}
	}
}

// Fixing an operation publishes it, and a published tool lists the
// credential it would use: that is not a change of authProfile. Repinning
// or dropping a pinned profile is.
func TestPublishingAnOperationIsNotAnAuthProfileChange(t *testing.T) {
	t.Parallel()
	pinned := &config.OperationOverride{AuthProfile: "main"}
	before := &Snapshot{Operations: []Operation{
		{Key: "fixed", Method: "GET", Path: "/f", Effect: "read", Support: "rejected"},
		{Key: "pinned", Method: "GET", Path: "/p", Effect: "read", Excluded: true, Override: pinned},
		{Key: "repinned", Method: "GET", Path: "/r", Effect: "read", Override: pinned},
		{Key: "unpinned", Method: "GET", Path: "/u", Effect: "read", Override: pinned},
		{Key: "dropped", Method: "GET", Path: "/d", Effect: "read", Override: pinned},
	}}
	after := &Snapshot{Operations: []Operation{
		{Key: "fixed", Method: "GET", Path: "/f", Effect: "read", Support: "supported", Published: true, AuthProfiles: []string{"main"}},
		{Key: "pinned", Method: "GET", Path: "/p", Effect: "read", Published: true, AuthProfiles: []string{"main"}, Override: pinned},
		{Key: "repinned", Method: "GET", Path: "/r", Effect: "read", Override: &config.OperationOverride{AuthProfile: "admin"}},
		{Key: "unpinned", Method: "GET", Path: "/u", Effect: "read", Override: &config.OperationOverride{Effect: "read"}},
		{Key: "dropped", Method: "GET", Path: "/d", Effect: "read"},
	}}
	cmp := Compare(before, after)
	SortWidening(cmp.Widening)
	var ids []string
	for _, w := range cmp.Widening {
		ids = append(ids, w.ID)
	}
	if strings.Join(ids, ",") != "dropped#auth-profile,pinned#enabled,repinned#auth-profile,unpinned#auth-profile" {
		t.Fatalf("widening = %v", ids)
	}
}

// An operation the base does not have (its document did not parse, or an
// overlay in the draft adds it) still owes acknowledgements for what its
// override says explicitly.
func TestWideningOfAnOperationTheBaseDoesNotHave(t *testing.T) {
	t.Parallel()
	yes := true
	before := &Snapshot{SpecError: "does not parse"}
	after := &Snapshot{Operations: []Operation{
		{Key: "del", Method: "DELETE", Path: "/d", Effect: "read", Override: &config.OperationOverride{Effect: "read"}},
		{Key: "get", Method: "GET", Path: "/g", Effect: "read", Override: &config.OperationOverride{Effect: "read"}},
		{Key: "on", Method: "GET", Path: "/o", Effect: "read", Override: &config.OperationOverride{Enabled: &yes, AuthProfile: "main"}},
		{Key: "plain", Method: "POST", Path: "/p", Effect: "write"},
	}}
	cmp := Compare(before, after)
	SortWidening(cmp.Widening)
	var ids []string
	for _, w := range cmp.Widening {
		ids = append(ids, w.ID)
	}
	if strings.Join(ids, ",") != "del#effect-read,on#auth-profile,on#enabled" {
		t.Fatalf("widening = %v", ids)
	}
}

// End to end through the workbench: with no base operations, effect: read on
// a DELETE is not saved without its acknowledgement.
func TestSaveRefusesUnacknowledgedWideningOnANewOperation(t *testing.T) {
	t.Parallel()
	pipe := Pipeline{
		Evaluate: func(_ context.Context, data []byte) (*Snapshot, error) {
			file, err := config.Parse(data)
			if err != nil || len(file.OperationOverrides) == 0 {
				return &Snapshot{SpecError: "does not parse"}, nil
			}
			ov := file.OperationOverrides[0]
			return &Snapshot{Operations: []Operation{{Key: "default:DELETE:/d", Method: "DELETE", Path: "/d",
				OperationID: "del", Effect: ov.Effect, Override: &ov}}}, nil
		},
		Explain: func(context.Context, []byte, string, []byte) (string, error) { return "", nil },
	}
	wb, path := newBenchWith(t, "apiVersion: lotsman.dev/v1alpha1\n", pipe)
	if _, err := wb.Apply(t.Context(), Entry{Kind: "operation", OperationKey: "default:DELETE:/d",
		Operation: &config.OperationOverride{Match: config.Match{OperationID: "del"}, Effect: "read"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); !errors.Is(err, ErrUnacknowledged) {
		t.Fatalf("save = %v, want ErrUnacknowledged", err)
	}
	if strings.Contains(readFile(t, path), "effect") {
		t.Fatal("the unacknowledged widening was written")
	}
	if _, err := wb.Save(t.Context(), []string{"default:DELETE:/d#effect-read"}); err != nil {
		t.Fatal(err)
	}
}

// A save touches only the lines of the entry it changes: the file keeps its
// four-space indentation, a long double-quoted string stays on one line, a
// flow mapping keeps its spacing, blank lines stay.
func TestSaveTouchesOnlyTheChangedEntry(t *testing.T) {
	t.Parallel()
	long := `"` + strings.Repeat("a long reason that goes past eighty columns ", 3) + `"`
	source := "# head comment\n" +
		"apiVersion: lotsman.dev/v1alpha1\n" +
		"spec:\n" +
		"    source: ./openapi.yaml   # spaced comment\n" +
		"\n" +
		"operationOverrides:\n" +
		"    -   match: {  operationId: other  }\n" +
		"        note: " + long + "\n" +
		"        enabled: false\n" +
		"\n" +
		"execution:\n" +
		"    allowMutations: false\n"
	wb, path := newBench(t, source)
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	diff, err := wb.Diff()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	for _, line := range strings.Split(strings.TrimSuffix(source, "\n"), "\n") {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("line %q changed; saved:\n%s", line, out)
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Errorf("the diff removes %q:\n%s", line, diff)
		}
	}
	if !strings.Contains(out, "    - match:\n") || !strings.Contains(out, "        operationId: createPet\n") ||
		!strings.Contains(out, "      effect: write\n") {
		t.Errorf("the new entry is not in the file's indentation:\n%s", out)
	}
}

// The style a file uses is read off it.
func TestStyleOf(t *testing.T) {
	t.Parallel()
	cases := map[string]layout{
		"":                              {indent: 2},
		"a:\n  b: 1\n":                  {indent: 2},
		"a:\n    b: 1\nl:\n    - x\n":   {indent: 4},
		"l:\n- match:\n    x: 1\n":      {indent: 2, compact: true},
		"l:\n  - match:\n      x: 1\n":  {indent: 2},
		"l:\n  - m: {a: 1}\n    e: w\n": {indent: 2},
		"# c\na:\n   b:\n      c: 1\n":  {indent: 3},
	}
	for src, want := range cases {
		if got := styleOf([]byte(src)); got != want {
			t.Errorf("styleOf(%q) = %+v, want %+v", src, got, want)
		}
	}
}

// A configuration reached through a symbolic link is saved into the file the
// link points to; the link stays a link.
func TestSaveThroughASymlinkKeepsTheLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realFile := filepath.Join(dir, "realFile.yaml")
	if err := os.WriteFile(realFile, []byte("apiVersion: lotsman.dev/v1alpha1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "lotsman.yaml")
	if err := os.Symlink(realFile, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	wb, err := New(t.Context(), link, "", fakePipeline())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", info, err)
	}
	if !strings.Contains(readFile(t, realFile), "effect: write") {
		t.Fatalf("the target was not written:\n%s", readFile(t, realFile))
	}
}

// A block scalar with a blank line next to a multi-line flow mapping: the
// encoder's lines and the source's do not line up, and the splice must not
// index the empty line. The file as reported (no apiVersion) is shown and an
// edit is answered without a panic; with apiVersion, State, Apply and Save
// work and the saved file says what the workbench evaluated.
func TestABlankLineInABlockScalarDoesNotBreakTheSplice(t *testing.T) {
	t.Parallel()
	source := "operationOverrides:\n" +
		"  - note: |\n" +
		"      one\n" +
		"\n" +
		"      two\n" +
		"    match: {operationId: x,\n" +
		"      method: GET}\n"
	reported, _ := newBench(t, source)
	if _, err := reported.State(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, _ = reported.Apply(t.Context(), createPetEffect("write")) // refused: no apiVersion; must not panic

	var last atomic.Value
	inner := fakePipeline()
	pipe := inner
	pipe.Evaluate = func(ctx context.Context, data []byte) (*Snapshot, error) {
		last.Store(append([]byte(nil), data...))
		return inner.Evaluate(ctx, data)
	}
	// The reported match names both an operationId and a method, which the
	// configuration refuses; the valid file keeps the same shape.
	valid := "apiVersion: lotsman.dev/v1alpha1\n" +
		strings.Replace(source, "{operationId: x,\n      method: GET}", "{method: GET,\n      path: /x}", 1) +
		"    enabled: false\n"
	wb, path := newBenchWith(t, valid, pipe)
	if _, err := wb.State(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	evaluated, ok := last.Load().([]byte)
	if !ok {
		t.Fatal("nothing was evaluated")
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Parse([]byte(readFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	want, err := config.Parse(evaluated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, want) {
		t.Fatalf("saved %+v, evaluated %+v", saved, want)
	}
	if len(saved.OperationOverrides) != 2 || saved.OperationOverrides[0].Note != "one\n\ntwo\n" {
		t.Fatalf("saved overrides = %+v", saved.OperationOverrides)
	}
}

// A CRLF file stays CRLF: the lines the edit adds end the same way as the
// lines it keeps.
func TestACRLFFileKeepsItsLineEndings(t *testing.T) {
	t.Parallel()
	source := "# head\r\napiVersion: lotsman.dev/v1alpha1\r\noperationOverrides:\r\n" +
		"  - match: {  operationId: other  }\r\n    enabled: false\r\n"
	wb, path := newBench(t, source)
	if _, err := wb.Apply(t.Context(), createPetEffect("write")); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, path)
	if strings.Count(out, "\n") != strings.Count(out, "\r\n") {
		t.Fatalf("mixed line endings: %q", out)
	}
	if !strings.HasPrefix(out, source) || !strings.Contains(out, "effect: write\r\n") {
		t.Fatalf("saved %q", out)
	}
}
