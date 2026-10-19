package fileinput_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/fileinput"
)

// pngBytes is a one-pixel PNG: http.DetectContentType types it by its magic
// number, which is the point -- the extension is never consulted.
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89,
}

func newResolver(t *testing.T, root string) *fileinput.Resolver {
	t.Helper()
	resolver, err := fileinput.New(fileinput.Limits{
		Roots:               []string{root},
		AllowedContentTypes: []string{"image/png"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return resolver
}

func TestAFileInsideARootIsStreamedAndHashed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "photo.png")
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	session := newResolver(t, root).Session()
	defer func() { _ = session.Close() }()

	file, err := session.Open(fileinput.Request{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if file.Name() != "photo.png" || file.ContentType() != "image/png" || file.Size() != int64(len(pngBytes)) {
		t.Fatalf("got %q %q %d", file.Name(), file.ContentType(), file.Size())
	}
	if file.SHA256() != "" {
		t.Fatal("a hash before the body was streamed describes something that was not sent")
	}

	sent, err := io.ReadAll(file.Reader())
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if string(sent) != string(pngBytes) {
		t.Fatal("the bytes streamed are not the bytes on disk")
	}
	want := sha256.Sum256(pngBytes)
	if file.SHA256() != hex.EncodeToString(want[:]) {
		t.Fatalf("hash %q", file.SHA256())
	}
}

func TestAPathOutsideEveryRootIsRefused(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	session := newResolver(t, root).Session()
	defer func() { _ = session.Close() }()

	_, err := session.Open(fileinput.Request{Path: outside})
	if got := fileinput.ReasonOf(err); got != domain.ReasonFileInputOutsideRoots {
		t.Fatalf("reason %q, error %v", got, err)
	}
}
