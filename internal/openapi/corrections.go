package openapi

import (
	"fmt"
	"reflect"
	"sort"
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
	// MediaType is as configured; newFixes normalizes it (lower case, no
	// parameters) with the same normalizer that reads the document.
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
	// A copy, so normalizing never writes into the caller's Options.
	f.corrections.Operations = append([]OperationCorrection(nil), c.Operations...)
	for i := range f.corrections.Operations {
		f.corrections.Operations[i].MediaType = normalizeMediaType(f.corrections.Operations[i].MediaType)
	}
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
	// A config patch nests its own named map type (config.Patch); it is the
	// same JSON object, and treating it as an opaque value would alias it.
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String {
		out := make(domain.Schema, rv.Len())
		for _, k := range rv.MapKeys() {
			out[k.String()] = rv.MapIndex(k).Interface()
		}
		return out, true
	}
	return nil, false
}

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

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
		if m, ok := asObject(v); ok {
			return map[string]any(cloneSchema(m))
		}
		return v
	}
}

// checkCorrectedShape applies to an already normalized schema the shape rules
// normalizeProxy applies to a document's, for the same target, so a correction
// cannot publish what the document could not. A body target keeps the
// top-level check; a scalars-only target (a URL or header value) refuses
// compositions, conditionals, typeless and object schemas, and arrays whose
// items are missing or are not themselves scalars.
func checkCorrectedShape(s domain.Schema, target schemaTarget) domain.ReasonCode {
	types := schemaTypes(s)
	if !target.scalarsOnly {
		return checkShape(types, s, target)
	}
	for _, keyword := range []string{"allOf", "oneOf", "anyOf", "not", "if", "then", "else"} {
		if _, ok := s[keyword]; ok {
			return target.refusal
		}
	}
	// Unlike a document schema, a corrected one is already normalized, so
	// annotations are present: the typeless test is on the types alone.
	if len(types) == 0 {
		return target.refusal
	}
	if reason := checkShape(types, domain.Schema{}, target); reason != "" {
		return reason
	}
	for _, t := range types {
		if t != "array" {
			continue
		}
		items, ok := asObject(s["items"])
		if !ok {
			return target.refusal
		}
		for _, it := range schemaTypes(items) {
			if it == "array" || it == "object" {
				return target.refusal
			}
		}
		if reason := checkCorrectedShape(items, target); reason != "" {
			return reason
		}
	}
	return ""
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

// applyParameters returns the operation's parameters as corrected, and the
// schemas that replace the document's. The document's parameters are never
// written to: a path-level parameter is shared by every operation under its
// path, and a correction names one.
func (f *fixes) applyParameters(c *OperationCorrection, merged map[paramKey]*v3.Parameter,
	result *domain.Operation) (out map[paramKey]*v3.Parameter, replaced map[paramKey]domain.Schema) {
	if c == nil || len(c.Parameters) == 0 {
		return merged, nil
	}
	out = make(map[paramKey]*v3.Parameter, len(merged))
	for key, p := range merged {
		out[key] = p
	}
	replaced = map[paramKey]domain.Schema{}
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
