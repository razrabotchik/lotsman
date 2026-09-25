package catalog

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// longSummary is a summary no budget would pass whole.
func longSummary() string { return strings.Repeat("описание ", 500) }

func budgetOperation() domain.Operation {
	return domain.Operation{
		Key:          domain.NewOperationKey("default", "GET", "/pets"),
		Method:       "GET",
		PathTemplate: "/pets",
		Summary:      longSummary(),
		Servers:      []string{"https://api.example.com"},
		Effect: domain.EffectDecision{
			Effect: domain.EffectRead, Source: domain.EffectSourceHTTPMethod,
			Confidence: domain.ConfidenceInferred,
		},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}
}

// A zero budget means the documented default, not "no budget".
//
// This is the way turning a constant into a field goes wrong: a caller that
// passes nothing gets infinity, and the ceiling that existed to bound what
// reaches a model's context quietly stops existing. The default is applied in
// one place and this is the test that says so.
func TestAZeroDescriptionBudgetMeansTheDocumentedDefault(t *testing.T) {
	built := Build("sha256:fixture", []domain.Operation{budgetOperation()}, Options{})
	if len(built.Tools) != 1 {
		t.Fatalf("published %d tools", len(built.Tools))
	}
	if got := len(built.Tools[0].Description); got != DefaultDescriptionBytesPerTool {
		t.Errorf("description is %d bytes with no budget configured, want the default %d",
			got, DefaultDescriptionBytesPerTool)
	}
	if got := built.Report.Estimate.DescriptionBytesPerTool; got != DefaultDescriptionBytesPerTool {
		t.Errorf("the report says the budget is %d, want %d", got, DefaultDescriptionBytesPerTool)
	}
}

// A configured budget is the one that holds, and it can only tighten in the
// sense that matters: whatever it is, it is what the description is cut to.
func TestAConfiguredDescriptionBudgetHolds(t *testing.T) {
	const tighter = 200
	built := Build("sha256:fixture", []domain.Operation{budgetOperation()},
		Options{DescriptionBytesPerTool: tighter})

	// At most the budget, not exactly it: the cut respects rune boundaries, so
	// a multi-byte character at the edge leaves a byte or two unspent rather
	// than being split in half.
	if got := len(built.Tools[0].Description); got > tighter || got < tighter-4 {
		t.Errorf("description is %d bytes, want at most the configured %d", got, tighter)
	}
	if got := built.Report.Estimate.DescriptionBytesPerTool; got != tighter {
		t.Errorf("the report says %d, want %d", got, tighter)
	}
}

// Changing a budget changes what a client sees, so it changes the digest.
// FR-74 promises a digest moves on a substantive change, and prose is part of
// what a model is given: a client that cached the longer descriptions would
// notice.
func TestChangingABudgetMovesTheDigest(t *testing.T) {
	operations := []domain.Operation{budgetOperation()}
	base := Build("sha256:fixture", operations, Options{})

	t.Run("a tighter description budget", func(t *testing.T) {
		changed := Build("sha256:fixture", operations, Options{DescriptionBytesPerTool: 200})
		if changed.Digest == base.Digest {
			t.Errorf("the digest did not move: %s", changed.Digest)
		}
	})

	// The catalog budget does not change any tool, so on its own it does not
	// move the digest. It moves it when it changes the *mode*, because the
	// mode changes what a client is shown -- which is the thing the digest
	// identifies.
	t.Run("a catalog budget that does not change the mode", func(t *testing.T) {
		changed := Build("sha256:fixture", operations, Options{MaxSerializedBytes: 1 << 20})
		if changed.Mode != base.Mode {
			t.Fatalf("this case is supposed to leave the mode alone, got %s", changed.Mode)
		}
		if changed.Digest != base.Digest {
			t.Errorf("the digest moved without anything a client sees changing")
		}
	})

	t.Run("a catalog budget small enough to flip the mode", func(t *testing.T) {
		changed := Build("sha256:fixture", operations, Options{MaxSerializedBytes: 1})
		if changed.Mode == base.Mode {
			t.Fatalf("the mode did not flip, so this proves nothing")
		}
		if changed.Digest == base.Digest {
			t.Error("the mode changed what a client is shown, and the digest did not move")
		}
	})

	// And the same options twice produce the same digest, which is the other
	// half of the promise (Principle IV).
	again := Build("sha256:fixture", operations, Options{DescriptionBytesPerTool: 200})
	once := Build("sha256:fixture", operations, Options{DescriptionBytesPerTool: 200})
	if again.Digest != once.Digest {
		t.Errorf("two identical builds disagree: %s vs %s", again.Digest, once.Digest)
	}
}

// The mode is part of what the digest identifies, because `tools/list` differs
// entirely between the two: one tool per operation, or five meta-tools over
// the same catalog.
//
// Before this held, a client could cache a tool list against a digest, the
// operator could flip the mode, and the digest would say nothing had changed
// (FR-74). A reload had the same blind spot: it publishes only when the digest
// moves, so a mode change alone decided there was nothing to publish.
func TestTheModeIsPartOfTheDigest(t *testing.T) {
	operations := []domain.Operation{budgetOperation()}
	tools := Build("sha256:fixture", operations, Options{Mode: ModeTools})
	search := Build("sha256:fixture", operations, Options{Mode: ModeSearch})

	if tools.Mode == search.Mode {
		t.Fatal("the two builds are in the same mode, so this proves nothing")
	}
	if tools.Digest == search.Digest {
		t.Errorf("the same digest in both modes: %s", tools.Digest)
	}
}
