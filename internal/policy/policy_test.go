package policy

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

func TestDecideFromMethod(t *testing.T) {
	for _, tt := range []struct {
		method string
		want   domain.Effect
	}{
		{"GET", domain.EffectRead},
		{"HEAD", domain.EffectRead},
		{"OPTIONS", domain.EffectRead},
		{"DELETE", domain.EffectDestructive},
		{"POST", domain.EffectUnknown},
		{"PUT", domain.EffectUnknown},
		{"PATCH", domain.EffectUnknown},
		{"TRACE", domain.EffectUnknown},
	} {
		t.Run(tt.method, func(t *testing.T) {
			got := Decide(Candidate{Method: tt.method, PathTemplate: "/widgets", OperationID: "listWidgets"})
			if got.Effect != tt.want {
				t.Errorf("effect = %q, want %q", got.Effect, tt.want)
			}
			if got.Source != domain.EffectSourceHTTPMethod || got.Confidence != domain.ConfidenceInferred {
				t.Errorf("provenance = %+v, want an inferred http_method decision", got)
			}
		})
	}
}

// A POST is never assumed to be a mere write: the method cannot tell a draft
// save from a payment.
func TestPostIsUnknownNotWrite(t *testing.T) {
	if got := Decide(Candidate{Method: "POST", PathTemplate: "/charges"}).Effect; got != domain.EffectUnknown {
		t.Errorf("effect = %q, want unknown", got)
	}
}

func TestSuspiciousVerbRaisesReadToUnknown(t *testing.T) {
	for _, tt := range []struct {
		name        string
		candidate   Candidate
		wantEffect  domain.Effect
		wantWarning bool
	}{
		{
			name:        "path verb",
			candidate:   Candidate{Method: "GET", PathTemplate: "/legacy/rebuild-cache"},
			wantEffect:  domain.EffectUnknown,
			wantWarning: true,
		},
		{
			name:        "camelCase operationId",
			candidate:   Candidate{Method: "GET", OperationID: "triggerExport", PathTemplate: "/exports"},
			wantEffect:  domain.EffectUnknown,
			wantWarning: true,
		},
		{
			name:        "snake_case operationId",
			candidate:   Candidate{Method: "GET", OperationID: "send_invoice", PathTemplate: "/invoices"},
			wantEffect:  domain.EffectUnknown,
			wantWarning: true,
		},
		{
			// The classic false positive: a plural noun is not a verb.
			name:       "plural noun is not a verb",
			candidate:  Candidate{Method: "GET", OperationID: "listUpdates", PathTemplate: "/updates"},
			wantEffect: domain.EffectRead,
		},
		{
			// A verb inside a camelCase name is a token, so this one is
			// caught -- a false positive lotsman accepts, because the cost is
			// a refusal an override can lift, not a call it should not make.
			name:        "verb inside a camelCase name is a token",
			candidate:   Candidate{Method: "GET", OperationID: "getResetPasswordPolicy", PathTemplate: "/policies"},
			wantEffect:  domain.EffectUnknown,
			wantWarning: true,
		},
		{
			name:       "ordinary listing stays read",
			candidate:  Candidate{Method: "GET", OperationID: "listProjects", PathTemplate: "/projects/{id}/members"},
			wantEffect: domain.EffectRead,
		},
		{
			// Untrusted prose must not move policy.
			name:       "summary is not scanned",
			candidate:  Candidate{Method: "GET", OperationID: "listPets", PathTemplate: "/pets", Summary: "delete send charge"},
			wantEffect: domain.EffectRead,
		},
		{
			// A verb on an already-unknown method adds nothing.
			name:       "post stays unknown without a warning",
			candidate:  Candidate{Method: "POST", OperationID: "createPet", PathTemplate: "/pets"},
			wantEffect: domain.EffectUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.candidate)
			if got.Effect != tt.wantEffect {
				t.Errorf("effect = %q, want %q (warnings %v)", got.Effect, tt.wantEffect, got.Warnings)
			}
			if hasWarning := len(got.Warnings) > 0; hasWarning != tt.wantWarning {
				t.Errorf("warnings = %v, want any = %t", got.Warnings, tt.wantWarning)
			}
		})
	}
}

func TestSuspiciousWarningNamesTheOperationAndVerb(t *testing.T) {
	got := Decide(Candidate{Method: "GET", PathTemplate: "/legacy/rebuild-cache"})
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", got.Warnings)
	}
	for _, want := range []string{"GET", "/legacy/rebuild-cache", "rebuild", "unknown"} {
		if !strings.Contains(got.Warnings[0], want) {
			t.Errorf("warning %q does not mention %q", got.Warnings[0], want)
		}
	}
}

func TestGateReadOnlyByDefault(t *testing.T) {
	readOnly := Config{} // the zero value is the documented default

	if v := readOnly.Evaluate(domain.EffectDecision{Effect: domain.EffectRead}); !v.Allowed {
		t.Errorf("a read was blocked: %+v", v)
	}
	for _, tt := range []struct {
		effect domain.Effect
		reason domain.ReasonCode
	}{
		{domain.EffectWrite, domain.ReasonPolicyMutationBlocked},
		{domain.EffectDestructive, domain.ReasonPolicyMutationBlocked},
		{domain.EffectUnknown, domain.ReasonPolicyUnknownEffectBlocked},
	} {
		t.Run(string(tt.effect), func(t *testing.T) {
			v := readOnly.Evaluate(domain.EffectDecision{Effect: tt.effect})
			if v.Allowed {
				t.Fatalf("%s was allowed in read-only mode", tt.effect)
			}
			if v.Reason != tt.reason {
				t.Errorf("reason = %q, want %q", v.Reason, tt.reason)
			}
			if v.Message == "" {
				t.Error("a refusal must say what would change it")
			}
		})
	}
}

func TestGateAllowsMutationsWhenEnabled(t *testing.T) {
	enabled := Config{AllowMutations: true}
	for _, effect := range []domain.Effect{
		domain.EffectRead, domain.EffectWrite, domain.EffectDestructive, domain.EffectUnknown,
	} {
		if v := enabled.Evaluate(domain.EffectDecision{Effect: effect}); !v.Allowed {
			t.Errorf("%s blocked with mutations enabled: %+v", effect, v)
		}
	}
}

// FR-42/43: annotations are derived conservatively and are hints only.
func TestAnnotationsAreConservative(t *testing.T) {
	if got := AnnotationsFor(domain.EffectDecision{Effect: domain.EffectRead}); !got.ReadOnly || got.Destructive {
		t.Errorf("read annotations = %+v", got)
	}
	for _, effect := range []domain.Effect{domain.EffectWrite, domain.EffectDestructive, domain.EffectUnknown} {
		got := AnnotationsFor(domain.EffectDecision{Effect: effect})
		if got.ReadOnly || !got.Destructive {
			t.Errorf("%s annotations = %+v, want potentially destructive", effect, got)
		}
	}
}

// A zero EffectDecision means nobody classified the operation. That must fail
// closed: forgetting to classify cannot be cheaper than classifying.
func TestGateTreatsUnsetEffectAsUnknown(t *testing.T) {
	v := Config{}.Evaluate(domain.EffectDecision{})
	if v.Allowed {
		t.Fatal("an unclassified operation was allowed")
	}
	if v.Reason != domain.ReasonPolicyUnknownEffectBlocked {
		t.Errorf("reason = %q, want %q", v.Reason, domain.ReasonPolicyUnknownEffectBlocked)
	}
}
