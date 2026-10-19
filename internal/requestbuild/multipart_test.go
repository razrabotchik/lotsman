package requestbuild_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/fileinput"
	"github.com/razrabotchik/lotsman/internal/requestbuild"
)

// A one-pixel PNG, so the detected content type comes from the content.
var png = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89,
}

func fileFixture(t *testing.T, name string, content []byte) (string, *fileinput.Resolver) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := fileinput.New(fileinput.Limits{
		Roots:               []string{root},
		AllowedContentTypes: []string{"image/png"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return path, resolver
}

func TestAMultipartBodyStreamsTheFileAndDeclaresItsLength(t *testing.T) {
	path, resolver := fileFixture(t, "photo.png", png)

	op := requestbuild.Operation{
		Method:       http.MethodPost,
		PathTemplate: "/images",
		Body: &domain.BodySpec{
			MediaType: "multipart/form-data",
			Kind:      domain.BodyMultipart,
			Parts: []domain.BodyPart{
				{Name: "file", Binary: true, ContentTypes: []string{"image/png"}},
				{Name: "is_main"},
			},
		},
	}
	args := requestbuild.Arguments{domain.GroupBody: map[string]any{
		"file":    map[string]any{"path": path},
		"is_main": true,
	}}

	req, err := requestbuild.Build(context.Background(), &op, args,
		requestbuild.Options{BaseURL: "https://api.test", Files: resolver, Boundary: "fixedboundary"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer requestbuild.CloseBody(req)

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	// The declared length is the sent length: a PHP backend behind nginx
	// receives an empty $_FILES when a request arrives chunked instead.
	if req.ContentLength != int64(len(body)) {
		t.Fatalf("Content-Length %d, body %d bytes", req.ContentLength, len(body))
	}

	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] != "fixedboundary" {
		t.Fatalf("Content-Type %q (%v)", req.Header.Get("Content-Type"), err)
	}

	reader := multipart.NewReader(newByteReader(body), params["boundary"])
	first, err := reader.NextPart()
	if err != nil {
		t.Fatalf("first part: %v", err)
	}
	// Part order is the document's, and the file keeps its own name and the
	// type detected from its content.
	if first.FormName() != "file" || first.FileName() != "photo.png" ||
		first.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("part = %q %q %q", first.FormName(), first.FileName(), first.Header.Get("Content-Type"))
	}
	sent, err := io.ReadAll(first)
	if err != nil {
		t.Fatalf("read part: %v", err)
	}
	want := sha256.Sum256(png)
	got := sha256.Sum256(sent)
	if hex.EncodeToString(got[:]) != hex.EncodeToString(want[:]) {
		t.Fatal("the bytes that arrived are not the bytes on disk")
	}

	second, err := reader.NextPart()
	if err != nil {
		t.Fatalf("second part: %v", err)
	}
	value, _ := io.ReadAll(second)
	if second.FormName() != "is_main" || string(value) != "true" {
		t.Fatalf("part = %q %q", second.FormName(), value)
	}

	// The file is reported for the prompt and the audit record, with the hash
	// of what was streamed.
	files := requestbuild.Files(req)
	if len(files) != 1 || files[0].Name() != "photo.png" ||
		files[0].SHA256() != hex.EncodeToString(want[:]) {
		t.Fatalf("files = %+v", files)
	}
}

func TestASingleFileBodyIsTheFile(t *testing.T) {
	path, resolver := fileFixture(t, "thumb.png", png)

	op := requestbuild.Operation{
		Method:       http.MethodPut,
		PathTemplate: "/thumbnail",
		Body: &domain.BodySpec{
			MediaType: "application/octet-stream",
			Kind:      domain.BodyBinary,
		},
	}
	args := requestbuild.Arguments{domain.GroupBody: map[string]any{"path": path}}

	req, err := requestbuild.Build(context.Background(), &op, args,
		requestbuild.Options{BaseURL: "https://api.test", Files: resolver})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer requestbuild.CloseBody(req)

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if req.ContentLength != int64(len(png)) || len(body) != len(png) {
		t.Fatalf("Content-Length %d, body %d bytes", req.ContentLength, len(body))
	}
	if req.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("Content-Type %q", req.Header.Get("Content-Type"))
	}
}

func TestAPathOutsideTheRootsNeverBecomesARequest(t *testing.T) {
	_, resolver := fileFixture(t, "photo.png", png)
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, png, 0o600); err != nil {
		t.Fatal(err)
	}

	op := requestbuild.Operation{
		Method:       http.MethodPut,
		PathTemplate: "/thumbnail",
		Body:         &domain.BodySpec{MediaType: "application/octet-stream", Kind: domain.BodyBinary},
	}
	args := requestbuild.Arguments{domain.GroupBody: map[string]any{"path": outside}}

	req, err := requestbuild.Build(context.Background(), &op, args,
		requestbuild.Options{BaseURL: "https://api.test", Files: resolver})
	if err == nil {
		requestbuild.CloseBody(req)
		t.Fatal("a path outside every root was built into a request")
	}
	if got := fileinput.ReasonOf(err); got != domain.ReasonFileInputOutsideRoots {
		t.Fatalf("reason %q, error %v", got, err)
	}
}

// newByteReader is io.Reader over a slice without pulling in bytes in the
// test's own prose.
func newByteReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
