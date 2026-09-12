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
