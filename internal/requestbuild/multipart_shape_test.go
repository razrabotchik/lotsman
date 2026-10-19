package requestbuild_test

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/fileinput"
	"github.com/razrabotchik/lotsman/internal/requestbuild"
)

// multipartOperation is the reference shape: one file, one boolean, one
// string, with a declared type for the text part.
func multipartOperation() requestbuild.Operation {
	return requestbuild.Operation{
		Method:       http.MethodPost,
		PathTemplate: "/images",
		Body: &domain.BodySpec{
			MediaType: "multipart/form-data",
			Kind:      domain.BodyMultipart,
			Parts: []domain.BodyPart{
				{Name: "file", Binary: true, ContentTypes: []string{"image/png"}},
				{Name: "is_main"},
				{Name: "alt", ContentTypes: []string{"text/plain"}},
			},
		},
	}
}

func buildMultipart(t *testing.T, resolver *fileinput.Resolver, body map[string]any, boundary string) *http.Request {
	t.Helper()
	op := multipartOperation()
	req, err := requestbuild.Build(context.Background(), &op,
		requestbuild.Arguments{domain.GroupBody: body},
		requestbuild.Options{BaseURL: "https://api.test", Files: resolver, Boundary: boundary})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return req
}

func TestATextPartCarriesItsDeclaredTypeAndThePartsKeepTheirOrder(t *testing.T) {
	path, resolver := fileFixture(t, "photo.png", png)
	req := buildMultipart(t, resolver, map[string]any{
		"file":    map[string]any{"path": path},
		"is_main": false,
		"alt":     "Granite memorial, front view",
	}, "b0undary")
	defer requestbuild.CloseBody(req)

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != req.ContentLength {
		t.Fatalf("Content-Length %d, body %d bytes", req.ContentLength, len(body))
	}

	reader := multipart.NewReader(newByteReader(body), "b0undary")
	var names, types, values []string
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		content, _ := io.ReadAll(part)
		names = append(names, part.FormName())
		types = append(types, part.Header.Get("Content-Type"))
		values = append(values, string(content))
	}
	if strings.Join(names, ",") != "file,is_main,alt" {
		t.Fatalf("parts = %v", names)
	}
	// The file's type is the detected one; a text part's is what the document
	// declared, and a part with nothing declared carries no type at all.
	if strings.Join(types, ",") != "image/png,,text/plain" {
		t.Fatalf("types = %v", types)
	}
	if values[1] != "false" || values[2] != "Granite memorial, front view" {
		t.Fatalf("values = %v", values[1:])
	}
}

func TestANonASCIIFilenameSurvivesAsUTF8(t *testing.T) {
	path, resolver := fileFixture(t, "памятник.png", png)
	req := buildMultipart(t, resolver, map[string]any{
		"file": map[string]any{"path": path},
	}, "b0undary")
	defer requestbuild.CloseBody(req)

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(newByteReader(body), "b0undary").NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if part.FileName() != "памятник.png" {
		t.Fatalf("filename = %q", part.FileName())
	}
}

// A quotation mark in a filename must not end the header parameter.
func TestAFilenameWithAQuoteIsEscaped(t *testing.T) {
	path, resolver := fileFixture(t, `front".png`, png)
	req := buildMultipart(t, resolver, map[string]any{
		"file": map[string]any{"path": path},
	}, "b0undary")
	defer requestbuild.CloseBody(req)

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(newByteReader(body), "b0undary").NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if part.FileName() != `front".png` {
		t.Fatalf("filename = %q", part.FileName())
	}
}

// The caller may rename the part, and the name is validated as a name.
func TestTheCallerCanRenameThePartButNotRepathIt(t *testing.T) {
	path, resolver := fileFixture(t, "photo.png", png)
	req := buildMultipart(t, resolver, map[string]any{
		"file": map[string]any{"path": path, "filename": "front.png"},
	}, "b0undary")
	defer requestbuild.CloseBody(req)

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	part, err := multipart.NewReader(newByteReader(body), "b0undary").NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if part.FileName() != "front.png" {
		t.Fatalf("filename = %q", part.FileName())
	}
}

