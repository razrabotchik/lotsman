package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArgs(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "args.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// FR-31: everything explain-call prints is computed on the path a real call
// takes, and then it stops.
func TestExplainCallShowsTheDecisionTrail(t *testing.T) {
	args := writeArgs(t, `{"path":{"petId":"p 1/2"},"query":{"verbose":true}}`)
	stdout, stderr, code := runCLI(t, "explain-call", "get_pet",
		"--spec", miniSpecPath(t), "--base-url", "https://api.example.com", "--args", args)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	for _, want := range []string{
		"default:GET:/pets/{petId}",
		"get_pet",
		"read (http_method, inferred)",
		"ALLOW",
		// The URL is the one the serializer would build, encoding included.
		"https://api.example.com/pets/p%201%2F2?verbose=true",
		"egress: ALLOW",
		"NO network request was made.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// A call that policy would refuse is explained as a refusal, with the reason
// and what would change it -- which is the question being asked.
func TestExplainCallExplainsARefusal(t *testing.T) {
	stdout, _, code := runCLI(t, "explain-call", "create_pet",
		"--spec", miniSpecPath(t), "--base-url", "https://api.example.com")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"DENY", "policy_unknown_effect_blocked", "allowMutations", "application/json (required)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
}

// The auth line names the profile and the *reference*: which environment
// variable has to be set on the machine that will run this.
func TestExplainCallNamesTheCredentialReferenceNotItsValue(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "secure.yaml")
	if err := os.WriteFile(specPath, []byte(`openapi: 3.0.3
info: { title: Secure, version: "1.0" }
security:
  - bearerAuth: []
paths:
  /profile:
    get:
      operationId: getProfile
      responses: { "200": { description: ok } }
components:
  securitySchemes:
    bearerAuth: { type: http, scheme: bearer }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`apiVersion: lotsman.dev/v1alpha1
authProfiles:
  bearerAuth:
    scheme: bearer
    tokenRef: env:LOTSMAN_CANARY
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOTSMAN_CANARY", canarySecret)

	stdout, _, code := runCLI(t, "explain-call", "get_profile",
		"--spec", specPath, "--config", configPath, "--base-url", "https://api.example.com")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout, "env:LOTSMAN_CANARY") {
		t.Errorf("output does not name the reference:\n%s", stdout)
	}
	if strings.Contains(stdout, canarySecret) {
		t.Errorf("explain-call printed the credential:\n%s", stdout)
	}
}

func TestExplainCallUsage(t *testing.T) {
	if _, _, code := runCLI(t, "explain-call", "get_pet"); code != 2 {
		t.Error("explain-call without --spec should be a usage error")
	}
	if _, _, code := runCLI(t, "explain-call", "no_such_tool", "--spec", miniSpecPath(t)); code != 4 {
		t.Error("an unknown operation should exit 4")
	}
}

// `explain-call` answers "what would happen", and for a minted credential it
// used to answer "unknown scheme" -- the one scheme where the interesting part
// is not a reference to read but a request lotsman makes on the operator's
// behalf. Feature 006 taught the runtime to mint; this printer was not told.
//
// Found by measuring coverage honestly: the CLI is exercised in a subprocess,
// so `-coverprofile` could not see it, and `describeCredential` read 18% with
// nothing pointing at the branch it was missing.
func TestExplainCallDescribesAMintedCredential(t *testing.T) {
	specPath, configPath := mintingFixture(t)

	stdout, stderr, code := runCLI(t, "explain-call", "list_things",
		"--spec", specPath, "--config", configPath, "--base-url", "https://api.example.com")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "unknown scheme") {
		t.Errorf("a supported scheme is described as unknown:\n%s", stdout)
	}
	for _, want := range []string{
		"minted from https://as.example.com/token", // where the token comes from
		"lotsman-explain",                          // as whom
		"scopes read",                              // asking for what
		"Authorization: Bearer",                    // and where it ends up
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the explanation does not say %q:\n%s", want, stdout)
		}
	}
	// The secret is not part of the answer, here as anywhere else.
	if strings.Contains(stdout, canarySecret) {
		t.Errorf("explain-call printed the client secret:\n%s", stdout)
	}
}

// The same question one command earlier: `inspect` summarizes which credential
// an operator has before they serve anything, and for a minting profile the
// useful part is where the token comes from and what it asks for. That branch
// had no test either.
func TestInspectNamesAMintingProfile(t *testing.T) {
	specPath, configPath := mintingFixture(t)

	stdout, stderr, code := runCLI(t, "inspect", specPath, "--config", configPath)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	want := "serviceAuth (oauth2-client-credentials) mints from https://as.example.com/token, scopes read"
	if !strings.Contains(stdout, want) {
		t.Errorf("the credentials line does not say %q:\n%s", want, stdout)
	}
	if strings.Contains(stdout, canarySecret) {
		t.Errorf("inspect printed the client secret:\n%s", stdout)
	}
}

// mintingFixture is a document whose only operation needs a client-credentials
// token, and a configuration that mints it.
func mintingFixture(t *testing.T) (specPath, configPath string) {
	t.Helper()
	specPath = oauthSpec(t, "https://as.example.com/token")
	configPath = filepath.Join(t.TempDir(), "lotsman.yaml")
	if err := os.WriteFile(configPath, []byte(`apiVersion: lotsman.dev/v1alpha1
authProfiles:
  serviceAuth:
    scheme: oauth2-client-credentials
    tokenURL: https://as.example.com/token
    clientID: lotsman-explain
    clientSecretRef: env:LOTSMAN_CANARY
    scopes: [read]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOTSMAN_CANARY", canarySecret)
	return specPath, configPath
}

// A credential that exists and does not fit is the one blocker an operator can
// act on today, and the code alone does not tell them how: they have a profile
// configured, and `authentication_not_implemented` reads as lotsman's gap rather
// than as "your key goes in the query string, not a header".
//
// The sentence was being written all along, in internal/auth, and then dropped
// twice on the way out -- once by the selector's caller and once by the catalog.
func TestTheReportSaysWhyAConfiguredProfileDoesNotFit(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(specPath, []byte(`openapi: 3.0.3
info: { title: Keys, version: "1.0" }
security:
  - queryKey: []
paths:
  /things:
    get: { operationId: listThings, responses: { "200": { description: ok } } }
components:
  securitySchemes:
    queryKey: { type: apiKey, in: query, name: sig }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "lotsman.yaml")
	if err := os.WriteFile(configPath, []byte(`apiVersion: lotsman.dev/v1alpha1
authProfiles:
  queryKey:
    scheme: apikey
    in: header
    name: X-Sig
    tokenRef: env:LOTSMAN_CANARY
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOTSMAN_CANARY", canarySecret)

	stdout, stderr, code := runCLI(t, "inspect", specPath, "--config", configPath, "--json")
	if code != 0 {
		t.Fatalf("inspect failed (%d): %s", code, stderr)
	}
	report := decodeReport(t, stdout)
	if len(report.Operations) != 1 {
		t.Fatalf("expected one operation, got %d", len(report.Operations))
	}
	var detail string
	for _, reason := range report.Operations[0].Reasons {
		if reason.Code == "authentication_not_implemented" {
			detail = reason.Detail
		}
	}
	if !strings.Contains(detail, "carries the key in the query") {
		t.Errorf("the report does not say where the API reads the key: %q", detail)
	}

	// The same answer from `explain-call`, which is where an operator looks
	// when the report has told them there is a problem.
	explained, _, code := runCLI(t, "explain-call", "list_things",
		"--spec", specPath, "--config", configPath, "--base-url", "https://api.example.com")
	if code != 0 {
		t.Fatalf("explain-call exited %d", code)
	}
	if !strings.Contains(explained, "carries the key in the query") {
		t.Errorf("explain-call does not say why the profile does not fit:\n%s", explained)
	}
}
