# Tasks: corrections in the core (feature 008, stage 1)

**Input**: plan.md, spec.md §1–§2, §6 stage 1, §7 stage 1
**Convention**: every task ends with `go build ./... && go test ./...` green and a commit.
Smoke commands assume `make build` has produced `bin/lotsman`. Tests beyond the smoke check are
task 8 (maintainer's decision; see plan.md "Global constraints").

**Before task 1**: work on branch `008-local-admin-ui` from a clean `main`. If the working tree
has someone else's uncommitted changes, stop and ask the maintainer — do not stash or commit them.

---

### Task 1: ADR-0021

**Files:**
- Create: `docs/adr/0021-operator-corrections-and-local-workbench.md`

- [ ] **Step 1: Write the ADR**

```markdown
# ADR-0021: Operator corrections, and a local workbench to make them

- **Status**: Accepted
- **Date**: 2026-10-16
- **Decision owner**: specs/008-local-admin-ui; FR-29; docs/spec.md §5.2, §10; Constitution IX

## Context

`inspect` names every refusal and then leaves the operator with two options: edit the vendor's
document, which the next update undoes, or live without the operation. Some refusals are a choice
lotsman must not make but a person can (Kubernetes `apps/v1`: 12 PATCH operations offering four
media types each). Some are the document's own mistakes. FR-29 promised a media-type override that
was never built.

docs/spec.md §10 defers an admin interface to M6, and Constitution IX lists it among the deferred
hypotheses that must not leak web infrastructure into the core.

## Decision

1. **Corrections are configuration.** `operationOverrides` gain `mediaType`, `parameters`,
   `requestBody.patch` and `note`; a new `componentOverrides` section patches a shared schema;
   `spec.overlays` applies standard OpenAPI Overlay documents. They are applied in the adapter,
   after schema normalization, because that is where the refusals they lift are decided.
2. **Strict, like overrides.** A correction that matches nothing is an error that names it. A patch
   cannot contain a `$`-keyword and nothing touches servers, security schemes, method or path.
   Corrected operations carry `local_override` provenance into every report.
3. **Reload republishes corrections, not policy.** A reload that finds anything else changed in the
   configuration is refused and the running catalog stays (consistent with FR-72 and feature 007).
4. **The local workbench is brought forward from M6, narrowed.** `lotsman ui` is single-user,
   bound to loopback, edits only the local configuration, and lives in packages `serve` does not
   import. The multi-user, authenticated admin of §10 stays deferred.
5. **Corrections are not made through MCP tools.** The model fills tool arguments; a model or a
   prompt injection could "answer" for the person and lower an effect to `read`.

## Consequences

- FR-29 is implemented. Kubernetes `apps/v1` reaches 77/77 with twelve `mediaType` lines.
- Components in external files of an exploded specification cannot be patched in this release.
- A schema a correction produces is compiled at load; one that does not compile stops `serve`.
- Constitution IX's "admin UI" hypothesis is confirmed for the local, single-user case only.
```

- [ ] **Step 2: Commit**

```bash
git add docs/adr/0021-operator-corrections-and-local-workbench.md specs/008-local-admin-ui
git commit -m "ADR-0021: исправления спецификации в конфигурации и локальная админка" \
  -m "Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: the correction language in `internal/config`

**Files:**
- Modify: `internal/config/config.go` (Spec ~201, File ~440, OperationOverride ~424, validate ~522, validateOverride ~904, resolvePaths ~470)
- Modify: `internal/config/runtime.go` (Runtime, Resolve ~117)
- Modify: `internal/config/export.go`

**Interfaces — Produces:**
- `config.Spec.Overlays []string` (absolute after `Load`)
- `config.ComponentOverride{Match ComponentMatch{Schema string}; Patch Patch; Note string}`
- `config.Patch map[string]any`
- `config.ParameterOverride{Name, In string; Schema Patch; Required *bool; Style string; Explode *bool}`
- `config.BodyOverride{Patch Patch}`
- `config.OperationOverride` + `MediaType string; Parameters []ParameterOverride; RequestBody *BodyOverride; Note string`
- `config.File.ComponentOverrides []ComponentOverride`
- `config.Runtime.Overlays []string; ComponentOverrides []ComponentOverride`

- [ ] **Step 1: Types.** In `Spec`, after `Strict`:

```go
	// Overlays are OpenAPI Overlay documents applied, in order, to the root
	// document before it is parsed (feature 008). They are operator input, read
	// from paths relative to this file like Source; a `$ref` one writes into the
	// document is confined like any other.
	Overlays []string `yaml:"overlays,omitempty"`
```

In `File`, after `OperationOverrides`:

```go
	ComponentOverrides []ComponentOverride `yaml:"componentOverrides,omitempty"`
```

Extend `OperationOverride` (keep the existing fields and comments):

```go
	// MediaType pins the request media type when the document offers several
	// (FR-29). It must be one the operation declares and one lotsman sends.
	MediaType string `yaml:"mediaType,omitempty"`
	// Parameters correct what the document says about this operation's
	// parameters, for this operation only.
	Parameters []ParameterOverride `yaml:"parameters,omitempty"`
	// RequestBody patches this operation's body schema.
	RequestBody *BodyOverride `yaml:"requestBody,omitempty"`
	// Note is the operator's reason, carried into every report.
	Note string `yaml:"note,omitempty"`
```

New types below `OperationOverride`:

```go
// Patch is an RFC 7396 merge patch over a schema as lotsman publishes it:
// JSON Schema 2020-12, after normalization. A key starting with `$` is
// refused, so a patch cannot reach another document or redefine $defs.
type Patch map[string]any

// ParameterOverride corrects one parameter, matched by name and location.
type ParameterOverride struct {
	Name string `yaml:"name"`
	In   string `yaml:"in"`
	// Schema replaces the parameter's schema.
	Schema   Patch  `yaml:"schema,omitempty"`
	Required *bool  `yaml:"required,omitempty"`
	Style    string `yaml:"style,omitempty"`
	Explode  *bool  `yaml:"explode,omitempty"`
}

// BodyOverride patches one operation's request body schema.
type BodyOverride struct {
	Patch Patch `yaml:"patch"`
}

// ComponentMatch names a schema under #/components/schemas of the root
// document.
type ComponentMatch struct {
	Schema string `yaml:"schema"`
}

// ComponentOverride patches a shared schema wherever it is used.
type ComponentOverride struct {
	Match ComponentMatch `yaml:"match"`
	Patch Patch          `yaml:"patch"`
	Note  string         `yaml:"note,omitempty"`
}
```

- [ ] **Step 2: Validation.** In `validateOverride`, replace the "states nothing" block and add checks:

```go
	// An override that states nothing is a typo, and a typo in an override is
	// a security control that silently did not apply.
	if override.Effect == "" && override.Enabled == nil && override.AuthProfile == "" &&
		override.MediaType == "" && len(override.Parameters) == 0 && override.RequestBody == nil {
		return where("states nothing; set effect, enabled, authProfile, mediaType, parameters or requestBody")
	}
	if override.MediaType != "" && !strings.Contains(override.MediaType, "/") {
		return where("mediaType %q is not a type/subtype", override.MediaType)
	}
	seen := map[string]bool{}
	for j := range override.Parameters {
		p := &override.Parameters[j]
		if err := validateParameterOverride(p); err != nil {
			return where("parameters[%d]: %v", j, err)
		}
		if seen[p.In+"/"+p.Name] {
			return where("parameters[%d]: %s/%s is corrected twice", j, p.In, p.Name)
		}
		seen[p.In+"/"+p.Name] = true
	}
	if override.RequestBody != nil {
		if len(override.RequestBody.Patch) == 0 {
			return where("requestBody states nothing; set patch")
		}
		if err := validatePatch("requestBody.patch", override.RequestBody.Patch); err != nil {
			return where("%v", err)
		}
	}
```

Add below `validateOverride`:

```go
func validateParameterOverride(p *ParameterOverride) error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	switch p.In {
	case "path", "query", "header", "cookie":
	default:
		return fmt.Errorf("in %q is not path, query, header or cookie", p.In)
	}
	if p.Schema == nil && p.Required == nil && p.Style == "" && p.Explode == nil {
		return fmt.Errorf("states nothing; set schema, required, style or explode")
	}
	if p.In == "path" && p.Required != nil && !*p.Required {
		return fmt.Errorf("a path parameter is always required")
	}
	if p.Schema != nil {
		if len(p.Schema) == 0 {
			return fmt.Errorf("schema is empty")
		}
		return validatePatch("schema", p.Schema)
	}
	return nil
}

func validateComponentOverride(index int, c *ComponentOverride) error {
	where := func(format string, args ...any) error {
		return errs.Errorf(errs.ClassUsage, "config: componentOverrides[%d]: "+format, append([]any{index}, args...)...)
	}
	if c.Match.Schema == "" {
		return where("match needs a schema name")
	}
	if len(c.Patch) == 0 {
		return where("states nothing; set patch")
	}
	if err := validatePatch("patch", c.Patch); err != nil {
		return where("%v", err)
	}
	return nil
}

