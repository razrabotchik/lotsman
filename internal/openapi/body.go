package openapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// jsonMediaType is the one request media type lotsman sends (docs/spec.md
// 4.5). Form-urlencoded, multipart and binary bodies are named as later work,
// not approximated.
const jsonMediaType = "application/json"

// maxBodySchemaDepth bounds how deep a request body schema may nest. It also
// terminates the conversion on a schema that libopenapi resolved into a cycle:
// truncation policy for cycles is T025's, and until it exists the honest
// answer is a refusal rather than a half-built schema.
const maxBodySchemaDepth = 24

// bodyBuild is the verdict of normalizing one operation's request body.
type bodyBuild struct {
	spec        *domain.BodySpec
	diagnostics []domain.Diagnostic
	rejections  []domain.ReasonCode
}

// buildBody normalizes a request body into the IR: one deterministically
// chosen media type and a JSON Schema 2020-12 fragment to validate against.
func buildBody(rb *v3.RequestBody, pointer string) bodyBuild {
	var out bodyBuild
	if rb == nil {
		return out
	}
	bodyPointer := pointer + "/requestBody"

	mediaType, content, reason := selectMediaType(rb.Content)
	if reason != "" {
		out.reject(reason, bodyPointer, fmt.Sprintf(
			"request body offers %s; lotsman sends %s only",
			describeMediaTypes(rb.Content), jsonMediaType))
		return out
	}
	if content.Schema == nil {
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer,
			"request body declares no schema, so no argument can be validated against it")
		return out
	}
	schema := content.Schema.Schema()
	if schema == nil {
		out.reject(domain.ReasonInvalidSchema, bodyPointer, "request body schema could not be resolved")
		return out
	}

	converted, convertReason := convertBodySchema(schema, 0, map[*base.Schema]bool{})
	if convertReason != "" {
		out.reject(convertReason, bodyPointer, bodyReasonText(convertReason))
		return out
	}

	out.spec = &domain.BodySpec{
		MediaType:   mediaType,
		Required:    rb.Required != nil && *rb.Required,
		Description: rb.Description,
		Schema:      converted,
	}
	return out
}

func (b *bodyBuild) reject(code domain.ReasonCode, pointer, message string) {
	b.diagnostics = append(b.diagnostics, domain.Diagnostic{
		Severity: domain.SeverityError,
		Code:     code,
		Pointer:  pointer,
		Message:  message,
	})
	b.rejections = appendReason(b.rejections, code)
}

func bodyReasonText(code domain.ReasonCode) string {
	switch code {
	case domain.ReasonInvalidSchema:
		return "a schema inside the request body could not be resolved"
	default:
		return "the request body schema uses a construct lotsman does not translate " +
			"(a conditional subschema, an unconstrained schema, a resolved reference cycle, " +
			"or nesting past the depth limit)"
	}
}

// selectMediaType picks the request media type deterministically (FR-29):
// exact application/json wins, then a single JSON-structured type such as
// application/merge-patch+json. Anything else is refused -- "pick the first
// one" is how a converter ends up sending a JSON body to a multipart endpoint.
func selectMediaType(content *orderedmap.Map[string, *v3.MediaType]) (string, *v3.MediaType, domain.ReasonCode) {
	if orderedmap.Len(content) == 0 {
		return "", nil, domain.ReasonUnsupportedMediaType
	}

	var jsonLike []string
	byName := make(map[string]*v3.MediaType, orderedmap.Len(content))
	for name, media := range content.FromOldest() {
		normalized := normalizeMediaType(name)
		byName[normalized] = media
		if normalized == jsonMediaType {
			return normalized, media, ""
		}
		if strings.HasSuffix(normalized, "+json") {
			jsonLike = append(jsonLike, normalized)
		}
	}
	if len(jsonLike) == 1 {
		return jsonLike[0], byName[jsonLike[0]], ""
	}

	// A sole wildcard range ("*/*" or "application/*") is not a guess to
	// resolve: it says the endpoint accepts any media type, and the schema
	// beside it applies to whichever one is sent. JSON is inside that set, so
	// lotsman sends JSON and says so in the Content-Type. Kubernetes declares
	// its DELETE and PATCH bodies exactly this way; refusing it costs half the
	// API for a wildcard nobody disputes.
	//
	// The wildcard must be alone: "*/*" alongside "application/xml" leaves two
	// schemas in play, and picking one would be the guess this avoids.
	if orderedmap.Len(content) == 1 {
		for name, media := range content.FromOldest() {
			if normalized := normalizeMediaType(name); normalized == "*/*" || normalized == "application/*" {
				return jsonMediaType, media, ""
			}
		}
	}
	return "", nil, domain.ReasonUnsupportedMediaType
}

