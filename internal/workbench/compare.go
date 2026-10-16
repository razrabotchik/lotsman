package workbench

import (
	"bytes"
	"encoding/json"
	"fmt"
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
			// Not in the base at all (the base document did not parse, or
			// an overlay in the draft adds it): nothing to compare, but an
			// explicit widening override still needs its acknowledgement.
			out.Widening = append(out.Widening, explicitWidening(a)...)
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
		out = append(out, effectRead(a))
	}
	if b.Excluded && !a.Excluded {
		out = append(out, enabled(a))
	}
	// Changing authProfile is the override's own field: the published tool's
	// credentials appear and disappear with publication, which is not a
	// change of credential.
	if profile := overrideProfile(a); profile != "" && profile != overrideProfile(b) {
		out = append(out, authProfile(a, profile))
	}
	return out
}

// explicitWidening is the widening an operation absent from the base can
// still be seen to carry: what its override says explicitly.
func explicitWidening(a *Operation) []Widening {
	ov := a.Override
	if ov == nil {
		return nil
	}
	var out []Widening
	if ov.Effect == string(domain.EffectRead) && !safeMethod(a.Method) {
		out = append(out, effectRead(a))
	}
	if ov.Enabled != nil && *ov.Enabled {
		out = append(out, enabled(a))
	}
	if ov.AuthProfile != "" {
		out = append(out, authProfile(a, ov.AuthProfile))
	}
	return out
}

func safeMethod(method string) bool {
	return strings.EqualFold(method, "GET") || strings.EqualFold(method, "HEAD")
}

func overrideProfile(op *Operation) string {
	if op.Override == nil {
		return ""
	}
	return op.Override.AuthProfile
}

func effectRead(a *Operation) Widening {
	return Widening{ID: a.Key + "#effect-read", Key: a.Key, Kind: "effect-read",
		Message: fmt.Sprintf("%s %s is treated as a read: it runs without the mutation policy and without approval", a.Method, a.Path)}
}

func enabled(a *Operation) Widening {
	return Widening{ID: a.Key + "#enabled", Key: a.Key, Kind: "enabled",
		Message: fmt.Sprintf("%s %s is published again", a.Method, a.Path)}
}

func authProfile(a *Operation, profile string) Widening {
	return Widening{ID: a.Key + "#auth-profile", Key: a.Key, Kind: "auth-profile",
		Message: fmt.Sprintf("%s %s uses credential %s", a.Method, a.Path, profile)}
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
	return bytes.Equal(a, b)
}

// SortWidening orders widening changes by ID, for stable output.
func SortWidening(w []Widening) { sort.Slice(w, func(i, j int) bool { return w[i].ID < w[j].ID }) }