// validatePatch refuses `$` keywords anywhere in a patch: `$ref` would reach
// another document or component, `$defs` would redefine one, and neither is a
// correction of what this schema says.
func validatePatch(field string, value any) error {
	switch v := value.(type) {
	case Patch:
		return validatePatch(field, map[string]any(v))
	case map[string]any:
		for key, nested := range v {
			if strings.HasPrefix(key, "$") {
				return fmt.Errorf("%s: %q is not allowed in a correction", field, key)
			}
			if err := validatePatch(field+"."+key, nested); err != nil {
				return err
			}
		}
	case []any:
		for i, nested := range v {
			if err := validatePatch(fmt.Sprintf("%s[%d]", field, i), nested); err != nil {
				return err
			}
		}
	}
	return nil
}
```

In `validate()`, after the `OperationOverrides` loop:

```go
	components := map[string]int{}
	for i := range f.ComponentOverrides {
		if err := validateComponentOverride(i, &f.ComponentOverrides[i]); err != nil {
			return err
		}
		name := f.ComponentOverrides[i].Match.Schema
		if first, dup := components[name]; dup {
			return errs.Errorf(errs.ClassUsage,
				"config: componentOverrides[%d] and [%d] both patch %q; one schema, one correction", first, i, name)
		}
		components[name] = i
	}
```

In `validateSpec`, before `return nil`:

```go
	for i, overlay := range spec.Overlays {
		if strings.TrimSpace(overlay) == "" || overlay == "-" {
			return errs.Errorf(errs.ClassUsage, "config: spec.overlays[%d] must name a file", i)
		}
	}
```

In `resolvePaths`, first lines (before the `Root` early return):

```go
	for i, overlay := range f.Spec.Overlays {
		if !filepath.IsAbs(overlay) {
			f.Spec.Overlays[i] = filepath.Join(base, overlay)
		}
	}
```

Make sure `config.go` imports `fmt` (add it if absent).

- [ ] **Step 3: Runtime.** In `Runtime`, after `Overrides`:

```go
	// ComponentOverrides patch shared schemas (feature 008), in file order.
	ComponentOverrides []ComponentOverride
	// Overlays are the OpenAPI Overlay files applied before parsing, absolute.
	Overlays []string
```

In `Resolve`, beside the existing `runtime.Overrides = ...` and `runtime.Source = ...` lines:

```go
		runtime.ComponentOverrides = append([]ComponentOverride(nil), file.ComponentOverrides...)
		runtime.Overlays = append([]string(nil), file.Spec.Overlays...)
```

In `export.go`, `Spec:` gains `Overlays: append([]string(nil), r.Overlays...),` and the `File`
literal gains `ComponentOverrides: append([]ComponentOverride(nil), r.ComponentOverrides...),`.

- [ ] **Step 4: Smoke**

```bash
go build ./... && go test ./internal/config/...
cat > /tmp/l8.yaml <<'EOF'
apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: path, required: false }]
EOF
bin/lotsman config check /tmp/l8.yaml; echo "exit $?"
```

Expected: `go build` ok; `config check` prints `…parameters[0]: a path parameter is always required` and a non-zero exit.

- [ ] **Step 5: Commit** — `git commit -am "config: язык исправлений — mediaType, parameters, requestBody, componentOverrides, spec.overlays"` (+ Co-Authored-By line).

---

### Task 3: media type pin end to end (FR-29), provenance, CLI wiring

**Files:**
- Modify: `internal/domain/domain.go` (Operation ~41; add Correction near EffectSource ~96)
- Create: `internal/openapi/corrections.go`
- Modify: `internal/openapi/openapi.go` (Options, Document, Parse, documentScope, buildOperation)
- Modify: `internal/openapi/body.go` (buildBody, selectMediaType)
- Modify: `internal/catalog/catalog.go` (Tool, Build ~220), `internal/catalog/report.go` (OperationVerdict, buildReport ~171)
- Create: `internal/cli/corrections.go`
- Modify: `internal/cli/serve.go` (parseSpec, loadCatalog), `inspect.go`, `explain.go`, `validate.go`, `operations.go`

**Interfaces — Produces:**
- `domain.Correction{Source EffectSource; Target, Note string}`; `domain.Operation.Corrections []Correction`
- `openapi.Corrections`, `openapi.OperationCorrection`, `openapi.ParameterCorrection`, `openapi.ComponentCorrection`, `openapi.CorrectionProblem` (see code)
- `openapi.Options.Corrections Corrections`; `openapi.Document.CorrectionProblems []CorrectionProblem`
- `(*fixes)` methods used by tasks 4–5: `forOperation`, `problem`, `pinnedMediaType`; helpers `mergePatch`, `cloneSchema`, `schemaTypes`, `correction`
- `cli.parseSpec(ctx, spec string, runtime config.Runtime, logger) (*specDoc, error)`
- `cli.correctionsFrom(config.Runtime) openapi.Corrections`; `cli.checkCorrectedSchemas(*catalog.Catalog) error`
- `catalog.Tool.Corrections`, `catalog.OperationVerdict.Corrections`

- [ ] **Step 1: domain.** Below `EffectDecision`:

```go
// Correction records that the operator changed what the document says about
// an operation (feature 008). It travels into every report, so that nobody
// mistakes a corrected operation for the document's own.
type Correction struct {
	Source EffectSource `json:"source"` // always local_override in this release
	// Target names what was corrected: "mediaType application/merge-patch+json",
	// "parameter query/limit", "requestBody", "component Pet".
	Target string `json:"target"`
	Note   string `json:"note,omitempty"`
}
```

In `Operation`, after `Effect`:

```go
	// Corrections are the operator's changes to what the document says,
	// in the order they were applied. Empty for an uncorrected operation.
	Corrections []Correction `json:"corrections,omitempty"`
```

- [ ] **Step 2: `internal/openapi/corrections.go`**

```go
package openapi

