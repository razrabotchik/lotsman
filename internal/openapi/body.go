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

// bodyFix is what the operator said about one operation's body.
type bodyFix struct {
	// mediaType is a validated pin, or "".
	mediaType string
	// patch is an RFC 7396 merge patch over the normalized body schema.
	patch domain.Schema
	// index is the operationOverrides entry, for messages.
	index int
}

// buildBody normalizes a request body into the IR: one deterministically
// chosen media type and a JSON Schema 2020-12 fragment to validate against.
func buildBody(rb *v3.RequestBody, fix bodyFix, pointer string, defs *bundle) bodyBuild {
	var out bodyBuild
	if rb == nil {
		return out
	}
	bodyPointer := pointer + "/requestBody"

	mediaType, content, reason := selectMediaType(rb.Content, fix.mediaType)
	if reason != "" {
		out.reject(reason, bodyPointer, fmt.Sprintf(
			"request body offers %s; lotsman sends %s, %s and a single-file body only",
			describeMediaTypes(rb.Content), jsonMediaType, multipartMediaType))
		return out
	}
	// A body made of file content is published as a file reference and
	// serialized by a different writer (feature 009), so it leaves the JSON
	// path here rather than threading a kind through it.
	switch bodyKindOf(mediaType) {
	case domain.BodyMultipart:
		return buildMultipartBody(rb, content, mediaType, bodyPointer, fix, defs)
	case domain.BodyBinary:
		return buildBinaryBody(rb, content, mediaType, bodyPointer, fix, defs)
	case domain.BodyJSON:
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
	if fix.patch != nil {
		patched := defs.mergePatch(converted, fix.patch, "", func(def, at string) {
			defs.fixes.problem("operationOverrides", fix.index,
				"requestBody.patch reaches through a reference to %s at %s; patch component %s instead",
				defs.componentName(def), at, defs.componentName(def))
		})
		if shapeReason := checkCorrectedShape(patched, bodyTarget); shapeReason != "" {
			out.spec = nil
			out.reject(shapeReason, bodyPointer, fmt.Sprintf(
				"after operationOverrides[%d].requestBody.patch the body schema has no shape lotsman can validate",
				fix.index))
			return out
		}
		out.spec.Schema = patched
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

// buildBinaryBody translates a body that is one file: the schema the caller
// is published is the file reference, whatever the document says the bytes
// look like, because "a path to a local file" is the only way content reaches
// lotsman (ADR-0022 decision 1).
func buildBinaryBody(rb *v3.RequestBody, content *v3.MediaType, mediaType, bodyPointer string, fix bodyFix, defs *bundle) bodyBuild {
	var out bodyBuild
	if patchRefused(fix, defs, mediaType) {
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
			"the %s body is published as a file input, which operationOverrides[%d].requestBody.patch cannot change",
			mediaType, fix.index))
		return out
	}
	if content.Schema != nil {
		schema := content.Schema.Schema()
		if !isBinarySchema(schema) && !isEmptySchema(schema) {
			out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
				"a %s body is sent as one file, but this one declares a schema with its own shape; "+
					"lotsman will not guess which part of it is the content", mediaType))
			return out
		}
	}
	out.spec = &domain.BodySpec{
		MediaType:   mediaType,
		Required:    rb.Required != nil && *rb.Required,
		Description: rb.Description,
		Schema:      fileInputSchema(),
		Kind:        domain.BodyBinary,
	}
	return out
}

