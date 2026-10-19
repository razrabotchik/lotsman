package fileinput

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// Defaults for the limits an operator may leave unstated (docs/spec.md §5.1).
// They bound memory and the time a body spends on the wire, not what an API
// happens to accept: a backend with a 2 MiB upload cap refuses the request
// itself, and that refusal is its answer to report, not ours to predict.
const (
	DefaultMaxFileBytes  int64 = 20 << 20
	DefaultMaxTotalBytes int64 = 100 << 20
)

// sniffBytes is how much of a file http.DetectContentType reads. The sniffer
// looks at no more than this, so reading more would only be reading more.
const sniffBytes = 512

// copyBufferBytes is the window a file travels through on its way to the
// socket. The point of streaming is that this number, and not the file's size,
// is what the process holds.
const copyBufferBytes = 32 << 10

// Limits is what the operator allowed: which directories may be read, how
// much may be read, and which content types may be sent.
type Limits struct {
	// Roots are the only directories a file may come from. Required: an
	// enabled file-input configuration with no roots is not a default to
	// resolve, and the configuration layer refuses it before this.
	Roots []string
	// MaxFileBytes bounds one file, MaxTotalBytes one call. Zero means the
	// documented default.
	MaxFileBytes  int64
	MaxTotalBytes int64
	// AllowedContentTypes is the set a file's *detected* type must be in.
	// Required, for the same reason as Roots.
	AllowedContentTypes []string
}

// Resolver opens files within one set of limits. It is safe for concurrent
// use: it holds no per-call state, which lives in a Session.
type Resolver struct {
	roots    []string
	maxFile  int64
	maxTotal int64
	allowed  map[string]bool
}

// New resolves the roots once, at startup, so that a call never pays for it
// and a misconfigured root is a startup error rather than a surprise during
// the first upload.
func New(limits Limits) (*Resolver, error) {
	if len(limits.Roots) == 0 {
		return nil, fmt.Errorf("file inputs need at least one root directory")
	}
	if len(limits.AllowedContentTypes) == 0 {
		return nil, fmt.Errorf("file inputs need at least one allowed content type")
	}
	r := &Resolver{
		maxFile:  limits.MaxFileBytes,
		maxTotal: limits.MaxTotalBytes,
		allowed:  make(map[string]bool, len(limits.AllowedContentTypes)),
	}
	if r.maxFile <= 0 {
		r.maxFile = DefaultMaxFileBytes
	}
	if r.maxTotal <= 0 {
		r.maxTotal = DefaultMaxTotalBytes
	}
	if r.maxFile > r.maxTotal {
		return nil, fmt.Errorf("maxFileBytes (%d) is above maxTotalBytes (%d)", r.maxFile, r.maxTotal)
	}
	for _, root := range limits.Roots {
		resolved, err := resolveRoot(root)
		if err != nil {
			return nil, err
		}
		r.roots = append(r.roots, resolved)
	}
	sort.Strings(r.roots)
	for _, raw := range limits.AllowedContentTypes {
		parsed, params, err := mime.ParseMediaType(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("allowed content type %q does not parse: %w", raw, err)
		}
		if len(params) > 0 {
			return nil, fmt.Errorf("allowed content type %q carries parameters; name the type alone", raw)
		}
		r.allowed[parsed] = true
	}
	return r, nil
}

// Roots is what an operator configured, for a report or a warning to name.
// The paths are the resolved ones, which is what actually bounds a read.
func (r *Resolver) Roots() []string {
	out := make([]string, len(r.roots))
	copy(out, r.roots)
	return out
}

// MaxFileBytes and MaxTotalBytes report the limits in force, for the same
// reason.
func (r *Resolver) MaxFileBytes() int64  { return r.maxFile }
func (r *Resolver) MaxTotalBytes() int64 { return r.maxTotal }

