package auth

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
)

// When a configured profile does not satisfy what a document asks for, the
// reason is what an operator acts on: it is the difference between "add a
// credential" and "you have one, it goes somewhere else". Half of these reasons
// had never been produced by a test.
//
// The `in` mismatch is the one with teeth. A key configured for a header cannot
// satisfy an API that reads it from the query string, and sending it anyway
// would put the credential in a place the API does not look -- a log, a proxy's
// access line, a browser history -- while the call fails anyway.
func TestAnIncompatibleProfileSaysWhyInTermsOfTheDocument(t *testing.T) {
	cases := []struct {
		name        string
		profile     config.Profile
		requirement domain.SecurityRequirement
		reason      string
	}{
		{
			name:        "a bearer token where the API wants a key",
			profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
			requirement: requirement("apiKeyAuth", "apiKey", "header", "X-API-Key", ""),
			reason:      "the API wants an API key",
		},
		{
			name:        "a key configured for the wrong place",
			profile:     config.Profile{Scheme: config.SchemeAPIKey, TokenRef: "env:KEY", In: config.InHeader, Name: "X-API-Key"},
			requirement: requirement("apiKeyAuth", "apiKey", "query", "X-API-Key", ""),
			reason:      "carries the key in the query",
		},
		{
			name:        "a key configured under the wrong name",
			profile:     config.Profile{Scheme: config.SchemeAPIKey, TokenRef: "env:KEY", In: config.InHeader, Name: "X-Wrong"},
			requirement: requirement("apiKeyAuth", "apiKey", "header", "X-API-Key", ""),
			reason:      "names the key X-API-Key",
		},
		{
			name:        "basic where the API wants bearer",
			profile:     config.Profile{Scheme: config.SchemeBasic, UsernameRef: "env:U", PasswordRef: "env:P"},
			requirement: requirement("bearerAuth", "http", "", "", "bearer"),
			reason:      "wants a bearer token",
		},
		{
			name:        "bearer where the API wants basic",
			profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
			requirement: requirement("basicAuth", "http", "", "", "basic"),
			reason:      "wants HTTP basic",
		},
		{
			name:        "an HTTP scheme lotsman does not implement",
			profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
			requirement: requirement("negotiate", "http", "", "", "negotiate"),
			reason:      "unsupported HTTP authentication scheme negotiate",
		},
		{
			name:        "a constant token where the API mints",
			profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
			requirement: requirement("serviceAuth", "oauth2", "", "", ""),
			reason:      "wants an OAuth2 token",
		},
		{
			name:        "a scheme type nobody translates",
			profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
			requirement: requirement("mtls", "mutualTLS", "", "", ""),
			reason:      "unsupported security scheme type mutualTLS",
		},
		{
			name:        "a reference to a scheme the document never defined",
			profile:     config.Profile{Scheme: config.SchemeBearer, TokenRef: "env:TOKEN"},
			requirement: requirement("ghost", "", "", "", ""),
			reason:      "defines no such security scheme",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profiles := NewProfiles(map[string]config.Profile{tc.requirement.Scheme: tc.profile})
			verdict := Select([]domain.SecurityAlternative{alternative(tc.requirement)}, profiles)

			if verdict.Bound() {
				t.Fatalf("the profile was accepted: %+v", verdict.Binding)
			}
			if !strings.Contains(verdict.Message, tc.reason) {
				t.Errorf("message = %q, want it to say %q", verdict.Message, tc.reason)
			}
		})
	}
}

// And the matching ones are accepted, which is what keeps the table above from
// passing on a selector that refuses everything.
func TestACompatibleProfileOfEachKindIsBound(t *testing.T) {
	cases := []struct {
		name        string
		profile     config.Profile
		requirement domain.SecurityRequirement
	}{
		{
			name:        "api key in the place the API reads",
			profile:     config.Profile{Scheme: config.SchemeAPIKey, TokenRef: "env:KEY", In: config.InQuery, Name: "sig"},
			requirement: requirement("apiKeyAuth", "apiKey", "query", "sig", ""),
		},
		{
			name:        "basic",
			profile:     config.Profile{Scheme: config.SchemeBasic, UsernameRef: "env:U", PasswordRef: "env:P"},
			requirement: requirement("basicAuth", "http", "", "", "basic"),
		},
		{
			name: "a minting profile against an oauth2 scheme",
			profile: config.Profile{
				Scheme: config.SchemeOAuth2ClientCredentials, TokenURL: "https://as.example.com/token",
				ClientID: "lotsman", ClientSecretRef: "env:SECRET",
			},
			requirement: requirement("serviceAuth", "oauth2", "", "", ""),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profiles := NewProfiles(map[string]config.Profile{tc.requirement.Scheme: tc.profile})
			verdict := Select([]domain.SecurityAlternative{alternative(tc.requirement)}, profiles)
			if !verdict.Bound() {
				t.Fatalf("a compatible profile was refused: %s (%s)", verdict.Message, verdict.Reason)
			}
		})
	}
}