// buildMultipartBody translates the one multipart shape feature 009 carries:
// an object with exactly one binary property and primitive properties beside
// it. Everything else is refused with the shape named -- a part serialization
// lotsman is not sure of would put bytes on the wire that the document did not
// describe (Constitution I).
func buildMultipartBody(rb *v3.RequestBody, content *v3.MediaType, mediaType, bodyPointer string, fix bodyFix, defs *bundle) bodyBuild {
	var out bodyBuild
	if patchRefused(fix, defs, mediaType) {
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
			"the %s body is published with a file input, which operationOverrides[%d].requestBody.patch cannot change",
			mediaType, fix.index))
		return out
	}
	if content.Schema == nil {
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer,
			"request body declares no schema, so no argument can be validated against it")
		return out
	}
	document := content.Schema.Schema()
	if document == nil {
		out.reject(domain.ReasonInvalidSchema, bodyPointer, bodyReasonText(domain.ReasonInvalidSchema))
		return out
	}

	// An empty property schema is the OAS 3.1 spelling of "this part is
	// whatever `encoding.contentType` says", and normalization refuses it
	// before this code could see it -- a schema that constrains nothing
	// cannot validate an argument (FR-23). Named here rather than left to the
	// generic refusal, because the remedy is one line of the document.
	for name, proxy := range document.Properties.FromOldest() {
		if proxy == nil {
			continue
		}
		encoding := encodingOf(content, name)
		if isEmptySchema(proxy.Schema()) && len(encodingContentTypes(encoding)) > 0 {
			out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
				"part %q has no schema and says what it is in encoding.contentType only; "+
					"lotsman needs the part's own schema to declare it (`type: string, format: binary` "+
					"in OAS 3.0, `contentMediaType` in 3.1)", name))
			return out
		}
	}

	converted, convertReason := defs.normalizeProxy(content.Schema, bodyTarget, 0, map[*base.Schema]bool{})
	if convertReason != "" {
		out.reject(convertReason, bodyPointer, bodyReasonText(convertReason))
		return out
	}
	// The published object is edited below, so a reference is inlined for this
	// body: a $defs entry is shared, and the file property is this
	// operation's business only.
	published, unavailable := defs.followRef(converted)
	if unavailable != "" {
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
			"the %s body references %s, which is not available to publish as a form",
			mediaType, defs.componentName(unavailable)))
		return out
	}
	properties, _ := published["properties"].(map[string]any)
	if typeOf, _ := published["type"].(string); typeOf != "object" || len(properties) == 0 {
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
			"a %s body is a set of named parts, and this one is not an object with properties", mediaType))
		return out
	}

	// Document order, not map order: part order decides the bytes on the wire
	// (Constitution IV), and the document is the only place an order is
	// written down.
	parts := make([]domain.BodyPart, 0, len(properties))
	fileField := ""
	for name, proxy := range document.Properties.FromOldest() {
		if proxy == nil {
			out.reject(domain.ReasonInvalidSchema, bodyPointer, bodyReasonText(domain.ReasonInvalidSchema))
			return out
		}
		property := proxy.Schema()
		if _, published := properties[name]; !published {
			// Normalization dropped it (a readOnly property); a part lotsman
			// does not publish is a part it must not send.
			continue
		}
		encoding := encodingOf(content, name)
		if keyword := unsupportedEncoding(encoding); keyword != "" {
			out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
				"part %q declares encoding.%s, which lotsman does not serialize for %s",
				name, keyword, mediaType))
			return out
		}
		contentTypes := encodingContentTypes(encoding)

		if isBinarySchema(property) {
			if fileField != "" {
				out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
					"parts %q and %q are both files; lotsman sends one file per call in this release",
					fileField, name))
				return out
			}
			fileField = name
			properties[name] = map[string]any(fileInputSchema())
			parts = append(parts, domain.BodyPart{Name: name, Binary: true, ContentTypes: contentTypes})
			continue
		}
		if reason := primitivePartRefusal(properties[name]); reason != "" {
			out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
				"part %q is %s; a %s part lotsman sends is a file or a single primitive value",
				name, reason, mediaType))
			return out
		}
		parts = append(parts, domain.BodyPart{Name: name, ContentTypes: contentTypes})
	}
	if fileField == "" {
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
			"no part of this %s body is a file; a form of text fields is form-urlencoded, which lotsman does not send yet",
			mediaType))
		return out
	}
	if len(parts) != len(properties) {
		// A correction added a property the document does not have, so the
		// part list and the published object would disagree about what is sent.
		out.reject(domain.ReasonUnsupportedBodySchema, bodyPointer, fmt.Sprintf(
			"the published %s body has %d properties and the document describes %d parts; "+
				"a corrected multipart body is not supported in this release",
			mediaType, len(properties), len(parts)))
		return out
	}

	out.spec = &domain.BodySpec{
		MediaType:   mediaType,
		Required:    rb.Required != nil && *rb.Required,
		Description: rb.Description,
		Schema:      published,
		Kind:        domain.BodyMultipart,
		Parts:       parts,
	}
	return out
}

// patchRefused reports a requestBody.patch on a body published as a file
// input. The patch language is JSON Merge Patch over the published schema, and
// the published schema here is lotsman's own file reference rather than the
// document's: a patch would be editing our wording, not the API's contract.
func patchRefused(fix bodyFix, defs *bundle, mediaType string) bool {
	if fix.patch == nil {
		return false
	}
	defs.fixes.problem("operationOverrides", fix.index,
		"requestBody.patch is set, but the %s body is published as a file input, which a patch cannot change",
		mediaType)
	return true
}

// primitivePartRefusal names why a published part schema is not a single
// primitive value, or "".
func primitivePartRefusal(published any) string {
	schema, ok := published.(map[string]any)
	if !ok {
		return "not a schema lotsman can read"
	}
	switch typed := schema["type"].(type) {
	case string:
		switch typed {
		case "string", "number", "integer", "boolean":
			return ""
		case "object":
			return "an object"
		case "array":
			return "an array"
		default:
			return "of type " + typed
		}
	case []any:
		return "of several types"
	default:
		return "without a single declared type"
	}
}

// selectMediaType picks the request media type deterministically (FR-29):
// exact application/json wins, then a single JSON-structured type such as
// application/merge-patch+json, then -- when it is the only thing offered or
// the operator pinned it -- a multipart or single-file body (feature 009).
// Anything else is refused: "pick the first one" is how a converter ends up
// sending a JSON body to a multipart endpoint.
func selectMediaType(content *orderedmap.Map[string, *v3.MediaType], pinned string) (string, *v3.MediaType, domain.ReasonCode) {
	if orderedmap.Len(content) == 0 {
		return "", nil, domain.ReasonUnsupportedMediaType
	}

	// A pin the operator stated wins (FR-29). It was checked against the
	// declared types and against what lotsman sends before it got here.
	if pinned != "" {
		for name, media := range content.FromOldest() {
			if normalizeMediaType(name) == pinned {
				return pinned, media, ""
			}
		}
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
			normalized := normalizeMediaType(name)
			if normalized == "*/*" || normalized == "application/*" {
				return jsonMediaType, media, ""
			}
			// A body made of file content, offered on its own: there is no
			// JSON alternative to prefer and nothing to choose between, which
			// is the only situation feature 009 picks one without being told.
			// Two file-ish types, or one beside an XML body, stay a refusal an
			// operator resolves with `mediaType`.
			if bodyKindOf(normalized) != domain.BodyJSON {
				return normalized, media, ""
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
