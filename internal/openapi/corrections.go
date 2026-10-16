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
