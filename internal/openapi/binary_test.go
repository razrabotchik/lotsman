package openapi

import (
	"reflect"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

func TestAMultipartBodyIsPublishedAsAFileReference(t *testing.T) {
	op := parseOne(t, bodySpec(`        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              required: [file]
              properties:
                file: { type: string, format: binary }
                is_main: { type: boolean }
                alt: { type: string }
            encoding:
              file: { contentType: "image/jpeg, image/png" }`))

	if op.Support.Level != domain.SupportSupported {
		t.Fatalf("support = %+v", op.Support)
	}
	body := op.Input.Body
	if body == nil || body.Kind != domain.BodyMultipart || body.MediaType != "multipart/form-data" {
		t.Fatalf("body = %+v", body)
	}
	// Part order is the document's, because it decides the bytes on the wire.
	want := []domain.BodyPart{
		{Name: "file", Binary: true, ContentTypes: []string{"image/jpeg", "image/png"}},
		{Name: "is_main"},
		{Name: "alt"},
	}
	if !reflect.DeepEqual(body.Parts, want) {
		t.Fatalf("parts = %+v", body.Parts)
	}

	properties, _ := body.Schema["properties"].(map[string]any)
	file, _ := properties["file"].(map[string]any)
	if file["additionalProperties"] != false {
		t.Fatalf("the file property is published as %+v", file)
	}
	fileProps, _ := file["properties"].(map[string]any)
	if _, ok := fileProps["path"]; !ok {
		t.Fatalf("the file property has no path: %+v", file)
	}
	if format, ok := file["format"]; ok {
		t.Fatalf("the document's binary format reached the published schema (%v)", format)
	}
	if text, _ := properties["alt"].(map[string]any); text["type"] != "string" {
		t.Fatalf("a text part was changed: %+v", text)
	}
}

func TestASingleFileBodyIsPublishedAsAFileReference(t *testing.T) {
	op := parseOne(t, bodySpec(`        required: true
        content:
          application/octet-stream:
            schema: { type: string, format: binary }`))

	body := op.Input.Body
	if body == nil || body.Kind != domain.BodyBinary || body.MediaType != "application/octet-stream" {
		t.Fatalf("body = %+v", body)
	}
	if len(body.Parts) != 0 {
		t.Fatalf("a single-file body has no parts, got %+v", body.Parts)
	}
	properties, _ := body.Schema["properties"].(map[string]any)
	if _, ok := properties["path"]; !ok {
		t.Fatalf("schema = %+v", body.Schema)
	}
}

func TestABase64StringStaysAString(t *testing.T) {
	op := parseOne(t, bodySpec(`        required: true
        content:
          application/json:
            schema:
              type: object
              required: [image]
              properties:
                image: { type: string, format: byte }`))

	body := op.Input.Body
	if body == nil || body.BodyKindOf() != domain.BodyJSON {
		t.Fatalf("body = %+v", body)
	}
	properties, _ := body.Schema["properties"].(map[string]any)
	image, _ := properties["image"].(map[string]any)
	if image["type"] != "string" || image["format"] != "byte" {
		t.Fatalf("image = %+v", image)
	}
}