func normalizeMediaType(name string) string {
	if semicolon := strings.IndexByte(name, ';'); semicolon >= 0 {
		name = name[:semicolon]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

func describeMediaTypes(content *orderedmap.Map[string, *v3.MediaType]) string {
	if orderedmap.Len(content) == 0 {
		return "no media type"
	}
	names := make([]string, 0, orderedmap.Len(content))
	for name := range content.FromOldest() {
		names = append(names, normalizeMediaType(name))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// convertBodySchema translates a request body schema into JSON Schema
// 2020-12. Unlike a parameter, a body may be an object or a composition: the
// shape is whatever the API accepts, and lotsman's job is to republish it
// faithfully, not to flatten it.
//
// readOnly properties are still published as inputs; excluding them is T024.
func convertBodySchema(schema *base.Schema, depth int, visiting map[*base.Schema]bool) (domain.Schema, domain.ReasonCode) {
	if depth > maxBodySchemaDepth {
		return nil, domain.ReasonUnsupportedBodySchema
	}
	if visiting[schema] {
		return nil, domain.ReasonUnsupportedBodySchema // a resolved cycle; see T025
	}
	visiting[schema] = true
	defer delete(visiting, schema)

	// Conditional subschemas decide validity by rules a caller cannot see in
	// the published shape; refusing keeps the tool's contract honest.
	if schema.Not != nil || schema.If != nil || schema.Then != nil || schema.Else != nil {
		return nil, domain.ReasonUnsupportedBodySchema
	}

	out := domain.Schema{}
	if reason := copyComposition(schema, out, depth, visiting); reason != "" {
		return nil, reason
	}

	types := effectiveTypes(schema)
	switch {
	case len(types) > 0:
		if len(types) == 1 {
			out["type"] = types[0]
		} else {
			out["type"] = anySlice(types)
		}
	case len(out) == 0:
		// No type, no properties, no composition: a schema that accepts
		// anything cannot validate anything (FR-23), so the operation is
		// refused rather than published with an empty contract.
		return nil, domain.ReasonUnsupportedBodySchema
	}

	if contains(types, "object") {
		if reason := copyObject(schema, out, depth, visiting); reason != "" {
			return nil, reason
		}
	}
	if contains(types, "array") {
		if schema.Items == nil || !schema.Items.IsA() || schema.Items.A == nil {
			return nil, domain.ReasonUnsupportedBodySchema
		}
		item := schema.Items.A.Schema()
		if item == nil {
			return nil, domain.ReasonInvalidSchema
		}
		converted, reason := convertBodySchema(item, depth+1, visiting)
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

func copyComposition(schema *base.Schema, out domain.Schema, depth int, visiting map[*base.Schema]bool) domain.ReasonCode {
	for keyword, branches := range map[string][]*base.SchemaProxy{
		"allOf": schema.AllOf, "oneOf": schema.OneOf, "anyOf": schema.AnyOf,
	} {
		if len(branches) == 0 {
			continue
		}
		converted := make([]any, 0, len(branches))
		for _, proxy := range branches {
			if proxy == nil {
				return domain.ReasonInvalidSchema
			}
			branch := proxy.Schema()
			if branch == nil {
				return domain.ReasonInvalidSchema
			}
			sub, reason := convertBodySchema(branch, depth+1, visiting)
			if reason != "" {
				return reason
			}
			converted = append(converted, map[string]any(sub))
		}
		out[keyword] = converted
	}
	return ""
}

func copyObject(schema *base.Schema, out domain.Schema, depth int, visiting map[*base.Schema]bool) domain.ReasonCode {
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
			converted, reason := convertBodySchema(property, depth+1, visiting)
			if reason != "" {
				return reason
			}
			properties[name] = map[string]any(converted)
		}
		out["properties"] = properties
	}
	if len(schema.Required) > 0 {
		required := append([]string(nil), schema.Required...)
		sort.Strings(required)
		out["required"] = anySlice(required)
	}
	if schema.AdditionalProperties != nil {
		switch {
		case schema.AdditionalProperties.IsB():
			out["additionalProperties"] = schema.AdditionalProperties.B
		case schema.AdditionalProperties.A != nil:
			nested := schema.AdditionalProperties.A.Schema()
			if nested == nil {
				return domain.ReasonInvalidSchema
			}
			converted, reason := convertBodySchema(nested, depth+1, visiting)
			if reason != "" {
				return reason
			}
			out["additionalProperties"] = map[string]any(converted)
		}
	}
	return ""
}