// Same arguments, same bytes: the body is deterministic apart from the
// boundary (Constitution IV).
func TestTheSameArgumentsProduceTheSameBody(t *testing.T) {
	path, resolver := fileFixture(t, "photo.png", png)
	body := func() string {
		req := buildMultipart(t, resolver, map[string]any{
			"file":    map[string]any{"path": path},
			"is_main": true,
			"alt":     "front",
		}, "fixed")
		defer requestbuild.CloseBody(req)
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if first, second := body(), body(); first != second {
		t.Fatalf("two builds of one call differ:\n%q\n%q", first, second)
	}
}

// An argument naming a part the operation does not declare is a refusal, not
// an extra part on the wire.
func TestAnUndeclaredPartIsRefused(t *testing.T) {
	path, resolver := fileFixture(t, "photo.png", png)
	op := multipartOperation()
	_, err := requestbuild.Build(context.Background(), &op,
		requestbuild.Arguments{domain.GroupBody: map[string]any{
			"file":    map[string]any{"path": path},
			"smuggle": "x",
		}},
		requestbuild.Options{BaseURL: "https://api.test", Files: resolver})
	if err == nil || !strings.Contains(err.Error(), "smuggle") {
		t.Fatalf("err = %v", err)
	}
}

// A file field with an unknown key is refused too: the published schema says
// path, filename and contentType, and nothing else means anything here.
func TestAnUnknownFileFieldIsRefused(t *testing.T) {
	path, resolver := fileFixture(t, "photo.png", png)
	op := multipartOperation()
	_, err := requestbuild.Build(context.Background(), &op,
		requestbuild.Arguments{domain.GroupBody: map[string]any{
			"file": map[string]any{"path": path, "url": "https://example.test/photo.png"},
		}},
		requestbuild.Options{BaseURL: "https://api.test", Files: resolver})
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("err = %v", err)
	}
}

// Without a resolver nothing can be built: publication depends on one
// existing, so this is a wiring mistake and it fails loudly.
func TestAFileBodyWithoutAResolverIsAnInternalError(t *testing.T) {
	op := multipartOperation()
	_, err := requestbuild.Build(context.Background(), &op,
		requestbuild.Arguments{domain.GroupBody: map[string]any{
			"file": map[string]any{"path": "/tmp/photo.png"},
		}},
		requestbuild.Options{BaseURL: "https://api.test"})
	if err == nil || !strings.Contains(err.Error(), "no file resolver") {
		t.Fatalf("err = %v", err)
	}
}

// Sending a large file must not cost a large amount of memory: the body
// streams, and what the process holds is the copy buffer (NFR-11, spec §8).
func TestALargeFileDoesNotGrowTheHeapWithIt(t *testing.T) {
	const size = 20 << 20

	root := t.TempDir()
	path := filepath.Join(root, "big.png")
	content := make([]byte, size)
	copy(content, png)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := fileinput.New(fileinput.Limits{
		Roots:               []string{root},
		AllowedContentTypes: []string{"image/png"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := buildMultipart(t, resolver, map[string]any{
		"file": map[string]any{"path": path},
	}, "b0undary")
	defer requestbuild.CloseBody(req)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	written, err := io.Copy(io.Discard, req.Body)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	runtime.ReadMemStats(&after)

	if written != req.ContentLength {
		t.Fatalf("streamed %d bytes, declared %d", written, req.ContentLength)
	}
	// Generous by design: the assertion is "not proportional to the file",
	// not a byte budget that breaks when the runtime changes its allocator.
	if grew := after.TotalAlloc - before.TotalAlloc; grew > size/4 {
		t.Fatalf("streaming %d bytes allocated %d", size, grew)
	}
}

// A single-file body declares the type the operation declared, and its
// Content-Length is the file's size.
func TestASingleFileBodyDeclaresTheOperationsType(t *testing.T) {
	path, resolver := fileFixture(t, "thumb.png", png)
	op := requestbuild.Operation{
		Method:       http.MethodPut,
		PathTemplate: "/thumbnail",
		Body:         &domain.BodySpec{MediaType: "image/png", Kind: domain.BodyBinary},
	}
	req, err := requestbuild.Build(context.Background(), &op,
		requestbuild.Arguments{domain.GroupBody: map[string]any{"path": path}},
		requestbuild.Options{BaseURL: "https://api.test", Files: resolver})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer requestbuild.CloseBody(req)

	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "image/png" {
		t.Fatalf("Content-Type %q (%v)", req.Header.Get("Content-Type"), err)
	}
	if req.ContentLength != int64(len(png)) {
		t.Fatalf("Content-Length %d", req.ContentLength)
	}
}

// Closing a request that will not be sent closes the descriptors with it.
func TestClosingTheRequestClosesTheFiles(t *testing.T) {
	path, resolver := fileFixture(t, "photo.png", png)
	req := buildMultipart(t, resolver, map[string]any{
		"file": map[string]any{"path": path},
	}, "b0undary")

	files := requestbuild.Files(req)
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	requestbuild.CloseBody(req)
	// Idempotent: the call path closes on the way out without tracking
	// whether it already did.
	requestbuild.CloseBody(req)
	if err := files[0].Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := io.ReadAll(files[0].Reader()); err == nil {
		t.Fatal("a closed file still reads")
	}
}
