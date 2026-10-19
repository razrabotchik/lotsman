package openapi

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// The body shapes feature 009 deliberately does not carry. Each is refused
// with the shape named, because a part serialization lotsman is unsure of
// would put bytes on the wire the document did not describe (Constitution I).
func TestTheFileBodyShapesThatAreRefused(t *testing.T) {
	for _, tt := range []struct {
		name    string
		body    string
		reason  domain.ReasonCode
		mention string
	}{
		{
			name: "an array of files",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                files:
                  type: array
                  items: { type: string, format: binary }`,
			reason:  domain.ReasonUnsupportedBodySchema,
			mention: `part "files" is an array`,
		},
		{
			name: "two file parts",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                front: { type: string, format: binary }
                back: { type: string, format: binary }`,
			reason:  domain.ReasonUnsupportedBodySchema,
			mention: "are both files",
		},
		{
			name: "an object part",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }
                meta:
                  type: object
                  properties:
                    locale: { type: string }`,
			reason:  domain.ReasonUnsupportedBodySchema,
			mention: `part "meta" is an object`,
		},
		{
			name: "a part with a serialization style",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }
                tags: { type: string }
            encoding:
              tags: { style: form, explode: true }`,
			reason:  domain.ReasonUnsupportedBodySchema,
			mention: "encoding.style",
		},
		{
			name: "a part with its own headers",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }
            encoding:
              file:
                headers:
                  X-Checksum:
                    schema: { type: string }`,
			reason:  domain.ReasonUnsupportedBodySchema,
			mention: "encoding.headers",
		},
		{
			name: "a single-file body with a shape of its own",
			body: `        content:
          application/octet-stream:
            schema:
              type: object
              properties:
                blob: { type: string, format: binary }`,
			reason:  domain.ReasonUnsupportedBodySchema,
			mention: "sent as one file",
		},
		{
			name: "a part that says what it is in encoding only",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: {}
                caption: { type: string }
            encoding:
              file: { contentType: image/png }`,
			reason:  domain.ReasonUnsupportedBodySchema,
			mention: "encoding.contentType only",
		},
		{
			name: "two file-ish media types with nothing to choose between them",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }
          application/octet-stream:
            schema: { type: string, format: binary }`,
			reason:  domain.ReasonUnsupportedMediaType,
			mention: "multipart/form-data",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			op := parseOne(t, bodySpec(tt.body))
			if op.Support.Level != domain.SupportRejected {
				t.Fatalf("support = %+v", op.Support)
			}
			if !hasReason(op.Support.Reasons, tt.reason) {
				t.Fatalf("reasons = %v, want %s", op.Support.Reasons, tt.reason)
			}
			var found bool
			for _, d := range op.Diagnostics {
				if strings.Contains(d.Message, tt.mention) {
					found = true
				}
			}
			if !found {
				t.Fatalf("no diagnostic mentions %q: %+v", tt.mention, op.Diagnostics)
			}
		})
	}
}

// The spellings of "this part is a file" that are recognized, and the one that
// is not.
func TestTheSpellingsOfABinaryPart(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want domain.BodyKind
	}{
		{
			name: "OAS 3.0 format binary",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }`,
			want: domain.BodyMultipart,
		},
		{
			name: "OAS 3.1 contentMediaType",
			body: `        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, contentMediaType: image/png }`,
			want: domain.BodyMultipart,
		},
		{
			name: "a concrete image type as the whole body",
			body: `        content:
          image/png:
            schema: { type: string, format: binary }`,
			want: domain.BodyBinary,
		},
		{
			name: "no schema at all beside an octet-stream body",
			body: `        content:
          application/octet-stream: {}`,
			want: domain.BodyBinary,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			op := parseOne(t, bodySpec(tt.body))
			if op.Support.Level != domain.SupportSupported {
				t.Fatalf("support = %+v, diagnostics %+v", op.Support, op.Diagnostics)
			}
			if got := op.Input.Body.BodyKindOf(); got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

// A base64 string and a JSON body beside a multipart one: neither becomes a
// file reference. JSON still wins when both are offered (FR-29).
func TestJSONStillWinsOverMultipart(t *testing.T) {
	op := parseOne(t, bodySpec(`        content:
          application/json:
            schema:
              type: object
              properties:
                name: { type: string }
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }`))

	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v", op.Support)
	}
	if op.Input.Body.MediaType != jsonMediaType || op.Input.Body.BodyKindOf() != domain.BodyJSON {
		t.Fatalf("body = %+v", op.Input.Body)
	}
}

// An operator can pin the multipart body of an operation that offers both.
func TestAPinSelectsTheMultipartBody(t *testing.T) {
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: Pinned, version: "1.0" }
paths:
  /images:
    post:
      operationId: upload
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                url: { type: string }
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }
      responses:
        "201": { description: created }
`, Corrections{Operations: []OperationCorrection{{
		OperationID: "upload",
		MediaType:   multipartMediaType,
	}}})

	op := opByID(t, doc, "upload")
	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v", op.Support)
	}
	if op.Input.Body.BodyKindOf() != domain.BodyMultipart {
		t.Fatalf("body = %+v", op.Input.Body)
	}
	noProblems(t, doc)
}

// A patch cannot edit a body published as a file reference: the published
// schema is lotsman's wording, not the API's contract.
func TestAPatchOnAFileBodyIsAConfigurationProblem(t *testing.T) {
	doc := parseCorrected(t, `openapi: 3.0.3
info: { title: Patched, version: "1.0" }
paths:
  /images:
    post:
      operationId: upload
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              properties:
                file: { type: string, format: binary }
      responses:
        "201": { description: created }
`, Corrections{Operations: []OperationCorrection{{
		OperationID: "upload",
		BodyPatch:   domain.Schema{"required": []any{"file"}},
	}}})

	op := opByID(t, doc, "upload")
	if op.Support.Level != domain.SupportRejected {
		t.Fatalf("support = %+v", op.Support)
	}
	oneProblem(t, doc, "operationOverrides", 0, "published as a file input")
}
