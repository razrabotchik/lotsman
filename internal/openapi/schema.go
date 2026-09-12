package openapi

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	"github.com/pb33f/libopenapi/orderedmap"
	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// maxSchemaDepth bounds how deep a single inline schema may nest. References
// do not consume depth: they are emitted as references and normalized once
// into the bundle, so a deep tree of distinct components stays translatable
// while a genuinely deep *inline* schema is still bounded.
const maxSchemaDepth = 24

// schemaTarget says what a normalized schema is for. The two targets differ in
// what shapes they accept and in the reason code they refuse with; everything
// else -- the OAS 3.0 translations, the constraints, the annotations -- is the
// same normalization, done once.
type schemaTarget struct {
	// scalarsOnly restricts the shape to what a URL path or query value can
	// carry: a scalar, or an array of scalars.
	scalarsOnly bool
	// refusal is the reason reported when the shape is not translatable.
	refusal domain.ReasonCode
}

var (
	parameterTarget = schemaTarget{scalarsOnly: true, refusal: domain.ReasonUnsupportedParameterSchema}
	bodyTarget      = schemaTarget{refusal: domain.ReasonUnsupportedBodySchema}
)

// bundle accumulates one operation's `$defs` while its schemas are normalized.
//
// Inlining every `$ref` is the obvious implementation and the wrong one: it
// republishes a shared component once per use (Kubernetes publishes megabytes
// this way), and it cannot express a recursive schema at all -- the walk has
// to stop somewhere and whatever it does at that point is a lie about the API.
// Keeping references as references solves both: each component is normalized
// once, a cycle closes naturally, and JSON Schema 2020-12 resolves `$defs`
// without any help from us (pipeline.md stage 2).
type bundle struct {
	defs domain.SchemaDefs
	// names maps a source pointer ("#/components/schemas/Pet") to the name it
	// was given in $defs, so the same component always lands in the same slot.
	names map[string]string
	// building guards the recursion while a component is still being built:
	// its own members may reference it.
	building map[string]bool
}

func newBundle() *bundle {
	return &bundle{defs: domain.SchemaDefs{}, names: map[string]string{}, building: map[string]bool{}}
}

// normalizeProxy is the entry point for any schema that arrives through a
// proxy, which is every schema in the document: it is the only place that can
// still see whether the author wrote a `$ref`.
func (b *bundle) normalizeProxy(proxy *base.SchemaProxy, target schemaTarget, depth int, visiting map[*base.Schema]bool) (domain.Schema, domain.ReasonCode) {
	if proxy == nil {
		return nil, domain.ReasonInvalidSchema
	}
	schema := proxy.Schema()
	if schema == nil {
		return nil, domain.ReasonInvalidSchema
	}
	if !b.shouldReference(proxy, schema) {
		return b.normalizeSchema(schema, target, depth, visiting)
	}

	pointer := proxy.GetReference()
	name, known := b.names[pointer]
	if !known {
		name = b.assign(pointer)
	}
	if b.building[name] {
		// A cycle closed. The reference is the answer: the definition being
		// built above will be complete by the time anyone follows it.
		return domain.Schema{"$ref": defsPointer(name)}, ""
	}
	if _, done := b.defs[name]; !done {
		b.building[name] = true
		// A referenced component starts its own depth budget: it is a
		// separate definition, not another level of the schema referring to it.
		normalized, reason := b.normalizeSchema(schema, target, 0, map[*base.Schema]bool{})
		delete(b.building, name)
		if reason != "" {
			return nil, reason
		}
		b.defs[name] = normalized
	}
	return domain.Schema{"$ref": defsPointer(name)}, ""
}

// shouldReference decides whether a `$ref` is worth keeping as one. A
// reference to a scalar is inlined: "#/$defs/Limit" pointing at
// {"type": "integer"} is an indirection that costs a reader more than it saves.
func (b *bundle) shouldReference(proxy *base.SchemaProxy, schema *base.Schema) bool {
	if !proxy.IsReference() || !isLocalRef(proxy.GetReference()) {
		return false
	}
	return orderedmap.Len(schema.Properties) > 0 || len(schema.AllOf) > 0 ||
		len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 ||
		schema.Items != nil || schema.AdditionalProperties != nil
}

