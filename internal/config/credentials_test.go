package config

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// A minting profile is refused at load when it could only fail at the moment
// it is first needed -- which, for a credential fetched on the first call,
// means in front of a user rather than in front of the operator.
func TestClientCredentialsProfileValidation(t *testing.T) {
	const canaryLiteral = "CANARY-literal-secret-DO-NOT-LEAK"
	cases := []struct {
		name    string
		profile string
		refuses string // a fragment the message must carry
	}{
		{
			name:    "no token endpoint",
			profile: "clientID: lotsman\n    clientSecretRef: env:S",
			refuses: "tokenURL",
		},
		{
			name:    "a token endpoint over plain http",
			profile: "tokenURL: http://issuer.example.com/token\n    clientID: lotsman\n    clientSecretRef: env:S",
			refuses: "https",
		},
		{
			name:    "a token endpoint that is not a URL",
			profile: "tokenURL: issuer\n    clientID: lotsman\n    clientSecretRef: env:S",
			refuses: "https",
		},
		{
			name:    "no client id",
			profile: "tokenURL: https://issuer.example.com/token\n    clientSecretRef: env:S",
			refuses: "clientID",
		},
		{
			name:    "no client secret",
			profile: "tokenURL: https://issuer.example.com/token\n    clientID: lotsman",
			refuses: "clientSecretRef",
		},
		{
			// FR-59: the refusal must not echo what it was given, because
			// what it was given may be the secret.
			name: "a literal secret instead of a reference",
			profile: "tokenURL: https://issuer.example.com/token\n    clientID: lotsman\n" +
				"    clientSecretRef: " + canaryLiteral,
			refuses: "env:NAME",
		},
		{
			name: "an authentication style nobody defines",
			profile: "tokenURL: https://issuer.example.com/token\n    clientID: lotsman\n" +
				"    clientSecretRef: env:S\n    authStyle: mtls",
			refuses: "authStyle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\nauthProfiles:\n  api:\n" +
				"    scheme: oauth2-client-credentials\n    " + tc.profile + "\n"))
			if err == nil {
				t.Fatal("the profile was accepted")
			}
			if errs.ClassOf(err) != errs.ClassUsage {
				t.Errorf("class = %s, want usage", errs.ClassOf(err))
			}
			if !strings.Contains(err.Error(), tc.refuses) {
				t.Errorf("the refusal does not say what is wrong: %v", err)
			}
			if strings.Contains(err.Error(), canaryLiteral) {
				t.Errorf("the refusal echoed the secret: %v", err)
			}
		})
	}
}

// And a complete one is accepted, with its defaults left alone rather than
// filled in: `authStyle` unset means auto, and auto is the oauth2 library's
// probe rather than a value this package invents.
func TestACompleteClientCredentialsProfileIsAccepted(t *testing.T) {
	file, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
authProfiles:
  api:
    scheme: oauth2-client-credentials
    tokenURL: https://issuer.example.com/token
    clientID: lotsman
    clientSecretRef: env:LOTSMAN_CLIENT_SECRET
    scopes: [read, write]
    audience: https://api.example.com/
    authStyle: basic
    satisfies: [serviceAuth]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	profile := file.AuthProfiles["api"]
	if profile.Scheme != SchemeOAuth2ClientCredentials {
		t.Errorf("scheme = %q", profile.Scheme)
	}
	if len(profile.Scopes) != 2 || profile.Audience != "https://api.example.com/" {
		t.Errorf("scopes/audience = %v / %q", profile.Scopes, profile.Audience)
	}
	if profile.AuthStyle != AuthStyleBasic {
		t.Errorf("authStyle = %q", profile.AuthStyle)
	}
	if len(profile.Satisfies) != 1 {
		t.Errorf("satisfies = %v", profile.Satisfies)
	}
}

// The other schemes keep their own required fields: adding a fourth must not
// have loosened the three that were there.
func TestTheOlderSchemesStillRequireWhatTheyRequired(t *testing.T) {
	for _, tc := range []struct{ name, profile string }{
		{"bearer without a token", "scheme: bearer"},
		{"apikey without a name", "scheme: apikey\n    in: header\n    tokenRef: env:S"},
		{"apikey without a location", "scheme: apikey\n    name: X-Key\n    tokenRef: env:S"},
		{"basic with only half a credential", "scheme: basic\n    usernameRef: env:U"},
		{"a scheme nobody defines", "scheme: mtls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte("apiVersion: lotsman.dev/v1alpha1\nauthProfiles:\n  api:\n    " +
				tc.profile + "\n")); err == nil {
				t.Error("the profile was accepted")
			}
		})
	}
}