import (
	"fmt"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// Corrections are the operator's statements about what the document says
// (feature 008). They are applied while an operation is translated, because
// that is where the refusals they lift are decided: a media type chosen after
// the body was refused would be a choice about nothing.
//
// They come from configuration, never from the document, and that is the
// whole difference between a correction and a guess.
type Corrections struct {
	Components []ComponentCorrection
	Operations []OperationCorrection
}

// ComponentCorrection patches one shared schema wherever it is used.
type ComponentCorrection struct {
	// Index is the entry's position in componentOverrides, for messages.
	Index int
	// Schema is the name under #/components/schemas in the root document.
	Schema string
	// Patch is an RFC 7396 merge patch over the normalized schema.
	Patch domain.Schema
	Note  string
}

// OperationCorrection is what one operationOverrides entry says about the
// document. Entries that only decide (effect, enabled, authProfile) are the
// catalog's and never reach this package.
type OperationCorrection struct {
	// Index is the entry's position in operationOverrides, for messages.
	Index       int
	OperationID string
	Method      string
	Path        string
	// MediaType is normalized (lower case, no parameters).
	MediaType  string
	Parameters []ParameterCorrection
	BodyPatch  domain.Schema
	Note       string
}

// ParameterCorrection replaces what the document says about one parameter.
type ParameterCorrection struct {
	Name     string
	In       string
	Schema   domain.Schema
	Required *bool
	Style    string
	Explode  *bool
}

// CorrectionProblem is a correction that could not be applied as written. It
// is never a smaller catalog: callers refuse to serve on any of these.
type CorrectionProblem struct {
	Section string `json:"section"`
	Index   int    `json:"index"`
	Message string `json:"message"`
}

func (p CorrectionProblem) Error() string {
	return fmt.Sprintf("config: %s[%d]: %s", p.Section, p.Index, p.Message)
}

// fixes is one Parse's view of the corrections and of what went wrong
// applying them.
type fixes struct {
	corrections Corrections
	// components maps a component pointer to its correction.
	components map[string]*ComponentCorrection
	problems   []CorrectionProblem
}

func newFixes(c Corrections) *fixes {
	f := &fixes{corrections: c, components: make(map[string]*ComponentCorrection, len(c.Components))}
	for i := range f.corrections.Components {
		f.components[componentPointer(f.corrections.Components[i].Schema)] = &f.corrections.Components[i]
	}
	return f
}

func componentPointer(name string) string { return "#/components/schemas/" + name }

func (f *fixes) problem(section string, index int, format string, args ...any) {
	f.problems = append(f.problems, CorrectionProblem{Section: section, Index: index, Message: fmt.Sprintf(format, args...)})
}

// forOperation returns the correction naming this operation, matched the way
// the catalog matches overrides. Ambiguity is the catalog's to refuse
// (catalog.ValidateOverrides), and it refuses before anything is served.
func (f *fixes) forOperation(method, path, operationID string) *OperationCorrection {
	if f == nil {
		return nil
	}
	for i := range f.corrections.Operations {
		c := &f.corrections.Operations[i]
		if c.OperationID != "" {
			if c.OperationID == operationID {
				return c
			}
			continue
		}
		if strings.EqualFold(c.Method, method) && c.Path == path {
			return c
		}
	}
	return nil
}

// checkComponents reports component corrections that name nothing.
func (f *fixes) checkComponents(schemas *orderedmap.Map[string, *base.SchemaProxy]) {
	for i := range f.corrections.Components {
		c := &f.corrections.Components[i]
		if schemas != nil {
			if _, ok := schemas.Get(c.Schema); ok {
				continue
			}
		}
		f.problem("componentOverrides", c.Index,
			"schema %q is not in #/components/schemas of the root document", c.Schema)
	}
}

// pinnedMediaType checks the operator's media type against the operation and
// returns it, or "" when there is nothing usable to pin.
func (f *fixes) pinnedMediaType(c *OperationCorrection, rb *v3.RequestBody, result *domain.Operation) string {
	if c == nil || c.MediaType == "" {
		return ""
	}
	if rb == nil || orderedmap.Len(rb.Content) == 0 {
		f.problem("operationOverrides", c.Index, "mediaType is set, but %s %s declares no request body",
			result.Method, result.PathTemplate)
		return ""
	}
	for name := range rb.Content.FromOldest() {
		if normalizeMediaType(name) != c.MediaType {
			continue
		}
		if !sendable(c.MediaType) {
			f.problem("operationOverrides", c.Index,
				"mediaType %q is declared, but lotsman sends JSON bodies only (application/json or a +json type)",
				c.MediaType)
			return ""
		}
		result.Corrections = append(result.Corrections, correction("mediaType "+c.MediaType, c.Note))
		return c.MediaType
	}
	f.problem("operationOverrides", c.Index, "mediaType %q is not one %s %s declares (%s)",
		c.MediaType, result.Method, result.PathTemplate, describeMediaTypes(rb.Content))
	return ""
}

// sendable reports whether lotsman can send a body of this media type.
func sendable(mediaType string) bool {
	return mediaType == jsonMediaType || strings.HasSuffix(mediaType, "+json")
}

func correction(target, note string) domain.Correction {
	return domain.Correction{Source: domain.EffectSourceLocalOverride, Target: target, Note: note}
}

// mergePatch applies an RFC 7396 merge patch to a copy of target: null removes
// a key, an object merges into an object, anything else replaces.
//
// Nested values come out as map[string]any, never domain.Schema: the catalog's
// schema walkers (sanitizeSchema and friends) assert map[string]any, and a
// nested domain.Schema would silently skip description sanitizing.
func mergePatch(target, patch domain.Schema) domain.Schema {
	out := cloneSchema(target)
	if out == nil {
		out = domain.Schema{}
	}
	for key, value := range patch {
		if value == nil {
			delete(out, key)
			continue
		}
		if patchObject, ok := asObject(value); ok {
			current, _ := asObject(out[key])
			out[key] = map[string]any(mergePatch(current, patchObject))
			continue
		}
		out[key] = cloneValue(value)
	}
	return out
}

func asObject(v any) (domain.Schema, bool) {
	switch m := v.(type) {
	case domain.Schema:
		return m, true
	case map[string]any:
		return domain.Schema(m), true
	}
	return nil, false
}

// cloneSchema deep-copies a schema, so a correction never writes into a
// schema another operation (or the bundle's $defs) still holds.
func cloneSchema(s domain.Schema) domain.Schema {
	if s == nil {
		return nil
	}
	out := make(domain.Schema, len(s))
	for k, v := range s {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case domain.Schema:
		return map[string]any(cloneSchema(t))
	case map[string]any:
		return map[string]any(cloneSchema(domain.Schema(t)))
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = cloneValue(t[i])
		}
		return out
	default:
		return v
	}
}

// schemaTypes reads the declared types off a normalized schema.
func schemaTypes(s domain.Schema) []string {
	switch t := s["type"].(type) {
	case string:
		return []string{t}
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			if name, ok := v.(string); ok {
				out = append(out, name)
			}
		}
		return out
	}
	return nil
}
```

- [ ] **Step 3: wire into `Parse`.** In `Options` add:

```go
	// Corrections are the operator's changes to what the document says
	// (feature 008). The zero value corrects nothing.
	Corrections Corrections
```

In `Document` add:

```go
	// CorrectionProblems are corrections that could not be applied as written.
	// A caller that serves must refuse on any of them.
	CorrectionProblems []CorrectionProblem `json:"correctionProblems,omitempty"`
```

In `documentScope` add `fixes *fixes`. In `Parse`, after `scope := documentScope{...}` and the
`Components` block:

```go
	scope.fixes = newFixes(opts.Corrections)
	var componentSchemas *orderedmap.Map[string, *base.SchemaProxy]
	if model.Model.Components != nil {
		componentSchemas = model.Model.Components.Schemas
	}
	scope.fixes.checkComponents(componentSchemas)
```

and before `return out, nil` at the end:

```go
	out.CorrectionProblems = scope.fixes.problems
```

In `buildOperation`, look the correction up once after `pointer := …`:

```go
	fix := scope.fixes.forOperation(method, path, op.OperationId)
```

and replace `body := buildBody(op.RequestBody, pointer, defs)` with:

```go
	body := buildBody(op.RequestBody, bodyFix{
		mediaType: scope.fixes.pinnedMediaType(fix, op.RequestBody, &result),
	}, pointer, defs)
```

Note `result.Method`/`result.PathTemplate` are already set when `pinnedMediaType` runs.

- [ ] **Step 4: `body.go`.** Add the type and thread it:

```go
// bodyFix is what the operator said about one operation's body.
type bodyFix struct {
	// mediaType is a validated pin, or "".
	mediaType string
	// patch is an RFC 7396 merge patch over the normalized body schema.
	patch domain.Schema
	// index is the operationOverrides entry, for messages.
	index int
}
```

`func buildBody(rb *v3.RequestBody, fix bodyFix, pointer string, defs *bundle) bodyBuild` and
`mediaType, content, reason := selectMediaType(rb.Content, fix.mediaType)`.

`selectMediaType` gains `pinned string`; right after the empty check:

```go
	// A pin the operator stated wins (FR-29). It was checked against the
	// declared types and against what lotsman sends before it got here.
	if pinned != "" {
		for name, media := range content.FromOldest() {
			if normalizeMediaType(name) == pinned {
				return pinned, media, ""
			}
		}
	}
```

Update every other caller of `selectMediaType`/`buildBody` (grep, including `_test.go`) to pass
`""`/`bodyFix{}`.

- [ ] **Step 5: catalog provenance.** `Tool` gains, after `Effect`:

```go
	// Corrections are the operator's changes to the document behind this
	// tool. Not part of the digest: a client is shown their effect, not them.
	Corrections []domain.Correction `json:"-"`
```

and `Build` sets `Corrections: op.Corrections,` in the `Tool{…}` literal. `OperationVerdict`
gains `Corrections []domain.Correction \`json:"corrections,omitempty"\`` and `buildReport` sets
`Corrections: op.Corrections,` in its literal.

- [ ] **Step 6: `internal/cli/corrections.go`**

```go
package cli

import (
	"strings"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/openapi"
)

// correctionsFrom translates the configuration's corrections into the
// adapter's vocabulary. Entries that only decide (effect, enabled,
// authProfile) stay with the catalog.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration, read once per load.
func correctionsFrom(runtime config.Runtime) openapi.Corrections {
	var out openapi.Corrections
	for i := range runtime.ComponentOverrides {
		c := &runtime.ComponentOverrides[i]
		out.Components = append(out.Components, openapi.ComponentCorrection{
			Index: i, Schema: c.Match.Schema, Patch: domain.Schema(c.Patch), Note: c.Note,
		})
	}
	for i := range runtime.Overrides {
		o := &runtime.Overrides[i]
		if o.MediaType == "" && len(o.Parameters) == 0 && o.RequestBody == nil {
			continue
		}
		c := openapi.OperationCorrection{
			Index: i, OperationID: o.Match.OperationID, Method: o.Match.Method, Path: o.Match.Path,
			MediaType: normalizeMediaType(o.MediaType), Note: o.Note,
		}
		for j := range o.Parameters {
			p := &o.Parameters[j]
			c.Parameters = append(c.Parameters, openapi.ParameterCorrection{
				Name: p.Name, In: p.In, Schema: domain.Schema(p.Schema),
				Required: p.Required, Style: p.Style, Explode: p.Explode,
			})
		}
		if o.RequestBody != nil {
			c.BodyPatch = domain.Schema(o.RequestBody.Patch)
		}
		out.Operations = append(out.Operations, c)
	}
	return out
}

func normalizeMediaType(name string) string {
	if semicolon := strings.IndexByte(name, ';'); semicolon >= 0 {
		name = name[:semicolon]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// correctionError turns the adapter's problems into the one error a command
// reports. All of them, because an operator fixing a configuration wants the
// list, not the first item of it.
func correctionError(problems []openapi.CorrectionProblem) error {
	if len(problems) == 0 {
		return nil
	}
	lines := make([]string, 0, len(problems))
	for _, p := range problems {
		lines = append(lines, p.Error())
	}
	return errs.Errorf(errs.ClassUsage, "%s", strings.Join(lines, "\n"))
}

// checkCorrectedSchemas compiles the input schema of every corrected tool. An
// uncorrected schema that does not compile is the document's problem and the
// runner already publishes it as not executable; a corrected one is the
// operator's, and the operator is told now rather than at the first call.
func checkCorrectedSchemas(cat *catalog.Catalog) error {
	for i := range cat.Tools {
		tool := &cat.Tools[i]
		if len(tool.Corrections) == 0 {
			continue
		}
		if _, err := argvalidate.Compile(tool.Name, tool.InputSchema); err != nil {
			return errs.Errorf(errs.ClassUsage,
				"config: the corrected input schema of %s does not compile: %v", tool.OperationKey, err)
		}
	}
	return nil
}
```

