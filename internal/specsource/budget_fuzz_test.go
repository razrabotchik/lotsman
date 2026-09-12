package specsource

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// FuzzLoadBudget pins the stage 0 contract for arbitrary input: whatever the
// bytes are, Load either returns a source or refuses in bounded time, and it
// never expands what it is measuring (pitfall #1).
func FuzzLoadBudget(f *testing.F) {
	for _, seed := range []string{
		"openapi: 3.0.0\n",
		`{"openapi":"3.0.0"}`,
		"",
		"a: &a [*a]\n",             // self-referential anchor
		"a: &a 1\nb: [*a, *a]\n",   // ordinary aliasing
		string(aliasBomb(4, 4)),    // small bomb
		string(aliasBomb(9, 9)),    // the classic
		strings.Repeat("[", 200),   // unbalanced nesting
		strings.Repeat("- ", 5000), // wide sequence
		"\x00\x01\x02",             // not text at all
		"key: [unterminated\n",     // malformed
		strings.Repeat("a: &x 1\nb: *x\n", 50),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, document string) {
		start := time.Now()
		src, err := load(context.Background(), "fuzz", "", strings.NewReader(document), Options{})
		elapsed := time.Since(start)

		// The point of measuring instead of expanding: no input of this size
		// may cost real time.
		if elapsed > 5*time.Second {
			t.Fatalf("stage 0 took %v for %d bytes", elapsed, len(document))
		}
		if err != nil {
			// Every refusal is classified, and a document refusal is never
			// reported as lotsman's own failure.
			switch {
			case errors.Is(err, ErrMalformed), errors.Is(err, ErrBudgetExceeded), errors.Is(err, ErrTooLarge):
				if got := errs.ClassOf(err); got != errs.ClassSpecInvalid {
					t.Fatalf("class = %q for %v", got, err)
				}
			default:
				t.Fatalf("unexpected error kind: %v", err)
			}
			return
		}
		if !bytes.Equal(src.Bytes, []byte(document)) {
			t.Fatal("Load changed the bytes it read")
		}
		if src.Digest == "" || !strings.HasPrefix(src.Digest, "sha256:") {
			t.Fatalf("digest = %q", src.Digest)
		}
	})
}