// AllowedContentTypes is the sorted allowlist, for a report.
func (r *Resolver) AllowedContentTypes() []string {
	out := make([]string, 0, len(r.allowed))
	for name := range r.allowed {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func resolveRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("a file-input root may not be empty")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("file-input root %q is not an absolute path", root)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("file-input root %q cannot be resolved: %w", root, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("file-input root %q cannot be resolved: %w", root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("file-input root %q cannot be read: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("file-input root %q is not a directory", root)
	}
	if resolved == string(filepath.Separator) || filepath.Dir(resolved) == resolved {
		return "", fmt.Errorf("file-input root %q is the filesystem root; name the directory the files live in", root)
	}
	return resolved, nil
}

// Request is one file argument as the caller wrote it, plus what the
// operation says about the part it belongs to.
type Request struct {
	// Path, Filename and ContentType are the caller's arguments. All three are
	// untrusted; Path is the only required one.
	Path        string
	Filename    string
	ContentType string
	// PartContentTypes is the operation's `encoding.contentType` set for this
	// part, already parsed from its comma-separated form. Empty means the
	// document said nothing, and only the operator's allowlist applies.
	PartContentTypes []string
}

// Session is the file budget of one call. Files opened through it are closed
// together, because the call either sends all of them or none.
type Session struct {
	resolver *Resolver
	mu       sync.Mutex
	total    int64
	files    []*File
	closed   bool
}

// Session starts one call's budget.
func (r *Resolver) Session() *Session { return &Session{resolver: r} }

// Open validates and opens one file argument. Every failure path closes
// whatever it opened, so a refusal never leaks a descriptor.
func (s *Session) Open(req Request) (*File, error) {
	path, err := checkPath(req.Path)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, refuse(domain.ReasonFileInputUnreadable, req.Path, errs.ClassUsage,
				"no such file")
		}
		return nil, refuse(domain.ReasonFileInputUnreadable, req.Path, errs.ClassUsage,
			"the path cannot be resolved")
	}
	// Containment is checked on the resolved path: a symlink inside a root
	// that points out of it is the whole reason the check happens here and
	// not on the argument.
	if !s.resolver.contains(resolved) {
		return nil, refuse(domain.ReasonFileInputOutsideRoots, req.Path, errs.ClassPolicy,
			"the file is outside every configured root (%s)", strings.Join(s.resolver.roots, ", "))
	}

	file, size, err := openRegular(resolved, req.Path)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()

	if size > s.resolver.maxFile {
		return nil, refuse(domain.ReasonFileInputTooLarge, req.Path, errs.ClassPolicy,
			"the file is %d bytes, over the %d-byte limit", size, s.resolver.maxFile)
	}

	contentType, err := detectContentType(file, req.Path)
	if err != nil {
		return nil, err
	}
	if !s.resolver.allowed[contentType] {
		return nil, refuse(domain.ReasonFileInputTypeNotAllowed, req.Path, errs.ClassPolicy,
			"the content is %s, which is not in execution.fileInputs.allowedContentTypes (%s)",
			contentType, strings.Join(s.resolver.AllowedContentTypes(), ", "))
	}
	if len(req.PartContentTypes) > 0 && !containsType(req.PartContentTypes, contentType) {
		return nil, refuse(domain.ReasonFileInputTypeNotAllowed, req.Path, errs.ClassUsage,
			"the content is %s; this operation declares %s for this part",
			contentType, strings.Join(req.PartContentTypes, ", "))
	}
	// A client-supplied type is a claim about the bytes, and the bytes are
	// right there: a mismatch is a refusal rather than an override, because
	// sending the claim would make lotsman the one lying to the API.
	if req.ContentType != "" {
		claimed, _, parseErr := mime.ParseMediaType(strings.TrimSpace(req.ContentType))
		if parseErr != nil {
			return nil, refuse(domain.ReasonFileInputTypeNotAllowed, req.Path, errs.ClassUsage,
				"contentType %q does not parse", req.ContentType)
		}
		if claimed != contentType {
			return nil, refuse(domain.ReasonFileInputTypeNotAllowed, req.Path, errs.ClassUsage,
				"contentType says %s, the file's content is %s", claimed, contentType)
		}
	}

	name, err := partFilename(req, resolved)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errs.Errorf(errs.ClassInternal, "fileinput: the call's file session is already closed")
	}
	if s.total+size > s.resolver.maxTotal {
		return nil, refuse(domain.ReasonFileInputTotalTooLarge, req.Path, errs.ClassPolicy,
			"this call would read %d bytes, over the %d-byte per-call limit", s.total+size, s.resolver.maxTotal)
	}
	s.total += size

	out := &File{
		file:        file,
		name:        name,
		size:        size,
		contentType: contentType,
		argument:    req.Path,
		digest:      sha256.New(),
	}
	s.files = append(s.files, out)
	ok = true
	return out, nil
}

// Files is what this call resolved, in the order it opened them: what the
// approval prompt describes and the audit event records.
func (s *Session) Files() []*File {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*File, len(s.files))
	copy(out, s.files)
	return out
}

// Total is how many bytes this call has opened.
func (s *Session) Total() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

// Close closes every descriptor the call opened. It is idempotent, so the
// call path can close on the way out without tracking whether it already did.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	for _, f := range s.files {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

func (r *Resolver) contains(path string) bool {
	for _, root := range r.roots {
		if within(root, path) {
			return true
		}
	}
	return false
}

// within compares path components rather than strings: "/srv/photos-private"
// has "/srv/photos" as a string prefix and is not inside it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkPath refuses an argument no filesystem should be consulted about: the
// cheapest refusals, and the ones a prompt injection reaches for first.
func checkPath(raw string) (string, error) {
	if raw == "" {
		return "", refuse(domain.ReasonFileInputInvalidPath, raw, errs.ClassUsage,
			"the path is empty")
	}
	if strings.ContainsRune(raw, 0) {
		return "", refuse(domain.ReasonFileInputInvalidPath, "", errs.ClassUsage,
			"the path contains a NUL byte")
	}
	if strings.HasPrefix(raw, "~") {
		return "", refuse(domain.ReasonFileInputInvalidPath, raw, errs.ClassUsage,
			"`~` is not expanded; pass the absolute path")
	}
	if !filepath.IsAbs(raw) {
		return "", refuse(domain.ReasonFileInputInvalidPath, raw, errs.ClassUsage,
			"the path is not absolute")
	}
	if filepath.Clean(raw) != raw {
		return "", refuse(domain.ReasonFileInputInvalidPath, raw, errs.ClassUsage,
			"the path is not in canonical form; `.` and `..` segments are not resolved for you")
	}
	return raw, nil
}

