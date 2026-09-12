package main_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// canarySecret is a value that must never appear in anything lotsman writes.
const canarySecret = "CANARY-a7f3e9d1c5b28046-DO-NOT-LEAK"

// TestCanarySecretNeverLeaks is FR-61's test: a real credential, a real call,
// and every channel lotsman writes to checked for the value (pitfall #14).
//
// The upstream deliberately echoes the Authorization header back in its
// response body -- the nastiest case, because a token that arrives as *data*
// would otherwise flow straight into a model's context.
func TestCanarySecretNeverLeaks(t *testing.T) {
	bin := buildBinary(t)
	ctx := t.Context()

	var sawCredential bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+canarySecret {
			sawCredential = true
		}
		w.Header().Set("Content-Type", "application/json")
		// The API hands the credential back as data.
		_, _ = w.Write([]byte(`{"youSent":"` + r.Header.Get("Authorization") + `"}`))
	}))
	defer api.Close()

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
	configPath := filepath.Join(dir, "lotsman.yaml")
	if err := os.WriteFile(configPath, []byte(`apiVersion: lotsman.dev/v1alpha1
authProfiles:
  bearerAuth:
    scheme: bearer
    tokenRef: env:LOTSMAN_CANARY
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "serve", specPath, "--config", configPath,
		"--base-url", api.URL, "--log-level", "debug")
	cmd.Env = append(os.Environ(), "LOTSMAN_CANARY="+canarySecret)
	var stderr syncBuffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "canary", Version: "v0"}, approving())
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	}()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_profile"})
	if err != nil {
		t.Fatalf("tools/call: %v\nstderr:\n%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("the authenticated call failed: %+v\nstderr:\n%s", res.Content, stderr.String())
	}
	if !sawCredential {
		t.Fatal("the upstream never received the credential, so this test proves nothing")
	}

	// 1. The tool result, which is what reaches a model's context.
	result, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result), canarySecret) {
		t.Errorf("the credential reached the tool result:\n%s", result)
	}

	// 2. Everything the server wrote to stderr, at debug level.
	if strings.Contains(stderr.String(), canarySecret) {
		t.Errorf("the credential reached the log:\n%s", stderr.String())
	}

	// 3. The report, which an operator may paste into an issue.
	stdout, reportErr, code := runCLI(t, "inspect", specPath, "--config", configPath, "--json")
	if code != 0 {
		t.Fatalf("inspect failed: %s", reportErr)
	}
	if strings.Contains(stdout, canarySecret) || strings.Contains(reportErr, canarySecret) {
		t.Error("the credential reached the report")
	}
	// The report does name the reference, which is the point of references.
	if !strings.Contains(stdout, "bearerAuth") {
		t.Errorf("the report does not say which profile the operation uses:\n%s", stdout)
	}

	// 4. An error path: the same call with the secret unavailable.
	_, missingErr, _ := runCLI(t, "operations", specPath, "--config", configPath)
	if strings.Contains(missingErr, canarySecret) {
		t.Error("the credential reached an error path")
	}
}