// assign names a component after the last segment of its pointer, which is
// what the author called it, and disambiguates a collision deterministically
// rather than by processing order.
func (b *bundle) assign(pointer string) string {
	stem := sanitizeDefName(lastSegment(pointer))
	if stem == "" {
		stem = "schema"
	}
	name := stem
	for suffix := 2; ; suffix++ {
		taken, exists := b.takenBy(name)
		if !exists || taken == pointer {
			break
		}
		name = stem + "_" + strconv.Itoa(suffix)
	}
	b.names[pointer] = name
	return name
}

func (b *bundle) takenBy(name string) (string, bool) {
	for pointer, assigned := range b.names {
		if assigned == name {
			return pointer, true
		}
	}
	return "", false
}

func isLocalRef(pointer string) bool {
	return strings.HasPrefix(pointer, "#/")
}

func lastSegment(pointer string) string {
	if index := strings.LastIndex(pointer, "/"); index >= 0 {
		return pointer[index+1:]
	}
	return pointer
}

var defNameCharset = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func sanitizeDefName(name string) string {
	return defNameCharset.ReplaceAllString(name, "_")
}

func defsPointer(name string) string {
	return "#/$defs/" + name
}

// normalizeSchema translates an OAS schema into the JSON Schema 2020-12
// fragment lotsman publishes and validates against.
//
// OAS 3.0 is not a JSON Schema dialect, it is a near-miss, and the misses are
// exactly the things that change what validates (pitfall #6):
//
//   - `nullable: true` is not a keyword in 2020-12 -- a validator ignores it,
//     so a spec that allows null would reject it. It becomes a union type.
//   - `exclusiveMinimum: true` is a boolean modifier on `minimum` in 3.0 and a
//     standalone number in 2020-12. Republished unchanged it would be read as
//     "exclusiveMinimum is the boolean true", which is not a bound at all.
//   - `example` is singular in OAS and plural in 2020-12.
//
// Keywords that exist only in OAS (`xml`, `discriminator`, `externalDocs`,
// `deprecated` on a schema) are not copied: they say nothing about whether an
// argument is valid, and a validation schema that carries them invites a
// client to act on them.
func (b *bundle) normalizeSchema(schema *base.Schema, target schemaTarget, depth int, visiting map[*base.Schema]bool) (domain.Schema, domain.ReasonCode) {
	if schema == nil {
		return nil, domain.ReasonInvalidSchema
	}
	if depth > maxSchemaDepth {
		return nil, target.refusal
	}
	if visiting[schema] {
		// A resolved reference cycle. Truncation policy is T025's; until then
		// the honest answer is a refusal rather than a half-built schema.
		return nil, target.refusal
	}
	visiting[schema] = true
	defer delete(visiting, schema)

	// Conditional subschemas decide validity by rules a caller cannot see in
	// the published shape.
	if schema.Not != nil || schema.If != nil || schema.Then != nil || schema.Else != nil {
		return nil, target.refusal
	}

	out := domain.Schema{}
	if reason := b.copyComposition(schema, out, target, depth, visiting); reason != "" {
		return nil, reason
	}

	types := effectiveTypes(schema)
	if reason := checkShape(types, out, target); reason != "" {
		return nil, reason
	}
	if len(types) == 1 {
		out["type"] = types[0]
	} else if len(types) > 1 {
		out["type"] = anySlice(types)
	}

	if contains(types, "object") {
		if reason := b.copyObject(schema, out, target, depth, visiting); reason != "" {
			return nil, reason
		}
	}
	if contains(types, "array") {
		if reason := b.copyArray(schema, out, target, depth, visiting); reason != "" {
			return nil, reason
		}
	}

	copyAnnotations(schema, out)
	copyStringConstraints(schema, out)
	copyNumericConstraints(schema, out)
	copyArrayConstraints(schema, out)
	return out, ""
}

// checkShape enforces the target's restrictions on the declared types.
func checkShape(types []string, out domain.Schema, target schemaTarget) domain.ReasonCode {
	if len(types) == 0 {
		if len(out) > 0 {
			return "" // a composition carries the shape
		}
		// A schema with no type, no properties and no composition accepts
		// anything, and a schema that accepts anything cannot validate an
		// argument (FR-23).
		return target.refusal
	}
	for _, t := range types {
		switch t {
		case "string", "number", "integer", "boolean", "null":
		case "array", "object":
			if target.scalarsOnly && t == "object" {
				return target.refusal
			}
		default: // a type OAS does not define
			return target.refusal
		}
	}
	return ""
}