- [ ] **Step 7: `parseSpec` takes the runtime.** Change the signature to

```go
//nolint:gocritic // hugeParam: Runtime is resolved configuration, copied per call so no callee holds a pointer to the decision its caller already made.
func parseSpec(ctx context.Context, spec string, runtime config.Runtime, logger *slog.Logger) (*specDoc, error) {
```

use `runtime.Root` where `root` was used, pass `Corrections: correctionsFrom(runtime)` in
`openapi.Options`, and right after `openapi.Parse` succeeds:

```go
	if err := correctionError(doc.CorrectionProblems); err != nil {
		return nil, err
	}
```

Update the five callers: `parseSpec(ctx, spec, runtime, logger)` in serve.go/inspect.go/
operations.go, `parseSpec(ctx, pick(*specPath, runtime.Source), runtime, logger)` in explain.go,
`parseSpec(ctx, spec, configured, logger)` in validate.go.

In `loadCatalog`, after `cat := catalog.Build(...)`:

```go
	if err := checkCorrectedSchemas(&cat); err != nil {
		return nil, err
	}
```

Do the same in `inspect` after its `catalog.Build` (print with the usual
`lotsman: [%s] %v` line and `return exitCode(err)`).

- [ ] **Step 8: show provenance.** In `explain` (explain.go:127), after the effect warnings loop:

```go
	for _, c := range tool.Corrections {
		line := fmt.Sprintf("corrected   %s (%s)", c.Target, c.Source)
		if c.Note != "" {
			line += ": " + c.Note
		}
		fmt.Fprintln(w, line)
	}
```

In `inspect`, in the human branch, after `writeHumanReport(stdout, &report)`:

```go
		writeCorrections(stdout, report.Operations)
```

and add to inspect.go:

```go
// writeCorrections says how many operations the configuration corrected, so a
// reader of the human report never takes a corrected document for the vendor's.
func writeCorrections(w io.Writer, operations []catalog.OperationVerdict) {
	corrected := 0
	for i := range operations {
		if len(operations[i].Corrections) > 0 {
			corrected++
		}
	}
	if corrected > 0 {
		fmt.Fprintf(w, "\ncorrected   %d operation(s) differ from the document by configuration (see --json)\n", corrected)
	}
}
```

- [ ] **Step 9: Smoke — Kubernetes 77/77**

```bash
go build ./... && make build
python3 - <<'EOF' > /tmp/k8s-fix.yaml
import json
d = json.load(open("test/testdata/corpus/kubernetes-apps-v1.json"))
print("apiVersion: lotsman.dev/v1alpha1\noperationOverrides:")
for p, item in sorted(d["paths"].items()):
    if "patch" in item:
        print(f"  - match: {{ operationId: {item['patch']['operationId']} }}")
        print("    mediaType: application/merge-patch+json")
EOF
bin/lotsman inspect test/testdata/corpus/kubernetes-apps-v1.json --config /tmp/k8s-fix.yaml | head -5
bin/lotsman inspect test/testdata/corpus/kubernetes-apps-v1.json --config /tmp/k8s-fix.yaml --json | python3 -c "import json,sys; print(json.load(sys.stdin)['totals'])"
```

Expected: `operations: 77 found, 77 supported, 0 partial, 0 rejected`; and a `corrected 12
operation(s)…` line. Then `go test ./...` green.

- [ ] **Step 10: Commit** — `git add -A internal && git commit -m "FR-29: выбор media type в operationOverrides, происхождение исправлений в отчётах"` (+ Co-Authored-By).

---

### Task 4: parameter corrections

**Files:**
- Modify: `internal/openapi/corrections.go` (add `applyParameters`)
- Modify: `internal/openapi/openapi.go` (buildOperation)
- Modify: `internal/openapi/parameters.go` (buildInput)

**Interfaces — Consumes:** `fixes`, `correction`, `cloneSchema`, `schemaTypes` (task 3).
**Produces:** `buildInput(merged, replaced map[paramKey]domain.Schema, pointer, defs)`.

- [ ] **Step 1: `applyParameters`** in corrections.go:

```go
// applyParameters returns the operation's parameters as corrected, and the
// schemas that replace the document's. The document's parameters are never
// written to: a path-level parameter is shared by every operation under its
// path, and a correction names one.
func (f *fixes) applyParameters(c *OperationCorrection, merged map[paramKey]*v3.Parameter,
	result *domain.Operation) (map[paramKey]*v3.Parameter, map[paramKey]domain.Schema) {
	if c == nil || len(c.Parameters) == 0 {
		return merged, nil
	}
	out := make(map[paramKey]*v3.Parameter, len(merged))
	for key, p := range merged {
		out[key] = p
	}
	replaced := map[paramKey]domain.Schema{}
	for i := range c.Parameters {
		pc := &c.Parameters[i]
		key := paramKey{pc.Name, pc.In}
		source, ok := out[key]
		if !ok {
			f.problem("operationOverrides", c.Index, "parameters[%d]: %s %s declares no parameter %q in %q",
				i, result.Method, result.PathTemplate, pc.Name, pc.In)
			continue
		}
		copied := *source
		if pc.Required != nil {
			copied.Required = pc.Required
		}
		if pc.Style != "" {
			copied.Style = pc.Style
		}
		if pc.Explode != nil {
			copied.Explode = pc.Explode
		}
		out[key] = &copied
		if pc.Schema != nil {
			replaced[key] = cloneSchema(pc.Schema)
		}
		result.Corrections = append(result.Corrections, correction("parameter "+pc.In+"/"+pc.Name, c.Note))
	}
	return out, replaced
}
```

- [ ] **Step 2: buildOperation.** Replace `merged := mergeParameters(in.params, op.Parameters)`
with the two lines below, and keep it *after* `fix := …` (move the `fix` lookup up if needed):

```go
	merged, replaced := scope.fixes.applyParameters(fix,
		mergeParameters(in.params, op.Parameters), &result)
```

and `input := buildInput(merged, replaced, pointer, defs)`.

- [ ] **Step 3: buildInput.** Signature
`func buildInput(merged map[paramKey]*v3.Parameter, replaced map[paramKey]domain.Schema, pointer string, defs *bundle) inputBuild`;
replace `schema, reason := parameterSchema(source, defs)` with:

```go
		var schema domain.Schema
		var reason domain.ReasonCode
		if correctedSchema, ok := replaced[key]; ok {
			// The operator's schema replaces the document's; it still has to be
			// something a URL or header value can carry.
			schema, reason = correctedSchema, checkShape(schemaTypes(correctedSchema), correctedSchema, parameterTarget)
		} else {
			schema, reason = parameterSchema(source, defs)
		}
```

Fix the other callers of `buildInput` (tests) to pass `nil`.

- [ ] **Step 4: Smoke**

```bash
go build ./... && make build
cat > /tmp/p.yaml <<'EOF'
openapi: 3.0.3
info: { title: P, version: "1" }
paths:
  /items:
    parameters:
      - { name: limit, in: query, schema: { type: integer } }
    get:
      operationId: listItems
      responses: { "200": { description: ok } }
    delete:
      operationId: deleteItems
      responses: { "204": { description: gone } }
EOF
cat > /tmp/p-fix.yaml <<'EOF'
apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: limit, in: query, schema: { type: string } }]
EOF
bin/lotsman operations /tmp/p.yaml --config /tmp/p-fix.yaml
bin/lotsman inspect /tmp/p.yaml --config /tmp/p-fix.yaml --json | python3 -c "
import json,sys; r=json.load(sys.stdin)
print([(o['key'], o.get('corrections')) for o in r['operations']])"
```

Expected: both operations supported; only `listItems` carries the `parameter query/limit`
correction. (If `operations` has no `--config`, skip that line — `inspect` is the check.)

- [ ] **Step 5: Commit** — `git commit -am "Исправления параметров: схема, required, style, explode — только для названной операции"` (+ Co-Authored-By).

---

### Task 5: body patches and component patches

**Files:**
- Modify: `internal/openapi/corrections.go` (`inlineTopRef`)
- Modify: `internal/openapi/body.go` (patch in buildBody)
- Modify: `internal/openapi/schema.go` (bundle patches)
- Modify: `internal/openapi/openapi.go` (bodyFix fields, bundle wiring, provenance)

