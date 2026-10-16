# Tasks: the workbench and `lotsman ui` (feature 008, stages 2–3)

**Input**: plan-ui.md, spec.md §3–§5, §7
**Convention**: every task ends with `go build ./... && go test ./...` green, its smoke command,
and a commit. Tests beyond the smoke are task 10 (maintainer's decision).

**Before task 1**: branch `008-local-admin-ui-stage2-3` from `main` in a worktree under
`.worktrees/`; copy the git-ignored corpus files from the main checkout's `test/testdata/corpus/`.

---

### Task 1: the adapter reports what the form needs

**Files:**
- Modify: `internal/domain/domain.go` (Operation, new DeclaredParameter)
- Modify: `internal/openapi/openapi.go` (buildOperation, Parse), `parameters.go` (buildInput),
  `corrections.go` (root component names)

**Interfaces — Produces:**
- `domain.Operation.RequestMediaTypes []string` — every media type the request body declares,
  normalized, sorted; empty without a body.
- `domain.Operation.DeclaredParameters []domain.DeclaredParameter` — every merged parameter, in IR
  order, including refused ones.
- `domain.DeclaredParameter{Name string; In ParameterLocation; Style ParameterStyle; Explode bool;
  Required bool; Component string; Reason ReasonCode}` — `Style` as written or the location's
  default; `Component` is the root `#/components/schemas` name the parameter's schema references
  directly ("" otherwise); `Reason` is empty for an accepted parameter.
- `domain.Operation.BodyComponent string` — root component the body schema references directly.

- [ ] **Step 1: domain.** Below `Parameter` in domain.go:

```go
// DeclaredParameter is a parameter as the document (and any correction)
// declares it, whether or not lotsman could translate it. Input.Parameters
// holds only the translatable ones; a workbench that is to offer a fix for a
// refused parameter has to be able to see it.
type DeclaredParameter struct {
	Name     string            `json:"name"`
	In       ParameterLocation `json:"in"`
	Style    ParameterStyle    `json:"style"`
	Explode  bool              `json:"explode"`
	Required bool              `json:"required"`
	// Component is the root component the schema references directly, or "".
	Component string `json:"component,omitempty"`
	// Reason is why the parameter was refused, or "" when it was translated.
	Reason ReasonCode `json:"reason,omitempty"`
}
```

In `Operation`, after `Corrections`:

```go
	// RequestMediaTypes are the media types the request body declares,
	// normalized and sorted, whether or not one could be chosen.
	RequestMediaTypes []string `json:"requestMediaTypes,omitempty"`
	// DeclaredParameters are all merged parameters, refused ones included.
	DeclaredParameters []DeclaredParameter `json:"declaredParameters,omitempty"`
	// BodyComponent is the root component the body schema references
	// directly, or "".
	BodyComponent string `json:"bodyComponent,omitempty"`
```

- [ ] **Step 2: root component names.** In corrections.go, `fixes` gains
`names map[*yaml.Node]string` and a method; fill it in `checkComponents` for *every* root component
(not only corrected ones), skipping aliases exactly as the existing code skips them:

```go
// componentOf names the root component a schema proxy resolves to, or "".
// Identity is the parsed node, as for corrections: a same-named component in
// another file of an exploded specification is a different component.
func (f *fixes) componentOf(proxy *base.SchemaProxy) string {
	if f == nil || len(f.names) == 0 || proxy == nil || !proxy.IsReference() {
		return ""
	}
	if node := sourceNode(proxy); node != nil {
		return f.names[node]
	}
	return ""
}
```

(Read `checkComponents`: wherever it computes the node for a root component, also record
`f.names[node] = name` for every component, including uncorrected ones. Initialise the map in
`newFixes`.)

- [ ] **Step 3: parameters.** `inputBuild` gains `declared []domain.DeclaredParameter`.
`buildInput` gains a `componentOf func(*base.SchemaProxy) string` parameter (pass
`scope.fixes.componentOf`). In the loop, build the entry once the location and style are known
and append it on *every* exit path of the iteration — refused or accepted — with `Reason` set to
the code the iteration refused with. Use a small closure:

```go
		declare := func(reason domain.ReasonCode) {
			out.declared = append(out.declared, domain.DeclaredParameter{
				Name: source.Name, In: in, Style: style, Explode: explode,
				Required:  source.Required != nil && *source.Required,
				Component: componentOf(source.Schema), Reason: reason,
			})
		}
```

Parameters refused *before* style is known (`invalid_parameter` for name/location) are declared
with `Style: ""`. Call sites of `buildInput` in tests pass `nil` for `componentOf`; guard
`componentOf == nil` inside the closure.

- [ ] **Step 4: buildOperation.** After `input := buildInput(...)`:
`result.DeclaredParameters = input.declared`. After the body is built:

```go
	if op.RequestBody != nil {
		for name := range op.RequestBody.Content.FromOldest() {
			result.RequestMediaTypes = append(result.RequestMediaTypes, normalizeMediaType(name))
		}
		sort.Strings(result.RequestMediaTypes)
		if content := firstContent(op.RequestBody, body.spec); content != nil {
			result.BodyComponent = scope.fixes.componentOf(content.Schema)
		}
	}
```

with, in body.go:

```go
// firstContent is the media type entry the body was built from, so its
// schema proxy can still be asked which component it names.
func firstContent(rb *v3.RequestBody, spec *domain.BodySpec) *v3.MediaType {
	if rb == nil || spec == nil || orderedmap.Len(rb.Content) == 0 {
		return nil
	}
	for name, media := range rb.Content.FromOldest() {
		if normalizeMediaType(name) == spec.MediaType {
			return media
		}
	}
	// A sole wildcard was sent as JSON.
	for _, media := range rb.Content.FromOldest() {
		return media
	}
	return nil
}
```

Guard `op.RequestBody.Content == nil` before ranging.

- [ ] **Step 5: Smoke**

```bash
go build ./... && make build
bin/lotsman operations cmd/api-gateway/openapi.yaml >/dev/null
go run ./cmd/lotsman inspect cmd/api-gateway/openapi.yaml --json >/dev/null && echo ok
cat > /tmp/ir.go <<'EOF'
package main
import ("context";"encoding/json";"fmt";"os";"github.com/razrabotchik/lotsman/internal/openapi")
func main(){ b,_:=os.ReadFile(os.Args[1]); d,err:=openapi.Parse(context.Background(),b,openapi.Options{})
 if err!=nil{panic(err)}; for _,o:=range d.Operations{ if o.SourceOperationID=="getFiltered"||o.SourceOperationID=="patchPetCompeting"{
 j,_:=json.Marshal(map[string]any{"mt":o.RequestMediaTypes,"params":o.DeclaredParameters,"body":o.BodyComponent}); fmt.Println(o.SourceOperationID,string(j))}}}
EOF
mkdir -p /tmp/irprobe && cp /tmp/ir.go /tmp/irprobe/main.go && go run /tmp/irprobe/main.go cmd/api-gateway/openapi.yaml
```

(Run the probe from the repo root so the module resolves: `go run ./...` with a file outside the
module will not; if so, put it temporarily under `cmd/irprobe/`, run it, and delete it before
committing.) Expected: `getFiltered` lists `filter`, `tags`, `ids` with their styles and
`unsupported_parameter_style`; `patchPetCompeting` lists four media types.

- [ ] **Step 6: Commit** — `git commit -am "IR: объявленные параметры, предложенные media types и компонент тела — для формы правок"` (+ trailer).

---

### Task 2: workbench snapshot, comparison and text diff

**Files:**
- Create: `internal/workbench/doc.go`, `snapshot.go`, `compare.go`, `textdiff.go`, `pipeline.go`

**Interfaces — Produces** (exact names; the cli and ui tasks rely on them):

```go
package workbench

type Pipeline struct {
	// Evaluate runs the pipeline serve runs over a configuration's bytes and
	// reports everything it finds as data. It returns an error only when it
	// could not run at all (context cancelled).
	Evaluate func(ctx context.Context, config []byte) (*Snapshot, error)
	// Explain renders explain-call for one operation under a configuration,
	// without the network.
	Explain func(ctx context.Context, config []byte, key string, arguments []byte) (string, error)
}
```

- [ ] **Step 1: `doc.go`**

```go
// Package workbench is the engine behind `lotsman ui` and `inspect
// --diff-against` (feature 008, spec §3): it keeps a draft of the
// configuration, runs the same pipeline serve runs over it, and says what a
// change fixes, breaks or alters before anything is written.
//
// It has no HTTP and does not know OpenAPI: the pipeline is handed to it as
// functions, so that what the workbench predicts is what serve does.
package workbench
```

- [ ] **Step 2: `pipeline.go`** — the `Pipeline` struct above, with `import "context"`.

- [ ] **Step 3: `snapshot.go`**

```go
package workbench

import (
	"strings"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
)

// Snapshot is the pipeline's verdict on one configuration.
type Snapshot struct {
	// SpecError is set when the specification could not be read or parsed at
	// all; Operations is then empty.
	SpecError string `json:"specError,omitempty"`
	// ConfigError is set when the configuration itself does not validate.
	// The snapshot is then of the document without any configuration.
	ConfigError string    `json:"configError,omitempty"`
	Problems    []Problem `json:"problems,omitempty"`
	// Operations are in catalog order.
	Operations []Operation                `json:"operations"`
	Profiles   []Profile                  `json:"profiles,omitempty"`
	Components []config.ComponentOverride `json:"components,omitempty"`
	Overlays   []string                   `json:"overlays,omitempty"`
}

// Problem is a correction that could not be applied as written.
type Problem struct {
	Section string `json:"section"`
	Index   int    `json:"index"`
	Message string `json:"message"`
	// Match names the operationOverrides entry, Schema the componentOverrides
	// one, so the workbench can offer to remove a stale correction.
	Match  *config.Match `json:"match,omitempty"`
	Schema string        `json:"schema,omitempty"`
}

// Profile is a configured credential, by name and scheme only: the
// workbench never sees a secret or even its reference.
type Profile struct {
	Name   string `json:"name"`
	Scheme string `json:"scheme"`
}

// Reason is a refusal with where in the document it applies.
type Reason struct {
	Code    string `json:"code"`
	Detail  string `json:"detail,omitempty"`
	Pointer string `json:"pointer,omitempty"`
}

// Body is the request body as published.
type Body struct {
	MediaType string         `json:"mediaType"`
	Required  bool           `json:"required"`
	Schema    map[string]any `json:"schema,omitempty"`
}

// Parameter is a declared parameter with its published schema, if any.
type Parameter struct {
	domain.DeclaredParameter
	Schema map[string]any `json:"schema,omitempty"`
}

// Operation is one operation as a configuration leaves it.
type Operation struct {
	Key          string   `json:"key"`
	Method       string   `json:"method"`
	Path         string   `json:"path"`
	OperationID  string   `json:"operationId,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Status       Status   `json:"status"`
	Support      string   `json:"support"`
	Reasons      []Reason `json:"reasons,omitempty"`
	Effect       string   `json:"effect"`
	EffectSource string   `json:"effectSource"`
	Published    bool     `json:"published"`
	Excluded     bool     `json:"excluded"`
	Executable   bool     `json:"executable"`
	Blockers     []string `json:"blockers,omitempty"`
	AuthProfiles []string `json:"authProfiles,omitempty"`

	MediaTypes    []string    `json:"mediaTypes,omitempty"`
	Parameters    []Parameter `json:"parameters,omitempty"`
	Body          *Body       `json:"body,omitempty"`
	BodyComponent string      `json:"bodyComponent,omitempty"`
	// InputSchema is the published tool input schema; nil when unpublished.
	InputSchema map[string]any `json:"inputSchema,omitempty"`

	Corrections []domain.Correction `json:"corrections,omitempty"`
	// Override is the operationOverrides entry that names this operation.
	Override *config.OperationOverride `json:"override,omitempty"`
}

// Status is what an operation needs from the operator.
type Status string

// Statuses, in the order the workbench shows them.
const (
	// StatusDecision: refused for a reason a correction can lift.
	StatusDecision Status = "decision"
	// StatusUnsupported: refused for a reason only new code or a fixed
	// document can lift.
	StatusUnsupported Status = "unsupported"
	// StatusBlocked: translated, but not callable — policy, a decision to
	// disable it, or a capability lotsman does not have yet.
	StatusBlocked Status = "blocked"
	// StatusWorks: translated and callable.
	StatusWorks Status = "works"
)

// decidable are the refusals a correction can lift (spec §1).
var decidable = map[string]bool{
	string(domain.ReasonUnsupportedParameterStyle):  true,
	string(domain.ReasonUnsupportedParameterSchema): true,
	string(domain.ReasonAmbiguousSecurity):          true,
}

// Classify sets op.Status from the rest of op.
func Classify(op *Operation) {
	switch {
	case op.Support == string(domain.SupportRejected):
		op.Status = StatusDecision
		for _, reason := range op.Reasons {
			if !decidableReason(reason.Code, op.MediaTypes) {
				op.Status = StatusUnsupported
				break
			}
		}
	case op.Excluded || !op.Executable:
		op.Status = StatusBlocked
		for _, blocker := range op.Blockers {
			if blocker == string(domain.ReasonAmbiguousSecurity) {
				op.Status = StatusDecision
			}
		}
	default:
		op.Status = StatusWorks
	}
}

func decidableReason(code string, mediaTypes []string) bool {
	if decidable[code] || strings.HasPrefix(code, "policy_") {
		return true
	}
	if code == string(domain.ReasonUnsupportedMediaType) {
		sendable := 0
		for _, mt := range mediaTypes {
			if Sendable(mt) {
				sendable++
			}
		}
		return sendable > 0
	}
	return false
}

// Sendable reports whether lotsman can send a body of this media type. It
// mirrors the adapter's rule; the snapshot says which types an operation
// offers, and the form greys out the rest.
func Sendable(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
```

Check the exact reason-code constant names in internal/domain/domain.go
(`ReasonAmbiguousSecurity` etc.) and use the real ones.

- [ ] **Step 4: `compare.go`**

```go
package workbench

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// Comparison is what a configuration change does.
type Comparison struct {
	Summary  Summary    `json:"summary"`
	Changes  []Change   `json:"changes,omitempty"`
	Widening []Widening `json:"widening,omitempty"`
}

// Summary counts the changes.
type Summary struct {
	Fixed   int `json:"fixed"`
	Broken  int `json:"broken"`
	Altered int `json:"altered"`
}

// Change is one operation that differs.
type Change struct {
	Key      string    `json:"key"`
	Kind     string    `json:"kind"` // fixed | broken | altered
	Before   Status    `json:"before"`
	After    Status    `json:"after"`
	Warnings []Warning `json:"warnings,omitempty"`
}

// Warning is shown under the form field it concerns.
type Warning struct {
	// Field is "mediaType", "parameter <in>/<name>", "requestBody",
	// "effect", "enabled", "authProfile" or "operation".
	Field   string `json:"field"`
	Level   string `json:"level"` // info | warn | error
	Message string `json:"message"`
}

// Widening is a change that lets an agent do more than before without a
// person in the loop. Each needs its own acknowledgement at save.
type Widening struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Kind    string `json:"kind"` // effect-read | enabled | auth-profile
	Message string `json:"message"`
}

// Compare says what changes between two snapshots, operation by operation,
// in the order of after's catalog.
func Compare(before, after *Snapshot) Comparison {
	old := make(map[string]*Operation, len(before.Operations))
	for i := range before.Operations {
		old[before.Operations[i].Key] = &before.Operations[i]
	}
	var out Comparison
	for i := range after.Operations {
		a := &after.Operations[i]
		b, ok := old[a.Key]
		if !ok {
			continue
		}
		if change, changed := compareOperation(b, a); changed {
			switch change.Kind {
			case "fixed":
				out.Summary.Fixed++
			case "broken":
				out.Summary.Broken++
			default:
				out.Summary.Altered++
			}
			out.Changes = append(out.Changes, change)
		}
		out.Widening = append(out.Widening, widening(b, a)...)
	}
	return out
}

func compareOperation(b, a *Operation) (Change, bool) {
	change := Change{Key: a.Key, Before: b.Status, After: a.Status}
	rejected := string(domain.SupportRejected)
	switch {
	case b.Support == rejected && a.Support != rejected:
		change.Kind = "fixed"
		change.Warnings = append(change.Warnings, Warning{Field: "operation", Level: "info",
			Message: "the operation is translated now"})
	case b.Support != rejected && a.Support == rejected:
		change.Kind = "broken"
		for _, reason := range a.Reasons {
			change.Warnings = append(change.Warnings, Warning{Field: fieldOf(reason.Pointer), Level: "error",
				Message: "the operation stops working: " + reason.Code + detailSuffix(reason.Detail)})
		}
	default:
		change.Kind = "altered"
	}
	change.Warnings = append(change.Warnings, alterations(b, a)...)
	if change.Kind == "altered" && len(change.Warnings) == 0 {
		return Change{}, false
	}
	return change, true
}

// alterations are the differences that do not change support.
func alterations(b, a *Operation) []Warning {
	var out []Warning
	beforeParams := map[string]Parameter{}
	for _, p := range b.Parameters {
		beforeParams[string(p.In)+"/"+p.Name] = p
	}
	for _, p := range a.Parameters {
		id := string(p.In) + "/" + p.Name
		if prior, ok := beforeParams[id]; ok && p.Reason == "" && prior.Reason == "" &&
			!sameJSON(prior.Schema, p.Schema) {
			out = append(out, Warning{Field: "parameter " + id, Level: "warn",
				Message: "the argument schema changes: " + describeType(prior.Schema) + " → " + describeType(p.Schema)})
		}
	}
	if b.Body != nil && a.Body != nil && !sameJSON(b.Body.Schema, a.Body.Schema) {
		out = append(out, Warning{Field: "requestBody", Level: "warn", Message: "the body schema changes"})
	}
	if b.Body != nil && a.Body != nil && b.Body.MediaType != a.Body.MediaType {
		out = append(out, Warning{Field: "mediaType", Level: "warn",
			Message: "the body is sent as " + a.Body.MediaType + " instead of " + b.Body.MediaType})
	}
	if b.Effect != a.Effect {
		out = append(out, Warning{Field: "effect", Level: "warn",
			Message: fmt.Sprintf("effect %s → %s", b.Effect, a.Effect)})
	}
	if b.Executable != a.Executable {
		word := "can no longer be called"
		if a.Executable {
			word = "can be called now"
		}
		out = append(out, Warning{Field: "operation", Level: "warn", Message: "the operation " + word})
	}
	if b.Excluded != a.Excluded {
		out = append(out, Warning{Field: "enabled", Level: "info",
			Message: map[bool]string{true: "hidden from agents", false: "published again"}[a.Excluded]})
	}
	return out
}

func widening(b, a *Operation) []Widening {
	var out []Widening
	read := string(domain.EffectRead)
	if a.Effect == read && b.Effect != read {
		out = append(out, Widening{ID: a.Key + "#effect-read", Key: a.Key, Kind: "effect-read",
			Message: fmt.Sprintf("%s %s is treated as a read: it runs without the mutation policy and without approval", a.Method, a.Path)})
	}
	if b.Excluded && !a.Excluded {
		out = append(out, Widening{ID: a.Key + "#enabled", Key: a.Key, Kind: "enabled",
			Message: fmt.Sprintf("%s %s is published again", a.Method, a.Path)})
	}
	if !reflect.DeepEqual(b.AuthProfiles, a.AuthProfiles) && len(a.AuthProfiles) > 0 {
		out = append(out, Widening{ID: a.Key + "#auth-profile", Key: a.Key, Kind: "auth-profile",
			Message: fmt.Sprintf("%s %s uses credential %s", a.Method, a.Path, strings.Join(a.AuthProfiles, ", "))})
	}
	return out
}

// fieldOf maps a refusal's document pointer to the form field it concerns.
func fieldOf(pointer string) string {
	if i := strings.Index(pointer, "/parameters/"); i >= 0 {
		return "parameter " + pointer[i+len("/parameters/"):]
	}
	if strings.Contains(pointer, "/requestBody") {
		return "requestBody"
	}
	return "operation"
}

func detailSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return " — " + detail
}

func describeType(schema map[string]any) string {
	if schema == nil {
		return "none"
	}
	if t, ok := schema["type"]; ok {
		if items, ok := schema["items"].(map[string]any); ok && t == "array" {
			return fmt.Sprintf("array of %v", items["type"])
		}
		return fmt.Sprint(t)
	}
	if _, ok := schema["$ref"]; ok {
		return "reference"
	}
	return "schema"
}

func sameJSON(x, y map[string]any) bool {
	a, _ := json.Marshal(x)
	b, _ := json.Marshal(y)
	return string(a) == string(b)
}

// SortWidening orders widening changes by ID, for stable output.
func SortWidening(w []Widening) { sort.Slice(w, func(i, j int) bool { return w[i].ID < w[j].ID }) }
```

(`json.Marshal` sorts map keys, so `sameJSON` is deterministic. Parameter `id` uses the pointer's
`<in>/<name>` spelling the adapter writes — check `paramPointer` in parameters.go: it is
`%s/parameters/%s/%s` with in then name, so `fieldOf` yields `parameter query/limit`.)

- [ ] **Step 5: `textdiff.go`**

```go
package workbench

import (
	"fmt"
	"strings"
)

// UnifiedDiff renders a line diff of two texts with three lines of context,
// in the familiar unified format. It is for configuration files a person
// reads, so a quadratic LCS is fine and a dependency is not.
func UnifiedDiff(name string, before, after []byte) string {
	a, b := splitLines(string(before)), splitLines(string(after))
	ops := lcsOps(a, b)
	if len(ops) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s (on disk)\n+++ %s (draft)\n", name, name)
	const context = 3
	for start := 0; start < len(ops); {
		// Find the next change.
		for start < len(ops) && ops[start].kind == ' ' {
			start++
		}
		if start == len(ops) {
			break
		}
		from := max(start-context, 0)
		end := start
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run-end > 2*context || run == len(ops) {
				end = min(end+context, len(ops))
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := hunkCoordinates(ops, from, end)
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart, aLen, bStart, bLen)
		for _, op := range ops[from:end] {
			fmt.Fprintf(&out, "%c%s\n", op.kind, op.line)
		}
		start = end
	}
	return out.String()
}

type diffOp struct {
	kind       byte // ' ', '-', '+'
	line       string
	aIdx, bIdx int // 1-based line numbers in a and b (0 when absent)
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func lcsOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	var ops []diffOp
	changed := false
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i], i + 1, j + 1})
			i++
			j++
		case j < m && (i == n || table[i][j+1] >= table[i+1][j]):
			ops = append(ops, diffOp{'+', b[j], 0, j + 1})
			j++
			changed = true
		default:
			ops = append(ops, diffOp{'-', a[i], i + 1, 0})
			i++
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return ops
}

func hunkCoordinates(ops []diffOp, from, end int) (aStart, bStart, aLen, bLen int) {
	for _, op := range ops[from:end] {
		if op.kind != '+' {
			if aStart == 0 {
				aStart = op.aIdx
			}
			aLen++
		}
		if op.kind != '-' {
			if bStart == 0 {
				bStart = op.bIdx
			}
			bLen++
		}
	}
	return aStart, bStart, aLen, bLen
}
```

- [ ] **Step 6: Smoke** — `go build ./... && go vet ./internal/workbench/` (the package is only
used from task 3 on).

- [ ] **Step 7: Commit** — `git add internal/workbench && git commit -m "workbench: снимок конвейера, сравнение и текстовый diff"` (+ trailer).

---

### Task 3: the cli side of the pipeline

**Files:**
- Modify: `internal/config/config.go` (LoadBytes)
- Modify: `internal/cli/serve.go` (split `loadDocument` out of `parseSpec`)
- Modify: `internal/cli/explain.go` (argument decoding from bytes)
- Create: `internal/cli/evaluate.go`

**Interfaces — Consumes:** task 1's IR fields; task 2's `workbench.Snapshot`, `Operation`,
`Parameter`, `Body`, `Problem`, `Profile`, `Reason`, `Classify`, `Pipeline`.
**Produces:** `config.LoadBytes(data []byte, path string) (*config.File, error)`;
`cli.newPipeline(in pipelineInputs) workbench.Pipeline` with
`type pipelineInputs struct{ configPath, spec, root string }`.

- [ ] **Step 1: `config.LoadBytes`** — refactor `Load` so both share one body:

```go
// Load reads and validates a configuration file.
func Load(path string) (*File, error) {
	// #nosec G304 -- the path is an operator-supplied CLI argument.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "config: cannot read %s", path)
	}
	return LoadBytes(data, path)
}

// LoadBytes validates a configuration that may not be on disk yet, as if it
// were the file at path: relative paths resolve against path's directory.
// The workbench evaluates drafts this way before writing them.
func LoadBytes(data []byte, path string) (*File, error) {
	file, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := file.resolvePaths(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if dir, absErr := filepath.Abs(filepath.Dir(path)); absErr == nil {
		file.dir = dir
	}
	return file, nil
}
```

Keep the existing comments of `Load` above it.

- [ ] **Step 2: `loadDocument`.** Rename the body of `parseSpec` to

```go
// loadDocument runs pipeline stages 0-1 and returns the document with its
// correction problems still inside it. parseSpec is the failing form every
// serving command uses; the workbench wants the problems as data.
//
//nolint:gocritic // hugeParam: see parseSpec.
func loadDocument(ctx context.Context, spec string, runtime config.Runtime, logger *slog.Logger) (*specDoc, error) {
```

(everything `parseSpec` does except the `correctionError` check), and make `parseSpec`:

```go
func parseSpec(ctx context.Context, spec string, runtime config.Runtime, logger *slog.Logger) (*specDoc, error) {
	doc, err := loadDocument(ctx, spec, runtime, logger)
	if err != nil {
		return nil, err
	}
	if err := correctionError(doc.CorrectionProblems); err != nil {
		return nil, err
	}
	return doc, nil
}
```

Keep the existing doc comment and nolint on `parseSpec`.

- [ ] **Step 3: explain arguments from bytes.** In explain.go extract:

```go
// decodeArguments decodes grouped arguments the way serve decodes a call.
func decodeArguments(data []byte) (requestbuild.Arguments, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return requestbuild.Arguments{}, nil
	}
	arguments, err := argvalidate.Decode(data)
	if err != nil {
		return nil, errs.Errorf(errs.ClassUsage, "arguments are not a JSON object of argument groups")
	}
	return requestbuild.Arguments(arguments), nil
}
```

and have `readArguments` call it after reading the file (keep its own error message for the
file case by wrapping: on decode error return the existing
`"explain-call: %s is not a JSON object of argument groups"` message).

- [ ] **Step 4: `internal/cli/evaluate.go`**

```go
package cli

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/workbench"
)

// pipelineInputs are what the command line fixed for the life of a
// workbench: which configuration file the draft stands for, and the document
// when the command line names one.
type pipelineInputs struct {
	configPath string
	spec       string
	root       string
}

// newPipeline hands the workbench the pipeline serve runs. Nothing here
// resolves a secret or makes a request: the catalog is built, never served.
func newPipeline(in pipelineInputs) workbench.Pipeline {
	return workbench.Pipeline{
		Evaluate: func(ctx context.Context, data []byte) (*workbench.Snapshot, error) {
			snap, _, _ := evaluate(ctx, data, in)
			return snap, ctx.Err()
		},
		Explain: func(ctx context.Context, data []byte, key string, arguments []byte) (string, error) {
			snap, cat, runtime := evaluate(ctx, data, in)
			if cat == nil {
				return "", errs.Errorf(errs.ClassUsage, "nothing to explain: %s", firstNonEmpty(snap.SpecError, "no catalog"))
			}
			tool := findTool(cat, key)
			if tool == nil {
				return "", errs.Errorf(errs.ClassUnsupported, "%s is not published under this configuration", key)
			}
			args, err := decodeArguments(arguments)
			if err != nil {
				return "", err
			}
			var out bytes.Buffer
			explain(&out, tool, args, runtime)
			return out.String(), nil
		},
	}
}

var quiet = slog.New(slog.DiscardHandler)

// evaluate runs stages 0-4 over a configuration's bytes and turns every
// problem into data. It returns the catalog too, for Explain.
func evaluate(ctx context.Context, data []byte, in pipelineInputs) (*workbench.Snapshot, *catalog.Catalog, config.Runtime) {
	snap := &workbench.Snapshot{}
	var file *config.File
	if len(bytes.TrimSpace(data)) > 0 {
		loaded, err := config.LoadBytes(data, in.configPath)
		if err != nil {
			snap.ConfigError = err.Error()
		} else {
			file = loaded
		}
	}
	runtime := config.Resolve(file, config.Environment{}, config.Overrides{})
	if in.root != "" {
		runtime.Root = in.root
	}
	snap.Overlays = append([]string(nil), runtime.Overlays...)
	snap.Components = append([]config.ComponentOverride(nil), runtime.ComponentOverrides...)
	for _, name := range sortedProfileNames(runtime.AuthProfiles) {
		snap.Profiles = append(snap.Profiles, workbench.Profile{Name: name, Scheme: string(runtime.AuthProfiles[name].Scheme)})
	}

	spec := pick(in.spec, runtime.Source)
	if spec == "" {
		snap.SpecError = "no specification: pass SPEC or set spec.source in the configuration"
		return snap, nil, runtime
	}
	doc, err := loadDocument(ctx, spec, runtime, quiet)
	if err != nil {
		snap.SpecError = err.Error()
		return snap, nil, runtime
	}
	for _, p := range doc.CorrectionProblems {
		snap.Problems = append(snap.Problems, problemFrom(p.Section, p.Index, p.Message, runtime))
	}

	opts, err := catalogOptions(runtime, catalog.ModeTools, doc.Operations)
	if err != nil {
		// An override that matches nothing (or several): reported, and the
		// catalog is built without the overrides so the tree still shows.
		snap.Problems = append(snap.Problems, workbench.Problem{Section: "operationOverrides", Index: -1, Message: err.Error()})
		stripped := runtime
		stripped.Overrides = nil
		opts, _ = catalogOptions(stripped, catalog.ModeTools, doc.Operations)
	}
	cat := catalog.Build(doc.digest, doc.Operations, opts)
	for i := range cat.Tools {
		tool := &cat.Tools[i]
		if len(tool.Corrections) == 0 {
			continue
		}
		if _, err := argvalidate.Compile(tool.Name, tool.InputSchema); err != nil {
			snap.Problems = append(snap.Problems, workbench.Problem{Section: "operationOverrides", Index: -1,
				Message: fmt.Sprintf("the corrected input schema of %s does not compile: %v", tool.OperationKey, err)})
		}
	}

	snap.Operations = snapshotOperations(doc.Operations, &cat, runtime)
	return snap, &cat, runtime
}

func snapshotOperations(ops []domain.Operation, cat *catalog.Catalog, runtime config.Runtime) []workbench.Operation {
	verdicts := map[domain.OperationKey]*catalog.OperationVerdict{}
	for i := range cat.Report.Operations {
		verdicts[cat.Report.Operations[i].Key] = &cat.Report.Operations[i]
	}
	tools := map[domain.OperationKey]*catalog.Tool{}
	for i := range cat.Tools {
		tools[cat.Tools[i].OperationKey] = &cat.Tools[i]
	}
	out := make([]workbench.Operation, 0, len(ops))
	for i := range ops {
		op := &ops[i]
		v := verdicts[op.Key]
		w := workbench.Operation{
			Key: string(op.Key), Method: op.Method, Path: op.PathTemplate, OperationID: op.SourceOperationID,
			Summary: op.Summary, Tags: op.Tags, Support: string(op.Support.Level),
			Effect: string(op.Effect.Effect), EffectSource: string(op.Effect.Source),
			MediaTypes: op.RequestMediaTypes, BodyComponent: op.BodyComponent, Corrections: op.Corrections,
		}
		if v != nil {
			w.Support, w.Published, w.Excluded, w.Executable = string(v.Support), v.Published, v.Excluded, v.Executable
			w.Effect, w.EffectSource = string(v.Effect.Effect), string(v.Effect.Source)
			w.AuthProfiles = v.AuthProfiles
			for _, r := range v.Reasons {
				w.Reasons = append(w.Reasons, workbench.Reason{Code: string(r.Code), Detail: r.Detail, Pointer: r.Pointer})
			}
			for _, b := range append(append([]domain.ReasonCode(nil), v.ExecutionBlockers...), v.PolicyBlockers...) {
				w.Blockers = append(w.Blockers, string(b))
			}
		}
		published := map[string]domain.Schema{}
		for _, p := range op.Input.Parameters {
			published[string(p.In)+"/"+p.Name] = p.Schema
		}
		for _, d := range op.DeclaredParameters {
			w.Parameters = append(w.Parameters, workbench.Parameter{DeclaredParameter: d,
				Schema: published[string(d.In)+"/"+d.Name]})
		}
		if op.Input.Body != nil {
			w.Body = &workbench.Body{MediaType: op.Input.Body.MediaType, Required: op.Input.Body.Required,
				Schema: op.Input.Body.Schema}
		}
		if tool := tools[op.Key]; tool != nil {
			w.InputSchema = tool.InputSchema
		}
		w.Override = overrideFor(op, runtime.Overrides)
		workbench.Classify(&w)
		out = append(out, w)
	}
	return out
}

// overrideFor finds the operationOverrides entry naming op, matched the way
// the catalog matches.
func overrideFor(op *domain.Operation, overrides []config.OperationOverride) *config.OperationOverride {
	for i := range overrides {
		m := overrides[i].Match
		if (m.OperationID != "" && m.OperationID == op.SourceOperationID) ||
			(m.OperationID == "" && strings.EqualFold(m.Method, op.Method) && m.Path == op.PathTemplate) {
			entry := overrides[i]
			return &entry
		}
	}
	return nil
}

func problemFrom(section string, index int, message string, runtime config.Runtime) workbench.Problem {
	p := workbench.Problem{Section: section, Index: index, Message: message}
	switch {
	case section == "operationOverrides" && index >= 0 && index < len(runtime.Overrides):
		m := runtime.Overrides[index].Match
		p.Match = &m
	case section == "componentOverrides" && index >= 0 && index < len(runtime.ComponentOverrides):
		p.Schema = runtime.ComponentOverrides[index].Match.Schema
	}
	return p
}

func sortedProfileNames(profiles map[string]config.Profile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
```

Adapt field names to the real `catalog.OperationVerdict`/`catalog.Tool`/`config.Profile` (e.g. the
`Scheme` type) where they differ — read them first. If `config.Resolve` needs a non-nil file,
pass an empty `&config.File{APIVersion: config.APIVersion}` instead of nil.

- [ ] **Step 5: Smoke** — a throwaway test file run once and deleted (tests proper are task 10):

```bash
cat > internal/cli/zz_smoke_test.go <<'EOF'
package cli
import ("context";"os";"testing")
func TestZZSmoke(t *testing.T){
 cfg,_:=os.ReadFile("/tmp/lotsman-demo/lotsman.yaml")
 p:=newPipeline(pipelineInputs{configPath:"/tmp/lotsman-demo/lotsman.yaml"})
 s,err:=p.Evaluate(context.Background(),cfg); if err!=nil{t.Fatal(err)}
 n:=map[string]int{}; for _,o:=range s.Operations{n[string(o.Status)]++}
 t.Logf("ops=%d status=%v problems=%v specErr=%q", len(s.Operations), n, s.Problems, s.SpecError)
 out,err:=p.Explain(context.Background(),cfg,"list_pets",[]byte(`{"query":{"limit":2}}`)); t.Log(out,err)
}
EOF
go test ./internal/cli/ -run TestZZSmoke -v | tail -20; rm internal/cli/zz_smoke_test.go
```

If `/tmp/lotsman-demo` is absent, create it: copy `cmd/api-gateway/openapi.yaml` there and write
a `lotsman.yaml` with `spec: {source: ./openapi.yaml, strict: false}` and one
`operationOverrides` entry `{match: {operationId: patchPetCompeting}, mediaType: application/merge-patch+json}`.
Expected: ~43 operations with all four statuses present, no problems, explain output with
`operation   default:GET:/v1/pets` and `NO network request was made.`

- [ ] **Step 6: Commit** — `git commit -am "cli: конвейер для workbench — снимок без отказов, explain по байтам конфига"` (+ trailer).

---

### Task 4: the draft — yaml.Node editing, render, save

**Files:**
- Create: `internal/workbench/draft.go`, `internal/workbench/yamlnode.go`

**Interfaces — Consumes:** `Pipeline`, `Snapshot`, `Compare`, `UnifiedDiff` (task 2).
**Produces:**

```go
func New(ctx context.Context, path, spec string, pipe Pipeline) (*Workbench, error)
func (w *Workbench) State(ctx context.Context) (*State, error)
func (w *Workbench) Apply(ctx context.Context, e Entry) (*State, error)   // ErrRejected when the entry adds problems
func (w *Workbench) Revert(ctx context.Context, id string) (*State, error)
func (w *Workbench) Diff() (string, error)
func (w *Workbench) Save(ctx context.Context, acknowledged []string) (*State, error) // ErrChanged, ErrUnacknowledged
func (w *Workbench) Reload(ctx context.Context) (*State, error)
func (w *Workbench) Explain(ctx context.Context, key string, arguments []byte) (string, error)
type Entry struct{ ID, Kind, OperationKey string; Operation *config.OperationOverride; Component *config.ComponentOverride; Overlays []string; Remove bool; Match *config.Match; Schema string }
type State struct{ ConfigPath, Spec string; Draft *Snapshot; Comparison Comparison; Entries []Entry; Problems []Problem /* new in draft */ }
var ErrChanged, ErrUnacknowledged, ErrRejected error
```

- [ ] **Step 1: `yamlnode.go`** — the node surgery, kept apart from the workbench logic:

```go
package workbench

import (
	"bytes"
	"strings"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/config"
)

// document parses a configuration into a node tree, or starts a new one.
// Comments live on the nodes, so everything the workbench does not touch is
// written back as the person wrote it.
func document(data []byte, spec string) (*yaml.Node, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "apiVersion", scalar(config.APIVersion))
		if spec != "" {
			specNode := &yaml.Node{Kind: yaml.MappingNode}
			setKey(specNode, "source", scalar(spec))
			setKey(root, "spec", specNode)
		}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errNotMapping
	}
	return &doc, nil
}

func render(doc *yaml.Node) ([]byte, error) {
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func lookup(m *yaml.Node, key string) (int, *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i, m.Content[i+1]
		}
	}
	return -1, nil
}

// setKey replaces a key's value in place, keeping the key node and its
// comments, or appends the pair.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	if i, _ := lookup(m, key); i >= 0 {
		value.HeadComment, value.LineComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment
		m.Content[i+1] = value
		return
	}
	m.Content = append(m.Content, scalar(key), value)
	m.Content[len(m.Content)-2].Tag = ""
}

func deleteKey(m *yaml.Node, key string) {
	if i, _ := lookup(m, key); i >= 0 {
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
	}
}

// sequence returns the root's sequence under key, creating it if asked.
func sequence(root *yaml.Node, key string, create bool) *yaml.Node {
	if _, seq := lookup(root, key); seq != nil && seq.Kind == yaml.SequenceNode {
		return seq
	}
	if !create {
		return nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	setKey(root, key, seq)
	return seq
}

// encode turns a value into a node using the config package's yaml tags.
func encode(v any) (*yaml.Node, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	return &n, nil
}

// mergeInto makes item say what desired says, key by key: unchanged keys keep
// their nodes (and comments), changed ones are replaced, absent ones removed.
func mergeInto(item, desired *yaml.Node) {
	keep := map[string]bool{}
	for i := 0; i+1 < len(desired.Content); i += 2 {
		key := desired.Content[i].Value
		keep[key] = true
		if _, current := lookup(item, key); current != nil && sameNode(current, desired.Content[i+1]) {
			continue
		}
		setKey(item, key, desired.Content[i+1])
	}
	for i := 0; i+1 < len(item.Content); {
		if !keep[item.Content[i].Value] {
			item.Content = append(item.Content[:i], item.Content[i+2:]...)
			continue
		}
		i += 2
	}
}

func sameNode(a, b *yaml.Node) bool {
	x, errA := yaml.Marshal(a)
	y, errB := yaml.Marshal(b)
	return errA == nil && errB == nil && string(x) == string(y)
}

func matchOf(item *yaml.Node) config.Match {
	var m config.Match
	if _, node := lookup(item, "match"); node != nil {
		_ = node.Decode(&m)
	}
	return m
}

func sameMatch(a, b config.Match) bool {
	return a.OperationID == b.OperationID && strings.EqualFold(a.Method, b.Method) && a.Path == b.Path
}
```

- [ ] **Step 2: `draft.go`**

```go
package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/config"
)

var (
	// ErrChanged: the file on disk is not the one the draft was based on.
	ErrChanged = errors.New("the configuration changed on disk since it was loaded; reload to see it")
	// ErrUnacknowledged: a widening change was not acknowledged.
	ErrUnacknowledged = errors.New("every widening change needs its own acknowledgement")
	// ErrRejected: the entry makes the configuration invalid or adds problems.
	ErrRejected = errors.New("the change was not taken")
	errNotMapping = errors.New("the configuration is not a YAML mapping")
)

// Entry is one pending change: the whole desired operationOverrides entry
// for one operation, componentOverrides entry for one component, or the
// overlays list.
type Entry struct {
	ID           string                    `json:"id"`
	Kind         string                    `json:"kind"` // operation | component | overlays
	OperationKey string                    `json:"operationKey,omitempty"`
	Match        *config.Match             `json:"match,omitempty"`
	Schema       string                    `json:"schema,omitempty"`
	Operation    *config.OperationOverride `json:"operation,omitempty"`
	Component    *config.ComponentOverride `json:"component,omitempty"`
	Overlays     []string                  `json:"overlays,omitempty"`
	Remove       bool                      `json:"remove,omitempty"`
}

// State is everything the frontend draws.
type State struct {
	ConfigPath string     `json:"configPath"`
	Spec       string     `json:"spec,omitempty"`
	Base       *Snapshot  `json:"-"`
	Draft      *Snapshot  `json:"draft"`
	Comparison Comparison `json:"comparison"`
	Entries    []Entry    `json:"entries"`
}

// Workbench is a draft of one configuration file.
type Workbench struct {
	mu       sync.Mutex
	path     string
	spec     string
	pipe     Pipeline
	disk     []byte
	diskHash string
	entries  map[string]Entry
	order    []string
	base     *Snapshot
}

// New loads the configuration at path (which may not exist yet) and
// evaluates it.
func New(ctx context.Context, path, spec string, pipe Pipeline) (*Workbench, error) {
	w := &Workbench{path: path, spec: spec, pipe: pipe, entries: map[string]Entry{}}
	if err := w.readDisk(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Workbench) readDisk(ctx context.Context) error {
	data, err := os.ReadFile(w.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	w.disk, w.diskHash = data, hashOf(data)
	base, err := w.pipe.Evaluate(ctx, data)
	if err != nil {
		return err
	}
	w.base = base
	return nil
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// EntryID is the identity of a pending entry: one per operation match, one
// per component, one for the overlays list.
func EntryID(e Entry) string {
	switch e.Kind {
	case "operation":
		m := e.Match
		if m == nil && e.Operation != nil {
			m = &e.Operation.Match
		}
		if m == nil {
			return "operation:"
		}
		if m.OperationID != "" {
			return "operation:" + m.OperationID
		}
		return "operation:" + m.Method + " " + m.Path
	case "component":
		if e.Schema == "" && e.Component != nil {
			return "component:" + e.Component.Match.Schema
		}
		return "component:" + e.Schema
	default:
		return "overlays"
	}
}

// draftBytes renders the disk configuration with every pending entry applied.
func (w *Workbench) draftBytes(entries map[string]Entry, order []string) ([]byte, error) {
	doc, err := document(w.disk, w.spec)
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]
	for _, id := range order {
		if err := applyEntry(root, entries[id]); err != nil {
			return nil, err
		}
	}
	if len(order) == 0 && len(w.disk) > 0 {
		return w.disk, nil
	}
	return render(doc)
}

func applyEntry(root *yaml.Node, e Entry) error {
	switch e.Kind {
	case "operation":
		var match config.Match
		if e.Match != nil {
			match = *e.Match
		} else if e.Operation != nil {
			match = e.Operation.Match
		}
		seq := sequence(root, "operationOverrides", !e.Remove)
		return upsert(seq, func(item *yaml.Node) bool { return sameMatch(matchOf(item), match) }, e.Remove, e.Operation)
	case "component":
		name := e.Schema
		if name == "" && e.Component != nil {
			name = e.Component.Match.Schema
		}
		seq := sequence(root, "componentOverrides", !e.Remove)
		return upsert(seq, func(item *yaml.Node) bool {
			var c config.ComponentOverride
			_ = item.Decode(&c)
			return c.Match.Schema == name
		}, e.Remove, e.Component)
	case "overlays":
		_, specNode := lookup(root, "spec")
		if specNode == nil {
			specNode = &yaml.Node{Kind: yaml.MappingNode}
			setKey(root, "spec", specNode)
		}
		if len(e.Overlays) == 0 {
			deleteKey(specNode, "overlays")
			return nil
		}
		node, err := encode(e.Overlays)
		if err != nil {
			return err
		}
		setKey(specNode, "overlays", node)
		return nil
	}
	return fmt.Errorf("unknown entry kind %q", e.Kind)
}

func upsert(seq *yaml.Node, is func(*yaml.Node) bool, remove bool, desired any) error {
	if seq == nil {
		return nil
	}
	for i, item := range seq.Content {
		if !is(item) {
			continue
		}
		if remove {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
			return nil
		}
		node, err := encode(desired)
		if err != nil {
			return err
		}
		mergeInto(item, node)
		return nil
	}
	if remove {
		return nil
	}
	node, err := encode(desired)
	if err != nil {
		return err
	}
	seq.Content = append(seq.Content, node)
	return nil
}

// State evaluates the current draft.
func (w *Workbench) State(ctx context.Context) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state(ctx, w.entries, w.order)
}

func (w *Workbench) state(ctx context.Context, entries map[string]Entry, order []string) (*State, error) {
	data, err := w.draftBytes(entries, order)
	if err != nil {
		return nil, err
	}
	draft, err := w.pipe.Evaluate(ctx, data)
	if err != nil {
		return nil, err
	}
	cmp := Compare(w.base, draft)
	SortWidening(cmp.Widening)
	st := &State{ConfigPath: w.path, Spec: w.spec, Base: w.base, Draft: draft, Comparison: cmp}
	for _, id := range order {
		st.Entries = append(st.Entries, entries[id])
	}
	return st, nil
}

// Apply adds or replaces one pending entry. An entry that makes the
// configuration invalid, or adds a problem the draft did not have, is not
// taken: the draft stays as it was (spec §5).
func (w *Workbench) Apply(ctx context.Context, e Entry) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e.ID = EntryID(e)
	entries, order := w.with(e)
	before, err := w.state(ctx, w.entries, w.order)
	if err != nil {
		return nil, err
	}
	after, err := w.state(ctx, entries, order)
	if err != nil {
		return nil, err
	}
	if after.Draft.ConfigError != "" && before.Draft.ConfigError == "" {
		return after, fmt.Errorf("%w: %s", ErrRejected, after.Draft.ConfigError)
	}
	if added := newProblems(before.Draft.Problems, after.Draft.Problems); len(added) > 0 {
		return after, fmt.Errorf("%w: %s", ErrRejected, added[0].Message)
	}
	w.entries, w.order = entries, order
	return after, nil
}

func (w *Workbench) with(e Entry) (map[string]Entry, []string) {
	entries := make(map[string]Entry, len(w.entries)+1)
	for k, v := range w.entries {
		entries[k] = v
	}
	order := append([]string(nil), w.order...)
	if _, exists := entries[e.ID]; !exists {
		order = append(order, e.ID)
	}
	entries[e.ID] = e
	return entries, order
}

func newProblems(before, after []Problem) []Problem {
	seen := map[string]bool{}
	for _, p := range before {
		seen[p.Message] = true
	}
	var out []Problem
	for _, p := range after {
		if !seen[p.Message] {
			out = append(out, p)
		}
	}
	return out
}

// Revert drops one pending entry.
func (w *Workbench) Revert(ctx context.Context, id string) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.entries[id]; ok {
		delete(w.entries, id)
		for i, existing := range w.order {
			if existing == id {
				w.order = append(w.order[:i], w.order[i+1:]...)
				break
			}
		}
	}
	return w.state(ctx, w.entries, w.order)
}

// Diff is the draft against the file on disk.
func (w *Workbench) Diff() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data, err := w.draftBytes(w.entries, w.order)
	if err != nil {
		return "", err
	}
	return UnifiedDiff(filepath.Base(w.path), w.disk, data), nil
}

// Save writes the draft if the file on disk is still the one it is based on
// and every widening change is acknowledged.
func (w *Workbench) Save(ctx context.Context, acknowledged []string) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	current, err := os.ReadFile(w.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if hashOf(current) != w.diskHash {
		return nil, ErrChanged
	}
	st, err := w.state(ctx, w.entries, w.order)
	if err != nil {
		return nil, err
	}
	if st.Draft.ConfigError != "" {
		return st, fmt.Errorf("%w: %s", ErrRejected, st.Draft.ConfigError)
	}
	ack := map[string]bool{}
	for _, id := range acknowledged {
		ack[id] = true
	}
	for _, wd := range st.Comparison.Widening {
		if !ack[wd.ID] {
			return st, fmt.Errorf("%w: %s", ErrUnacknowledged, wd.Message)
		}
	}
	data, err := w.draftBytes(w.entries, w.order)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(w.path, data); err != nil {
		return nil, err
	}
	w.entries, w.order = map[string]Entry{}, nil
	if err := w.readDisk(ctx); err != nil {
		return nil, err
	}
	return w.state(ctx, w.entries, w.order)
}

// Reload re-reads the file on disk and re-applies the pending entries.
func (w *Workbench) Reload(ctx context.Context) (*State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.readDisk(ctx); err != nil {
		return nil, err
	}
	return w.state(ctx, w.entries, w.order)
}

// Explain renders a dry run under the draft.
func (w *Workbench) Explain(ctx context.Context, key string, arguments []byte) (string, error) {
	w.mu.Lock()
	data, err := w.draftBytes(w.entries, w.order)
	w.mu.Unlock()
	if err != nil {
		return "", err
	}
	return w.pipe.Explain(ctx, data, key, arguments)
}

// writeAtomic replaces path with data: a temporary file in the same
// directory, synced, then renamed over it, keeping the existing mode.
func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SortedEntryIDs is for tests and stable output.
func SortedEntryIDs(st *State) []string {
	ids := make([]string, 0, len(st.Entries))
	for _, e := range st.Entries {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}
```

- [ ] **Step 3: Smoke** — throwaway test, deleted after:

```bash
cat > internal/cli/zz_smoke_test.go <<'EOF'
package cli
import ("context";"os";"testing";"github.com/razrabotchik/lotsman/internal/config";"github.com/razrabotchik/lotsman/internal/workbench")
func TestZZDraft(t *testing.T){
 dir:=t.TempDir(); b,_:=os.ReadFile("../../cmd/api-gateway/openapi.yaml"); os.WriteFile(dir+"/openapi.yaml",b,0o644)
 cfg:=dir+"/lotsman.yaml"; os.WriteFile(cfg,[]byte("# my comment\napiVersion: lotsman.dev/v1alpha1\nspec: {source: ./openapi.yaml, strict: false} # keep\n"),0o644)
 ctx:=context.Background(); wb,err:=workbench.New(ctx,cfg,"",newPipeline(pipelineInputs{configPath:cfg})); if err!=nil{t.Fatal(err)}
 st,err:=wb.Apply(ctx,workbench.Entry{Kind:"operation",Operation:&config.OperationOverride{Match:config.Match{OperationID:"patchPetCompeting"},MediaType:"application/merge-patch+json"}})
 if err!=nil{t.Fatal(err)}; t.Logf("summary=%+v", st.Comparison.Summary)
 d,_:=wb.Diff(); t.Log(d)
 if _,err:=wb.Save(ctx,nil); err!=nil{t.Fatal(err)}; out,_:=os.ReadFile(cfg); t.Log(string(out))
}
EOF
go test ./internal/cli/ -run TestZZDraft -v | tail -30; rm internal/cli/zz_smoke_test.go
```

Expected: `Fixed:1`; the diff adds an `operationOverrides` block; the saved file still has
`# my comment` and `# keep`.

- [ ] **Step 4: Commit** — `git add internal/workbench && git commit -m "workbench: черновик конфига на yaml.Node, сохранение с проверкой хеша и подтверждением расширений"` (+ trailer).

---

### Task 5: `inspect --diff-against OLD.yaml`

**Files:**
- Modify: `internal/cli/inspect.go`

**Interfaces — Consumes:** `newPipeline`, `pipelineInputs` (task 3); `workbench.Compare`,
`Comparison`, `SortWidening` (task 2).

- [ ] **Step 1: the flag.** In `inspect`, add
`diffAgainst := fs.String("diff-against", "", "compare --config with an older configuration file and print what changes (CI)")`.
Right after the flags are parsed and before the normal report:

```go
	if *diffAgainst != "" {
		return inspectDiff(ctx, spec, *configPath, *diffAgainst, *asJSON, stdout, stderr)
	}
```

(`spec` is the positional argument, possibly empty — the configurations may name it.)

- [ ] **Step 2: `inspectDiff`**

```go
// inspectDiff is the workbench's comparison for CI: what a configuration
// change fixes, breaks, alters and widens (spec §3).
func inspectDiff(ctx context.Context, spec, newPath, oldPath string, asJSON bool, stdout, stderr io.Writer) int {
	if newPath == "" {
		fmt.Fprintln(stderr, "lotsman: inspect --diff-against needs --config (the new configuration)")
		return exitUsage
	}
	read := func(path string) (*workbench.Snapshot, bool) {
		// #nosec G304 -- operator-supplied CLI arguments.
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "lotsman: [usage] cannot read %s\n", path)
			return nil, false
		}
		snap, _, _ := evaluate(ctx, data, pipelineInputs{configPath: path, spec: spec})
		if snap.SpecError != "" || snap.ConfigError != "" {
			fmt.Fprintf(stderr, "lotsman: [usage] %s: %s\n", path, firstNonEmpty(snap.ConfigError, snap.SpecError))
			return nil, false
		}
		return snap, true
	}
	before, ok := read(oldPath)
	if !ok {
		return exitUsage
	}
	after, ok := read(newPath)
	if !ok {
		return exitUsage
	}
	cmp := workbench.Compare(before, after)
	workbench.SortWidening(cmp.Widening)
	if asJSON {
		if err := newJSONEncoder(stdout).Encode(cmp); err != nil {
			fmt.Fprintf(stderr, "lotsman: %v\n", err)
			return exitError
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "changes   %d fixed, %d broken, %d altered\n",
		cmp.Summary.Fixed, cmp.Summary.Broken, cmp.Summary.Altered)
	for _, c := range cmp.Changes {
		fmt.Fprintf(stdout, "%-8s  %s (%s → %s)\n", c.Kind, c.Key, c.Before, c.After)
		for _, w := range c.Warnings {
			fmt.Fprintf(stdout, "          %s: %s\n", w.Field, w.Message)
		}
	}
	for _, w := range cmp.Widening {
		fmt.Fprintf(stdout, "widening  %s\n", w.Message)
	}
	for _, p := range after.Problems {
		fmt.Fprintf(stdout, "problem   %s[%d]: %s\n", p.Section, p.Index, p.Message)
	}
	return exitOK
}
```

Add `"os"` and the workbench import. Update the `usage` text in cli.go: the `inspect` line gains
`[--diff-against OLD.yaml]`.

- [ ] **Step 3: Smoke**

```bash
go build ./... && make build
printf 'apiVersion: lotsman.dev/v1alpha1\nspec: {source: ./openapi.yaml, strict: false}\n' > /tmp/lotsman-demo/old.yaml
bin/lotsman inspect --config /tmp/lotsman-demo/lotsman.yaml --diff-against /tmp/lotsman-demo/old.yaml
bin/lotsman inspect --config /tmp/lotsman-demo/lotsman.yaml --diff-against /tmp/lotsman-demo/old.yaml --json | head -c 400
```

Expected: `changes   2 fixed, …` (patchPetCompeting, getFiltered), `altered` for listPets with a
`parameter query/limit` warning; JSON starts with `{"summary":`.

- [ ] **Step 4: Commit** — `git commit -am "inspect --diff-against: что изменение конфига чинит, ломает и меняет — для CI"` (+ trailer).

---

### Task 6: `lotsman ui` — command and loopback server

**Files:**
- Create: `internal/ui/doc.go`, `internal/ui/server.go`, `internal/ui/open.go`, `internal/ui/web/index.html` (placeholder replaced in task 7)
- Create: `internal/cli/ui.go`
- Modify: `internal/cli/cli.go` (dispatch + usage)

**Interfaces — Consumes:** `workbench.New`, `(*Workbench).State/Apply/Revert/Diff/Save/Reload/Explain`,
`Entry`, `State`, `ErrChanged`, `ErrUnacknowledged`, `ErrRejected` (task 4); `newPipeline` (task 3).
**Produces (HTTP API, all JSON, all under the guard):**

| Method & path | Body | 200 response | Errors |
|---|---|---|---|
| `GET /api/state` | — | `State` without per-operation schemas (see `lighten`) | — |
| `GET /api/operation?key=K` | — | `{"before": Operation, "after": Operation}` full | 404 unknown key |
| `POST /api/draft` | `Entry` | light `State` | 422 `{"error": "...", "state": light State}` on `ErrRejected` |
| `DELETE /api/draft?id=ID` | — | light `State` | — |
| `GET /api/diff` | — | `{"diff": "..."}` | — |
| `POST /api/save` | `{"acknowledged": [ids]}` | light `State` | 409 on `ErrChanged`, 422 on `ErrUnacknowledged`/`ErrRejected` |
| `POST /api/reload` | — | light `State` | — |
| `POST /api/explain` | `{"key": K, "arguments": {...}}` | `{"text": "..."}` | 422 with `{"error"}` |

- [ ] **Step 1: `internal/ui/doc.go`**

```go
// Package ui is the local workbench behind `lotsman ui` (feature 008, spec
// §4): a loopback HTTP server over internal/workbench and an embedded
// frontend. It writes one file, the configuration fixed at start, and is
// imported only by the ui command -- serve never sees it.
package ui
```

- [ ] **Step 2: `internal/ui/server.go`**

```go
package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/razrabotchik/lotsman/internal/workbench"
)

//go:embed web
var web embed.FS

const cookieName = "lotsman_ui"

// maxBody bounds a request body; a correction is a few kilobytes.
const maxBody = 1 << 20

// csp lets the page load only itself: no inline script, no third-party
// anything, no framing.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; " +
	"img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Server serves one workbench on loopback.
type Server struct {
	wb       *workbench.Workbench
	token    string
	listener net.Listener
	port     int
}

// Listen binds 127.0.0.1:port (0 picks a free port) and generates the token.
func Listen(wb *workbench.Workbench, port int) (*Server, error) {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		l.Close()
		return nil, err
	}
	return &Server{wb: wb, token: hex.EncodeToString(raw), listener: l,
		port: l.Addr().(*net.TCPAddr).Port}, nil
}

// URL is the address to open, token included. It is printed once; the token
// then lives in an HttpOnly cookie.
func (s *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", s.port, s.token)
}

// Serve runs until ctx ends.
func (s *Server) Serve(ctx context.Context) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler is the whole server: guard first, then routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(web, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/operation", s.operation)
	mux.HandleFunc("POST /api/draft", s.apply)
	mux.HandleFunc("DELETE /api/draft", s.revert)
	mux.HandleFunc("GET /api/diff", s.diff)
	mux.HandleFunc("POST /api/save", s.save)
	mux.HandleFunc("POST /api/reload", s.reload)
	mux.HandleFunc("POST /api/explain", s.explain)
	return s.guard(mux)
}

// guard enforces spec §4 before any route: Host (DNS rebinding), token
// (another local user or a page in the browser), Origin on writes (CSRF),
// and the security headers on every response.
func (s *Server) guard(next http.Handler) http.Handler {
	hosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", s.port): true,
		fmt.Sprintf("localhost:%d", s.port): true,
	}
	origins := map[string]bool{
		fmt.Sprintf("http://127.0.0.1:%d", s.port): true,
		fmt.Sprintf("http://localhost:%d", s.port): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if !hosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !origins[r.Header.Get("Origin")] {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		// The token arrives once in the URL the terminal printed; it is
		// accepted only if it is ours, so a page cannot fix a token of its own
		// choosing into the cookie.
		if q := r.URL.Query().Get("token"); q != "" && r.Method == http.MethodGet && r.URL.Path == "/" {
			if !s.tokenMatches(q) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || !s.tokenMatches(c.Value) {
			http.Error(w, "open the address lotsman ui printed in the terminal", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenMatches(v string) bool {
	return subtle.ConstantTimeCompare([]byte(v), []byte(s.token)) == 1
}

// lighten drops per-operation schemas from a state: the tree needs status
// and names, and Kubernetes' schemas alone are megabytes. The form fetches
// one operation in full.
func lighten(st *workbench.State) *workbench.State {
	if st == nil || st.Draft == nil {
		return st
	}
	light := *st
	snap := *st.Draft
	snap.Operations = make([]workbench.Operation, len(st.Draft.Operations))
	for i, op := range st.Draft.Operations {
		op.InputSchema = nil
		op.Body = nil
		params := make([]workbench.Parameter, len(op.Parameters))
		for j, p := range op.Parameters {
			p.Schema = nil
			params[j] = p
		}
		op.Parameters = params
		snap.Operations[i] = op
	}
	light.Draft = &snap
	return &light
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st, err := s.wb.State(r.Context())
	reply(w, lighten(st), err)
}

func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	st, err := s.wb.State(r.Context())
	if err != nil {
		reply(w, nil, err)
		return
	}
	find := func(snap *workbench.Snapshot) *workbench.Operation {
		if snap == nil {
			return nil
		}
		for i := range snap.Operations {
			if snap.Operations[i].Key == key {
				return &snap.Operations[i]
			}
		}
		return nil
	}
	after := find(st.Draft)
	if after == nil {
		http.Error(w, "unknown operation", http.StatusNotFound)
		return
	}
	reply(w, map[string]any{"before": find(st.Base), "after": after}, nil)
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	var e workbench.Entry
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		http.Error(w, "invalid entry", http.StatusBadRequest)
		return
	}
	st, err := s.wb.Apply(r.Context(), e)
	reply(w, lighten(st), err)
}

func (s *Server) revert(w http.ResponseWriter, r *http.Request) {
	st, err := s.wb.Revert(r.Context(), r.URL.Query().Get("id"))
	reply(w, lighten(st), err)
}

func (s *Server) diff(w http.ResponseWriter, _ *http.Request) {
	d, err := s.wb.Diff()
	reply(w, map[string]string{"diff": d}, err)
}

func (s *Server) save(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Acknowledged []string `json:"acknowledged"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	st, err := s.wb.Save(r.Context(), body.Acknowledged)
	reply(w, lighten(st), err)
}

func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	st, err := s.wb.Reload(r.Context())
	reply(w, lighten(st), err)
}