// effectiveTypes returns the declared types plus the OAS 3.0 nullable
// translation, inferring "object" for the very common schema that declares
// properties without a type -- properties on a non-object mean nothing, so
// this states what the document already implies.
func effectiveTypes(schema *base.Schema) []string {
	types := append([]string(nil), schema.Type...)
	if len(types) == 0 && orderedmap.Len(schema.Properties) > 0 {
		types = append(types, "object")
	}
	if schema.Nullable != nil && *schema.Nullable && len(types) > 0 && !contains(types, "null") {
		types = append(types, "null")
	}
	return types
}

func (b *bundle) copyComposition(schema *base.Schema, out domain.Schema, target schemaTarget, depth int, visiting map[*base.Schema]bool) domain.ReasonCode {
	branchSets := []struct {
		keyword  string
		branches []*base.SchemaProxy
	}{
		{"allOf", schema.AllOf}, {"oneOf", schema.OneOf}, {"anyOf", schema.AnyOf},
	}
	for _, set := range branchSets {
		if len(set.branches) == 0 {
			continue
		}
		if target.scalarsOnly {
			// A composed value has no single serialization, and a path or
			// query slot holds one string.
			return target.refusal
		}
		converted := make([]any, 0, len(set.branches))
		for _, proxy := range set.branches {
			if proxy == nil {
				return domain.ReasonInvalidSchema
			}
			sub, reason := b.normalizeProxy(proxy, target, depth+1, visiting)
			if reason != "" {
				return reason
			}
			converted = append(converted, map[string]any(sub))
		}
		out[set.keyword] = converted
	}
	return ""
}

// copyObject translates an object's members.
//
// A readOnly property is excluded from the input schema (pitfall #7): the
// server sends it, the client does not, and publishing it invites a model to
// invent an id for a resource that does not exist yet. It is dropped from
// `required` for the same reason -- in OAS, required+readOnly means required
// in the response.
func (b *bundle) copyObject(schema *base.Schema, out domain.Schema, target schemaTarget, depth int, visiting map[*base.Schema]bool) domain.ReasonCode {
	readOnly := map[string]bool{}

	if orderedmap.Len(schema.Properties) > 0 {
		properties := make(map[string]any, orderedmap.Len(schema.Properties))
		for name, proxy := range schema.Properties.FromOldest() {
			if proxy == nil {
				return domain.ReasonInvalidSchema
			}
			property := proxy.Schema()
			if property == nil {
				return domain.ReasonInvalidSchema
			}
			if property.ReadOnly != nil && *property.ReadOnly {
				readOnly[name] = true
				continue
			}
			converted, reason := b.normalizeProxy(proxy, target, depth+1, visiting)
			if reason != "" {
				return reason
			}
			properties[name] = map[string]any(converted)
		}
		out["properties"] = properties
	}

	if len(schema.Required) > 0 {
		required := make([]string, 0, len(schema.Required))
		for _, name := range schema.Required {
			if readOnly[name] {
				continue
			}
			required = append(required, name)
		}
		sort.Strings(required)
		if len(required) > 0 {
			out["required"] = anySlice(required)
		}
	}

	if schema.AdditionalProperties != nil {
		switch {
		case schema.AdditionalProperties.IsB():
			out["additionalProperties"] = schema.AdditionalProperties.B
		case schema.AdditionalProperties.A != nil:
			converted, reason := b.normalizeProxy(schema.AdditionalProperties.A, target, depth+1, visiting)
			if reason != "" {
				return reason
			}
			out["additionalProperties"] = map[string]any(converted)
		}
	}
	return ""
}

func (b *bundle) copyArray(schema *base.Schema, out domain.Schema, target schemaTarget, depth int, visiting map[*base.Schema]bool) domain.ReasonCode {
	if schema.Items == nil || !schema.Items.IsA() || schema.Items.A == nil {
		return target.refusal
	}
	itemTarget := target
	if target.scalarsOnly {
		// An array of arrays, or of objects, cannot be serialized into one
		// path or query value: the item target refuses both.
		itemTarget.scalarsOnly = true
	}
	item := schema.Items.A.Schema()
	if item == nil {
		return domain.ReasonInvalidSchema
	}
	if target.scalarsOnly {
		for _, t := range effectiveTypes(item) {
			if t == "array" || t == "object" {
				return target.refusal
			}
		}
	}
	converted, reason := b.normalizeProxy(schema.Items.A, itemTarget, depth+1, visiting)
	if reason != "" {
		return reason
	}
	out["items"] = map[string]any(converted)
	return ""
}

