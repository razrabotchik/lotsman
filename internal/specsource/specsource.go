package specsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
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
// back to the package defaults.
type Options struct {
	MaxBytes int64
	Timeout  time.Duration
}

// Source is the byte-level result of loading a spec. No parsing happens
// here: that is the openapi package's job (pipeline stage 1).
type Source struct {
	Bytes    []byte
	Origin   string // file path, or "stdin"
	Digest   string // sha256 of Bytes, hex-encoded
	LoadedAt time.Time
}

// Load reads path (or Stdin) into memory under byte and time limits and
// records its sha256 digest.
func Load(ctx context.Context, path string, opts Options) (*Source, error) {
	if path == "" {
		return nil, errors.New("specsource: empty path")
	}

	if path == Stdin {
		return load(ctx, "stdin", os.Stdin, opts)
	}

	// #nosec G304 -- path is an operator-supplied CLI/config argument, the
	// documented way to select a spec source (FR-3), not attacker input.
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("specsource: open %s: %w", path, err)
	}
	defer f.Close()

	return load(ctx, path, f, opts)
}

// load is the testable core of Load: it knows nothing about paths or files.
func load(ctx context.Context, origin string, r io.Reader, opts Options) (*Source, error) {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	data, err := readBounded(ctx, r, maxBytes)
	if err != nil {
		return nil, fmt.Errorf("specsource: read %s: %w", origin, err)
	}

	sum := sha256.Sum256(data)
	return &Source{
		Bytes:    data,
		Origin:   origin,
		Digest:   hex.EncodeToString(sum[:]),
		LoadedAt: time.Now().UTC(),
	}, nil
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
