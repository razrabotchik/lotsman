package openapi

import (
	"fmt"
	"sort"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	yaml "go.yaml.in/yaml/v4"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// serializableStyle is the one style lotsman serializes per location
// (docs/spec.md 4.5). Anything else -- label, matrix, spaceDelimited,
// pipeDelimited, deepObject -- rejects the operation by name instead of being
// approximated (Principle I).
var serializableStyle = map[domain.ParameterLocation]domain.ParameterStyle{
	domain.LocationPath:   domain.StyleSimple,
	domain.LocationQuery:  domain.StyleForm,
	domain.LocationHeader: domain.StyleSimple,
	domain.LocationCookie: domain.StyleForm,
}

// implementedLocations are the locations the request builder can write today.
// A cookie parameter is translatable (it enumerates, it publishes a schema)
// but blocks execution until its serialization exists.
var implementedLocations = map[domain.ParameterLocation]bool{
	domain.LocationPath:   true,
	domain.LocationQuery:  true,
	domain.LocationHeader: true,
}

// locationOrder fixes the order parameters appear in the IR, and with it the
// order of the query string: map iteration order must never reach the wire
// (Constitution IV).
var locationOrder = map[domain.ParameterLocation]int{
	domain.LocationPath:   0,
	domain.LocationQuery:  1,
	domain.LocationHeader: 2,
	domain.LocationCookie: 3,
}

// inputBuild is the verdict of normalizing one operation's parameters.
type inputBuild struct {
	model       domain.InputModel
	diagnostics []domain.Diagnostic
	rejections  []domain.ReasonCode
	blockers    []domain.ReasonCode
}

// buildInput normalizes merged path+operation parameters into the IR's input
// model, resolving the location-dependent style/explode defaults and refusing
// everything it cannot serialize exactly.
func buildInput(merged map[paramKey]*v3.Parameter, pointer string) inputBuild {
	var out inputBuild

	for _, key := range sortedParamKeys(merged) {
		source := merged[key]
		paramPointer := fmt.Sprintf("%s/parameters/%s/%s", pointer, key.in, key.name)

		in := domain.ParameterLocation(source.In)
		_, knownLocation := locationOrder[in]
		if key.name == "" || !knownLocation {
			out.reject(domain.ReasonInvalidParameter, paramPointer,
				fmt.Sprintf("parameter %q has no name or an unknown location %q", key.name, key.in))
			continue
		}

		style := domain.ParameterStyle(source.Style)
		if style == "" {
			style = domain.DefaultStyle(in)
		}
		if style != serializableStyle[in] {
			out.reject(domain.ReasonUnsupportedParameterStyle, paramPointer,
				fmt.Sprintf("style %q in %q is not serialized; only %q is", style, in, serializableStyle[in]))
			continue
		}

		explode := domain.DefaultExplode(style)
		if source.Explode != nil {
			explode = *source.Explode
		}

		if in == domain.LocationHeader && domain.IsProtectedHeader(key.name) {
			// A spec that parameterizes Authorization, Host or Content-Type is
			// asking the model to control the transport or to supply its own
			// credentials. That is not a translation lotsman makes.
			out.reject(domain.ReasonInvalidParameter, paramPointer, fmt.Sprintf(
				"header parameter %q controls the transport or lotsman's own credentials and is never settable", key.name))
			continue
		}

		schema, reason := parameterSchema(source)
		if reason != "" {
			out.reject(reason, paramPointer,
				fmt.Sprintf("parameter %q in %q: %s", key.name, in, schemaReasonText(reason)))
			continue
		}

		if !implementedLocations[in] {
			out.block(domain.ReasonParametersNotImplemented)
		}

		out.model.Parameters = append(out.model.Parameters, domain.Parameter{
			Name:          source.Name,
			In:            in,
			Required:      source.Required != nil && *source.Required,
			Deprecated:    source.Deprecated,
			Style:         style,
			Explode:       explode,
			AllowReserved: source.AllowReserved,
			Description:   source.Description,
			Schema:        schema,
		})
	}

	return out
}

func (b *inputBuild) reject(code domain.ReasonCode, pointer, message string) {
	b.diagnostics = append(b.diagnostics, domain.Diagnostic{
		Severity: domain.SeverityError,
		Code:     code,
		Pointer:  pointer,
		Message:  message,
	})
	b.rejections = appendReason(b.rejections, code)
}

func (b *inputBuild) block(code domain.ReasonCode) {
	b.blockers = appendReason(b.blockers, code)
}

func schemaReasonText(code domain.ReasonCode) string {
	switch code {
	case domain.ReasonUnsupportedMediaType:
		return "content-typed parameters are not translated; use a schema"
	case domain.ReasonUnsupportedParameterSchema:
		return "only scalars and arrays of scalars can be carried in a path or query value"
	case domain.ReasonInvalidSchema:
		return "the schema could not be resolved"
	default:
		return "the parameter declares neither a schema nor content"
	}
}

// sortedParamKeys orders parameters by location (path, query, header, cookie)
// and then by name.
func sortedParamKeys(merged map[paramKey]*v3.Parameter) []paramKey {
	keys := make([]paramKey, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		li, lj := locationOrder[domain.ParameterLocation(keys[i].in)], locationOrder[domain.ParameterLocation(keys[j].in)]
		if li != lj {
			return li < lj
		}
		if keys[i].in != keys[j].in {
			return keys[i].in < keys[j].in
		}
		return keys[i].name < keys[j].name
	})
	return keys
}