func copyAnnotations(schema *base.Schema, out domain.Schema) {
	if schema.Title != "" {
		out["title"] = schema.Title
	}
	// Description is copied as authored: it is untrusted text bound for an LLM
	// context, and the catalog sanitizes it when it builds the tool schema.
	if schema.Description != "" {
		out["description"] = schema.Description
	}
	if schema.Format != "" {
		out["format"] = schema.Format
	}
	if len(schema.Enum) > 0 {
		values := make([]any, 0, len(schema.Enum))
		for _, node := range schema.Enum {
			values = append(values, decodeNode(node))
		}
		out["enum"] = values
	}
	if schema.Const != nil {
		out["const"] = decodeNode(schema.Const)
	}
	if schema.Default != nil {
		// Published for the model to see; never applied silently (FR-24), and
		// the catalog turns it into prose rather than a keyword.
		out["default"] = decodeNode(schema.Default)
	}
	if examples := normalizeExamples(schema); len(examples) > 0 {
		out["examples"] = examples
	}
	// writeOnly survives: the client sends it, the server does not return it,
	// which is exactly what an input schema describes (pitfall #7).
	if schema.WriteOnly != nil && *schema.WriteOnly {
		out["writeOnly"] = true
	}
}

// normalizeExamples folds OAS 3.0's singular `example` and 2020-12's plural
// `examples` into the plural form the dialect defines.
func normalizeExamples(schema *base.Schema) []any {
	if len(schema.Examples) > 0 {
		out := make([]any, 0, len(schema.Examples))
		for _, node := range schema.Examples {
			out = append(out, decodeNode(node))
		}
		return out
	}
	if schema.Example != nil {
		return []any{decodeNode(schema.Example)}
	}
	return nil
}

func copyStringConstraints(schema *base.Schema, out domain.Schema) {
	if schema.Pattern != "" {
		// Safe to publish and to compile: Go's regexp is RE2, so a hostile
		// pattern cannot backtrack the validator into a hang.
		out["pattern"] = schema.Pattern
	}
	if schema.MinLength != nil {
		out["minLength"] = *schema.MinLength
	}
	if schema.MaxLength != nil {
		out["maxLength"] = *schema.MaxLength
	}
}

// copyNumericConstraints translates both spellings of exclusive bounds: OAS
// 3.0's boolean modifier on minimum/maximum, and 2020-12's standalone numeric
// keyword (pitfall #6).
func copyNumericConstraints(schema *base.Schema, out domain.Schema) {
	if schema.MultipleOf != nil {
		out["multipleOf"] = *schema.MultipleOf
	}
	exclusiveMin, minIsExclusive := exclusiveBound(schema.ExclusiveMinimum, schema.Minimum)
	if exclusiveMin != nil {
		out["exclusiveMinimum"] = *exclusiveMin
	}
	if schema.Minimum != nil && !minIsExclusive {
		out["minimum"] = *schema.Minimum
	}
	exclusiveMax, maxIsExclusive := exclusiveBound(schema.ExclusiveMaximum, schema.Maximum)
	if exclusiveMax != nil {
		out["exclusiveMaximum"] = *exclusiveMax
	}
	if schema.Maximum != nil && !maxIsExclusive {
		out["maximum"] = *schema.Maximum
	}
}

// exclusiveBound returns the numeric exclusive bound and whether it consumed
// the companion inclusive bound (the OAS 3.0 form).
func exclusiveBound(value *base.DynamicValue[bool, float64], inclusive *float64) (*float64, bool) {
	if value == nil {
		return nil, false
	}
	if value.IsB() {
		bound := value.B
		return &bound, false
	}
	if value.A && inclusive != nil {
		bound := *inclusive
		return &bound, true
	}
	return nil, false
}

func copyArrayConstraints(schema *base.Schema, out domain.Schema) {
	if schema.MinItems != nil {
		out["minItems"] = *schema.MinItems
	}
	if schema.MaxItems != nil {
		out["maxItems"] = *schema.MaxItems
	}
	if schema.UniqueItems != nil {
		out["uniqueItems"] = *schema.UniqueItems
	}
}

// decodeNode turns a YAML scalar node into the Go value it represents, so the
// published schema carries JSON values rather than YAML syntax.
func decodeNode(node *yaml.Node) any {
	if node == nil {
		return nil
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return node.Value
	}
	return value
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func anySlice(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
