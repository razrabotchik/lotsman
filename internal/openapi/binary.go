package openapi

import (
	"mime"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// The media types feature 009 sends besides JSON.
const (
	multipartMediaType   = "multipart/form-data"
	octetStreamMediaType = "application/octet-stream"
)

// singleFileTypes are the concrete media types a whole request body can be a
// file of. The list is short on purpose: a type here says "these bytes are the
// body", and lotsman has to be sure of that before it reads a file and streams
// it. Anything else -- including a wildcard range, which says the endpoint
// takes several things -- is `unsupported_media_type` and the operator can
// still pin a declared type they know is a file.
var singleFileTypes = map[string]bool{
	octetStreamMediaType: true,
	"application/pdf":    true,
	"application/zip":    true,
	"application/gzip":   true,
}

// singleFilePrefixes are the type families whose every subtype is bytes.
var singleFilePrefixes = []string{"image/", "audio/", "video/", "font/"}

// isSingleFileMediaType reports whether a body of this media type is one file.
func isSingleFileMediaType(name string) bool {
	if strings.Contains(name, "*") {
		return false
	}
	if singleFileTypes[name] {
		return true
	}
	for _, prefix := range singleFilePrefixes {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	return false
}

// bodyKindOf is how a body of this media type is serialized.
func bodyKindOf(mediaType string) domain.BodyKind {
	switch {
	case mediaType == multipartMediaType:
		return domain.BodyMultipart
	case isSingleFileMediaType(mediaType):
		return domain.BodyBinary
	default:
		return domain.BodyJSON
	}
}

// isBinarySchema reports whether a property holds file content rather than a
// value the caller writes.
//
// `format: byte` is deliberately not binary: it is a base64 string, small
// enough to be written by hand, and turning it into a file reference would
// take an ability away. OAS 3.1 spells a file as `contentMediaType` without
// `contentEncoding` -- with an encoding, the string carries the content again.
func isBinarySchema(schema *base.Schema) bool {
	if schema == nil {
		return false
	}
	if len(schema.Type) > 0 && !contains(schema.Type, "string") {
		return false
	}
	if schema.Format == "binary" {
		return true
	}
	return schema.ContentMediaType != "" && schema.ContentEncoding == ""
}

// isEmptySchema reports a schema that says nothing. For a single-file body
// that is fine -- the media type already says the body is bytes. For a
// multipart part it is the OAS 3.1 spelling that puts the whole statement in
// `encoding.contentType`, which lotsman refuses by name: a schema that
// constrains nothing cannot validate an argument (FR-23), and the remedy is
// one line of the document.
func isEmptySchema(schema *base.Schema) bool {
	if schema == nil {
		return true
	}
	return len(schema.Type) == 0 && schema.Format == "" &&
		schema.ContentMediaType == "" && schema.ContentEncoding == "" &&
		orderedmap.Len(schema.Properties) == 0 && schema.Items == nil &&
		len(schema.AllOf) == 0 && len(schema.OneOf) == 0 && len(schema.AnyOf) == 0
}

// encodingOf returns the encoding object declared for one part, or nil.
func encodingOf(content *v3.MediaType, name string) *v3.Encoding {
	if content == nil || orderedmap.Len(content.Encoding) == 0 {
		return nil
	}
	encoding, ok := content.Encoding.Get(name)
	if !ok {
		return nil
	}
	return encoding
}

// encodingContentTypes parses `encoding.contentType`, which OAS defines as a
// comma-separated list -- the reference operation of feature 009 declares
// "image/jpeg, image/png, image/webp" -- into the set a part's type must be
// in. An entry that does not parse is dropped: the document's mistake must not
// widen the set, and an empty set means only the operator's allowlist applies.
func encodingContentTypes(encoding *v3.Encoding) []string {
	if encoding == nil || strings.TrimSpace(encoding.ContentType) == "" {
		return nil
	}
	var out []string
	for _, raw := range strings.Split(encoding.ContentType, ",") {
		parsed, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		out = append(out, parsed)
	}
	return out
}

// unsupportedEncoding names the encoding keyword that makes a part's
// serialization something lotsman does not implement, or "".
//
// `style`/`explode` for multipart decide how an object or array becomes a
// part, which is exactly the shape feature 009 does not carry; `headers` lets
// a document add headers to a part, and a header lotsman has not implemented
// is not a header it should invent.
func unsupportedEncoding(encoding *v3.Encoding) string {
	switch {
	case encoding == nil:
		return ""
	case encoding.Style != "":
		return "style"
	case encoding.Explode != nil:
		return "explode"
	case encoding.AllowReserved:
		return "allowReserved"
	case orderedmap.Len(encoding.Headers) > 0:
		return "headers"
	default:
		return ""
	}
}

// fileInputSchema is how a file reaches lotsman: a path, never content
// (ADR-0022 decision 1). The wording is read by a model, so it says what to
// pass and what not to.
func fileInputSchema() domain.Schema {
	return domain.Schema{
		"type":                 "object",
		"required":             []any{"path"},
		"additionalProperties": false,
		"description": "A local file on the machine running lotsman. Pass the path; " +
			"do not read the file and do not pass its content.",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute path to the file, inside a directory the operator allowed.",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "Optional. The name sent to the API; defaults to the file's own name.",
			},
			"contentType": map[string]any{
				"type":        "string",
				"description": "Optional. Must match the type detected from the file's content.",
			},
		},
	}
}