- [ ] **Step 1: `inlineTopRef`** in corrections.go:

```go
// inlineTopRef returns a copy of schema that a correction may change without
// touching anything shared: a top-level reference into the bundle is replaced
// by a copy of what it points at, its sibling annotations kept.
func inlineTopRef(schema domain.Schema, defs *bundle) domain.Schema {
	ref, _ := schema["$ref"].(string)
	name, isDef := strings.CutPrefix(ref, "#/$defs/")
	target, known := defs.defs[name]
	if !isDef || !known {
		return cloneSchema(schema)
	}
	out := cloneSchema(target)
	for key, value := range schema {
		if key != "$ref" {
			out[key] = cloneValue(value)
		}
	}
	return out
}
```

- [ ] **Step 2: buildBody patch.** In `buildBody`, after `out.spec = &domain.BodySpec{…}`:

```go
	if fix.patch != nil {
		patched := mergePatch(inlineTopRef(converted, defs), fix.patch)
		if shapeReason := checkShape(schemaTypes(patched), patched, bodyTarget); shapeReason != "" {
			out.spec = nil
			out.reject(shapeReason, bodyPointer, fmt.Sprintf(
				"after operationOverrides[%d].requestBody.patch the body schema has no shape lotsman can validate",
				fix.index))
			return out
		}
		out.spec.Schema = patched
	}
```

In `buildOperation`, the `bodyFix` literal becomes:

```go
	bf := bodyFix{mediaType: scope.fixes.pinnedMediaType(fix, op.RequestBody, &result)}
	if fix != nil {
		bf.patch, bf.index = fix.BodyPatch, fix.Index
	}
	body := buildBody(op.RequestBody, bf, pointer, defs)
	if bf.patch != nil && body.spec != nil {
		result.Corrections = append(result.Corrections, correction("requestBody", fix.Note))
	}
```

- [ ] **Step 3: component patches in the bundle.** `bundle` gains:

```go
	// fixes are the operator's component corrections; applied records which
	// ones this operation's schemas used, for provenance.
	fixes   *fixes
	applied map[string]bool
```

Add to schema.go:

```go
// patchComponent applies the operator's correction for the component a
// reference points at, if there is one. The result must still have a shape the
// target can carry; if it does not, the operation is refused for its usual
// reason -- a correction can break an operation, and saying so is the point.
func (b *bundle) patchComponent(pointer string, normalized domain.Schema, target schemaTarget) (domain.Schema, domain.ReasonCode) {
	if b.fixes == nil {
		return normalized, ""
	}
	fix, ok := b.fixes.components[pointer]
	if !ok {
		return normalized, ""
	}
	patched := mergePatch(normalized, fix.Patch)
	if reason := checkShape(schemaTypes(patched), patched, target); reason != "" {
		return nil, reason
	}
	if b.applied == nil {
		b.applied = map[string]bool{}
	}
	b.applied[fix.Schema] = true
	return patched, ""
}
```

In `normalizeProxy`, the inlined branch becomes:

```go
	if !b.shouldReference(proxy, schema) {
		normalized, reason := b.normalizeSchema(schema, target, depth, visiting)
		if reason != "" || !proxy.IsReference() {
			return normalized, reason
		}
		return b.patchComponent(proxy.GetReference(), normalized, target)
	}
```

and in the `$defs` branch, after `normalized, reason := b.normalizeSchema(schema, target, 0, …)` /
the `reason != ""` check, before `b.defs[name] = normalized`:

```go
		normalized, reason = b.patchComponent(pointer, normalized, target)
		if reason != "" {
			return nil, reason
		}
```

- [ ] **Step 4: wire the bundle.** In `buildOperation`, after `defs := newBundle()`:

```go
	defs.fixes = scope.fixes
```

and just before the `if len(rejections) > 0` block:

```go
	for _, name := range sortedKeys(defs.applied) {
		result.Corrections = append(result.Corrections, correction("component "+name,
			scope.fixes.components[componentPointer(name)].Note))
	}
```

with, in corrections.go (`sort` import):

```go
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
```

- [ ] **Step 5: Smoke**

```bash
go build ./... && make build
cat > /tmp/c.yaml <<'EOF'
openapi: 3.0.3
info: { title: C, version: "1" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "201": { description: ok } }
    put:
      operationId: replacePet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "200": { description: ok } }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
EOF
cat > /tmp/c-fix.yaml <<'EOF'
apiVersion: lotsman.dev/v1alpha1
componentOverrides:
  - match: { schema: Pet }
    patch: { properties: { age: { type: string } } }
operationOverrides:
  - match: { operationId: createPet }
    requestBody: { patch: { required: [age] } }
EOF
bin/lotsman inspect /tmp/c.yaml --config /tmp/c-fix.yaml --json | python3 -c "
import json,sys; r=json.load(sys.stdin)
print([(o['key'], [c['target'] for c in o.get('corrections',[])]) for o in r['operations']])"
```

Expected: `createPet` → `['requestBody', 'component Pet']`, `replacePet` → `['component Pet']`;
both supported.

- [ ] **Step 6: Commit** — `git commit -am "Патчи тела операции и общих схем (componentOverrides)"` (+ Co-Authored-By).

---

### Task 6: OpenAPI Overlay input

**Files:**
- Create: `internal/openapi/overlays.go`
- Modify: `internal/cli/serve.go` (parseSpec, specDoc), `internal/cli/inspect.go` (specIdentity)

**Interfaces — Produces:** `openapi.OverlayDocument{Path string; Bytes []byte}`,
`openapi.ApplyOverlays(spec []byte, overlays []OverlayDocument) ([]byte, error)`.

- [ ] **Step 1: `internal/openapi/overlays.go`**

```go
package openapi

import (
	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/overlay"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// OverlayDocument is one operator-supplied OpenAPI Overlay, named for messages.
type OverlayDocument struct {
	Path  string
	Bytes []byte
}

// ApplyOverlays applies OpenAPI Overlay documents, in order, to the root
// document's bytes (feature 008). It runs before Parse, so everything an
// overlay writes -- a `$ref` included -- meets the same confinement and
// budgets as what the vendor wrote.
//
// libopenapi's convenience wrappers also build a document, with a default
// configuration lotsman does not use; overlay.Apply works on bytes alone.
// An action whose target matches nothing is an error, not a warning: an
// overlay the operator believes is applied has to be applied.
func ApplyOverlays(spec []byte, overlays []OverlayDocument) ([]byte, error) {
	out := spec
	for _, doc := range overlays {
		parsed, err := libopenapi.NewOverlayDocument(doc.Bytes)
		if err != nil {
			return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: %w", doc.Path, err)
		}
		result, err := overlay.Apply(out, parsed)
		if err != nil {
			return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: %w", doc.Path, err)
		}
		if len(result.Warnings) > 0 {
			return nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays %s: %s", doc.Path, result.Warnings[0].String())
		}
		out = result.Bytes
	}
	return out, nil
}
```

- [ ] **Step 2: parseSpec applies them.** In serve.go, `specDoc` gains
`overlays []domain.RefDocument`. Add:

```go
// readOverlays reads the configured overlay files and their digests. The
// digest is reported beside the document's, because an overlay changes what
// was parsed as surely as an edit to the document would.
func readOverlays(paths []string) ([]openapi.OverlayDocument, []domain.RefDocument, error) {
	docs := make([]openapi.OverlayDocument, 0, len(paths))
	manifest := make([]domain.RefDocument, 0, len(paths))
	for _, path := range paths {
		// #nosec G304 -- operator-named in the configuration.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, errs.Errorf(errs.ClassUsage, "config: spec.overlays: cannot read %s", path)
		}
		sum := sha256.Sum256(data)
		docs = append(docs, openapi.OverlayDocument{Path: path, Bytes: data})
		manifest = append(manifest, domain.RefDocument{
			Path: filepath.Base(path), Digest: "sha256:" + hex.EncodeToString(sum[:]),
		})
	}
	return docs, manifest, nil
}
```

In `parseSpec`, between `rootPath` and `openapi.Parse`:

```go
	specBytes := src.Bytes
	var overlays []domain.RefDocument
	if len(runtime.Overlays) > 0 {
		docs, manifest, err := readOverlays(runtime.Overlays)
		if err != nil {
			return nil, err
		}
		if specBytes, err = openapi.ApplyOverlays(specBytes, docs); err != nil {
			return nil, err
		}
		overlays = manifest
	}
```

pass `specBytes` to `openapi.Parse`, and return `&specDoc{…, overlays: overlays}`. Imports:
`crypto/sha256`, `encoding/hex`, `os`.

In inspect.go `specIdentity` add
`Overlays []domain.RefDocument \`json:"overlays,omitempty"\`` and set `Overlays: doc.overlays`.

- [ ] **Step 3: Smoke**

