package mcpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/razrabotchik/lotsman/internal/audit"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
)

// ADR-0019: how far a request got decides what the record says and what the
// caller is told. These are the four ways a request can stop short of an
// ordinary answer, each against a real connection.

// closeWithoutAnswer drops the connection under a handler.
func closeWithoutAnswer(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Error("the test server cannot hand over its connection")
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	_ = conn.Close()
}

// hangUp is an API that reads the whole request and closes the connection
// without answering: the request was delivered, and nobody can say what the
// API did with it.
func hangUp(t *testing.T, received *atomic.Int64) *httptest.Server {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received.Add(1)
		closeWithoutAnswer(t, w)
	}))
	t.Cleanup(api.Close)
	return api
}

func mutating(r *runner) *runner {
	r.tool.Method = http.MethodPost
	r.tool.Effect = domain.EffectDecision{Effect: domain.EffectWrite}
	return r
}

// The case version 1 of the record got wrong: the API received the mutation,
// the answer was lost, and the record said "refused" -- a promise that
// nothing happened, made about a call that may well have.
func TestASentMutationWithNoAnswerHasAnUnknownOutcome(t *testing.T) {
	var received atomic.Int64
	api := hangUp(t, &received)
	events := &sink{}
	r := mutating(newRunner(t, api, nil, events))

	_, err := r.call(t.Context(), nil, map[string]any{})
	if err == nil {
		t.Fatal("a call with no answer succeeded")
	}
	if received.Load() != 1 {
		t.Fatalf("the API received %d requests, want 1", received.Load())
	}
	if len(events.events) != 1 {
		t.Fatalf("events = %d, want 1", len(events.events))
	}
	event := events.events[0]
	if event.Decision != audit.DecisionOutcomeUnknown {
		t.Errorf("decision = %s, want %s", event.Decision, audit.DecisionOutcomeUnknown)
	}
	if event.Reason != "upstream" {
		t.Errorf("reason = %q, want upstream", event.Reason)
	}
	// The caller is an agent deciding whether to try again. It must hear
	// that trying again may do the thing twice.
	if !strings.Contains(err.Error(), "may have acted") {
		t.Errorf("the caller is not told the outcome is unknown: %v", err)
	}
}

// A read with no answer is just as unknown, but there is nothing to warn
// about: repeating a read is what reads are for.
func TestASentReadWithNoAnswerDoesNotWarnAgainstRetrying(t *testing.T) {
	var received atomic.Int64
	events := &sink{}
	r := newRunner(t, hangUp(t, &received), nil, events)
	r.tool.Effect = domain.EffectDecision{Effect: domain.EffectRead}

	_, err := r.call(t.Context(), nil, map[string]any{})
	if err == nil {
		t.Fatal("a call with no answer succeeded")
	}
	if got := events.events[0].Decision; got != audit.DecisionOutcomeUnknown {
		t.Errorf("decision = %s, want %s", got, audit.DecisionOutcomeUnknown)
	}
	if strings.Contains(err.Error(), "may have acted") {
		t.Errorf("a read is warned against retrying: %v", err)
	}
}

// No connection means no request: the call failed, provably without effect,
// and is not dressed up as either a refusal or an unknown.
func TestARequestThatCouldNotConnectFailedWithoutEffect(t *testing.T) {
	api := httptest.NewServer(http.NotFoundHandler())
	events := &sink{}
	r := mutating(newRunner(t, api, nil, events))
	api.Close()

	_, err := r.call(t.Context(), nil, map[string]any{})
	if err == nil {
		t.Fatal("a call to a closed server succeeded")
	}
	if got := events.events[0].Decision; got != audit.DecisionFailed {
		t.Errorf("decision = %s, want %s", got, audit.DecisionFailed)
	}
	if !strings.Contains(err.Error(), "nothing reached the API") {
		t.Errorf("the caller is not told the request never left: %v", err)
	}
}

// A credential that cannot be applied stops the call before the transport.
// That is a refusal, and its class is the credential's: reporting it as an
// upstream failure would send an operator to the API to fix a secret.
func TestACredentialThatCannotBeAppliedIsARefusalByAuth(t *testing.T) {
	var received atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(api.Close)
	profile := &config.Profile{
		Scheme:   config.SchemeBearer,
		TokenRef: config.SecretRef("env:LOTSMAN_TEST_TOKEN_NOBODY_SET"),
	}
	events := &sink{}
	r := newRunner(t, api, profile, events)

	if _, err := r.call(t.Context(), nil, map[string]any{}); err == nil {
		t.Fatal("a call proceeded without its credential")
	}
	if received.Load() != 0 {
		t.Errorf("the API was called %d times", received.Load())
	}
	event := events.events[0]
	if event.Decision != audit.DecisionRefused || event.Reason != "auth" {
		t.Errorf("decision = %s, reason = %q; want refused by auth", event.Decision, event.Reason)
	}
}

// An answer whose body is cut off is still an answer: the API acted and said
// so with a status. The record keeps the status rather than calling the call
// refused.
func TestAnAnswerWithALostBodyIsAnExecution(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":`))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		closeWithoutAnswer(t, w)
	}))
	t.Cleanup(api.Close)
	events := &sink{}
	r := mutating(newRunner(t, api, nil, events))

	_, err := r.call(t.Context(), nil, map[string]any{})
	if err == nil {
		t.Fatal("a truncated body was accepted as a whole one")
	}
	event := events.events[0]
	if event.Decision != audit.DecisionExecuted || event.Status != http.StatusCreated {
		t.Errorf("decision = %s, status = %d; want executed with 201", event.Decision, event.Status)
	}
	if !strings.Contains(err.Error(), "answered 201") {
		t.Errorf("the caller is not told the API answered: %v", err)
	}
}