func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key       string          `json:"key"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	text, err := s.wb.Explain(r.Context(), body.Key, body.Arguments)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// reply maps workbench errors to statuses; a refused change still carries
// the state so the frontend can show why.
func reply(w http.ResponseWriter, v any, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, v)
	case errors.Is(err, workbench.ErrChanged):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	case errors.Is(err, workbench.ErrRejected), errors.Is(err, workbench.ErrUnacknowledged):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error(), "state": v})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

Note: `reply` receives `lighten(st)` as `v` — when `st` is a typed nil pointer, `lighten` returns
nil; fine.

- [ ] **Step 3: `internal/ui/open.go`**

```go
package ui

import (
	"os/exec"
	"runtime"
)

// Open asks the desktop to open url. Failure is not an error: the address
// is printed either way.
func Open(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
```

- [ ] **Step 4: placeholder frontend** — `internal/ui/web/index.html`:

```html
<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>lotsman ui</title></head>
<body><p>lotsman ui — frontend arrives in the next task.</p></body></html>
```

- [ ] **Step 5: `internal/cli/ui.go`**

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/ui"
	"github.com/razrabotchik/lotsman/internal/workbench"
)

// uiCommand implements `lotsman ui [SPEC] [--config FILE] [--root DIR]
// [--port N] [--no-open]` (feature 008, spec §4).
func uiCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "configuration file to edit (default ./lotsman.yaml, created on first save)")
	root := fs.String("root", "", "directory $ref resolution is confined to")
	port := fs.Int("port", 0, "loopback port (default: a free one)")
	noOpen := fs.Bool("no-open", false, "print the address instead of opening a browser")
	spec, err := parseWithTrailingSpec(fs, args)
	if err != nil {
		return exitUsage
	}
	path := *configPath
	if path == "" {
		path = "lotsman.yaml"
	}
	if abs, absErr := filepath.Abs(path); absErr == nil {
		path = abs
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) && spec == "" {
		fmt.Fprintf(stderr, "lotsman: ui: %s does not exist; pass SPEC to start a new configuration\n", path)
		return exitUsage
	}
	// A new configuration names the document relative to itself, so it means
	// the same thing from any directory.
	sourceForNew := spec
	if spec != "" && spec != "-" {
		if abs, absErr := filepath.Abs(spec); absErr == nil {
			if rel, relErr := filepath.Rel(filepath.Dir(path), abs); relErr == nil {
				sourceForNew = rel
			}
		}
	}
	wb, err := workbench.New(ctx, path, sourceForNew, newPipeline(pipelineInputs{configPath: path, spec: spec, root: *root}))
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: [%s] %v\n", errs.ClassOf(err), err)
		return exitCode(err)
	}
	srv, err := ui.Listen(wb, *port)
	if err != nil {
		fmt.Fprintf(stderr, "lotsman: ui: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "lotsman ui: editing %s\nopen %s\n(ctrl-c to stop)\n", path, srv.URL())
	if !*noOpen {
		ui.Open(srv.URL())
	}
	if err := srv.Serve(ctx); err != nil {
		fmt.Fprintf(stderr, "lotsman: ui: %v\n", err)
		return exitError
	}
	return exitOK
}
```

In cli.go `Run`: `case "ui": return uiCommand(ctx, rest, stdout, stderr)`; add to `usage`:
`  lotsman ui [SPEC] [--config FILE] [--root DIR] [--port N] [--no-open]` with a one-line
description matching the style of the other entries.

- [ ] **Step 6: Smoke**

```bash
go build ./... && make build
(bin/lotsman ui --config /tmp/lotsman-demo/lotsman.yaml --port 18999 --no-open > /tmp/ui.out 2>&1 &) ; sleep 2
URL=$(grep -o 'http://127.0.0.1:18999/?token=[0-9a-f]*' /tmp/ui.out)
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18999/api/state                          # 403
curl -s -c /tmp/jar -o /dev/null -w '%{http_code}\n' "$URL"                                          # 303
curl -s -b /tmp/jar http://127.0.0.1:18999/api/state | head -c 200; echo
curl -s -b /tmp/jar -H 'Host: evil.example:18999' -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18999/api/state   # 403
curl -s -b /tmp/jar -X POST -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18999/api/reload       # 403 (no Origin)
curl -s -b /tmp/jar -X POST -H 'Origin: http://127.0.0.1:18999' -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18999/api/reload  # 200
pkill -f 'lotsman ui --config /tmp/lotsman-demo'
```

- [ ] **Step 7: Commit** — `git add internal/ui internal/cli && git commit -m "lotsman ui: loopback-сервер с токеном, проверкой Host и Origin, API черновика"` (+ trailer).

---

## Frontend rules (tasks 7–9)

- Plain ES modules, no framework, no build step. Files live in `internal/ui/web/` and are
  embedded as they are.
- **Never `innerHTML` with data.** Operation summaries, paths, parameter names and messages come
  from an untrusted vendor document. Build DOM with the `h()` helper (task 7) and `textContent`.
- No inline `<script>`, no `style=` attributes, no `on*=` attributes (the CSP forbids them);
  classes only.
- Visible text is English. Colours and type follow `design/` (paper `#FBF9F4`, ink `#0D1B2A`,
  signal red `#D7262E` only for refusals, teal `#008C7E` the one accent, amber `#E0A100` for
  "needs a decision", grey `#B4BCC6` for blocked).