// partFilename decides the name the API is told, which is a name and never a
// path: a separator here would let an argument choose where an API writes.
func partFilename(req Request, resolved string) (string, error) {
	name := req.Filename
	if name == "" {
		return filepath.Base(resolved), nil
	}
	if name != filepath.Base(name) || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return "", refuse(domain.ReasonFileInputInvalidFilename, req.Path, errs.ClassUsage,
			"filename %q is not a bare file name", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", refuse(domain.ReasonFileInputInvalidFilename, req.Path, errs.ClassUsage,
				"filename %q contains a control character", name)
		}
	}
	return name, nil
}

// detectContentType types the file by its content and rewinds. The extension
// is never consulted: it is part of the same untrusted argument as the path.
func detectContentType(file *os.File, argument string) (string, error) {
	head := make([]byte, sniffBytes)
	n, err := file.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
			"the file could not be read")
	}
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		return "", refuse(domain.ReasonFileInputUnreadable, argument, errs.ClassUsage,
			"the file could not be rewound after sniffing its type")
	}
	detected, _, err := mime.ParseMediaType(http.DetectContentType(head[:n]))
	if err != nil {
		return "", errs.Errorf(errs.ClassInternal, "fileinput: detected content type does not parse: %w", err)
	}
	return detected, nil
}

func containsType(set []string, want string) bool {
	for _, candidate := range set {
		if strings.EqualFold(strings.TrimSpace(candidate), want) {
			return true
		}
	}
	return false
}

// File is an opened, measured, typed file waiting to be streamed.
type File struct {
	file        *os.File
	name        string
	size        int64
	contentType string
	// argument is the path the caller wrote. It is reported back to the
	// caller, who authored it, and never logged (ADR-0022 decision 8).
	argument string
	digest   hash.Hash
	read     int64
	closed   bool
}

// Name is the file name sent to the API; Size is the length the request was
// built around; ContentType is what the content turned out to be.
func (f *File) Name() string        { return f.name }
func (f *File) Size() int64         { return f.size }
func (f *File) ContentType() string { return f.contentType }

// Argument is the caller's own path string, for a message addressed to the
// caller.
func (f *File) Argument() string { return f.argument }

// SHA256 is the hash of what was actually sent, and is empty until the body
// has been streamed: hashing before the send would mean either reading the
// file twice -- and the second read may differ -- or holding it in memory
// (ADR-0022 decision 7).
func (f *File) SHA256() string {
	if f.read != f.size {
		return ""
	}
	return hex.EncodeToString(f.digest.Sum(nil))
}

// Reader streams the file's bytes once, hashing them on the way out, and
// fails if the file no longer delivers exactly the size the request declared:
// a Content-Length that does not match its body is a corrupt upload the API
// would have to guess about.
func (f *File) Reader() io.Reader { return &fileReader{file: f} }

// Close releases the descriptor. Idempotent.
func (f *File) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return f.file.Close()
}

type fileReader struct {
	file *File
	done bool
}

func (r *fileReader) Read(p []byte) (int, error) {
	f := r.file
	if r.done {
		return 0, io.EOF
	}
	remaining := f.size - f.read
	if remaining <= 0 {
		// One byte past the declared size is the check: a file that grew is
		// not a file this request can send.
		var probe [1]byte
		if n, err := f.file.Read(probe[:]); n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			r.done = true
			return 0, refuse(domain.ReasonFileInputChanged, f.argument, errs.ClassUsage,
				"the file grew past the %d bytes this request was built around", f.size)
		}
		r.done = true
		return 0, io.EOF
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := f.file.Read(p)
	if n > 0 {
		f.read += int64(n)
		if _, hashErr := f.digest.Write(p[:n]); hashErr != nil {
			return n, errs.Errorf(errs.ClassInternal, "fileinput: hashing failed: %w", hashErr)
		}
	}
	switch {
	case err == nil:
		return n, nil
	case errors.Is(err, io.EOF) && f.read < f.size:
		r.done = true
		return n, refuse(domain.ReasonFileInputChanged, f.argument, errs.ClassUsage,
			"the file shrank to %d bytes; this request was built around %d", f.read, f.size)
	case errors.Is(err, io.EOF):
		return n, nil
	default:
		return n, refuse(domain.ReasonFileInputUnreadable, f.argument, errs.ClassUsage,
			"the file could not be read to the end")
	}
}

// CopyBuffer is the buffer a caller should stream through, so that the one
// number the process holds is this one and not the file's size.
func CopyBuffer() []byte { return make([]byte, copyBufferBytes) }
