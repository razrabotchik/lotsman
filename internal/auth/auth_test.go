package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/redact"
)

const canary = "CANARY-TOKEN-DO-NOT-LEAK"

func requirement(scheme, schemeType, in, name, httpScheme string) domain.SecurityRequirement {
	return domain.SecurityRequirement{
		Scheme: scheme, Type: schemeType, In: in, Name: name, HTTP: httpScheme, Satisfiable: true,
	}
}

func alternative(requirements ...domain.SecurityRequirement) domain.SecurityAlternative {
	return domain.SecurityAlternative{Requirements: requirements}
}

func TestSelectBindsAMatchingProfile(t *testing.T) {
	profiles := NewProfiles(map[string]config.Profile{
		"bearerAuth": {Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
	})
	verdict := Select([]domain.SecurityAlternative{
		alternative(requirement("bearerAuth", "http", "", "", "bearer")),
	}, profiles)

	if !verdict.Bound() {
		t.Fatalf("verdict = %+v, want a binding", verdict)
	}
	if len(verdict.Binding.Credentials) != 1 || verdict.Binding.Credentials[0].Name != "bearerAuth" {
		t.Errorf("binding = %+v", verdict.Binding)
	}
}

// A profile satisfies the schemes it lists, which is how a credential named
// after the operator's own vocabulary reaches a scheme named after the API's.
func TestSelectHonoursSatisfies(t *testing.T) {
	profiles := NewProfiles(map[string]config.Profile{
		"my-token": {Scheme: config.SchemeBearer, TokenRef: "env:TOKEN", Satisfies: []string{"bearerAuth"}},
	})
	verdict := Select([]domain.SecurityAlternative{
		alternative(requirement("bearerAuth", "http", "", "", "bearer")),
	}, profiles)
	if !verdict.Bound() {
		t.Fatalf("verdict = %+v", verdict)
	}
}

// Every requirement inside one alternative must be met together (AND).
func TestSelectRequiresEveryRequirementOfAnAlternative(t *testing.T) {
	both := alternative(
		requirement("apiKeyAuth", "apiKey", "header", "X-API-Key", ""),
		requirement("signature", "apiKey", "query", "sig", ""),
	)
	half := NewProfiles(map[string]config.Profile{
		"apiKeyAuth": {Scheme: config.SchemeAPIKey, In: config.InHeader, Name: "X-API-Key", TokenRef: "env:K"},
	})
	if verdict := Select([]domain.SecurityAlternative{both}, half); verdict.Bound() {
		t.Fatal("an AND alternative was bound with only one of its credentials")
	}

	full := NewProfiles(map[string]config.Profile{
		"apiKeyAuth": {Scheme: config.SchemeAPIKey, In: config.InHeader, Name: "X-API-Key", TokenRef: "env:K"},
		"signature":  {Scheme: config.SchemeAPIKey, In: config.InQuery, Name: "sig", TokenRef: "env:S"},
	})
	verdict := Select([]domain.SecurityAlternative{both}, full)
	if !verdict.Bound() || len(verdict.Binding.Credentials) != 2 {
		t.Fatalf("verdict = %+v, want both credentials", verdict)
	}
}

// FR-57: two satisfiable alternatives and nothing to choose between them is
// not a coin toss.
func TestSelectRefusesAmbiguity(t *testing.T) {
	profiles := NewProfiles(map[string]config.Profile{
		"bearerAuth": {Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
		"apiKeyAuth": {Scheme: config.SchemeAPIKey, In: config.InHeader, Name: "X-API-Key", TokenRef: "env:K"},
	})
	verdict := Select([]domain.SecurityAlternative{
		alternative(requirement("bearerAuth", "http", "", "", "bearer")),
		alternative(requirement("apiKeyAuth", "apiKey", "header", "X-API-Key", "")),
	}, profiles)

	if verdict.Bound() {
		t.Fatal("an ambiguous operation was bound")
	}
	if verdict.Reason != domain.ReasonAmbiguousSecurity {
		t.Errorf("reason = %q, want %q", verdict.Reason, domain.ReasonAmbiguousSecurity)
	}
	if !strings.Contains(verdict.Message, "exactly one") {
		t.Errorf("message = %q, want it to say what would fix it", verdict.Message)
	}
}

func TestSelectWithoutProfiles(t *testing.T) {
	verdict := Select([]domain.SecurityAlternative{
		alternative(requirement("bearerAuth", "http", "", "", "bearer")),
	}, NewProfiles(nil))

	if verdict.Bound() {
		t.Fatal("an operation was bound with no profiles configured")
	}
	if verdict.Reason != domain.ReasonAuthenticationNotImplemented {
		t.Errorf("reason = %q", verdict.Reason)
	}
	if !strings.Contains(verdict.Message, "bearerAuth") {
		t.Errorf("message = %q, want it to name the scheme that is missing", verdict.Message)
	}
}

// A public operation needs nothing, whatever is configured.
func TestSelectPublicOperations(t *testing.T) {
	profiles := NewProfiles(map[string]config.Profile{
		"bearerAuth": {Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
	})
	if verdict := Select(nil, profiles); !verdict.Bound() || !verdict.Binding.Empty() {
		t.Errorf("no security = %+v, want an empty binding", verdict)
	}
	optional := []domain.SecurityAlternative{{}, alternative(requirement("bearerAuth", "http", "", "", "bearer"))}
	if verdict := Select(optional, profiles); !verdict.Bound() || !verdict.Binding.Empty() {
		t.Errorf("optional auth = %+v, want an empty binding", verdict)
	}
}

// Sending a credential where the API does not look for it leaks it to a place
// nobody is guarding.
func TestSelectRefusesMisplacedAPIKey(t *testing.T) {
	profiles := NewProfiles(map[string]config.Profile{
		"apiKeyAuth": {Scheme: config.SchemeAPIKey, In: config.InHeader, Name: "X-API-Key", TokenRef: "env:K"},
	})
	verdict := Select([]domain.SecurityAlternative{
		alternative(requirement("apiKeyAuth", "apiKey", "query", "api_key", "")),
	}, profiles)
	if verdict.Bound() {
		t.Fatal("a header key was bound to an API that reads the query string")
	}
}

func TestSelectRefusesWrongSchemeType(t *testing.T) {
	profiles := NewProfiles(map[string]config.Profile{
		"bearerAuth": {Scheme: config.SchemeBasic, UsernameRef: "env:U", PasswordRef: "env:P"},
	})
	verdict := Select([]domain.SecurityAlternative{
		alternative(requirement("bearerAuth", "http", "", "", "bearer")),
	}, profiles)
	if verdict.Bound() {
		t.Fatal("basic credentials were bound to a bearer scheme")
	}
}

// The credential is applied to the request that goes out, and only to it.
func TestTransportAppliesCredentials(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_TOKEN", canary)
	t.Setenv("LOTSMAN_TEST_USER", "alice")
	t.Setenv("LOTSMAN_TEST_PASS", canary)

	for _, tt := range []struct {
		name    string
		profile config.Profile
		req     domain.SecurityRequirement
		check   func(t *testing.T, r *http.Request)
	}{
		{
			name:    "bearer",
			profile: config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:LOTSMAN_TEST_TOKEN"},
			req:     requirement("s", "http", "", "", "bearer"),
			check: func(t *testing.T, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer "+canary {
					t.Errorf("Authorization = %q", got)
				}
			},
		},
		{
			name: "basic",
			profile: config.Profile{Scheme: config.SchemeBasic,
				UsernameRef: "env:LOTSMAN_TEST_USER", PasswordRef: "env:LOTSMAN_TEST_PASS"},
			req: requirement("s", "http", "", "", "basic"),
			check: func(t *testing.T, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if !ok || user != "alice" || pass != canary {
					t.Errorf("basic auth = %q/%q ok=%t", user, pass, ok)
				}
			},
		},
		{
			name:    "apikey in header",
			profile: config.Profile{Scheme: config.SchemeAPIKey, TokenRef: "env:LOTSMAN_TEST_TOKEN"},
			req:     requirement("s", "apiKey", "header", "X-API-Key", ""),
			check: func(t *testing.T, r *http.Request) {
				if got := r.Header.Get("X-API-Key"); got != canary {
					t.Errorf("X-API-Key = %q", got)
				}
			},
		},
		{
			name:    "apikey in query",
			profile: config.Profile{Scheme: config.SchemeAPIKey, TokenRef: "env:LOTSMAN_TEST_TOKEN"},
			req:     requirement("s", "apiKey", "query", "api_key", ""),
			check: func(t *testing.T, r *http.Request) {
				if got := r.URL.Query().Get("api_key"); got != canary {
					t.Errorf("api_key = %q", got)
				}
				if r.URL.Query().Get("limit") != "1" {
					t.Error("the credential replaced the caller's own query parameters")
				}
			},
		},
		{
			name:    "apikey in cookie",
			profile: config.Profile{Scheme: config.SchemeAPIKey, TokenRef: "env:LOTSMAN_TEST_TOKEN"},
			req:     requirement("s", "apiKey", "cookie", "session", ""),
			check: func(t *testing.T, r *http.Request) {
				cookie, err := r.Cookie("session")
				if err != nil || cookie.Value != canary {
					t.Errorf("cookie = %+v err=%v", cookie, err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tt.check(t, r)
			}))
			defer srv.Close()

			binding := Binding{Credentials: []Credential{{Name: "p", Profile: tt.profile, Requirement: tt.req}}}
			client := Client(srv.Client(), binding)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/pets?limit=1", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			_ = resp.Body.Close()

			// The caller's request must come back untouched: the credential
			// belongs to the copy that went out.
			if req.Header.Get("Authorization") != "" || strings.Contains(req.URL.RawQuery, canary) {
				t.Error("the credential was written onto the caller's own request")
			}
		})
	}
}

// Resolving a credential registers it for redaction, so a value this process
// has handled can never be printed by it.
func TestResolvedSecretsAreRegisteredForRedaction(t *testing.T) {
	t.Setenv("LOTSMAN_TEST_TOKEN", canary)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	binding := Binding{Credentials: []Credential{{
		Name:        "p",
		Profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:LOTSMAN_TEST_TOKEN"},
		Requirement: requirement("s", "http", "", "", "bearer"),
	}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Client(srv.Client(), binding).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got := redact.String("token is " + canary); strings.Contains(got, canary) {
		t.Errorf("the resolved secret was not registered for redaction: %q", got)
	}
}

func TestTransportReportsMissingSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request reached the server without a credential")
	}))
	defer srv.Close()

	binding := Binding{Credentials: []Credential{{
		Name:        "p",
		Profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:LOTSMAN_DEFINITELY_NOT_SET"},
		Requirement: requirement("s", "http", "", "", "bearer"),
	}}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Client(srv.Client(), binding).Do(req); err == nil {
		t.Fatal("a call with an unresolvable credential succeeded")
	}
}

// Two alternatives that resolve to the same credential are not a choice.
// DigitalOcean writes `bearer_auth: []` and `bearer_auth: [scope]` on the same
// operation; refusing that as ambiguous would be pedantry with an outage
// attached.
func TestSelectCollapsesIdenticalAlternatives(t *testing.T) {
	profiles := NewProfiles(map[string]config.Profile{
		"bearerAuth": {Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
	})
	scoped := requirement("bearerAuth", "http", "", "", "bearer")
	scoped.Scopes = []string{"read"}

	verdict := Select([]domain.SecurityAlternative{
		alternative(requirement("bearerAuth", "http", "", "", "bearer")),
		alternative(scoped),
	}, profiles)

	if !verdict.Bound() {
		t.Fatalf("verdict = %+v, want a binding: both alternatives are the same credential", verdict)
	}
	if len(verdict.Binding.Credentials) != 1 {
		t.Errorf("binding = %+v", verdict.Binding)
	}
}