---

### Task 7: shell, tree, landing pane

**Files:**
- Create: `internal/ui/web/index.html`, `style.css`, `api.js`, `dom.js`, `app.js`, `tree.js`,
  `landing.js`, `fonts/` (4 woff2 + `OFL.txt`)

**Interfaces — Produces** (used by tasks 8–9):
- `dom.js`: `h(tag, props, ...children)`, `clear(el)`
- `api.js`: `api.state()`, `api.operation(key)`, `api.apply(entry)`, `api.revert(id)`,
  `api.diff()`, `api.save(ack)`, `api.reload()`, `api.explain(key, args)` — each resolves to the
  JSON body or throws `Error` with `.status` and `.data`.
- `app.js` exports `store` and `refresh(state)`, `select(key)`, `toast(message, kind)`; it imports
  `renderForm` from `form.js` and `renderTray` from `tray.js` — create both as stubs here:
  `export function renderForm(el, store) { el.textContent = store.selected || ''; }` and
  `export function renderTray(el, store) {}`.

- [ ] **Step 1: fonts.** Download into `internal/ui/web/fonts/`:

```bash
cd internal/ui/web && mkdir -p fonts && cd fonts
for f in ibm-plex-sans-latin-400-normal ibm-plex-sans-latin-600-normal; do
  curl -fsSLo $f.woff2 https://cdn.jsdelivr.net/npm/@fontsource/ibm-plex-sans@5/files/$f.woff2; done
for f in ibm-plex-mono-latin-400-normal ibm-plex-mono-latin-600-normal; do
  curl -fsSLo $f.woff2 https://cdn.jsdelivr.net/npm/@fontsource/ibm-plex-mono@5/files/$f.woff2; done
curl -fsSLo OFL.txt https://raw.githubusercontent.com/IBM/plex/master/LICENSE.txt
ls -la; du -ch *.woff2 | tail -1
```

