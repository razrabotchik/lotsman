package errs

import (
	"errors"
	"fmt"
	"testing"
)

var errSentinel = errors.New("sentinel")

func TestErrorfCarriesClassAndWrapping(t *testing.T) {
	err := Errorf(ClassSpecInvalid, "parse %s: %w", "spec.yaml", errSentinel)

	if got := ClassOf(err); got != ClassSpecInvalid {
		t.Errorf("ClassOf = %q, want %q", got, ClassSpecInvalid)
	}
	if !errors.Is(err, errSentinel) {
		t.Error("errors.Is lost the wrapped errSentinel")
	}
	if want := "parse spec.yaml: sentinel"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestWrapNilIsNil(t *testing.T) {
	if err := Wrap(ClassPolicy, nil); err != nil {
		t.Errorf("Wrap(nil) = %v, want nil", err)
	}
	if got := ClassOf(nil); got != "" {
		t.Errorf("ClassOf(nil) = %q, want empty", got)
	}
}

func TestClassSurvivesOuterWrapping(t *testing.T) {
	inner := Errorf(ClassAuth, "resolve secret")
	outer := fmt.Errorf("lotsman: %w", inner)

	if got := ClassOf(outer); got != ClassAuth {
		t.Errorf("ClassOf = %q, want %q: a plain fmt.Errorf wrap must not erase the class", got, ClassAuth)
	}
}

func TestOutermostClassWins(t *testing.T) {
	// A boundary re-stating the class in its own terms overrides the inner
	// one: an unreadable file is I/O, but at the spec-loading boundary the
	// operator's problem is an invalid spec source.
	inner := Errorf(ClassInternal, "read: %w", errSentinel)
	outer := Wrap(ClassSpecInvalid, inner)

	if got := ClassOf(outer); got != ClassSpecInvalid {
		t.Errorf("ClassOf = %q, want %q", got, ClassSpecInvalid)
	}
	if !errors.Is(outer, errSentinel) {
		t.Error("re-tagging broke the error chain")
	}
}

func TestUnclassifiedIsInternal(t *testing.T) {
	if got := ClassOf(errSentinel); got != ClassInternal {
		t.Errorf("ClassOf(plain error) = %q, want %q", got, ClassInternal)
	}
}

func TestClassFoundThroughJoin(t *testing.T) {
	joined := errors.Join(errSentinel, Errorf(ClassUnsupported, "rejected"))
	if got := ClassOf(joined); got != ClassUnsupported {
		t.Errorf("ClassOf(joined) = %q, want %q", got, ClassUnsupported)
	}
}

func TestHasClass(t *testing.T) {
	err := Errorf(ClassUpstream, "dial tcp: refused")
	if !HasClass(err, ClassUpstream) {
		t.Error("HasClass(ClassUpstream) = false, want true")
	}
	if HasClass(err, ClassPolicy) {
		t.Error("HasClass(ClassPolicy) = true, want false")
	}
	if HasClass(nil, ClassInternal) {
		t.Error("HasClass(nil) = true, want false")
	}
}