// parameterSchema converts a parameter's OAS schema into the JSON Schema
// 2020-12 fragment published to the client, or returns the reason it cannot.
func parameterSchema(p *v3.Parameter) (domain.Schema, domain.ReasonCode) {
	if p.Schema == nil {
		if orderedmap.Len(p.Content) > 0 {
			return nil, domain.ReasonUnsupportedMediaType
		}
		return nil, domain.ReasonInvalidParameter
	}
	schema := p.Schema.Schema()
	if schema == nil {
		return nil, domain.ReasonInvalidSchema
	}
	return convertSchema(schema, true)
}

// convertSchema translates the scalar/array subset a path or query value can
// carry. Composition, objects and nested arrays are refused rather than
// half-translated: a value lotsman cannot serialize exactly must not become a
// tool argument (Principle I).
//
// The two OAS 3.0 constructs that would otherwise emit invalid 2020-12 --
// `nullable` and boolean exclusive bounds -- are translated here for the
// parameter subset; T023 does the same for body and response schemas.
func convertSchema(schema *base.Schema, allowArray bool) (domain.Schema, domain.ReasonCode) {
	if hasComposition(schema) {
		return nil, domain.ReasonUnsupportedParameterSchema
	}

	types := append([]string(nil), schema.Type...)
	if len(types) == 0 {
		return nil, domain.ReasonUnsupportedParameterSchema
	}
	for _, t := range types {
		switch t {
		case "string", "number", "integer", "boolean", "null":
		case "array":
			if !allowArray {
				return nil, domain.ReasonUnsupportedParameterSchema
			}
		default: // object, or a type OAS does not define
			return nil, domain.ReasonUnsupportedParameterSchema
		}
	}
	if schema.Nullable != nil && *schema.Nullable && !contains(types, "null") {
		types = append(types, "null")
	}

	out := domain.Schema{}
	if len(types) == 1 {
		out["type"] = types[0]
	} else {
		out["type"] = anySlice(types)
	}

	if contains(types, "array") {
		if schema.Items == nil || !schema.Items.IsA() || schema.Items.A == nil {
			return nil, domain.ReasonUnsupportedParameterSchema
		}
		item := schema.Items.A.Schema()
		if item == nil {
			return nil, domain.ReasonInvalidSchema
		}
		converted, reason := convertSchema(item, false)
		if reason != "" {
			return nil, reason
		}
		out["items"] = map[string]any(converted)
	}

	copyAnnotations(schema, out)
	copyStringConstraints(schema, out)
	copyNumericConstraints(schema, out)
	copyArrayConstraints(schema, out)
	return out, ""
}

func hasComposition(schema *base.Schema) bool {
	return len(schema.AllOf) > 0 || len(schema.OneOf) > 0 || len(schema.AnyOf) > 0 ||
		len(schema.PrefixItems) > 0 || schema.Not != nil || schema.If != nil ||
		schema.Then != nil || schema.Else != nil ||
		orderedmap.Len(schema.Properties) > 0 || orderedmap.Len(schema.PatternProperties) > 0
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
		// Published for the model to see; never applied silently (FR-24).
		out["default"] = decodeNode(schema.Default)
	}
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
