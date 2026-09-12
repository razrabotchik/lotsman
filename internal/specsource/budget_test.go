package specsource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// aliasBomb builds the classic "billion laughs" YAML: each level references
// the previous one fan times, so the expanded size is fan^levels while the
// document itself stays a few hundred bytes.
func aliasBomb(levels, fan int) []byte {
	var b strings.Builder
	b.WriteString("lol0: &lol0 \"payload\"\n")
	for level := 1; level <= levels; level++ {
		fmt.Fprintf(&b, "lol%d: &lol%d [", level, level)
		for i := 0; i < fan; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "*lol%d", level-1)
		}
		b.WriteString("]\n")
	}
	return []byte(b.String())
}

func TestLoadRejectsAliasBomb(t *testing.T) {
	bomb := aliasBomb(9, 9) // 9^9 = 387,420,489 expanded nodes from ~400 bytes
	if len(bomb) > 1024 {
		t.Fatalf("bomb is %d bytes; the point is that the byte limit cannot catch it", len(bomb))
	}

	start := time.Now()
	_, err := load(context.Background(), "test", "", bytes.NewReader(bomb), Options{})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrBudgetExceeded", err)
	}
	if got := errs.ClassOf(err); got != errs.ClassSpecInvalid {
		t.Errorf("class = %q, want %q", got, errs.ClassSpecInvalid)
	}
	// Measuring the expansion must not cost what performing it would: if the
	// memoization ever regresses this test hangs instead of failing fast.
	if elapsed > 2*time.Second {
		t.Errorf("took %v to refuse a bomb; expansion is being paid, not measured", elapsed)
	}
}

func TestLoadAcceptsModestAliasing(t *testing.T) {
	// Anchors are legal and real specs use them; only expansion is budgeted.
	spec := []byte("defaults: &d {type: string}\na: *d\nb: *d\nc: *d\n")
	if _, err := load(context.Background(), "test", "", bytes.NewReader(spec), Options{}); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestBudgetLimits(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		opts Options
	}{
		{
			name: "node budget",
			doc:  "a: [1,2,3,4,5,6,7,8,9,10]",
			opts: Options{MaxNodes: 5},
		},
		{
			name: "alias budget",
			doc:  "a: &a 1\nb: [*a,*a,*a,*a]",
			opts: Options{MaxAliases: 2},
		},
		{
			name: "depth budget",
			doc:  "a: " + strings.Repeat("[", 20) + strings.Repeat("]", 20),
			opts: Options{MaxDepth: 5},
		},
		{
			name: "expansion budget",
			doc:  string(aliasBomb(5, 5)),
			opts: Options{MaxExpandedNodes: 100},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(context.Background(), "test", "", strings.NewReader(tt.doc), tt.opts)
			if !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("err = %v, want ErrBudgetExceeded", err)
			}
		})
	}
}

func TestLoadRejectsMalformedDocument(t *testing.T) {
	_, err := load(context.Background(), "test", "", strings.NewReader("key: [unterminated\n"), Options{})
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if got := errs.ClassOf(err); got != errs.ClassSpecInvalid {
		t.Errorf("class = %q, want %q", got, errs.ClassSpecInvalid)
	}
}

func TestLoadAcceptsJSON(t *testing.T) {
	// JSON is a YAML subset: the stage 0 budget must not reject it.
	doc := `{"openapi":"3.0.0","paths":{"/pets":{"get":{}}}}`
	if _, err := load(context.Background(), "test", "", strings.NewReader(doc), Options{}); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestLoadAcceptsEmptyDocument(t *testing.T) {
	// Empty bytes carry no expansion risk; rejecting them is stage 1's job,
	// with a message about OpenAPI rather than about budgets.
	if _, err := load(context.Background(), "test", "", strings.NewReader(""), Options{}); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestLoadRecordsRootPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(path, []byte("openapi: 3.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := Load(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if src.RootPath != want {
		t.Errorf("RootPath = %q, want %q", src.RootPath, want)
	}
}

func TestLoadStdinHasNoRootPath(t *testing.T) {
	// stdin has no location, so it can never authorize reading a sibling
	// file: an empty root is the refusal, not a missing value.
	src, err := load(context.Background(), "stdin", "", strings.NewReader("openapi: 3.0.0\n"), Options{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if src.RootPath != "" {
		t.Errorf("RootPath = %q, want empty", src.RootPath)
	}
}

func TestLoadErrorClasses(t *testing.T) {
	if got := errs.ClassOf(mustErr(Load(context.Background(), "", Options{}))); got != errs.ClassUsage {
		t.Errorf("empty path class = %q, want %q", got, errs.ClassUsage)
	}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	if got := errs.ClassOf(mustErr(Load(context.Background(), missing, Options{}))); got != errs.ClassInternal {
		t.Errorf("missing file class = %q, want %q", got, errs.ClassInternal)
	}
	tooLarge := mustErr(load(context.Background(), "test", "", strings.NewReader("aaaaaaaaaa"), Options{MaxBytes: 5}))
	if got := errs.ClassOf(tooLarge); got != errs.ClassSpecInvalid {
		t.Errorf("too large class = %q, want %q", got, errs.ClassSpecInvalid)
	}
	if !errors.Is(tooLarge, ErrTooLarge) {
		t.Error("re-classifying the read error broke errors.Is(ErrTooLarge)")
	}
}

func mustErr[T any](_ T, err error) error { return err }
