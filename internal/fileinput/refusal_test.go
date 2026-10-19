package fileinput_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/fileinput"
)

// Every refusal has its own code, and every one of them happens before a byte
// could have been sent (spec §6, Constitution I). One case per code, so a
// refusal a client cannot branch on cannot pass unnoticed.
func TestEveryRefusalHasItsOwnCode(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "photo.png")
	if err := os.WriteFile(good, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// A file inside the root that is text, for the allowlist case.
	text := filepath.Join(root, "notes.png")
	if err := os.WriteFile(text, []byte(strings.Repeat("plain text, not a png. ", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory, a FIFO and a symlink leaving the root.
	directory := filepath.Join(root, "album")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "pipe.png")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	elsewhere := t.TempDir()
	secret := filepath.Join(elsewhere, "id_rsa.png")
	if err := os.WriteFile(secret, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape.png")
	if err := os.Symlink(secret, escape); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(root, "big.png")
	if err := os.WriteFile(big, append(append([]byte(nil), pngBytes...), make([]byte, 4096)...), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name    string
		limits  fileinput.Limits
		request fileinput.Request
		want    domain.ReasonCode
	}{
		{"a relative path", fileinput.Limits{}, fileinput.Request{Path: "photo.png"}, domain.ReasonFileInputInvalidPath},
		{"an empty path", fileinput.Limits{}, fileinput.Request{Path: ""}, domain.ReasonFileInputInvalidPath},
		{"a tilde", fileinput.Limits{}, fileinput.Request{Path: "~/photo.png"}, domain.ReasonFileInputInvalidPath},
		{"a dot-dot segment", fileinput.Limits{}, fileinput.Request{Path: root + "/album/../photo.png"}, domain.ReasonFileInputInvalidPath},
		{"a NUL byte", fileinput.Limits{}, fileinput.Request{Path: root + "/photo\x00.png"}, domain.ReasonFileInputInvalidPath},
		{"a path outside every root", fileinput.Limits{}, fileinput.Request{Path: secret}, domain.ReasonFileInputOutsideRoots},
		{"a symlink leaving the root", fileinput.Limits{}, fileinput.Request{Path: escape}, domain.ReasonFileInputOutsideRoots},
		{"the process's own environment", fileinput.Limits{}, fileinput.Request{Path: "/proc/self/environ"}, domain.ReasonFileInputOutsideRoots},
		{"a directory", fileinput.Limits{}, fileinput.Request{Path: directory}, domain.ReasonFileInputNotRegularFile},
		{"a named pipe", fileinput.Limits{}, fileinput.Request{Path: fifo}, domain.ReasonFileInputNotRegularFile},
		{"a file that is not there", fileinput.Limits{}, fileinput.Request{Path: filepath.Join(root, "missing.png")}, domain.ReasonFileInputUnreadable},
		{"a file over the per-file limit", fileinput.Limits{MaxFileBytes: 64, MaxTotalBytes: 1 << 20}, fileinput.Request{Path: big}, domain.ReasonFileInputTooLarge},
		{"a type outside the allowlist", fileinput.Limits{}, fileinput.Request{Path: text}, domain.ReasonFileInputTypeNotAllowed},
		{"a type outside what the operation declares", fileinput.Limits{}, fileinput.Request{Path: good, PartContentTypes: []string{"image/jpeg"}}, domain.ReasonFileInputTypeNotAllowed},
		{"a contentType the content contradicts", fileinput.Limits{}, fileinput.Request{Path: good, ContentType: "image/jpeg"}, domain.ReasonFileInputTypeNotAllowed},
		{"a filename with a separator", fileinput.Limits{}, fileinput.Request{Path: good, Filename: "a/b.png"}, domain.ReasonFileInputInvalidFilename},
		{"a filename that is a parent", fileinput.Limits{}, fileinput.Request{Path: good, Filename: ".."}, domain.ReasonFileInputInvalidFilename},
		{"a filename with a newline", fileinput.Limits{}, fileinput.Request{Path: good, Filename: "a\nb.png"}, domain.ReasonFileInputInvalidFilename},
	} {
		t.Run(tt.name, func(t *testing.T) {
			limits := tt.limits
			limits.Roots = []string{root}
			limits.AllowedContentTypes = []string{"image/png"}
			resolver, err := fileinput.New(limits)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			session := resolver.Session()
			defer func() { _ = session.Close() }()

			file, err := session.Open(tt.request)
			if err == nil {
				_ = file.Close()
				t.Fatalf("%s was accepted", tt.name)
			}
			if got := fileinput.ReasonOf(err); got != tt.want {
				t.Fatalf("reason %q, want %q (error: %v)", got, tt.want, err)
			}
			// A refusal says nothing about the file's content.
			if strings.Contains(err.Error(), string(pngBytes[1:4])) {
				t.Fatalf("the refusal quotes the file's content: %v", err)
			}
		})
	}
}

// A file that grows between fstat and the read must fail the call: the body
// would no longer match the Content-Length the request declared.
func TestAFileThatGrowsAfterItIsMeasuredFailsTheCall(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "growing.png")
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	session := newResolver(t, root).Session()
	defer func() { _ = session.Close() }()
	file, err := session.Open(fileinput.Request{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Appended after the size the request was built around was taken.
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Write(make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	reader := file.Reader()
	buffer := make([]byte, 8)
	var readErr error
	for readErr == nil {
		_, readErr = reader.Read(buffer)
	}
	if got := fileinput.ReasonOf(readErr); got != domain.ReasonFileInputChanged {
		t.Fatalf("reason %q, error %v", got, readErr)
	}
}

// A file that shrank is the same problem from the other side.
func TestAFileThatShrankFailsTheCall(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shrinking.png")
	if err := os.WriteFile(path, append(append([]byte(nil), pngBytes...), make([]byte, 4096)...), 0o600); err != nil {
		t.Fatal(err)
	}

	session := newResolver(t, root).Session()
	defer func() { _ = session.Close() }()
	file, err := session.Open(fileinput.Request{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	reader := file.Reader()
	buffer := make([]byte, 1024)
	var readErr error
	for readErr == nil {
		_, readErr = reader.Read(buffer)
	}
	if got := fileinput.ReasonOf(readErr); got != domain.ReasonFileInputChanged {
		t.Fatalf("reason %q, error %v", got, readErr)
	}
}

// A configuration that would read more than its author meant to is refused at
// construction, where an operator can still see it.
func TestTheLimitsAreCheckedBeforeAnythingIsOpened(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		name   string
		limits fileinput.Limits
	}{
		{"no roots", fileinput.Limits{AllowedContentTypes: []string{"image/png"}}},
		{"no content types", fileinput.Limits{Roots: []string{root}}},
		{"a relative root", fileinput.Limits{Roots: []string{"photos"}, AllowedContentTypes: []string{"image/png"}}},
		{"the filesystem root", fileinput.Limits{Roots: []string{"/"}, AllowedContentTypes: []string{"image/png"}}},
		{"a root that is a file", fileinput.Limits{Roots: []string{writeFile(t, root, "not-a-dir")}, AllowedContentTypes: []string{"image/png"}}},
		{"a content type that is not one", fileinput.Limits{Roots: []string{root}, AllowedContentTypes: []string{"image"}}},
		{"a per-file limit above the per-call one", fileinput.Limits{
			Roots: []string{root}, AllowedContentTypes: []string{"image/png"},
			MaxFileBytes: 1 << 20, MaxTotalBytes: 1 << 10,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := fileinput.New(tt.limits); err == nil {
				t.Fatalf("%s was accepted", tt.name)
			}
		})
	}
}

func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// FuzzThePathArgument asserts the one property that matters whatever arrives
// in the argument: a file outside the roots is never opened (NFR-6).
func FuzzThePathArgument(f *testing.F) {
	root := f.TempDir()
	inside := filepath.Join(root, "photo.png")
	if err := os.WriteFile(inside, pngBytes, 0o600); err != nil {
		f.Fatal(err)
	}
	resolver, err := fileinput.New(fileinput.Limits{
		Roots:               []string{root},
		AllowedContentTypes: []string{"image/png"},
	})
	if err != nil {
		f.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		f.Fatal(err)
	}

	f.Add(inside)
	f.Add("/etc/passwd")
	f.Add(root + "/../../etc/passwd")
	f.Add("~/.ssh/id_rsa")
	f.Add("")
	f.Add("/proc/self/environ")
	f.Fuzz(func(t *testing.T, path string) {
		session := resolver.Session()
		defer func() { _ = session.Close() }()
		file, err := session.Open(fileinput.Request{Path: path})
		if err != nil {
			return
		}
		// Whatever was opened is inside the root, by its resolved path.
		opened, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatalf("a file was opened that cannot be resolved: %q", path)
		}
		if !strings.HasPrefix(opened, resolved+string(filepath.Separator)) {
			t.Fatalf("opened %q, which is outside %q", opened, resolved)
		}
		if file.Name() != filepath.Base(opened) {
			t.Fatalf("the part would be named %q for %q", file.Name(), opened)
		}
	})
}

// The per-call limit is about the call, not about one file: two files that
// each fit can still be more than a call may read.
func TestThePerCallLimitBoundsTheWholeCall(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "one.png")
	second := filepath.Join(root, "two.png")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	resolver, err := fileinput.New(fileinput.Limits{
		Roots:               []string{root},
		AllowedContentTypes: []string{"image/png"},
		MaxFileBytes:        int64(len(pngBytes)),
		MaxTotalBytes:       int64(len(pngBytes)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session := resolver.Session()
	defer func() { _ = session.Close() }()

	if _, opened := session.Open(fileinput.Request{Path: first}); opened != nil {
		t.Fatalf("the first file did not fit: %v", opened)
	}
	_, err = session.Open(fileinput.Request{Path: second})
	if got := fileinput.ReasonOf(err); got != domain.ReasonFileInputTotalTooLarge {
		t.Fatalf("reason %q, error %v", got, err)
	}
}
