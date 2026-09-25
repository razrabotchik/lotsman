package config

import (
	"strings"
	"testing"

	"github.com/razrabotchik/lotsman/internal/errs"
)

// The introspection client is how an opaque token is checked (RFC 7662), and
// every refusal below describes a deployment that looks configured and is not.
// None of them had a test: the validator was written, wired in, and never
// exercised -- which coverage said plainly once it was asked cross-package.
//
// The first case is the one that matters most. Introspection sends the caller's
// token to the authorization server together with lotsman's own client secret,
// so a plaintext endpoint puts both on the wire for anyone on the path.
func TestIntrospectionValidation(t *testing.T) {
	const preamble = "apiVersion: lotsman.dev/v1alpha1\nserver:\n  inboundAuth:\n    mode: oauth\n    oauth:\n"

	cases := []struct {
		name string
		yaml string
		// names is text the refusal has to contain, so a reader is told which
		// field to fix rather than that something is wrong.
		names string
	}{
		{
			name: "an introspection endpoint over plaintext",
			yaml: "      issuer: https://as.example.com\n      resource: https://lotsman.example.com\n" +
				"      introspection:\n        url: http://as.example.com/introspect\n" +
				"        clientID: lotsman\n        clientSecretRef: env:AS_SECRET\n",
			names: "https",
		},
		{
			name: "no client identity",
			yaml: "      issuer: https://as.example.com\n      resource: https://lotsman.example.com\n" +
				"      introspection:\n        url: https://as.example.com/introspect\n" +
				"        clientSecretRef: env:AS_SECRET\n",
			names: "clientID",
		},
		{
			name: "no client secret",
			yaml: "      issuer: https://as.example.com\n      resource: https://lotsman.example.com\n" +
				"      introspection:\n        url: https://as.example.com/introspect\n" +
				"        clientID: lotsman\n",
			names: "clientSecretRef",
		},
		{
			name: "a literal secret instead of a reference",
			yaml: "      issuer: https://as.example.com\n      resource: https://lotsman.example.com\n" +
				"      introspection:\n        url: https://as.example.com/introspect\n" +
				"        clientID: lotsman\n        clientSecretRef: hunter2\n",
			names: "env:",
		},
		{
			name: "a timeout that is not a duration",
			yaml: "      issuer: https://as.example.com\n      resource: https://lotsman.example.com\n" +
				"      introspection:\n        url: https://as.example.com/introspect\n" +
				"        clientID: lotsman\n        clientSecretRef: env:AS_SECRET\n" +
				"        timeout: soon\n",
			names: "timeout",
		},
		{
			name: "a timeout of zero, which is not 'no limit'",
			yaml: "      issuer: https://as.example.com\n      resource: https://lotsman.example.com\n" +
				"      introspection:\n        url: https://as.example.com/introspect\n" +
				"        clientID: lotsman\n        clientSecretRef: env:AS_SECRET\n" +
				"        timeout: 0s\n",
			names: "timeout",
		},
		{
			// Two ways to validate the same token is two answers waiting to
			// disagree, and the disagreement would be settled by whichever code
			// path ran first.
			name: "both signed keys and introspection",
			yaml: "      issuer: https://as.example.com\n      resource: https://lotsman.example.com\n" +
				"      jwksURI: https://as.example.com/jwks\n" +
				"      introspection:\n        url: https://as.example.com/introspect\n" +
				"        clientID: lotsman\n        clientSecretRef: env:AS_SECRET\n",
			names: "introspection",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(preamble + tc.yaml))
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if got := errs.ClassOf(err); got != errs.ClassUsage {
				t.Errorf("class = %s, want %s (%v)", got, errs.ClassUsage, err)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("the refusal does not name %q: %v", tc.names, err)
			}
		})
	}
}

// And the configuration those refusals describe the absence of is accepted,
// which is what keeps the test above from passing on a parser that refuses
// everything.
func TestIntrospectionAccepted(t *testing.T) {
	file, err := Parse([]byte(`apiVersion: lotsman.dev/v1alpha1
server:
  inboundAuth:
    mode: oauth
    oauth:
      issuer: https://as.example.com
      resource: https://lotsman.example.com
      introspection:
        url: https://as.example.com/introspect
        clientID: lotsman
        clientSecretRef: env:AS_SECRET
        timeout: 3s
`))
	if err != nil {
		t.Fatalf("a usable introspection configuration was refused: %v", err)
	}
	got := file.Server.InboundAuth.OAuth.Introspection
	if got.URL != "https://as.example.com/introspect" || got.ClientID != "lotsman" {
		t.Errorf("the introspection settings did not survive parsing: %+v", got)
	}
	if got.ClientSecretRef != "env:AS_SECRET" {
		t.Errorf("clientSecretRef = %q", got.ClientSecretRef)
	}
	if got.Timeout != "3s" {
		t.Errorf("timeout = %q", got.Timeout)
	}
}
