package openapi

import (
	"fmt"
	"sort"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

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
func buildInput(merged map[paramKey]*v3.Parameter, pointer string, defs *bundle) inputBuild {
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

		schema, reason := parameterSchema(source, defs)
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
func parameterSchema(p *v3.Parameter, defs *bundle) (domain.Schema, domain.ReasonCode) {
	if p.Schema == nil {
		if orderedmap.Len(p.Content) > 0 {
			return nil, domain.ReasonUnsupportedMediaType
		}
		return nil, domain.ReasonInvalidParameter
	}
	return defs.normalizeProxy(p.Schema, parameterTarget, 0, map[*base.Schema]bool{})
}