```bash
go build ./... && make build
cat > /tmp/ov.yaml <<'EOF'
overlay: 1.0.0
info: { title: fix, version: "1" }
actions:
  - target: $.paths['/items'].parameters[0].schema
    update: { type: string }
EOF
printf 'apiVersion: lotsman.dev/v1alpha1\nspec:\n  overlays: [ov.yaml]\n' > /tmp/ov-cfg.yaml
bin/lotsman inspect /tmp/p.yaml --config /tmp/ov-cfg.yaml --json | python3 -c "
import json,sys; r=json.load(sys.stdin); print(r['spec'].get('overlays'), r['totals'])"
sed -i "s|/items|/nothing|" /tmp/ov.yaml
bin/lotsman inspect /tmp/p.yaml --config /tmp/ov-cfg.yaml; echo "exit $?"
```

Expected: first run lists `ov.yaml` with a digest; second run fails naming the overlay (target
matched nothing), non-zero exit. If `update` merges into the schema rather than replacing `type`,
check the output's published schema via `--json` — `type` must be `string`.

- [ ] **Step 4: Commit** — `git commit -am "spec.overlays: стандартные OpenAPI Overlay до разбора документа"` (+ Co-Authored-By).

---

### Task 7: reload re-reads the catalog-shaping configuration

**Files:**
- Modify: `internal/cli/corrections.go` (reloadRuntime, changedOutsideCatalog, withoutCatalog)
- Modify: `internal/cli/serve.go` (flag values, resolve closure, source closure, watchSet)

- [ ] **Step 1: comparison** in corrections.go (imports `reflect`):

```go
// reloadRuntime is the configuration a reload builds from: the file as it is
// now, provided it differs from the running one only where a catalog is
// shaped. Policy, credentials and the server are fixed for the life of the
// process -- FR-72 republishes a catalog, not a policy.
//
//nolint:gocritic // hugeParam: Runtime is resolved configuration; the running one is never modified.
func reloadRuntime(running config.Runtime, configPath string,
	resolve func() (config.Runtime, error)) (config.Runtime, catalog.Mode, error) {
	current := running
	if configPath != "" {
		fresh, err := resolve()
		if err != nil {
			return config.Runtime{}, "", err
		}
		if changed := changedOutsideCatalog(running, fresh); len(changed) > 0 {
			return config.Runtime{}, "", errs.Errorf(errs.ClassUsage,
				"reload: the configuration changed in %s; a reload applies spec.overlays, componentOverrides, "+
					"operationOverrides and catalog only, the rest needs a restart", strings.Join(changed, ", "))
		}
		current = fresh
	}
	mode, err := parseMode(current.Mode)
	if err != nil {
		return config.Runtime{}, "", err
	}
	return current, mode, nil
}

// changedOutsideCatalog names the Runtime fields that differ outside the
// sections a reload may change.
//
//nolint:gocritic // hugeParam: compared by value on purpose.
func changedOutsideCatalog(before, after config.Runtime) []string {
	b, a := reflect.ValueOf(withoutCatalog(before)), reflect.ValueOf(withoutCatalog(after))
	var changed []string
	for i := range b.NumField() {
		if !reflect.DeepEqual(b.Field(i).Interface(), a.Field(i).Interface()) {
			changed = append(changed, b.Type().Field(i).Name)
		}
	}
	return changed
}

//nolint:gocritic // hugeParam: returns a modified copy.
func withoutCatalog(r config.Runtime) config.Runtime {
	r.Overrides, r.ComponentOverrides, r.Overlays, r.IncludeTags = nil, nil, nil, nil
	r.Mode, r.MaxSerializedBytes, r.DescriptionBytesPerTool = "", 0, 0
	return r
}
```

- [ ] **Step 2: serve.** Hoist the `&flagValues{…}` literal into `values := &flagValues{…}` and
pass `values` to the existing `resolveConfig` call. After `runtime.AllowPrivateNetworks = …`:

```go
	// A reload resolves the file again exactly as startup did, flags included,
	// so a flag keeps winning over the file after the file changes (FR-62).
	resolve := func() (config.Runtime, error) {
		fresh, err := resolveConfig(*configPath, fs, values)
		if err == nil {
			fresh.AllowPrivateNetworks = fresh.AllowPrivateNetworks || *allowPrivate
		}
		return fresh, err
	}
```

Replace the `source` closure:

```go
	source := func(ctx context.Context) (*catalog.Catalog, error) {
		current, currentMode, err := reloadRuntime(runtime, *configPath, resolve)
		if err != nil {
			return nil, err
		}
		return loadCatalog(ctx, spec, logger, current, currentMode, watched)
	}
```

`watchSet` gains a `config string` field, set where it is created:

```go
	watched := &watchSet{config: absolute(*configPath)}
```

with

```go
// absolute is path made absolute, or "" for no path. The watcher compares
// files by path, and a relative one would follow the working directory.
func absolute(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
```

`record` becomes `func (w *watchSet) record(root string, references []domain.RefDocument, overlays []string)`
and appends, after the reference loop:

```go
	paths = append(paths, overlays...)
	if w.config != "" {
		paths = append(paths, w.config)
	}
```

and `loadCatalog` calls `watched.record(doc.rootPath, doc.References, runtime.Overlays)`.

- [ ] **Step 3: Smoke**

```bash
go build ./... && make build
bin/api-gateway --tls-addr '' & GW=$!
cp examples/quickstart/openapi.yaml /tmp/q.yaml
printf 'apiVersion: lotsman.dev/v1alpha1\n' > /tmp/q-cfg.yaml
bin/lotsman serve /tmp/q.yaml --config /tmp/q-cfg.yaml --transport=http --listen 127.0.0.1:18990 \
  --base-url http://127.0.0.1:18080 --watch 2> /tmp/q.log & SV=$!
sleep 2
OP=$(bin/lotsman operations /tmp/q.yaml | awk 'NR==2{print $3}')
printf 'apiVersion: lotsman.dev/v1alpha1\noperationOverrides:\n  - match: { operationId: %s }\n    enabled: false\n' "$OP" > /tmp/q-cfg.yaml
sleep 4; grep -E 'catalog reloaded|reload refused' /tmp/q.log | tail -1
printf 'apiVersion: lotsman.dev/v1alpha1\nexecution: { allowMutations: true }\n' > /tmp/q-cfg.yaml
sleep 4; grep -E 'reload refused' /tmp/q.log | tail -1
kill $SV $GW
```

Expected: first line `catalog reloaded … tools=2 previous_tools=3` (or the counts of the
quickstart); second line `reload refused … changed in AllowMutations …`. If a `Server`/`InboundAuth`
field differs between two resolutions of the same file (a func or pointer that `DeepEqual` cannot
compare), the second reload would be refused too — fix `withoutCatalog` to compare that field by
value before moving on.

- [ ] **Step 4: Commit** — `git commit -am "Reload перечитывает исправления и catalog; изменения политики требуют перезапуска"` (+ Co-Authored-By).

---

### Task 8: the stage 1 test suite

Written after integration by the maintainer's decision. Every test below lands in this task.

**Files:**
- Create: `internal/openapi/corrections_test.go`, `internal/openapi/overlays_test.go`
- Create: `internal/config/corrections_test.go`
- Create: `internal/cli/corrections_test.go`
- Create: `test/e2e/corrections_test.go`

- [ ] **Step 1: `internal/openapi/corrections_test.go`**

```go
package openapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

func parseCorrected(t *testing.T, spec string, c Corrections) *Document {
	t.Helper()
	doc, err := Parse(t.Context(), []byte(spec), Options{Corrections: c})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return doc
}

func opByID(t *testing.T, doc *Document, id string) domain.Operation {
	t.Helper()
	for _, op := range doc.Operations {
		if op.SourceOperationID == id {
			return op
		}
	}
	t.Fatalf("no operation %q", id)
	return domain.Operation{}
}

const twoMediaTypes = `openapi: 3.0.3
info: { title: M, version: "1" }
paths:
  /x:
    patch:
      operationId: patchX
      requestBody:
        content:
          application/merge-patch+json: { schema: { type: object } }
          application/strategic-merge-patch+json: { schema: { type: object } }
          application/apply-patch+yaml: { schema: { type: object } }
      responses: { "200": { description: ok } }
`

func TestMediaTypePin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, pin, wantMedia, wantProblem string
		wantLevel                         domain.SupportLevel
	}{
		{"no pin is refused", "", "", "", domain.SupportRejected},
		{"declared json type is used", "application/merge-patch+json", "application/merge-patch+json", "", domain.SupportSupported},
		{"undeclared type is a problem", "application/json-patch+json", "", "is not one", domain.SupportRejected},
		{"declared non-json type is a problem", "application/apply-patch+yaml", "", "sends JSON bodies only", domain.SupportRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var c Corrections
			if tc.pin != "" {
				c.Operations = []OperationCorrection{{OperationID: "patchX", MediaType: tc.pin}}
			}
			doc := parseCorrected(t, twoMediaTypes, c)
			op := opByID(t, doc, "patchX")
			if op.Support.Level != tc.wantLevel {
				t.Fatalf("support = %+v", op.Support)
			}
			if tc.wantMedia != "" && op.Input.Body.MediaType != tc.wantMedia {
				t.Fatalf("media = %q", op.Input.Body.MediaType)
			}
			if tc.wantProblem == "" && len(doc.CorrectionProblems) > 0 {
				t.Fatalf("unexpected problems %v", doc.CorrectionProblems)
			}
			if tc.wantProblem != "" && (len(doc.CorrectionProblems) != 1 ||
				!strings.Contains(doc.CorrectionProblems[0].Message, tc.wantProblem)) {
				t.Fatalf("problems = %v, want one containing %q", doc.CorrectionProblems, tc.wantProblem)
			}
		})
	}
}

const sharedPathParameter = `openapi: 3.0.3
info: { title: P, version: "1" }
paths:
  /items:
    parameters:
      - { name: limit, in: query, schema: { type: integer } }
    get:
      operationId: listItems
      responses: { "200": { description: ok } }
    delete:
      operationId: deleteItems
      responses: { "204": { description: gone } }
