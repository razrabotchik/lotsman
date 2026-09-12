package specsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.yaml")
	content := []byte("openapi: 3.0.0\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := Load(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(src.Bytes, content) {
		t.Errorf("Bytes = %q, want %q", src.Bytes, content)
	}
	if src.Origin != path {
		t.Errorf("Origin = %q, want %q", src.Origin, path)
	}
	sum := sha256.Sum256(content)
	if want := "sha256:" + hex.EncodeToString(sum[:]); src.Digest != want {
		t.Errorf("Digest = %q, want %q", src.Digest, want)
	}
	if src.LoadedAt.IsZero() {
		t.Error("LoadedAt is zero")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), Options{})
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestLoadEmptyPath(t *testing.T) {
	_, err := Load(context.Background(), "", Options{})
	if err == nil {
		t.Fatal("want error for empty path")
	}
}

func TestLoadDeterministicDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(path, []byte("openapi: 3.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	a, err := Load(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Errorf("digests differ across loads: %q != %q", a.Digest, b.Digest)
	}
}

func TestLoadTooLarge(t *testing.T) {
	r := bytes.NewReader(bytes.Repeat([]byte("a"), 10))
	_, err := load(context.Background(), "test", "", r, Options{MaxBytes: 5})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestLoadExactlyMaxBytes(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 5)
	r := bytes.NewReader(data)
	src, err := load(context.Background(), "test", "", r, Options{MaxBytes: 5})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(src.Bytes) != 5 {
		t.Errorf("len(Bytes) = %d, want 5", len(src.Bytes))
	}
}

// blockingReader never returns, simulating a stdin source that withholds
// data (or an attacker stalling the connection).
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	select {}
}

func TestLoadTimeout(t *testing.T) {
	_, err := load(context.Background(), "test", "", blockingReader{}, Options{Timeout: 20 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

// closeBlockingReader blocks until Close is called, then reports EOF. It
// stands in for os.Stdin: readBounded must close it to unblock, not just
// abandon the goroutine.
type closeBlockingReader struct {
	closed chan struct{}
}

func newCloseBlockingReader() *closeBlockingReader {
	return &closeBlockingReader{closed: make(chan struct{})}
}

func (r *closeBlockingReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *closeBlockingReader) Close() error {
	close(r.closed)
	return nil
}

func TestLoadTimeoutClosesReader(t *testing.T) {
	r := newCloseBlockingReader()
	done := make(chan struct{})
	go func() {
		_, _ = load(context.Background(), "test", "", r, Options{Timeout: 20 * time.Millisecond})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("load did not return after timeout; reader goroutine leaked")
	}
}

func TestLoadStdin(t *testing.T) {
	content := []byte("openapi: 3.1.0\n")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	go func() {
		_, _ = w.Write(content)
		_ = w.Close()
	}()

	src, err := Load(context.Background(), Stdin, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(src.Bytes, content) {
		t.Errorf("Bytes = %q, want %q", src.Bytes, content)
	}
	if src.Origin != "stdin" {
		t.Errorf("Origin = %q, want %q", src.Origin, "stdin")
	}
}
