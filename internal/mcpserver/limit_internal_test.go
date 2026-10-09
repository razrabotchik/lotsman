package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/razrabotchik/lotsman/internal/audit"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// A call over the limit is refused at once and never reaches the upstream;
// the slot comes back when the call holding it finishes.
func TestACallOverTheLimitIsRefusedBeforeTheNetwork(t *testing.T) {
	var received atomic.Int64
	arrived, release := make(chan struct{}, 1), make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if received.Add(1) == 1 {
			arrived <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(api.Close)
	events := &sink{}
	r := newRunner(t, api, nil, events)
	r.calls = NewCallLimit(1)

	first := make(chan error, 1)
	go func() {
		_, err := r.call(t.Context(), nil, map[string]any{})
		first <- err
	}()
	<-arrived

	_, err := r.call(t.Context(), nil, map[string]any{})
	if err == nil {
		t.Fatal("a second call ran past a limit of one")
	}
	if class := errs.ClassOf(err); class != errs.ClassPolicy || !strings.Contains(err.Error(), "maxConcurrentCalls") {
		t.Errorf("refusal = %v (%s), want a policy refusal naming the setting", err, class)
	}
	if received.Load() != 1 {
		t.Errorf("the upstream received %d requests, want only the first", received.Load())
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the first call: %v", err)
	}
	if _, err := r.call(t.Context(), nil, map[string]any{}); err != nil {
		t.Errorf("the slot did not come back: %v", err)
	}

	decisions := make([]audit.Decision, len(events.events))
	for i, event := range events.events {
		decisions[i] = event.Decision
	}
	want := []audit.Decision{audit.DecisionRefused, audit.DecisionExecuted, audit.DecisionExecuted}
	if strings.Join(asStrings(decisions), ",") != strings.Join(asStrings(want), ",") {
		t.Errorf("recorded %v, want %v", decisions, want)
	}
}

func asStrings(decisions []audit.Decision) []string {
	out := make([]string, len(decisions))
	for i, decision := range decisions {
		out[i] = string(decision)
	}
	return out
}
