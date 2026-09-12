package main_test

import (
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

// This file checks the feature's user scenarios (specs/001-core-runtime/spec.md)
// end to end, through the real binary and the official MCP client. Scenarios
// already pinned elsewhere are named here rather than repeated:
//
//	US-2 (inspect before serving)  → inspect_test.go
//	US-3 (read-only default)       → TestServeMutationPolicyEndToEnd
//	US-4 (canary secret)           → TestCanarySecretNeverLeaks

// session starts the binary with the given arguments and returns a connected
// client session plus the server's stderr.
func session(t *testing.T, env []string, args ...string) (*mcp.ClientSession, *syncBuffer) {
	t.Helper()
	cmd := exec.Command(buildBinary(t), args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance", Version: "v0"}, approving())
	connected, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
	}
	t.Cleanup(func() {
		if closeErr := connected.Close(); closeErr != nil {
			t.Errorf("close: %v", closeErr)
		}
	})
	return connected, stderr
}

// TestAcceptanceUS1 is the headline scenario: an agent calls a real API
// through lotsman. A GET with path and query parameters and a JSON POST (with
// mutations enabled) both execute correctly, in one session, against a live
// server -- which is the acceptance criterion as written.
func TestAcceptanceUS1(t *testing.T) {
	type received struct{ method, uri, body, contentType string }
	requests := make(chan received, 8)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		requests <- received{
			method: r.Method, uri: r.URL.RequestURI(),
			body: string(body), contentType: r.Header.Get("Content-Type"),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"p-1"}`))
	}))
	defer api.Close()

	client, stderr := session(t, nil, "serve", miniSpecPath(t), "--lax",
		"--base-url", api.URL, "--allow-mutations", "--log-level", "warn")

	// A GET with a path parameter and a query array.
	res, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "get_pet",
		Arguments: map[string]any{
			"path":  map[string]any{"petId": "p-1"},
			"query": map[string]any{"verbose": true},
		},
	})
	if err != nil || res.IsError {
		t.Fatalf("get_pet failed: %v %+v\nstderr:\n%s", err, res, stderr.String())
	}
	got := <-requests
	if got.method != http.MethodGet || got.uri != "/pets/p-1?verbose=true" {
		t.Errorf("GET arrived as %+v", got)
	}

	// A JSON POST, which only runs because mutations were enabled.
	res, err = client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "create_pet",
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})
	if err != nil || res.IsError {
		t.Fatalf("create_pet failed: %v %+v\nstderr:\n%s", err, res, stderr.String())
	}
	got = <-requests
	if got.method != http.MethodPost || got.contentType != "application/json" || got.body != `{"name":"Murka"}` {
		t.Errorf("POST arrived as %+v", got)
	}

	// A query array serializes as repeated keys, which is the default nobody
	// gets right by accident (pitfall #5).
	res, err = client.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "list_pets",
		Arguments: map[string]any{"query": map[string]any{"tags": []any{"cat", "calm"}, "limit": 2}},
	})
	if err != nil || res.IsError {
		t.Fatalf("list_pets failed: %v %+v", err, res)
	}
	got = <-requests
	if got.uri != "/pets?limit=2&tags=cat&tags=calm" {
		t.Errorf("query array arrived as %q", got.uri)
	}
}

// TestAcceptanceUS5 is the messy-specification scenario: --lax serves what it
// can while the operator is told what was dropped, and nothing unsupported
// executes.
func TestAcceptanceUS5(t *testing.T) {
	client, stderr := session(t, nil, "serve", miniSpecPath(t), "--lax",
		"--base-url", "https://api.example.com", "--log-level", "warn")

	tools, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["list_pets"] || !names["get_pet"] {
		t.Errorf("the supported subset is not served: %v", names)
	}
	// The rejected operation is not published at all -- there is no honest
	// tool to publish for it.
	if names["get_order"] {
		t.Error("a rejected operation was published")
	}
	if _, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_order"}); err == nil {
		t.Error("a rejected operation was callable")
	}

	// And strict mode refuses the whole document rather than quietly serving
	// a subset the operator did not ask for.
	_, strictErr, code := runCLI(t, "serve", miniSpecPath(t), "--base-url", "https://api.example.com")
	if code == 0 {
		t.Error("strict mode accepted a document with a rejected operation")
	}
	if !strings.Contains(strictErr, "--lax") {
		t.Errorf("strict refusal does not mention --lax: %q", strictErr)
	}
	_ = stderr
}

// TestAcceptanceExitCodes checks the contract a pipeline depends on: the exit
// code says which kind of problem it was (FR-77).
func TestAcceptanceExitCodes(t *testing.T) {
	dir := t.TempDir()
	swagger := filepath.Join(dir, "swagger.yaml")
	if err := os.WriteFile(swagger, []byte("swagger: \"2.0\"\ninfo: {title: X, version: \"1\"}\npaths: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("hello: world\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name string
		args []string
		want int
	}{
		{"usable document", []string{"validate", miniSpecPath(t)}, 0},
		{"unknown command", []string{"nonsense"}, 2},
		{"unusable document", []string{"validate", broken}, 3},
		{"unsupported document", []string{"validate", swagger}, 4},
		{"rejected operations in CI", []string{"inspect", miniSpecPath(t), "--fail-on-rejected"}, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, stderr, code := runCLI(t, tt.args...); code != tt.want {
				t.Errorf("exit = %d, want %d\n%s", code, tt.want, stderr)
			}
		})
	}
}