`

// Review focus 1: a path-level parameter is shared; a correction names one operation.
func TestParameterCorrectionDoesNotLeakToSiblingOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems",
		Parameters:  []ParameterCorrection{{Name: "limit", In: "query", Schema: domain.Schema{"type": "string"}}},
	}}})
	list, del := opByID(t, doc, "listItems"), opByID(t, doc, "deleteItems")
	if got := findParam(t, &list, "limit").Schema["type"]; got != "string" {
		t.Fatalf("listItems limit type = %v", got)
	}
	if got := findParam(t, &del, "limit").Schema["type"]; got != "integer" {
		t.Fatalf("deleteItems limit type = %v, want the document's integer", got)
	}
	if len(list.Corrections) != 1 || len(del.Corrections) != 0 {
		t.Fatalf("corrections: list=%v delete=%v", list.Corrections, del.Corrections)
	}
}

func TestParameterStyleCorrectionCanRejectAnOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems",
		Parameters:  []ParameterCorrection{{Name: "limit", In: "query", Style: "deepObject"}},
	}}})
	op := opByID(t, doc, "listItems")
	if op.Support.Level != domain.SupportRejected ||
		!reflect.DeepEqual(op.Support.Reasons, []domain.ReasonCode{domain.ReasonUnsupportedParameterStyle}) {
		t.Fatalf("support = %+v", op.Support)
	}
	if len(doc.CorrectionProblems) != 0 {
		t.Fatalf("a correction that breaks an operation is a consequence, not a problem: %v", doc.CorrectionProblems)
	}
}

// Review focus 3: an object in a query value.
func TestParameterSchemaReplacementMustStayCarriable(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		OperationID: "listItems",
		Parameters:  []ParameterCorrection{{Name: "limit", In: "query", Schema: domain.Schema{"type": "object"}}},
	}}})
	if op := opByID(t, doc, "listItems"); op.Support.Level != domain.SupportRejected {
		t.Fatalf("support = %+v", op.Support)
	}
}

func TestUndeclaredParameterIsAProblem(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedPathParameter, Corrections{Operations: []OperationCorrection{{
		Index: 3, OperationID: "listItems",
		Parameters: []ParameterCorrection{{Name: "offset", In: "query", Schema: domain.Schema{"type": "string"}}},
	}}})
	if len(doc.CorrectionProblems) != 1 || doc.CorrectionProblems[0].Index != 3 {
		t.Fatalf("problems = %v", doc.CorrectionProblems)
	}
}

const sharedBody = `openapi: 3.0.3
info: { title: C, version: "1" }
paths:
  /pets:
    post:
      operationId: createPet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "201": { description: ok } }
    put:
      operationId: replacePet
      requestBody:
        content: { application/json: { schema: { $ref: '#/components/schemas/Pet' } } }
      responses: { "200": { description: ok } }
components:
  schemas:
    Pet: { type: object, properties: { age: { type: integer } } }
`

// Review focus 2: a body patch on a $ref must not change the shared definition.
func TestBodyPatchIsLocalToTheOperation(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedBody, Corrections{Operations: []OperationCorrection{{
		OperationID: "createPet", BodyPatch: domain.Schema{"required": []any{"age"}},
	}}})
	create, replace := opByID(t, doc, "createPet"), opByID(t, doc, "replacePet")
	if _, ok := create.Input.Body.Schema["required"]; !ok {
		t.Fatalf("createPet body = %v", create.Input.Body.Schema)
	}
	if _, ok := replace.Input.Body.Schema["required"]; ok {
		t.Fatalf("replacePet body changed: %v", replace.Input.Body.Schema)
	}
	if _, ok := create.Input.Defs["Pet"]["required"]; ok {
		t.Fatalf("createPet's $defs entry was patched: %v", create.Input.Defs["Pet"])
	}
}

func TestComponentPatchAppliesEverywhereWithProvenance(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedBody, Corrections{Components: []ComponentCorrection{{
		Schema: "Pet", Note: "API takes strings",
		Patch: domain.Schema{"properties": map[string]any{"age": map[string]any{"type": "string"}}},
	}}})
	for _, id := range []string{"createPet", "replacePet"} {
		op := opByID(t, doc, id)
		age := op.Input.Defs["Pet"]["properties"].(map[string]any)["age"].(map[string]any)
		if age["type"] != "string" {
			t.Fatalf("%s age = %v", id, age)
		}
		want := []domain.Correction{{Source: domain.EffectSourceLocalOverride, Target: "component Pet", Note: "API takes strings"}}
		if !reflect.DeepEqual(op.Corrections, want) {
			t.Fatalf("%s corrections = %v", id, op.Corrections)
		}
	}
}

func TestMissingComponentIsAProblem(t *testing.T) {
	t.Parallel()
	doc := parseCorrected(t, sharedBody, Corrections{Components: []ComponentCorrection{{
		Index: 1, Schema: "Dog", Patch: domain.Schema{"type": "object"},
	}}})
	if len(doc.CorrectionProblems) != 1 || doc.CorrectionProblems[0].Section != "componentOverrides" {
		t.Fatalf("problems = %v", doc.CorrectionProblems)
	}
}

func TestMergePatch(t *testing.T) {
	t.Parallel()
	target := domain.Schema{"type": "object", "properties": map[string]any{
		"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "string"}}}
	patched := mergePatch(target, domain.Schema{"properties": map[string]any{
		"a": map[string]any{"type": "string"}, "b": nil}, "required": []any{"a"}})

	props := patched["properties"].(map[string]any)
	if props["a"].(map[string]any)["type"] != "string" || props["b"] != nil {
		t.Fatalf("patched = %v", patched)
	}
	if target["properties"].(map[string]any)["a"].(map[string]any)["type"] != "integer" {
		t.Fatal("mergePatch wrote into its target")
	}
	// Nested values must be plain maps: catalog walkers assert map[string]any.
	if _, ok := patched["properties"].(map[string]any); !ok {
		t.Fatalf("nested type = %T", patched["properties"])
	}
}

func TestCorrectionsAreDeterministic(t *testing.T) {
	t.Parallel()
	c := Corrections{Components: []ComponentCorrection{{Schema: "Pet",
		Patch: domain.Schema{"properties": map[string]any{"age": map[string]any{"type": "string"}}}}}}
	first := parseCorrected(t, sharedBody, c)
	for range 5 {
		if again := parseCorrected(t, sharedBody, c); !reflect.DeepEqual(first.Operations, again.Operations) {
			t.Fatal("two parses with the same corrections differ")
		}
	}
}
```

- [ ] **Step 2: `internal/openapi/overlays_test.go`**

```go
package openapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlayUpdatesTheDocument(t *testing.T) {
	t.Parallel()
	out, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{{Path: "fix.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: fix, version: "1" }
actions:
  - target: $.paths['/items'].parameters[0].schema
    update: { type: string }
`)}})
	if err != nil {
		t.Fatalf("ApplyOverlays: %v", err)
	}
	doc, err := Parse(t.Context(), out, Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	list := opByID(t, doc, "listItems")
	if got := findParam(t, &list, "limit").Schema["type"]; got != "string" {
		t.Fatalf("limit type = %v", got)
	}
}

