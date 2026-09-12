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

// bodyBuild is the verdict of normalizing one operation's request body.
type bodyBuild struct {
	spec        *domain.BodySpec
	diagnostics []domain.Diagnostic
	rejections  []domain.ReasonCode
}

// buildBody normalizes a request body into the IR: one deterministically
// chosen media type and a JSON Schema 2020-12 fragment to validate against.
func buildBody(rb *v3.RequestBody, pointer string, defs *bundle) bodyBuild {
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
	converted, convertReason := defs.normalizeProxy(content.Schema, bodyTarget, 0, map[*base.Schema]bool{})
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
