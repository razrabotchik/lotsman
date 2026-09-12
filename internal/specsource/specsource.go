package specsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// Stdin is the pseudo-path selecting standard input as the source. Callers
// must opt into it explicitly; Load rejects an empty path rather than
// defaulting to stdin.
const Stdin = "-"

// Defaults for reading an untrusted OpenAPI document. Real-world specs (even
// large ones like Stripe's) run a few MB; ten times that is a hostile input,
// not a spec. The timeout bounds a stdin source that never closes.
const (
	DefaultMaxBytes = 10 << 20 // 10 MiB
	DefaultTimeout  = 5 * time.Second
)

// ErrTooLarge is returned when the source exceeds Options.MaxBytes.
var ErrTooLarge = errors.New("specsource: document exceeds max byte limit")

// Options bounds an untrusted spec read (pipeline stage 0). Zero values fall
// back to the package defaults, so Options{} is the safe configuration.
type Options struct {
	MaxBytes int64
	Timeout  time.Duration

	// Structural budgets applied after the bytes are in memory; see budget.go
	// for why a byte limit alone does not bound a YAML document.
	MaxNodes         int64
	MaxAliases       int64
	MaxExpandedNodes int64
	MaxDepth         int
}

func (o Options) maxBytes() int64 { return orDefault(o.MaxBytes, DefaultMaxBytes) }
func (o Options) maxNodes() int64 { return orDefault(o.MaxNodes, DefaultMaxNodes) }
func (o Options) maxAliases() int64 {
	return orDefault(o.MaxAliases, DefaultMaxAliases)
}
func (o Options) maxExpandedNodes() int64 {
	return orDefault(o.MaxExpandedNodes, DefaultMaxExpandedNodes)
}
func (o Options) maxDepth() int { return orDefault(o.MaxDepth, DefaultMaxDepth) }
func (o Options) timeout() time.Duration {
	return orDefault(o.Timeout, DefaultTimeout)
}

func orDefault[T int | int64 | time.Duration](v, fallback T) T {
	if v <= 0 {
		return fallback
	}
	return v
}

// Source is the byte-level result of loading a spec. No OpenAPI parsing
// happens here: that is the openapi package's job (pipeline stage 1).
type Source struct {
	Bytes    []byte
	Origin   string // file path, or "stdin"
	Digest   string // sha256 of Bytes, encoded as "sha256:<hex>"
	LoadedAt time.Time

	// RootPath is the absolute directory the document was read from, and the
	// confinement root for any future $ref resolution: a spec may not reach
	// outside it (pipeline.md stage 2). It is empty for stdin, which has no
	// location and therefore cannot authorize reading any file at all.
	RootPath string
}

// Load reads path (or Stdin) into memory under byte, time and structural
// limits and records its sha256 digest.
func Load(ctx context.Context, path string, opts Options) (*Source, error) {
	if path == "" {
		return nil, errs.Errorf(errs.ClassUsage, "specsource: empty path")
	}

	if path == Stdin {
		return load(ctx, "stdin", "", os.Stdin, opts)
	}

	// #nosec G304 -- path is an operator-supplied CLI/config argument, the
	// documented way to select a spec source (FR-3), not attacker input.
	f, err := os.Open(path)
	if err != nil {
		return nil, errs.Errorf(errs.ClassInternal, "specsource: open %s: %w", path, err)
	}
	defer f.Close()

	// Resolved before the read so the confinement root is the real directory
	// even when path is relative or reached through a symlink; a spec that
	// escapes via ".." must fail the comparison, not redefine the root.
	root, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, errs.Errorf(errs.ClassInternal, "specsource: resolve root of %s: %w", path, err)
	}
	if resolved, linkErr := filepath.EvalSymlinks(root); linkErr == nil {
		root = resolved
	}

	return load(ctx, path, root, f, opts)
}

// load is the testable core of Load: it knows nothing about paths or files.
func load(ctx context.Context, origin, rootPath string, r io.Reader, opts Options) (*Source, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()

	data, err := readBounded(ctx, r, opts.maxBytes())
	if err != nil {
		return nil, errs.Wrap(readClass(err), errs.Errorf(errs.ClassInternal, "specsource: read %s: %w", origin, err))
	}

	if err := checkBudget(data, opts); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(data)
	return &Source{
		Bytes:    data,
		Origin:   origin,
		Digest:   "sha256:" + hex.EncodeToString(sum[:]),
		LoadedAt: time.Now().UTC(),
		RootPath: rootPath,
	}, nil
}

// readClass separates "the document is hostile" from "reading failed". A
// source that overruns the byte limit or stalls past the deadline is not an
// I/O problem the operator can retry: it is an input we refuse.
func readClass(err error) errs.Class {
	if errors.Is(err, ErrTooLarge) || errors.Is(err, context.DeadlineExceeded) {
		return errs.ClassSpecInvalid
	}
	return errs.ClassInternal
}

// readBounded reads at most maxBytes+1 bytes from r, cancellable via ctx.
// Reading one byte past the limit is what tells "exactly maxBytes" and "too
// large" apart without buffering the excess.
//
// r is closed on cancellation when it implements io.Closer, which unblocks
// the background read so it cannot leak; this is why Load always hands in a
// fresh *os.File or os.Stdin rather than a shared reader. Without a Closer
// there is no way to interrupt a blocked Read, so the goroutine is
// abandoned and readBounded returns ctx.Err() immediately regardless.
func readBounded(ctx context.Context, r io.Reader, maxBytes int64) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
		done <- result{data, err}
	}()

	select {
	case <-ctx.Done():
		if c, ok := r.(io.Closer); ok {
			_ = c.Close()
			<-done // wait for the goroutine to unblock so it never leaks
		}
		return nil, ctx.Err()
	case res := <-done:
		if res.err != nil {
			return nil, res.err
		}
		if int64(len(res.data)) > maxBytes {
			return nil, ErrTooLarge
		}
		return res.data, nil
	}
}
