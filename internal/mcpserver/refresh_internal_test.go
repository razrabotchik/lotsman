package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/audit"
	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/egress"
)

// sink collects the events a runner writes.
type sink struct{ events []audit.Event }

//nolint:gocritic // hugeParam: an Event travels by value so that a sink cannot edit the record the next sink in a Multi is about to write.
func (s *sink) Record(event audit.Event) { s.events = append(s.events, event) }

// stale is an API that rejects the first token it is shown and accepts the
// second, which is what an expiry looks like from the outside.
type stale struct {
	server   *httptest.Server
	calls    atomic.Int64
	rejectUp int64 // how many leading calls answer 401
	status   int   // the status after that; 200 unless set
}

func newStale(t *testing.T, rejectUp int64) *stale {
	t.Helper()
	s := &stale{rejectUp: rejectUp}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if s.calls.Add(1) <= s.rejectUp {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(s.server.Close)
	return s
}

// mintingEndpoint is an authorization server that counts what it issues.
type mintingEndpoint struct {
	server *httptest.Server
	mints  atomic.Int64
}

func newMintingEndpoint(t *testing.T) *mintingEndpoint {
	t.Helper()
	e := &mintingEndpoint{}
	e.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count := e.mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			// A different token each time, so "the retry presented a new one"
			// is observable rather than assumed.
			"access_token": "token-" + string(rune('a'+count-1)),
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(e.server.Close)
	return e
}

// newRunner assembles one runner against the given API and credential.
func newRunner(t *testing.T, api *httptest.Server, profile *config.Profile, events *sink) *runner {
	t.Helper()
	tool := &catalog.Tool{
		Name:         "call",
		OperationKey: domain.OperationKey("default:GET:/thing"),
		Method:       http.MethodGet,
		PathTemplate: "/thing",
		Servers:      []string{api.URL},
		Executable:   true,
	}
	var bound auth.Binding
	if profile != nil {
		bound = auth.Binding{Credentials: []auth.Credential{{Name: "api", Profile: *profile}}}
	}
	tool.AuthBinding = bound

	policy, err := egress.PolicyFromBaseURL(api.URL, egress.Budget{}, true)
	if err != nil {
		t.Fatalf("egress: %v", err)
	}
	validator, err := argvalidate.Compile("call", map[string]any{
		"type": "object", "properties": map[string]any{}, "additionalProperties": false,
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	minter := auth.NewMinter(api.Client())
	return &runner{
		tool:      tool,
		validator: validator,
		client:    auth.Client(api.Client(), bound, minter),
		egress:    &policy,
		approval:  approver{mode: config.ApprovalNever, log: discard()},
		audit:     events,
		minter:    minter,
	}
}

func discard() logger { return (&Options{}).logger() }

// mintingProfile points at a token endpoint.
func mintingProfile(t *testing.T, endpoint *mintingEndpoint) *config.Profile {
	t.Helper()
	t.Setenv("LOTSMAN_TEST_CLIENT_SECRET", "secret")
	return &config.Profile{
		Scheme:          config.SchemeOAuth2ClientCredentials,
		TokenURL:        endpoint.server.URL,
		ClientID:        "lotsman",
		ClientSecretRef: config.SecretRef("env:LOTSMAN_TEST_CLIENT_SECRET"),
	}
}

// FR-35: one refresh and one retry, and the record says it happened.
func TestAStaleTokenIsRefreshedOnceAndTheCallRetriedOnce(t *testing.T) {
	api := newStale(t, 1)
	endpoint := newMintingEndpoint(t)
	events := &sink{}
	prepared := newRunner(t, api.server, mintingProfile(t, endpoint), events)

	result, err := prepared.call(t.Context(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 after the retry", result.Status)
	}
	if got := api.calls.Load(); got != 2 {
		t.Errorf("the API was called %d times, want 2 (the original and one retry)", got)
	}
	if got := endpoint.mints.Load(); got != 2 {
		t.Errorf("minted %d tokens, want 2 (the first and the replacement)", got)
	}
	if len(events.events) != 1 || !events.events[0].Refreshed {
		t.Errorf("the record does not say a credential was refreshed: %+v", events.events)
	}
}

// One retry, not a retry policy. A second 401 is the API's own answer.
func TestASecondRefusalIsTheAPIsOwnAnswer(t *testing.T) {
	api := newStale(t, 99) // it will never accept anything
	endpoint := newMintingEndpoint(t)
	events := &sink{}
	prepared := newRunner(t, api.server, mintingProfile(t, endpoint), events)

	result, err := prepared.call(t.Context(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want the API's own 401", result.Status)
	}
	if got := api.calls.Load(); got != 2 {
		t.Errorf("the API was called %d times, want exactly 2", got)
	}
	if got := endpoint.mints.Load(); got != 2 {
		t.Errorf("minted %d tokens, want exactly 2", got)
	}
}

// Not a retry on anything else. A 403 means the credential was understood
// and refused, and re-minting it would produce the same credential.
func TestNoRetryForAnyOtherStatus(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusTooManyRequests} {
		api := newStale(t, 0)
		api.status = status
		endpoint := newMintingEndpoint(t)
		events := &sink{}
		prepared := newRunner(t, api.server, mintingProfile(t, endpoint), events)

		if _, err := prepared.call(t.Context(), nil, map[string]any{}); err != nil {
			t.Fatalf("call: %v", err)
		}
		if got := api.calls.Load(); got != 1 {
			t.Errorf("status %d: the API was called %d times, want 1", status, got)
		}
		if len(events.events) != 1 || events.events[0].Refreshed {
			t.Errorf("status %d: the record claims a refresh that did not happen", status)
		}
	}
}

// And no retry for a credential nothing can re-mint. An API key read from a
// file is not renewed by asking anyone, so a 401 against one goes to the
// caller unchanged.
func TestNoRetryForACredentialThatCannotBeReminted(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_KEY", "static-key")
	api := newStale(t, 99)
	events := &sink{}
	prepared := newRunner(t, api.server, &config.Profile{
		Scheme:   config.SchemeAPIKey,
		In:       config.InHeader,
		Name:     "X-API-Key",
		TokenRef: config.SecretRef("env:LOTSMAN_TEST_KEY"),
	}, events)

	result, err := prepared.call(t.Context(), nil, map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want the API's own 401", result.Status)
	}
	if got := api.calls.Load(); got != 1 {
		t.Errorf("the API was called %d times, want 1", got)
	}
	if len(events.events) != 1 || events.events[0].Refreshed {
		t.Error("the record claims a refresh for a credential that cannot be refreshed")
	}
}