func TestOverlayTargetMatchingNothingIsAnError(t *testing.T) {
	t.Parallel()
	_, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{{Path: "fix.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: fix, version: "1" }
actions:
  - target: $.paths['/nothing']
    update: { description: x }
`)}})
	if err == nil || !strings.Contains(err.Error(), "fix.yaml") {
		t.Fatalf("err = %v", err)
	}
}

// Review focus 5: what an overlay writes is confined like what the vendor wrote.
func TestOverlayRefIsConfinedToTheSpecRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	secret := filepath.Join(filepath.Dir(root), "secret.yaml")
	if err := os.WriteFile(secret, []byte("type: string\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })
	out, err := ApplyOverlays([]byte(sharedPathParameter), []OverlayDocument{{Path: "evil.yaml", Bytes: []byte(`overlay: 1.0.0
info: { title: evil, version: "1" }
actions:
  - target: $.paths['/items'].parameters[0]
    update: { schema: { $ref: '../secret.yaml' } }
`)}})
	if err != nil {
		t.Fatalf("ApplyOverlays: %v", err)
	}
	doc, err := Parse(t.Context(), out, Options{RootPath: root})
	if err == nil {
		list := opByID(t, doc, "listItems")
		if list.Support.Level == "supported" {
			t.Fatalf("a $ref outside the root was followed: %+v", list.Input.Parameters)
		}
	}
}
```

- [ ] **Step 3: `internal/config/corrections_test.go`**

```go
package config

import (
	"strings"
	"testing"
)

func TestCorrectionValidation(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, yaml, want string }{
		{"path parameter cannot be optional", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: path, required: false }]`, "always required"},
		{"parameter states nothing", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: query }]`, "states nothing"},
		{"unknown location", `
operationOverrides:
  - match: { operationId: x }
    parameters: [{ name: id, in: body, required: true }]`, "is not path"},
		{"ref in a patch", `
componentOverrides:
  - match: { schema: Pet }
    patch: { properties: { a: { $ref: '#/x' } } }`, `"$ref" is not allowed`},
		{"duplicate component", `
componentOverrides:
  - { match: { schema: Pet }, patch: { type: object } }
  - { match: { schema: Pet }, patch: { type: object } }`, "both patch"},
		{"media type shape", `
operationOverrides:
  - match: { operationId: x }
    mediaType: json`, "type/subtype"},
		{"empty overlay entry", `
spec: { overlays: [""] }`, "must name a file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte("apiVersion: " + APIVersion + "\n" + strings.TrimPrefix(tc.yaml, "\n")))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestACorrectionAloneIsNotAnEmptyOverride(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("apiVersion: " + APIVersion + `
operationOverrides:
  - match: { operationId: x }
    mediaType: application/merge-patch+json
`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 4: `internal/cli/corrections_test.go`** (review focus 4)

```go
package cli

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
)

func TestReloadAcceptsCatalogChangesAndRefusesPolicyChanges(t *testing.T) {
	t.Parallel()
	running := config.Runtime{AllowMutations: false}

	catalogOnly := running
	catalogOnly.Overrides = []config.OperationOverride{{Match: config.Match{OperationID: "x"}, MediaType: "application/json"}}
	got, _, err := reloadRuntime(running, "cfg.yaml", func() (config.Runtime, error) { return catalogOnly, nil })
	if err != nil || len(got.Overrides) != 1 {
		t.Fatalf("catalog change: got %+v, err %v", got.Overrides, err)
	}

	policy := catalogOnly
	policy.AllowMutations = true
	_, _, err = reloadRuntime(running, "cfg.yaml", func() (config.Runtime, error) { return policy, nil })
	if err == nil || !strings.Contains(err.Error(), "AllowMutations") {
		t.Fatalf("policy change: err = %v", err)
	}
}

func TestReloadWithoutAConfigurationKeepsTheRunningOne(t *testing.T) {
	t.Parallel()
	running := config.Runtime{Mode: "search"}
	got, mode, err := reloadRuntime(running, "", func() (config.Runtime, error) {
		t.Fatal("resolve must not be called without a configuration file")
		return config.Runtime{}, nil
	})
	if err != nil || got.Mode != "search" || mode != "search" {
		t.Fatalf("got %+v %q %v", got, mode, err)
	}
}
```

- [ ] **Step 5: `test/e2e/corrections_test.go`** — Kubernetes 77/77 and a stale correction stops serve

```go
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestKubernetesMediaTypeCorrectionsPublishEveryOperation(t *testing.T) {
	t.Parallel()
	spec := filepath.Join("..", "testdata", "corpus", "kubernetes-apps-v1.json")
	raw, err := os.ReadFile(spec)
	if err != nil {
		t.Skipf("corpus not fetched (make corpus): %v", err)
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range doc.Paths {
		if patch, ok := item["patch"]; ok {
			ids = append(ids, patch.OperationID)
		}
	}
	sort.Strings(ids)
	var cfg strings.Builder
	cfg.WriteString("apiVersion: lotsman.dev/v1alpha1\noperationOverrides:\n")
	for _, id := range ids {
		fmt.Fprintf(&cfg, "  - match: { operationId: %s }\n    mediaType: application/merge-patch+json\n", id)
	}
	cfgPath := filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runCLI(t, "inspect", spec, "--config", cfgPath, "--json")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, stderr)
	}
	var report inspectReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report.Totals.Found != 77 || report.Totals.Supported != 77 {
		t.Fatalf("totals = %+v, want 77/77", report.Totals)
	}
}

func TestAStaleCorrectionStopsServe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	cfg := filepath.Join(dir, "lotsman.yaml")
	if err := os.WriteFile(spec, []byte(`openapi: 3.0.3
info: { title: S, version: "1" }
paths:
  /items:
    get:
      operationId: listItems
      parameters: [{ name: limit, in: query, schema: { type: integer } }]
      responses: { "200": { description: ok } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`apiVersion: lotsman.dev/v1alpha1
operationOverrides:
  - match: { operationId: listItems }
    parameters: [{ name: offset, in: query, schema: { type: string } }]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runCLI(t, "serve", spec, "--config", cfg, "--base-url", "http://127.0.0.1:1")
	if code == 0 || !strings.Contains(stderr, `"offset"`) {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
}
```

If `inspectReport.Totals` does not have `Found`/`Supported` with those names, read
`test/e2e/inspect_test.go` and use the fields it declares.

- [ ] **Step 6: Fuzz** — append to `internal/openapi/corrections_test.go`:

```go
func FuzzMergePatchNeverWritesItsTarget(f *testing.F) {
	f.Add("type", "string", "required")
	f.Fuzz(func(t *testing.T, key, value, other string) {
		target := domain.Schema{key: map[string]any{other: value}, "keep": []any{value}}
		before := cloneSchema(target)
		_ = mergePatch(target, domain.Schema{key: nil, other: map[string]any{key: value}})
		if !reflect.DeepEqual(target, before) {
			t.Fatalf("target changed: %v -> %v", before, target)
		}
	})
}
```

- [ ] **Step 7: Run everything**

```bash
make check
```

Expected: lint clean, all tests pass with the race detector. Fix what fails in the code under test,
not by loosening a test, unless the test contradicts spec.md — then say so in the commit message.

- [ ] **Step 8: Commit** — `git add -A && git commit -m "Тесты этапа 1: исправления, overlays, reload, Kubernetes 77/77"` (+ Co-Authored-By).

---

### Task 9: documentation

**Files:**
- Modify: `docs/spec.md` §5.2, `README.md` ("Narrowing the catalog, and correcting it"), `docs/corpus.md` (Kubernetes section)

- [ ] **Step 1: docs/spec.md §5.2** — append bullets (Russian, matching the section):

```markdown
- Исправления спецификации (ADR-0021, feature 008): `operationOverrides[].mediaType` (FR-29),
  `operationOverrides[].parameters`, `operationOverrides[].requestBody.patch`, `componentOverrides`
  и `spec.overlays` (стандартный OpenAPI Overlay). Исправление, которое ни с чем не совпало, —
  ошибка запуска; патч не содержит ключей на `$`; исправленные операции помечаются `local_override`.
- Reload переприменяет `spec.overlays`, `componentOverrides`, `operationOverrides` и `catalog`;
  любое другое изменение конфигурации отклоняется до перезапуска.
```

- [ ] **Step 2: README.md** — under "Narrowing the catalog, and correcting it", after the existing
`operationOverrides` example, add:

````markdown
When the document itself is wrong — or offers a choice lotsman will not make for you — correct it
in the same file, not in the vendor's:

```yaml
componentOverrides:
  - match: { schema: io.k8s.apimachinery.pkg.apis.meta.v1.DeleteOptions }
    patch: { properties: { gracePeriodSeconds: { type: string } } }   # JSON Merge Patch, JSON Schema 2020-12
    note: "the API takes a string"
operationOverrides:
  - match: { operationId: patchAppsV1NamespacedDeployment }
    mediaType: application/merge-patch+json      # one of the four the document offers
  - match: { operationId: listItems }
    parameters: [{ name: limit, in: query, schema: { type: string } }]
spec:
  overlays: [./vendor-fixes.overlay.yaml]        # standard OpenAPI Overlay, applied before parsing
```

A correction that no longer matches the document stops `serve` and is named; `inspect` and
`explain-call` mark every corrected operation. `serve --watch` picks corrections up without a
restart; a change to policy or credentials in the same file waits for one.
````

- [ ] **Step 3: docs/corpus.md** — in the Kubernetes section, after "a media-type override in
config lifts these.", replace that sentence's promise with the measurement:

```markdown
With one `mediaType: application/merge-patch+json` override per PATCH operation (twelve lines of
configuration, feature 008) all 77 operations are supported; the test
`TestKubernetesMediaTypeCorrectionsPublishEveryOperation` holds that number.
```

- [ ] **Step 4: Commit** — `git commit -am "Документация: исправления спецификации, reload, Kubernetes 77/77"` (+ Co-Authored-By).