Verify each file starts with `wOF2` (`head -c4`). If the CDN is unreachable, report BLOCKED
rather than shipping without fonts.

- [ ] **Step 2: `index.html`**

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>lotsman ui</title>
<link rel="stylesheet" href="style.css">
<script type="module" src="app.js"></script>
</head>
<body>
<header class="top">
  <span class="brand"><span class="mark" aria-hidden="true"></span>lotsman</span>
  <span id="file" class="file"></span>
  <span id="counts" class="counts"></span>
  <input id="search" type="search" placeholder="Search path, operationId, summary" autocomplete="off">
  <label class="toggle"><input id="only-problems" type="checkbox"> only problems</label>
  <button id="next-problem" type="button">Next problem →</button>
</header>
<div class="layout">
  <nav id="tree" class="tree" aria-label="API"></nav>
  <main id="main" class="main"></main>
</div>
<footer id="tray" class="tray"></footer>
<div id="toast" class="toast" role="status" aria-live="polite"></div>
</body>
</html>
```

- [ ] **Step 3: `style.css`**

```css
@font-face { font-family: "IBM Plex Sans"; font-weight: 400; src: url(fonts/ibm-plex-sans-latin-400-normal.woff2) format("woff2"); }
@font-face { font-family: "IBM Plex Sans"; font-weight: 600; src: url(fonts/ibm-plex-sans-latin-600-normal.woff2) format("woff2"); }
@font-face { font-family: "IBM Plex Mono"; font-weight: 400; src: url(fonts/ibm-plex-mono-latin-400-normal.woff2) format("woff2"); }
@font-face { font-family: "IBM Plex Mono"; font-weight: 600; src: url(fonts/ibm-plex-mono-latin-600-normal.woff2) format("woff2"); }
:root {
  --paper: #FBF9F4; --paper-2: #F3EFE6; --ink: #0D1B2A; --ink-2: #3B4A5C; --ink-3: #6B7889;
  --line: #E2DCCF; --red: #D7262E; --red-soft: #FBE7E6; --teal: #008C7E; --teal-ink: #00695F;
  --teal-soft: #E1F2EF; --amber: #E0A100; --amber-soft: #FFF3D6; --grey: #B4BCC6; --grey-soft: #ECEAE4;
  --sans: "IBM Plex Sans", system-ui, sans-serif; --mono: "IBM Plex Mono", ui-monospace, monospace;
}
* { box-sizing: border-box; }
html, body { margin: 0; height: 100%; }
body { display: grid; grid-template-rows: auto 1fr auto; background: var(--paper); color: var(--ink); font: 14px/1.45 var(--sans); }
button, input, select, textarea { font: inherit; }
button { border: 1px solid var(--line); background: #fff; border-radius: 6px; padding: 4px 10px; cursor: pointer; }
button.primary { background: var(--ink); color: #fff; border-color: var(--ink); font-weight: 600; }
button:disabled { opacity: .5; cursor: default; }
.top { display: flex; gap: 12px; align-items: center; padding: 8px 14px; border-bottom: 1px solid var(--line); }
.brand { display: flex; gap: 8px; align-items: center; font: 600 17px var(--mono); }
.mark { width: 20px; height: 20px; border-radius: 5px; background: var(--ink); position: relative; }
.mark::after { content: ""; position: absolute; left: 5px; top: 3px; width: 6px; height: 6px; border-radius: 50%; background: var(--red); }
.file, .counts { font: 12px var(--mono); color: var(--ink-3); }
.counts { margin-left: auto; }
#search { width: 260px; padding: 4px 8px; border: 1px solid var(--line); border-radius: 6px; }
.toggle { font-size: 13px; color: var(--ink-2); }
.layout { display: grid; grid-template-columns: minmax(280px, 30%) 1fr; min-height: 0; }
.tree { overflow: auto; background: var(--paper-2); border-right: 1px solid var(--line); padding: 6px 0; font: 12.5px var(--mono); }
.tree ul { list-style: none; margin: 0; padding: 0 0 0 12px; }
.tree > ul { padding-left: 4px; }
.tree .branch, .tree .leaf { display: flex; gap: 6px; align-items: center; width: 100%; border: 0; background: none; padding: 2px 8px; text-align: left; border-radius: 0; }
.tree .leaf.selected { background: var(--paper); box-shadow: inset 3px 0 0 var(--teal); }
.tree .count { margin-left: auto; font-size: 11px; padding: 0 5px; border-radius: 3px; background: var(--amber-soft); color: #8A6200; }
.tree .method { width: 52px; color: var(--ink-3); }
.tree .group { padding: 8px 10px 2px; font: 600 10px var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
.dot { width: 8px; height: 8px; border-radius: 50%; flex: none; }
.s-decision { background: var(--amber); } .s-unsupported { background: var(--red); }
.s-blocked { background: var(--grey); } .s-works { background: var(--teal); }
.pending { width: 6px; height: 6px; border-radius: 50%; background: var(--ink); margin-left: auto; }
.main { overflow: auto; padding: 16px 22px 90px; }
.main h2 { font: 600 16px var(--mono); margin: 0 0 4px; }
.muted { color: var(--ink-3); }
.pill { font: 500 11px var(--mono); padding: 2px 6px; border-radius: 4px; }
.p-decision { background: var(--amber-soft); color: #6B4D00; } .p-unsupported { background: var(--red-soft); color: #8E1A1F; }
.p-blocked { background: var(--grey-soft); color: var(--ink-3); } .p-works { background: var(--teal-soft); color: var(--teal-ink); }
.banner { padding: 10px 12px; border-radius: 8px; margin: 0 0 12px; }
.banner.error { background: var(--red-soft); color: #8E1A1F; } .banner.warn { background: var(--amber-soft); color: #6B4D00; }
.banner.info { background: var(--teal-soft); color: var(--teal-ink); }
section.field { border: 1px solid var(--line); border-radius: 8px; padding: 10px 12px; margin: 10px 0; background: #fff; }
section.field h3 { margin: 0 0 6px; font: 600 11px var(--mono); letter-spacing: .08em; text-transform: uppercase; color: var(--ink-3); }
.warning { font-size: 12.5px; padding: 4px 8px; border-radius: 4px; margin-top: 6px; }
.w-error { background: var(--red-soft); color: #8E1A1F; } .w-warn { background: var(--amber-soft); color: #6B4D00; }
.w-info { background: var(--teal-soft); color: var(--teal-ink); }
table.params { width: 100%; border-collapse: collapse; font: 12.5px var(--mono); }
table.params td, table.params th { text-align: left; padding: 4px 6px; border-bottom: 1px solid var(--line); vertical-align: top; }
textarea { width: 100%; min-height: 90px; font: 12.5px var(--mono); border: 1px solid var(--line); border-radius: 6px; padding: 6px; }
pre { font: 12.5px/1.5 var(--mono); background: var(--ink); color: #D8E1EA; padding: 10px 12px; border-radius: 8px; overflow: auto; }
.diff .add { color: #4CC9B7; } .diff .del { color: #F0484F; } .diff .hunk { color: #7D8FA3; }
.actions { display: flex; gap: 8px; margin-top: 12px; }
.tabs { display: flex; gap: 4px; margin: 12px 0 0; } .tabs button.active { background: var(--ink); color: #fff; }
.tray { position: fixed; left: 0; right: 0; bottom: 0; display: flex; gap: 10px; align-items: center; padding: 8px 14px; background: var(--ink); color: #EEF2F6; font: 12.5px var(--mono); }
.tray .chip { background: #1B2B3D; border: 0; color: #EEF2F6; padding: 2px 8px; border-radius: 4px; }
.tray .fixed { color: #4CC9B7; } .tray .broken { color: #F0484F; } .tray .altered { color: #E8B65A; }
.tray .spacer { margin-left: auto; }
.tray button.primary { background: #22A896; border-color: #22A896; color: #08131F; }
dialog { border: 1px solid var(--ink); border-radius: 10px; padding: 16px 18px; width: min(900px, 92vw); max-height: 86vh; overflow: auto; }
dialog .widening { border: 1px solid var(--red); background: var(--red-soft); border-radius: 8px; padding: 8px 12px; margin: 10px 0; }
.toast { position: fixed; right: 16px; bottom: 56px; padding: 8px 12px; border-radius: 6px; background: var(--ink); color: #fff; opacity: 0; transition: opacity .2s; }
.toast.show { opacity: 1; } .toast.error { background: var(--red); }
```

- [ ] **Step 4: `dom.js`**

```js
// h builds an element without ever parsing HTML: every string becomes a text
// node. The document this UI shows is untrusted, so there is no innerHTML here.
export function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
    else if (k in el && typeof v !== "string") el[k] = v;
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

export function clear(el) { while (el.firstChild) el.firstChild.remove(); return el; }
```

- [ ] **Step 5: `api.js`**

```js
async function call(method, path, body) {
  const init = { method, credentials: "same-origin", headers: {} };
  if (body !== undefined) { init.headers["Content-Type"] = "application/json"; init.body = JSON.stringify(body); }
  const res = await fetch(path, init);
  let data = {};
  try { data = await res.json(); } catch { data = {}; }
  if (!res.ok) {
    const err = new Error((data && data.error) || res.statusText);
    err.status = res.status; err.data = data;
    throw err;
  }
  return data;
}

const q = encodeURIComponent;
export const api = {
  state: () => call("GET", "/api/state"),
  operation: (key) => call("GET", "/api/operation?key=" + q(key)),
  apply: (entry) => call("POST", "/api/draft", entry),
  revert: (id) => call("DELETE", "/api/draft?id=" + q(id)),
  diff: () => call("GET", "/api/diff"),
  save: (acknowledged) => call("POST", "/api/save", { acknowledged }),
  reload: () => call("POST", "/api/reload"),
  explain: (key, args) => call("POST", "/api/explain", { key, arguments: args }),
};
```

- [ ] **Step 6: `tree.js`**

```js
import { h, clear } from "./dom.js";

const PROBLEM = new Set(["decision", "unsupported"]);

// pendingKeys maps an operation key to true when the draft holds an entry for it.
export function pendingKeys(state) {
  const keys = new Set();
  for (const e of state.entries || []) if (e.operationKey) keys.add(e.operationKey);
  return keys;
}

function visible(op, filter, pending) {
  if (filter.onlyProblems && !PROBLEM.has(op.status) && !pending.has(op.key)) return false;
  if (!filter.q) return true;
  const q = filter.q.toLowerCase();
  return [op.path, op.operationId || "", op.summary || ""].some((s) => s.toLowerCase().includes(q));
}

// build groups operations by first tag, then by path segment, collapsing
// chains of single-child segments the way an IDE compacts folders.
export function build(ops, filter, pending) {
  const root = { name: "", children: new Map(), ops: [] };
  for (const op of ops) {
    if (!visible(op, filter, pending)) continue;
    const tag = (op.tags && op.tags[0]) || "untagged";
    let node = child(root, tag);
    for (const seg of op.path.split("/").filter(Boolean)) node = child(node, seg);
    node.ops.push(op);
  }
  return root;
}

function child(node, name) {
  if (!node.children.has(name)) node.children.set(name, { name, children: new Map(), ops: [] });
  return node.children.get(name);
}

function problems(node) {
  let n = node.ops.filter((o) => PROBLEM.has(o.status)).length;
  for (const c of node.children.values()) n += problems(c);
  return n;
}

// order lists the visible operations in tree order, for "next problem".
export function order(node, out = []) {
  for (const op of node.ops) out.push(op);
  for (const c of node.children.values()) order(c, out);
  return out;
}

export function render(el, store, onSelect) {
  const pending = pendingKeys(store.state);
  const root = build(store.state.draft.operations || [], store.filter, pending);
  clear(el);
  const stale = staleGroup(store);
  if (stale) el.append(stale);
  const ul = h("ul");
  for (const c of root.children.values()) ul.append(branch(c, "", store, pending, onSelect, 0));
  el.append(ul);
  store.order = order(root);
}

function branch(node, prefix, store, pending, onSelect, depth) {
  // Compact single-child chains.
  let name = node.name;
  while (node.ops.length === 0 && node.children.size === 1 && depth > 0) {
    node = [...node.children.values()][0];
    name += "/" + node.name;
  }
  const id = prefix + "/" + name;
  const count = problems(node);
  const open = store.filter.q || store.expanded.has(id) || (count > 0 && !store.collapsed.has(id));
  const li = h("li");
  li.append(h("button", { class: "branch", type: "button", onclick: () => {
      if (open) { store.collapsed.add(id); store.expanded.delete(id); } else { store.expanded.add(id); store.collapsed.delete(id); }
      store.rerender();
    } }, open ? "▾ " : "▸ ", name, count ? h("span", { class: "count" }, count) : null));
  if (open) {
    const ul = h("ul");
    for (const op of node.ops) ul.append(leaf(op, store, pending, onSelect));
    for (const c of node.children.values()) ul.append(branch(c, id, store, pending, onSelect, depth + 1));
    li.append(ul);
  }
  return li;
}

function leaf(op, store, pending, onSelect) {
  return h("li", {}, h("button", {
      class: "leaf" + (store.selected === op.key ? " selected" : ""), type: "button",
      title: op.summary || op.path, onclick: () => onSelect(op.key) },
    h("span", { class: "dot s-" + op.status }),
    h("span", { class: "method" }, op.method),
    op.operationId || op.path,
    pending.has(op.key) ? h("span", { class: "pending", title: "pending change" }) : null));
}

// staleGroup lists corrections that match nothing, with a remove action each.
function staleGroup(store) {
  const stale = (store.state.draft.problems || []).filter((p) => p.match || p.schema);
  if (!stale.length) return null;
  const box = h("div", {}, h("div", { class: "group" }, "Stale corrections · " + stale.length));
  const ul = h("ul");
  for (const p of stale) {
    const label = p.match ? (p.match.operationId || p.match.method + " " + p.match.path) : "component " + p.schema;
    ul.append(h("li", {}, h("button", { class: "branch", type: "button", title: p.message,
      onclick: () => store.removeStale(p) }, h("span", { class: "dot s-unsupported" }), label, h("span", { class: "count" }, "remove"))));
  }
  box.append(ul);
  return box;
}
```

- [ ] **Step 7: `landing.js`** — the pane shown when no operation is selected:

```js
import { h, clear } from "./dom.js";

export function renderLanding(el, store, act) {
  const st = store.state, d = st.draft;
  clear(el);
  el.append(h("h2", {}, "Configuration"), h("p", { class: "muted" }, st.configPath));
  if (d.specError) el.append(h("div", { class: "banner error" }, "The specification could not be read: ", d.specError,
    h("br"), "Fix the document, or correct it with an overlay below."));
  if (d.configError) el.append(h("div", { class: "banner error" }, "The configuration does not validate: ", d.configError));
  const counts = { decision: 0, unsupported: 0, blocked: 0, works: 0 };
  for (const op of d.operations || []) counts[op.status]++;
  if (!d.specError) el.append(h("p", {},
    h("span", { class: "pill p-decision" }, counts.decision + " need a decision"), " ",
    h("span", { class: "pill p-unsupported" }, counts.unsupported + " need new code or a fixed document"), " ",
    h("span", { class: "pill p-blocked" }, counts.blocked + " blocked"), " ",
    h("span", { class: "pill p-works" }, counts.works + " work")));
  for (const p of d.problems || []) el.append(h("div", { class: "banner warn" }, p.section, p.index >= 0 ? "[" + p.index + "]" : "", ": ", p.message));

  // Overlays: the one thing editable even when the document does not parse.
  const list = [...(d.overlays || [])];
  const box = h("section", { class: "field" }, h("h3", {}, "spec.overlays"));
  const ul = h("ul");
  list.forEach((path, i) => ul.append(h("li", {}, path, " ", h("button", { type: "button",
    onclick: () => act({ kind: "overlays", overlays: list.filter((_, j) => j !== i) }) }, "remove"))));
  const input = h("input", { type: "text", placeholder: "./vendor-fixes.overlay.yaml" });
  box.append(ul, input, " ", h("button", { type: "button", onclick: () => {
    if (input.value.trim()) act({ kind: "overlays", overlays: [...list, input.value.trim()] });
  } }, "add overlay"));
  el.append(box, h("p", { class: "muted" }, "Pick an operation on the left. Amber needs a decision you can make here; red needs new code in lotsman or a fixed document."));
}
```

- [ ] **Step 8: `app.js`**

```js
import { api } from "./api.js";
import { h, clear } from "./dom.js";
import * as tree from "./tree.js";
import { renderLanding } from "./landing.js";
import { renderForm } from "./form.js";
import { renderTray } from "./tray.js";

export const store = {
  state: null, selected: null, detail: null, error: null,
  filter: { q: "", onlyProblems: false },
  expanded: new Set(), collapsed: new Set(), order: [],
};

const $ = (id) => document.getElementById(id);

export function toast(message, kind = "") {
  const t = $("toast");
  t.textContent = message;
  t.className = "toast show " + kind;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.className = "toast"), 3500);
}

// refresh installs a new state from the server and redraws everything.
export function refresh(state) {
  store.state = state;
  rerender();
}

function rerender() {
  const st = store.state;
  $("file").textContent = st.configPath;
  const ops = st.draft.operations || [];
  const n = (s) => ops.filter((o) => o.status === s).length;
  $("counts").textContent = `${ops.length} operations · ${n("decision")} decide · ${n("unsupported")} unsupported`;
  tree.render($("tree"), store, select);
  if (store.selected) renderForm($("main"), store); else renderLanding($("main"), store, act);
  renderTray($("tray"), store);
}
store.rerender = rerender;

export async function select(key) {
  store.selected = key;
  store.error = null;
  try {
    store.detail = key ? await api.operation(key) : null;
  } catch (e) {
    store.detail = null;
    toast(e.message, "error");
  }
  rerender();
}

// act sends one entry; a refused change keeps the draft and says why.
export async function act(entry) {
  try {
    refresh(await api.apply(entry));
    store.error = null;
    if (store.selected) store.detail = await api.operation(store.selected);
    rerender();
    return true;
  } catch (e) {
    store.error = e.message;
    if (e.data && e.data.state) store.state = e.data.state;
    rerender();
    return false;
  }
}
store.act = act;

store.removeStale = (p) => act(p.match
  ? { kind: "operation", match: p.match, remove: true }
  : { kind: "component", schema: p.schema, remove: true });

function nextProblem() {
  const list = store.order.filter((o) => o.status === "decision" || o.status === "unsupported");
  if (!list.length) return toast("No problems left");
  const at = list.findIndex((o) => o.key === store.selected);
  select(list[(at + 1) % list.length].key);
}

async function start() {
  $("search").addEventListener("input", (e) => { store.filter.q = e.target.value.trim(); rerender(); });
  $("only-problems").addEventListener("change", (e) => { store.filter.onlyProblems = e.target.checked; rerender(); });
  $("next-problem").addEventListener("click", nextProblem);
  try {
    refresh(await api.state());
  } catch (e) {
    clear($("main")).append(h("div", { class: "banner error" }, e.message));
  }
}

start();
```

- [ ] **Step 9: stubs** — `form.js`:
`import { h, clear } from "./dom.js"; export function renderForm(el, store) { clear(el).append(h("h2", {}, store.selected || "")); }`
and `tray.js`: `import { clear } from "./dom.js"; export function renderTray(el) { clear(el); }`.

- [ ] **Step 10: Smoke**

```bash
go build ./... && make build
(bin/lotsman ui --config /tmp/lotsman-demo/lotsman.yaml --port 18999 --no-open > /tmp/ui.out 2>&1 &); sleep 2
grep -o 'http://[^ ]*' /tmp/ui.out
du -cb internal/ui/web | tail -1   # must stay under 400000
```

Open the printed URL in a browser (or with the Playwright MCP tools if available): the tree
groups operations by tag and path, amber/red dots, problem counts on branches; search filters;
"only problems" hides teal/grey; "Next problem →" walks amber and red operations; the landing pane
shows counts and the overlays list. Check the browser console has no CSP errors. Then
`pkill -f 'lotsman ui --config /tmp/lotsman-demo'`.

- [ ] **Step 11: Commit** — `git add internal/ui/web && git commit -m "lotsman ui: каркас, дерево API с проблемами, стартовая панель, шрифты Plex"` (+ trailer).

---

### Task 8: the smart form

**Files:**
- Replace: `internal/ui/web/form.js`

**Interfaces — Consumes:** `store` (`state`, `detail = {before, after}`, `error`, `act(entry)`),
`h`, `clear`, `api.explain`, `api.revert`; snapshot fields from task 2 (`after.override`,
`after.parameters[]` with `name,in,style,explode,required,component,reason,schema`,
`after.mediaTypes`, `after.body`, `after.bodyComponent`, `after.effect`, `after.effectSource`,
`after.excluded`, `after.authProfiles`, `after.reasons`, `after.corrections`), `state.comparison.changes`,
`state.draft.profiles`.

- [ ] **Step 1: `form.js`**

```js
import { h, clear } from "./dom.js";
import { api } from "./api.js";

const HINTS = {
  unsupported_media_type: "Pick the media type lotsman should send. Only JSON bodies can be sent.",
  unsupported_parameter_style: "Change the style to one lotsman serializes (form in query, simple in path and header) — only if the API really accepts it.",
  unsupported_parameter_schema: "Replace the parameter's schema with a scalar or an array of scalars.",
  ambiguous_security: "Pick which configured credential this operation uses.",
  policy_unknown_effect_blocked: "Lotsman could not tell what this does. State its effect; anything but read still needs mutations enabled.",
  policy_mutation_blocked: "Mutations are off (execution.allowMutations, restart needed).",
};
const CANNOT = "This needs new code in lotsman or a fixed document; the workbench cannot lift it.";
const STYLES = { query: ["form", "spaceDelimited", "pipeDelimited", "deepObject"], path: ["simple", "label", "matrix"],
  header: ["simple"], cookie: ["form"] };
const TYPES = ["keep", "string", "integer", "number", "boolean", "array of string", "array of integer", "array of number"];

function typeSchema(choice) {
  if (choice.startsWith("array of ")) return { type: "array", items: { type: choice.slice(9) } };
  return { type: choice };
}

function warningsFor(store, key) {
  const change = (store.state.comparison.changes || []).find((c) => c.key === key);
  return (change && change.warnings) || [];
}

function warnings(list, field) {
  return list.filter((w) => w.field === field).map((w) => h("div", { class: "warning w-" + w.level }, w.message));
}

function usage(store, component) {
  return (store.state.draft.operations || []).filter((o) => o.bodyComponent === component).length;
}

export function renderForm(el, store) {
  clear(el);
  const d = store.detail;
  if (!d || !d.after) { el.append(h("p", { class: "muted" }, "Loading…")); return; }
  const op = d.after;
  const ov = structuredClone(op.override || {});
  const ws = warningsFor(store, op.key);
  const entryId = "operation:" + (ov.match ? (ov.match.operationId || ov.match.method + " " + ov.match.path)
    : (op.operationId || op.method + " " + op.path));

  el.append(h("h2", {}, op.method, " ", op.path, " ", h("span", { class: "pill p-" + op.status }, op.status)));
  el.append(h("p", { class: "muted" }, op.operationId || "", op.summary ? " — " + op.summary : ""));
  if (store.error) el.append(h("div", { class: "banner error" }, "The change was not taken: ", store.error));
  for (const r of op.reasons || []) {
    const hint = HINTS[r.code] || CANNOT;
    el.append(h("div", { class: "banner " + (HINTS[r.code] ? "warn" : "error") }, h("b", {}, r.code), r.detail ? " — " + r.detail : "", h("br"), hint));
  }
  if ((op.corrections || []).length) el.append(h("div", { class: "banner info" }, "Corrected by configuration: ",
    op.corrections.map((c) => c.target + (c.note ? " (" + c.note + ")" : "")).join("; ")));
  el.append(...warnings(ws, "operation"));

  const tabs = h("div", { class: "tabs" });
  const body = h("div");
  const show = (which) => {
    for (const b of tabs.children) b.classList.toggle("active", b.dataset.tab === which);
    clear(body).append(which === "edit" ? editor(store, op, ov, ws, entryId) : dryRun(op));
  };
  for (const [id, label] of [["edit", "Correct"], ["dry", "Dry run"]]) {
    const b = h("button", { type: "button", onclick: () => show(id) }, label);
    b.dataset.tab = id;
    tabs.append(b);
  }
  el.append(tabs, body);
  show("edit");
}

function editor(store, op, ov, ws, entryId) {
  const form = h("div");
  const controls = {};

  // Media type.
  if ((op.mediaTypes || []).length) {
    const sec = h("section", { class: "field" }, h("h3", {}, "Media type"));
    const current = ov.mediaType || (op.body && op.body.mediaType) || "";
    for (const mt of op.mediaTypes) {
      const sendable = mt === "application/json" || mt.endsWith("+json");
      const radio = h("input", { type: "radio", name: "mediaType", value: mt, checked: mt === current, disabled: !sendable });
      sec.append(h("label", { class: "row" }, radio, " ", mt, sendable ? "" : " — lotsman cannot send this"), h("br"));
    }
    sec.append(...warnings(ws, "mediaType"));
    controls.mediaType = () => { const r = sec.querySelector("input[name=mediaType]:checked"); return r ? r.value : ""; };
    form.append(sec);
  }

  // Parameters.
  if ((op.parameters || []).length) {
    const sec = h("section", { class: "field" }, h("h3", {}, "Parameters"));
    const table = h("table", { class: "params" }, h("tr", {}, ["name", "style", "explode", "required", "type", ""].map((t) => h("th", {}, t))));
    controls.params = [];
    for (const p of op.parameters) {
      const id = p.in + "/" + p.name;
      const style = h("select", {}, (STYLES[p.in] || [p.style]).map((s) => h("option", { value: s, selected: s === p.style }, s)));
      const explode = h("input", { type: "checkbox", checked: p.explode });
      const required = h("input", { type: "checkbox", checked: p.required || p.in === "path", disabled: p.in === "path" });
      const type = h("select", {}, TYPES.map((t) => h("option", { value: t }, t)));
      table.append(h("tr", {},
        h("td", {}, p.in, "/", p.name, p.reason ? h("div", { class: "warning w-error" }, p.reason) : null),
        h("td", {}, style), h("td", {}, explode), h("td", {}, required),
        h("td", {}, type, p.schema ? h("details", {}, h("summary", {}, "current schema"), h("pre", {}, JSON.stringify(p.schema, null, 2))) : null),
        h("td", {}, ...warnings(ws, "parameter " + id))));
      controls.params.push({ p, style, explode, required, type });
    }
    sec.append(table);
    form.append(sec);
  }

  // Body patch, for this operation or for the shared component.
  if (op.body || op.bodyComponent) {
    const sec = h("section", { class: "field" }, h("h3", {}, "Request body"));
    if (op.body) sec.append(h("details", {}, h("summary", {}, "current schema (" + op.body.mediaType + ")"), h("pre", {}, JSON.stringify(op.body.schema, null, 2))));
    const existing = ov.requestBody && ov.requestBody.patch;
    const patch = h("textarea", { placeholder: '{"required": ["name"]}  — JSON Merge Patch over JSON Schema 2020-12' });
    patch.value = existing ? JSON.stringify(existing, null, 2) : "";
    sec.append(patch);
    let scope = null;
    if (op.bodyComponent) {
      const n = usage(store, op.bodyComponent);
      scope = h("select", {}, h("option", { value: "operation" }, "this operation only"),
        h("option", { value: "component" }, `everywhere — component ${op.bodyComponent} (used by ${n} operation${n === 1 ? "" : "s"})`));
      sec.append(h("label", {}, "Apply to: ", scope));
    }
    sec.append(...warnings(ws, "requestBody"));
    controls.body = { patch, scope };
    form.append(sec);
  }

  // Effect, publication, credential, note.
  const effect = h("select", {}, h("option", { value: "" }, `as inferred (${op.effectSource === "local_override" ? "overridden" : op.effect})`),
    ["read", "write", "destructive", "unknown"].map((e) => h("option", { value: e, selected: ov.effect === e }, e)));
  const enabled = h("input", { type: "checkbox", checked: !op.excluded });
  const profiles = store.state.draft.profiles || [];
  const auth = h("select", {}, h("option", { value: "" }, "automatic"), profiles.map((p) => h("option", { value: p.name, selected: ov.authProfile === p.name }, p.name + " (" + p.scheme + ")")));
  const note = h("input", { type: "text", value: ov.note || "", placeholder: "why — kept in lotsman.yaml and shown in reports" });
  form.append(
    h("section", { class: "field" }, h("h3", {}, "Effect"), effect, ...warnings(ws, "effect")),
    h("section", { class: "field" }, h("h3", {}, "Publication"), h("label", {}, enabled, " published to agents"), ...warnings(ws, "enabled")),
    profiles.length ? h("section", { class: "field" }, h("h3", {}, "Credential"), auth, ...warnings(ws, "authProfile")) : null,
    h("section", { class: "field" }, h("h3", {}, "Note"), note));

  const apply = async () => {
    const entry = buildEntry(op, ov, controls, { effect: effect.value, enabled: enabled.checked, auth: auth.value, note: note.value.trim() });
    if (!entry) return;
    for (const e of entry) if (!(await store.act(e))) return;
  };
  form.append(h("div", { class: "actions" },
    h("button", { class: "primary", type: "button", onclick: apply }, "Apply"),
    h("button", { type: "button", onclick: async () => { store.state = await api.revert(entryId); store.detail = await api.operation(op.key); store.rerender(); } }, "Revert this operation")));
  return form;
}

// buildEntry turns the form into the whole desired operationOverrides entry
// (and a componentOverrides entry when the body patch is shared). It returns
// a list because one Apply can touch both.
function buildEntry(op, ov, controls, rest) {
  const match = ov.match || (op.operationId ? { operationId: op.operationId } : { method: op.method, path: op.path });
  const next = { match };
  const out = [];
  if (controls.mediaType) {
    const mt = controls.mediaType();
    const automatic = op.body && op.body.mediaType === mt && !ov.mediaType;
    if (mt && !automatic) next.mediaType = mt;
  }
  if (controls.params) {
    const prior = new Map((ov.parameters || []).map((p) => [p.in + "/" + p.name, p]));
    for (const c of controls.params) {
      const id = c.p.in + "/" + c.p.name;
      const pc = Object.assign({ name: c.p.name, in: c.p.in }, prior.get(id) || {});
      if (c.style.value !== c.p.style) pc.style = c.style.value;
      if (c.explode.checked !== c.p.explode) pc.explode = c.explode.checked;
      if (c.p.in !== "path" && c.required.checked !== c.p.required) pc.required = c.required.checked;
      if (c.type.value !== "keep") pc.schema = typeSchema(c.type.value);
      if (Object.keys(pc).length > 2) prior.set(id, pc);
    }
    if (prior.size) next.parameters = [...prior.values()];
  }
  if (controls.body && controls.body.patch.value.trim()) {
    let patch;
    try { patch = JSON.parse(controls.body.patch.value); } catch (e) { alert("The body patch is not valid JSON: " + e.message); return null; }
    if (controls.body.scope && controls.body.scope.value === "component") {
      out.push({ kind: "component", schema: op.bodyComponent, component: { match: { schema: op.bodyComponent }, patch, note: rest.note || undefined } });
    } else {
      next.requestBody = { patch };
    }
  }
  if (rest.effect) next.effect = rest.effect;
  if (!rest.enabled) next.enabled = false;
  else if (op.excluded) next.enabled = true;
  if (rest.auth) next.authProfile = rest.auth;
  if (rest.note) next.note = rest.note;
  const states = Object.keys(next).filter((k) => k !== "match" && k !== "note");
  out.unshift(states.length
    ? { kind: "operation", operationKey: op.key, match, operation: next }
    : { kind: "operation", operationKey: op.key, match, remove: true });
  return out;
}

function dryRun(op) {
  const box = h("div");
  const args = h("textarea", {}, "{}");
  const out = h("pre", {}, "Arguments are grouped: {\"path\": {...}, \"query\": {...}, \"headers\": {...}, \"body\": ...}");
  box.append(h("section", { class: "field" }, h("h3", {}, "Arguments (JSON)"), args,
    h("div", { class: "actions" }, h("button", { class: "primary", type: "button", onclick: async () => {
      let parsed;
      try { parsed = JSON.parse(args.value || "{}"); } catch (e) { out.textContent = "Not JSON: " + e.message; return; }
      try { out.textContent = (await api.explain(op.key, parsed)).text; } catch (e) { out.textContent = e.message; }
    } }, "Explain the call"))), out, h("p", { class: "muted" }, "No network request is made. Credentials are shown as references."));
  return box;
}
```

Notes for the implementer: (1) task 9 replaces the "Revert this operation" handler with the
shared `store.apply` path; (2) `alert` is the only blocking UI in the form and is acceptable for a
malformed JSON patch; (3) confirm the entry
shape matches `workbench.Entry` JSON tags (`kind`, `operationKey`, `match`, `operation`,
`component`, `schema`, `remove`).

- [ ] **Step 2: Smoke** — run the UI on the demo config (task 7 step 10) and in the browser:
  1. `patchPetCompeting` (amber): pick `application/merge-patch+json`, Apply → the dot turns
     grey/teal, the tray shows `+1 fixed`, the form shows the correction banner.
  2. `listPets` (teal): set `limit` type to `string`, Apply → warning
     "the argument schema changes: integer → string" under the row.
  3. `getFiltered`: set `filter` style to `deepObject` back (or a working op's style to
     `deepObject`), Apply → error warning "the operation stops working: unsupported_parameter_style".
  4. Dry run on `listPets` with `{"query":{"limit":2}}` → explain text, `NO network request was made.`
  5. A body patch `{"type": "object", "properties": {"x": {"$ref": "#/a"}}}` → "The change was
     not taken" banner, draft unchanged.

- [ ] **Step 3: Commit** — `git commit -am "lotsman ui: умная форма — media type, параметры, тело, effect, публикация, учётка, пробный вызов"` (+ trailer).

---

### Task 9: tray, review and save

**Files:**
- Replace: `internal/ui/web/tray.js`

- [ ] **Step 1: `tray.js`**

```js
import { h, clear } from "./dom.js";
import { api } from "./api.js";

export function renderTray(el, store) {
  clear(el);
  const st = store.state;
  const entries = st.entries || [];
  const s = st.comparison.summary;
  if (!entries.length) { el.append(h("span", { class: "muted" }, "No pending changes")); return; }
  el.append(h("span", {}, `${entries.length} change${entries.length === 1 ? "" : "s"} · `,
    h("span", { class: "fixed" }, `+${s.fixed} fixed`), " · ",
    h("span", { class: "broken" }, `−${s.broken} broken`), " · ",
    h("span", { class: "altered" }, `${s.altered} altered`)));
  for (const e of entries) {
    const label = e.kind === "component" ? "component " + (e.schema || (e.component && e.component.match.schema))
      : e.kind === "overlays" ? "overlays" : (e.match ? (e.match.operationId || e.match.method + " " + e.match.path) : e.id);
    el.append(h("span", { class: "chip" },
      h("button", { class: "chip", type: "button", onclick: () => e.operationKey && store.selectKey(e.operationKey) }, label + (e.remove ? " (remove)" : "")),
      h("button", { class: "chip", type: "button", title: "revert", onclick: async () => store.apply(await api.revert(e.id)) }, "×")));
  }
  el.append(h("span", { class: "spacer" }), h("button", { class: "primary", type: "button", onclick: () => review(store) }, "Review and save"));
}

async function review(store) {
  const dlg = h("dialog");
  document.body.append(dlg);
  const close = () => { dlg.close(); dlg.remove(); };
  let diff = "";
  try { diff = (await api.diff()).diff; } catch (e) { diff = e.message; }
  const st = store.state;
  const pre = h("pre", { class: "diff" });
  for (const line of diff.split("\n")) {
    const cls = line.startsWith("@@") ? "hunk" : line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : "";
    pre.append(h("span", { class: cls }, line), "\n");
  }
  const widening = st.comparison.widening || [];
  const ticks = widening.map((w) => ({ w, box: h("input", { type: "checkbox" }) }));
  const save = h("button", { class: "primary", type: "button" }, "Save");
  const status = h("div");
  const sync = () => { save.disabled = ticks.some((t) => !t.box.checked); };
  ticks.forEach((t) => t.box.addEventListener("change", sync));
  sync();
  dlg.append(h("h2", {}, "Review and save"), h("p", { class: "muted" }, st.configPath));
  if (widening.length) {
    const box = h("div", { class: "widening" }, h("b", {}, "These changes let an agent do more without asking a person. Confirm each:"));
    for (const t of ticks) box.append(h("div", {}, h("label", {}, t.box, " ", t.w.message)));
    dlg.append(box);
  }
  for (const p of st.draft.problems || []) dlg.append(h("div", { class: "banner warn" }, "serve will refuse this configuration: ", p.message));
  dlg.append(pre, status, h("div", { class: "actions" }, save, h("button", { type: "button", onclick: close }, "Cancel")));
  save.addEventListener("click", async () => {
    try {
      store.apply(await api.save(ticks.map((t) => t.w.id)));
      close();
      store.toast("Saved. A running `serve --watch` picks it up; stdio clients need a restart.");
    } catch (e) {
      clear(status).append(h("div", { class: "banner error" }, e.message));
      if (e.status === 409) status.append(h("button", { type: "button", onclick: async () => { store.apply(await api.reload()); close(); } }, "Reload from disk, keep my changes"));
    }
  });
  dlg.showModal();
}
```

- [ ] **Step 2: wire the helpers in `app.js`** — add to `store` after its definition:

```js
store.apply = (state) => { refresh(state); if (store.selected) select(store.selected); };
store.selectKey = (key) => select(key);
store.toast = (m, k) => toast(m, k);
```

and in `form.js`, replace the "Revert this operation" handler with
`onclick: async () => store.apply(await api.revert(entryId))`.

- [ ] **Step 3: Smoke** — on a *copy* of the demo config:

```bash
cp /tmp/lotsman-demo/lotsman.yaml /tmp/lotsman-demo/ui-test.yaml
(bin/lotsman ui --config /tmp/lotsman-demo/ui-test.yaml --port 18999 --no-open > /tmp/ui.out 2>&1 &); sleep 2
```

In the browser: make one fixing change and one effect change to `read` on `createCharge` (a
POST) → the tray counts update; "Review and save" shows the diff and a red widening block; Save is
disabled until ticked; Save writes the file (`diff /tmp/lotsman-demo/lotsman.yaml
/tmp/lotsman-demo/ui-test.yaml` shows only the new entries; comments intact). Make another change,
then edit `ui-test.yaml` by hand (append a comment line) and press Save → 409 banner with
"Reload from disk, keep my changes"; reload keeps the pending change. Stop the UI.

- [ ] **Step 4: Commit** — `git commit -am "lotsman ui: корзина изменений, проверка и сохранение с подтверждением расширений"` (+ trailer).

---

### Task 10: the stage 2–3 test suite

Written after integration by the maintainer's decision. Spec §7 asks for a Playwright smoke test;
the project has no Node toolchain and must not gain one (Constitution VII, spec §4), so the
browser smoke is done once by hand (or with the Playwright MCP tools) in step 7 and the
committed end-to-end test drives the same flow through the HTTP API.

**Files:**
- Create: `internal/workbench/workbench_test.go`, `internal/workbench/textdiff_test.go`
- Create: `internal/ui/server_test.go`
- Create: `internal/cli/evaluate_test.go`
- Create: `test/e2e/ui_test.go`
- Modify: `internal/openapi/corrections_test.go` (declared parameters)

- [ ] **Step 1: `internal/workbench/workbench_test.go`** — a fake pipeline makes the engine
testable without OpenAPI:

```go
package workbench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
)

// fakePipeline turns a configuration into a snapshot by reading it: one
// operation, whose effect is whatever the first override says.
func fakePipeline() Pipeline {
	return Pipeline{
		Evaluate: func(_ context.Context, data []byte) (*Snapshot, error) {
			op := Operation{Key: "default:POST:/pets", Method: "POST", Path: "/pets", OperationID: "createPet",
				Support: "supported", Effect: "unknown", Executable: false}
			if file, err := config.Parse(data); err == nil {
				for _, o := range file.OperationOverrides {
					if o.Effect != "" {
						op.Effect = o.Effect
					}
					if o.MediaType == "bad/type" {
						return &Snapshot{Operations: []Operation{op}, Problems: []Problem{{Section: "operationOverrides", Message: "bad media type"}}}, nil
					}
				}
			} else if len(strings.TrimSpace(string(data))) > 0 {
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

func newBench(t *testing.T, content string) (*Workbench, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lotsman.yaml")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wb, err := New(t.Context(), path, "./openapi.yaml", fakePipeline())
	if err != nil {
		t.Fatal(err)
	}
	return wb, path
}

func createPetEffect(effect string) Entry {
	return Entry{Kind: "operation", OperationKey: "default:POST:/pets",
		Operation: &config.OperationOverride{Match: config.Match{OperationID: "createPet"}, Effect: effect}}
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
	out, _ := os.ReadFile(path)
	for _, want := range []string{"# keep this comment", "# and this one", "# an unrelated override", "operationId: other", "operationId: createPet", "effect: write"} {
		if !strings.Contains(string(out), want) {
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
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "source: ./openapi.yaml") || !strings.Contains(string(out), "apiVersion: lotsman.dev/v1alpha1") {
		t.Fatalf("new file:\n%s", out)
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
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "# edited by hand") || !strings.Contains(string(out), "effect: write") {
		t.Fatalf("saved:\n%s", out)
	}
}

// Review focus 4: widening needs an acknowledgement, enforced here, not in the browser.
func TestWideningNeedsAcknowledgement(t *testing.T) {
	t.Parallel()
	wb, _ := newBench(t, withComment)
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
	if _, err := wb.Save(t.Context(), []string{st.Comparison.Widening[0].ID}); err != nil {
		t.Fatal(err)
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

func TestRevertAndRemove(t *testing.T) {
	t.Parallel()
	wb, path := newBench(t, withComment)
	st, _ := wb.Apply(t.Context(), createPetEffect("write"))
	st, _ = wb.Revert(t.Context(), st.Entries[0].ID)
	if len(st.Entries) != 0 {
		t.Fatal("revert left the entry")
	}
	if _, err := wb.Apply(t.Context(), Entry{Kind: "operation", Match: &config.Match{OperationID: "other"}, Remove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.Save(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if strings.Contains(string(out), "operationId: other") {
		t.Fatalf("remove did not remove:\n%s", out)
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
	}
	for i, tc := range cases {
		Classify(&tc.op)
		if tc.op.Status != tc.want {
			t.Errorf("case %d: %s, want %s", i, tc.op.Status, tc.want)
		}
	}
}
```

- [ ] **Step 2: `internal/workbench/textdiff_test.go`**

```go
package workbench

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()
	before := "a\nb\nc\nd\ne\nf\ng\nh\n"
	after := "a\nb\nc\nX\ne\nf\ng\nh\nY\n"
	d := UnifiedDiff("lotsman.yaml", []byte(before), []byte(after))
	for _, want := range []string{"--- lotsman.yaml (on disk)", "+++ lotsman.yaml (draft)", "-d", "+X", "+Y", "@@ -1,"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if UnifiedDiff("x", []byte(before), []byte(before)) != "" {
		t.Error("identical texts produced a diff")
	}
	if !strings.Contains(UnifiedDiff("x", nil, []byte("new\n")), "+new") {
		t.Error("diff from empty")
	}
}
```

- [ ] **Step 3: `internal/ui/server_test.go`** (review focus 3)

```go
package ui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/workbench"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lotsman.yaml")
	_ = os.WriteFile(path, []byte("apiVersion: lotsman.dev/v1alpha1\n"), 0o600)
	pipe := workbench.Pipeline{
		Evaluate: func(context.Context, []byte) (*workbench.Snapshot, error) {
			return &workbench.Snapshot{Operations: []workbench.Operation{{Key: "k", InputSchema: map[string]any{"big": true}}}}, nil
		},
		Explain: func(context.Context, []byte, string, []byte) (string, error) { return "x", nil },
	}
	wb, err := workbench.New(t.Context(), path, "", pipe)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := Listen(wb, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.listener.Close() })
	return srv
}

func do(t *testing.T, srv *Server, method, target, host, origin string, cookie bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: srv.token})
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGuard(t *testing.T) {
	t.Parallel()
	srv := testServer(t)
	own := fmt.Sprintf("127.0.0.1:%d", srv.port)
	origin := "http://" + own
	cases := []struct {
		name, method, target, host, origin string
		cookie                             bool
		want                               int
	}{
		{"no token", "GET", "/api/state", own, "", false, 403},
		{"foreign host (DNS rebinding)", "GET", "/api/state", "evil.example:" + fmt.Sprint(srv.port), "", true, 403},
		{"write without origin", "POST", "/api/reload", own, "", true, 403},
		{"write from another site", "POST", "/api/reload", own, "http://evil.example", true, 403},
		{"someone else's token in the URL", "GET", "/?token=" + strings.Repeat("0", 64), own, "", false, 403},
		{"our token sets the cookie", "GET", "/?token=" + srv.token, own, "", false, 303},
		{"read with cookie", "GET", "/api/state", own, "", true, 200},
		{"localhost alias", "GET", "/api/state", fmt.Sprintf("localhost:%d", srv.port), "", true, 200},
		{"write with cookie and origin", "POST", "/api/reload", own, origin, true, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, srv, tc.method, tc.target, tc.host, tc.origin, tc.cookie)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
				t.Fatal("no CSP")
			}
		})
	}
}

func TestStateIsLight(t *testing.T) {
	t.Parallel()
	srv := testServer(t)
	rec := do(t, srv, "GET", "/api/state", fmt.Sprintf("127.0.0.1:%d", srv.port), "", true)
	if strings.Contains(rec.Body.String(), "big") {
		t.Fatalf("state carries schemas: %s", rec.Body.String())
	}
	rec = do(t, srv, "GET", "/api/operation?key=k", fmt.Sprintf("127.0.0.1:%d", srv.port), "", true)
	if !strings.Contains(rec.Body.String(), "big") {
		t.Fatalf("operation lacks its schema: %s", rec.Body.String())
	}
}

func TestEmbeddedFrontendFitsTheBudget(t *testing.T) {
	t.Parallel()
	var total int64
	_ = fsWalk(func(size int64) { total += size })
	if total > 400_000 {
		t.Fatalf("embedded frontend is %d bytes; the budget is 400 000 (spec §4)", total)
	}
}
```

with a helper in the same file:

```go
func fsWalk(add func(int64)) error {
	return fs.WalkDir(web, "web", func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err == nil {
			add(info.Size())
		}
		return err
	})
}
```

(add `"io/fs"` to the imports).

- [ ] **Step 4: `internal/cli/evaluate_test.go`** (review focus 5)

```go
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateReportsInsteadOfFailing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	cfg := filepath.Join(dir, "lotsman.yaml")

	// A document that does not parse.
	_ = os.WriteFile(spec, []byte("swagger: \"2.0\"\n"), 0o600)
	snap, _, _ := evaluate(t.Context(), []byte("apiVersion: lotsman.dev/v1alpha1\nspec: {source: ./openapi.yaml}\n"), pipelineInputs{configPath: cfg})
	if !strings.Contains(snap.SpecError, "Swagger") && !strings.Contains(snap.SpecError, "swagger") {
		t.Fatalf("spec error = %q", snap.SpecError)
	}

	// A configuration that does not validate still shows the document.
	_ = os.WriteFile(spec, []byte(`openapi: 3.0.3
info: { title: T, version: "1" }
paths:
  /x:
    get: { operationId: getX, responses: { "200": { description: ok } } }
`), 0o600)
	snap, _, _ = evaluate(t.Context(), []byte("apiVersion: lotsman.dev/v1alpha1\nbogus: 1\n"), pipelineInputs{configPath: cfg, spec: spec})
	if snap.ConfigError == "" || len(snap.Operations) != 1 {
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
}
```

- [ ] **Step 5: `test/e2e/ui_test.go`** — the committed end-to-end flow (replaces Playwright,
see the task header). Start `lotsman ui --config CFG --port 0 --no-open` as a subprocess with the
e2e package's `buildBinary(t)`, read the printed URL from stdout, then with an `http.Client` that
has a cookie jar and sets `Origin` on non-GET requests:
1. `GET URL` → 303 and a cookie;
2. `GET /api/state` → 200, the amber operation `patchPetCompeting` present (use a copy of
   `cmd/api-gateway/openapi.yaml` and a config with `spec.strict: false`);
3. `POST /api/draft` with `{"kind":"operation","operationKey":…,"operation":{"match":{"operationId":"patchPetCompeting"},"mediaType":"application/merge-patch+json"}}`
   → 200 and `comparison.summary.fixed == 1`;
4. `POST /api/save` with `{"acknowledged":[]}` → 200; the file now contains `mediaType: application/merge-patch+json`;
5. `lotsman inspect --config CFG --json` reports that operation supported;
6. kill the subprocess. Use the existing e2e helpers (`buildBinary`, `runCLI`) — read
   test/e2e/inspect_test.go and transport_test.go for how subprocesses are started and stopped.

Also: `lotsman inspect --config NEW --diff-against OLD` and `--json` (one fixed, one altered).

- [ ] **Step 6: adapter** — in `internal/openapi/corrections_test.go`, add a test that a
`deepObject` query parameter appears in `DeclaredParameters` with
`Reason == unsupported_parameter_style`, that an operation offering four media types lists all
four sorted in `RequestMediaTypes`, and that a body `$ref: '#/components/schemas/Pet'` yields
`BodyComponent == "Pet"` while a same-named component in another file does not (reuse the
multi-file fixture from `TestComponentPatchDoesNotReachASameNamedExternalComponent`).

- [ ] **Step 7: browser smoke (not committed)** — with the demo config, run the UI and, in a real
browser or the Playwright MCP tools: open the printed URL, fix `patchPetCompeting`, change
`listPets` limit type, review (diff + no widening), save, confirm the file; make `createCharge`
`read`, confirm the widening block blocks Save until ticked; confirm the browser console shows no
CSP violation. Record what you saw in the report.

- [ ] **Step 8: Run everything** — `go test -race ./...` and
`/tmp/lintbin/golangci-lint run ./...` (or the repo's linter if it works with the installed Go);
report pre-existing findings outside this feature without fixing them.

- [ ] **Step 9: Commit** — `git add -A internal test && git status --short && git commit -m "Тесты этапов 2–3: workbench, защита ui, конвейер, сквозной сценарий"` (+ trailer).

---

### Task 11: documentation

**Files:**
- Modify: `README.md`, `docs/spec.md` §5.2, `specs/008-local-admin-ui/spec.md` (status line),
  `docs/adr/0021-operator-corrections-and-local-workbench.md` (Consequences)

- [ ] **Step 1: README** — after the corrections section added in stage 1, add:

````markdown
### Correcting in a browser

```bash
lotsman ui ./openapi.yaml                  # edits ./lotsman.yaml, creates it on first save
lotsman ui --config ./deploy/lotsman.yaml  # edits that file
```

A local page opens: the API as a tree with the operations that need a decision first, a form per
operation, and a tray of pending changes. Every change is run through the same pipeline `serve`
runs before anything is written, and the form says what it fixes, breaks or alters. Changes that
let an agent do more without asking — an effect lowered to `read`, an operation published again, a
different credential — each need a tick at save. The page listens on 127.0.0.1 only, behind a
one-time token, makes no request of its own and never reads a secret.

In CI, `lotsman inspect --config new.yaml --diff-against old.yaml [--json]` prints the same
comparison.
````

- [ ] **Step 2: docs/spec.md §5.2** — one Russian bullet: the local workbench (`lotsman ui`,
ADR-0021) edits only the local configuration, on loopback, with a token; `inspect --diff-against`
compares two configurations.

- [ ] **Step 3: spec status** — first line of `specs/008-local-admin-ui/spec.md` status:
"implemented 2026-10-?? (stages 1–3)" with today's date; ADR-0021 Consequences: the workbench is
implemented in `internal/workbench` and `internal/ui`, imported only by `ui` and `inspect`.

- [ ] **Step 4: Commit** — `git commit -am "Документация: lotsman ui и inspect --diff-against"` (+ trailer).
