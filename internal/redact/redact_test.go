package redact

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

const canary = "CANARY-b81f4e2a9c7d3650-DO-NOT-LEAK"

func newRegistry(values ...string) *Registry {
	r := &Registry{values: map[string]struct{}{}}
	for _, value := range values {
		r.Add(value)
	}
	return r
}

func TestRegistryRedactsEverywhereInAString(t *testing.T) {
	r := newRegistry(canary)
	got := r.String("Authorization: Bearer " + canary + " (also " + canary + ")")

	if strings.Contains(got, canary) {
		t.Fatalf("value survived: %q", got)
	}
	if strings.Count(got, Placeholder) != 2 {
		t.Errorf("got %q, want both occurrences replaced", got)
	}
}

// A value short enough to appear in ordinary text is not a credential, and
// redacting it would turn every log line into confetti.
func TestRegistryIgnoresShortValues(t *testing.T) {
	r := newRegistry("ab")
	if got := r.String("a table of abbreviations"); got != "a table of abbreviations" {
		t.Errorf("got %q, want the text untouched", got)
	}
}

// Redacting an error must not break the chain: errors.Is and the error class
// are what callers branch on.
func TestErrorRedactionKeepsTheChain(t *testing.T) {
	sentinel := errors.New("sentinel")
	original := errs.Errorf(errs.ClassAuth, "token %s rejected: %w", canary, sentinel)

	redacted := newRegistry(canary).Error(original)
	if strings.Contains(redacted.Error(), canary) {
		t.Fatalf("error still carries the value: %v", redacted)
	}
	if !errors.Is(redacted, sentinel) {
		t.Error("errors.Is stopped working after redaction")
	}
	if got := errs.ClassOf(redacted); got != errs.ClassAuth {
		t.Errorf("class = %q, want it preserved", got)
	}
}

func TestErrorRedactionLeavesCleanErrorsAlone(t *testing.T) {
	original := errors.New("nothing secret here")
	if got := newRegistry(canary).Error(original); !errors.Is(got, original) || got.Error() != original.Error() {
		t.Error("a clean error was needlessly wrapped")
	}
}

// The handler is the last thing between any package's log call and the bytes
// leaving the process, so it has to reach into messages, attributes, groups
// and errors alike.
func TestHandlerRedactsMessagesAttributesAndGroups(t *testing.T) {
	Default.Add(canary)

	var buf bytes.Buffer
	logger := slog.New(NewHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	logger.Info("sending "+canary,
		slog.String("token", canary),
		slog.Any("err", errs.Errorf(errs.ClassAuth, "rejected %s", canary)),
		slog.Group("request", slog.String("header", "Bearer "+canary)),
	)
	logger.With(slog.String("preset", canary)).Info("with attrs")

	if strings.Contains(buf.String(), canary) {
		t.Fatalf("the handler let a secret through:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), Placeholder) {
		t.Errorf("nothing was marked as redacted:\n%s", buf.String())
	}
}

func TestHandlerKeepsOrdinaryLogsIntact(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewHandler(slog.NewTextHandler(&buf, nil)))
	logger.Info("serving mcp over stdio", slog.Int("tools", 12))

	if !strings.Contains(buf.String(), "serving mcp over stdio") || !strings.Contains(buf.String(), "tools=12") {
		t.Errorf("ordinary logging was damaged:\n%s", buf.String())
	}
}

// `logger.WithGroup("x")` is a different path from a `slog.Group` attribute:
// the first asks the handler for a new handler, the second is a value the
// handler walks. The walking one was tested; the handler one was not, and it is
// the one where redaction is lost by returning the inner handler instead of a
// wrapped one -- after which every attribute logged under that group reaches the
// output as written.
//
// A group is where a subsystem logs, so this is exactly where a credential
// would travel: `logger.WithGroup("upstream").Debug("...", "authorization", …)`.
func TestHandlerKeepsRedactingUnderAGroup(t *testing.T) {
	Default.Add(canary)

	var buf bytes.Buffer
	logger := slog.New(NewHandler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	grouped := logger.WithGroup("upstream")
	grouped.Info("calling "+canary, slog.String("authorization", "Bearer "+canary))
	// And a group inside a group, with the attribute preset rather than passed:
	// each hop has to keep the registry, not only the first.
	grouped.WithGroup("token").With(slog.String("value", canary)).Info("minted")

	if strings.Contains(buf.String(), canary) {
		t.Fatalf("a secret reached the output under a group:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), Placeholder) {
		t.Errorf("nothing was marked as redacted, so the test proved nothing:\n%s", buf.String())
	}
	// The group is still there: redaction replaces values, it does not drop
	// structure a reader needs to know where a line came from.
	if !strings.Contains(buf.String(), "upstream.authorization=") {
		t.Errorf("the group was flattened or lost:\n%s", buf.String())
	}
}
