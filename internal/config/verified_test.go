package config

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// A field with exactly one legal value still earns its place.
//
// The gain is not configurability, it is the message: an operator who writes
// `redirects: follow` believes redirects will be followed, and learning that
// from a refusal which names the rule is different from learning it from an
// unknown-field error, or worse, from a document that quietly resolves
// nothing.
func TestSettingsLotsmanCannotHonourAreRefusedByName(t *testing.T) {
	cases := []struct {
		name     string
		document string
		mentions string
	}{
		{
			name:     "a redirect lotsman would have to follow",
			document: "execution:\n  redirects: follow",
			mentions: "FR-33",
		},
		{
			name:     "a posture this build does not have",
			document: "execution:\n  defaultPolicy: allow-all",
			mentions: "allowMutations",
		},
		{
			name:     "remote references, which are never fetched",
			document: "spec:\n  remoteRefs: true",
			mentions: "FR-5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\n" + tc.document + "\n"))
			if err == nil {
				t.Fatal("the setting was accepted")
			}
			if errs.ClassOf(err) != errs.ClassUsage {
				t.Errorf("class = %s, want usage", errs.ClassOf(err))
			}
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("the refusal does not explain the rule (%q): %v", tc.mentions, err)
			}
		})
	}
}

// And the values lotsman does deliver are accepted, so the documented example
// is not refused for saying what is true.
func TestTheDeliverableValuesAreAccepted(t *testing.T) {
	if _, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
spec:
  remoteRefs: false
  strict: true
execution:
  defaultPolicy: read-only
  allowMutations: false
  redirects: deny
`)); err != nil {
		t.Fatalf("the documented values were refused: %v", err)
	}
}

// Two spellings of one setting must not disagree silently. lotsman does not
// pick one, and it does not invent a precedence an operator would have to
// learn.
func TestAContradictionIsRefusedRatherThanResolved(t *testing.T) {
	_, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
execution:
  defaultPolicy: read-only
  allowMutations: true
`))
	if err == nil {
		t.Fatal("a configuration that says both read-only and allowMutations was accepted")
	}
	if !strings.Contains(err.Error(), "contradict") {
		t.Errorf("the refusal does not name the problem: %v", err)
	}
}

// `spec.strict: false` is the file spelling of `--lax`, and the flag wins
// (FR-62).
func TestStrictIsTheFileSpellingOfLax(t *testing.T) {
	strict := &File{Spec: Spec{Strict: boolPtr(true)}}
	relaxed := &File{Spec: Spec{Strict: boolPtr(false)}}

	if Resolve(strict, Environment{}, Overrides{}).Lax {
		t.Error("strict: true resolved to lax")
	}
	if !Resolve(relaxed, Environment{}, Overrides{}).Lax {
		t.Error("strict: false did not resolve to lax")
	}
	// Said nothing: strict, which is the documented default.
	if Resolve(&File{}, Environment{}, Overrides{}).Lax {
		t.Error("an unset strict resolved to lax")
	}
	// And the flag wins over the file, in both directions.
	if !Resolve(strict, Environment{}, Overrides{Lax: boolPtr(true)}).Lax {
		t.Error("--lax did not win over strict: true")
	}
	if Resolve(relaxed, Environment{}, Overrides{Lax: boolPtr(false)}).Lax {
		t.Error("an explicit --lax=false did not win over strict: false")
	}
}

func boolPtr(b bool) *bool { return &b }
