package main_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// buildBinary compiles the CLI once per test binary and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: skipping the subprocess e2e test")
	}

	bin := filepath.Join(t.TempDir(), "lotsman")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestStdioEndToEnd drives the real binary over stdio with the official MCP
// client — the same channel Claude Desktop uses (T003/T004; the full suite of
// scenarios arrives with T039).
func TestStdioEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	ctx := t.Context()

	cmd := exec.Command(bin, "serve", "--log-level", "debug")
	var stderr syncBuffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-e2e", Version: "v0"}, approving())
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect over stdio: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("close session: %v", closeErr)
		}
	}()

	if got := session.InitializeResult().ServerInfo.Name; got != "lotsman" {
		t.Errorf("server name = %q, want lotsman", got)
	}
	if session.InitializeResult().Instructions == "" {
		t.Error("server sent no instructions")
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v\nstderr:\n%s", err, stderr.String())
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "ping" {
		t.Fatalf("tools/list = %+v, want exactly ping", tools.Tools)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ping",
		Arguments: map[string]any{"message": "e2e"},
	})
	if err != nil {
		t.Fatalf("tools/call ping: %v\nstderr:\n%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("ping returned an error result: %+v", res.Content)
	}

	var out struct {
		Pong bool   `json:"pong"`
		Echo string `json:"echo"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", raw, err)
	}
	if !out.Pong || out.Echo != "e2e" {
		t.Errorf("ping output = %s, want pong with echo %q", raw, "e2e")
	}

	// The session above only works if stdout stayed protocol-clean, and the
	// logs must have gone somewhere: stderr (Constitution, Constraints).
	if !strings.Contains(stderr.String(), "serving mcp over stdio") {
		t.Errorf("startup log missing from stderr:\n%s", stderr.String())
	}
}

// TestServeWithSpecPublishesTools covers Step 3's checkpoint end to end: a
// real spec's operations must be visible to an agent over the same stdio
// transport Claude Desktop uses. Since Step 6 that includes the mutating
// ones -- publication is discovery, and the policy gate decides execution.
func TestServeWithSpecPublishesTools(t *testing.T) {
	bin := buildBinary(t)
	ctx := t.Context()

	cmd := exec.Command(bin, "serve", miniSpecPath(t), "--lax", "--log-level", "debug")
	var stderr syncBuffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-e2e", Version: "v0"}, approving())
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect over stdio: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("close session: %v", closeErr)
		}
	}()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v\nstderr:\n%s", err, stderr.String())
	}

	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	// ping plus every supported operation, mutating ones included.
	for _, want := range []string{"ping", "list_pets", "get_pet", "create_pet", "delete_pet"} {
		if !names[want] {
			t.Errorf("tools/list missing %q: got %v", want, names)
		}
	}
	// getOrder is rejected outright (its path placeholder has no parameter),
	// and a rejected operation is never published at all.
	if names["get_order"] {
		t.Error(`tools/list published "get_order", want it withheld: it is rejected, not merely blocked`)
	}

	// Without an explicit --base-url a call is refused before the network:
	// a server URL authored by the spec is not egress authorization (ADR-0005).
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_pets"})
	if err != nil {
		t.Fatalf("tools/call listPets: %v\nstderr:\n%s", err, stderr.String())
	}
	if !res.IsError {
		t.Error("list_pets succeeded without --base-url, want a refusal")
	}
}

// TestServeExecutesParameterizedGETEndToEnd is Step 5's checkpoint over the
// real transport: the agent calls a tool with a path and a query argument,
// and a live HTTP server receives both, serialized per OAS.
func TestServeExecutesParameterizedGETEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	ctx := t.Context()

	requests := make(chan string, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"p-1","name":"Murka"}`))
	}))
	defer api.Close()

	cmd := exec.Command(bin, "serve", miniSpecPath(t), "--lax", "--base-url", api.URL, "--log-level", "debug")
	var stderr syncBuffer
	cmd.Stderr = &stderr

	client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-e2e", Version: "v0"}, approving())
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect over stdio: %v\nstderr:\n%s", err, stderr.String())
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			t.Errorf("close session: %v", closeErr)
		}
	}()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_pet",
		Arguments: map[string]any{
			"path":  map[string]any{"petId": "p 1/2"},
			"query": map[string]any{"verbose": true},
		},
	})
	if err != nil {
		t.Fatalf("tools/call get_pet: %v\nstderr:\n%s", err, stderr.String())
	}
	if res.IsError {
		t.Fatalf("get_pet returned an error result: %+v\nstderr:\n%s", res.Content, stderr.String())
	}

	select {
	case got := <-requests:
		if want := "/pets/p%201%2F2?verbose=true"; got != want {
			t.Errorf("upstream received %q, want %q", got, want)
		}
	default:
		t.Fatal("upstream received no request")
	}

	// A tool whose parameters are all optional must be callable with no
	// arguments at all. Over the wire that arrives as an absent argument
	// object, which is not the same thing as an empty Go map -- a distinction
	// only the real transport exercises.
	bare, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_pets"})
	if err != nil {
		t.Fatalf("tools/call list_pets: %v\nstderr:\n%s", err, stderr.String())
	}
	if bare.IsError {
		t.Errorf("list_pets rejected a call with no arguments: %+v", bare.Content)
	}
	select {
	case got := <-requests:
		if got != "/pets" {
			t.Errorf("upstream received %q, want /pets", got)
		}
	default:
		t.Error("upstream received no request for the argument-less call")
	}

	// The same tool with an argument the schema does not allow must be
	// refused, and the refusal must not reach the API.
	bad, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_pet",
		Arguments: map[string]any{"path": map[string]any{"petId": "p-1"}, "query": map[string]any{"admin": true}},
	})
	if err == nil && !bad.IsError {
		t.Errorf("an undeclared argument was accepted: %+v", bad.StructuredContent)
	}
	select {
	case got := <-requests:
		t.Errorf("upstream was called with %q despite invalid arguments", got)
	default:
	}
}

// TestVersionCommand covers the one command that is allowed to write to stdout.
func TestVersionCommand(t *testing.T) {
	bin := buildBinary(t)

	out, err := exec.Command(bin, "version", "--json").Output()
	if err != nil {
		t.Fatalf("lotsman version --json: %v", err)
	}
	var info struct {
		Name               string `json:"name"`
		MCPProtocolVersion string `json:"mcpProtocolVersion"`
		MCPSDKVersion      string `json:"mcpSdkVersion"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if info.Name != "lotsman" || info.MCPProtocolVersion == "" {
		t.Errorf("version output = %s", out)
	}
	if !strings.HasPrefix(info.MCPSDKVersion, "v1.") {
		t.Errorf("mcp sdk version = %q, want the linked v1.x module version", info.MCPSDKVersion)
	}
}

// TestUnknownCommandFails guards the exit-code contract (FR-77, expanded in T034).
func TestUnknownCommandFails(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "nope")
	var stderr syncBuffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errorAs(err, &exitErr) {
		t.Fatalf("unknown command: err = %v, want a non-zero exit", err)
	}
	if got := exitErr.ExitCode(); got != 2 {
		t.Errorf("exit code = %d, want 2 (usage)", got)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

// TestClientClosesStdinExitsCleanly reproduces how a desktop client shuts a
// stdio server down: it stops writing and closes the pipe while the last
// response is still in flight. That is a disconnect, not a crash — it must not
// produce a non-zero exit or an ERROR log, or the client reports a failed server.
func TestClientClosesStdinExitsCleanly(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "serve")
	var stderr syncBuffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	send := func(frame string) {
		t.Helper()
		if _, err := io.WriteString(stdin, frame+"\n"); err != nil {
			t.Fatalf("write frame: %v\nstderr:\n%s", err, stderr.String())
		}
	}
	responses := bufio.NewScanner(stdout)

	// Handshake in the order a real client uses it: the initialized
	// notification only goes out after the initialize response arrives.
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"manual","version":"0"}}}`)
	if !responses.Scan() {
		t.Fatalf("no initialize response; stderr:\n%s", stderr.String())
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping","arguments":{}}}`)

	// Close without reading the reply: the response is in flight when the pipe
	// goes away.
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("serve exited with %v; stderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "level=ERROR") {
		t.Errorf("clean disconnect logged an error:\n%s", stderr.String())
	}
}

// TestServeMutationPolicyEndToEnd is Step 6's checkpoint over the real
// transport: the same POST, with the same arguments, is refused under the
// default policy and executes when mutations are enabled.
func TestServeMutationPolicyEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	ctx := t.Context()

	type received struct{ method, contentType, body string }
	requests := make(chan received, 4)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- received{method: r.Method, contentType: r.Header.Get("Content-Type"), body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"p-1"}`))
	}))
	defer api.Close()

	callAs := func(t *testing.T, clientOpts *mcp.ClientOptions, extraArgs ...string) *mcp.CallToolResult {
		t.Helper()
		args := append([]string{"serve", miniSpecPath(t), "--lax", "--base-url", api.URL, "--log-level", "debug"}, extraArgs...)
		cmd := exec.Command(bin, args...)
		var stderr syncBuffer
		cmd.Stderr = &stderr

		client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-e2e", Version: "v0"}, clientOpts)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
		if err != nil {
			t.Fatalf("connect over stdio: %v\nstderr:\n%s", err, stderr.String())
		}
		defer func() {
			if closeErr := session.Close(); closeErr != nil {
				t.Errorf("close session: %v", closeErr)
			}
		}()

		// The mutating tool is published either way: publication is
		// discovery, policy decides execution.
		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("tools/list: %v\nstderr:\n%s", err, stderr.String())
		}
		var found bool
		for _, tool := range tools.Tools {
			if tool.Name == "create_pet" {
				found = true
				if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
					t.Error("create_pet must not be advertised as read-only")
				}
			}
		}
		if !found {
			t.Fatalf("create_pet missing from tools/list: %+v", tools.Tools)
		}

		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "create_pet",
			Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
		})
		if err != nil {
			t.Fatalf("tools/call create_pet: %v\nstderr:\n%s", err, stderr.String())
		}
		return res
	}
	call := func(t *testing.T, extraArgs ...string) *mcp.CallToolResult {
		t.Helper()
		return callAs(t, approving(), extraArgs...)
	}

	t.Run("blocked by default", func(t *testing.T) {
		res := call(t)
		if !res.IsError {
			t.Fatalf("a mutation executed under the default policy: %+v", res.StructuredContent)
		}
		select {
		case got := <-requests:
			t.Fatalf("upstream was called: %+v", got)
		default:
		}
	})

	t.Run("executes when allowed", func(t *testing.T) {
		res := call(t, "--allow-mutations")
		if res.IsError {
			t.Fatalf("an allowed mutation failed: %+v", res.Content)
		}
		select {
		case got := <-requests:
			if got.method != http.MethodPost || got.contentType != "application/json" || got.body != `{"name":"Murka"}` {
				t.Errorf("upstream received %+v", got)
			}
		default:
			t.Fatal("upstream received no request")
		}
	})

	// Acceptance criterion 6 (FR-45): mutations enabled, approval required by
	// default, and a client that never declared it can be asked. The refusal
	// happens before the network, over the real transport, against the real
	// binary.
	t.Run("fails closed when the client cannot be asked", func(t *testing.T) {
		res := callAs(t, nil, "--allow-mutations")
		if !res.IsError {
			t.Fatalf("a mutation ran without approval: %+v", res.StructuredContent)
		}
		if text := resultText(res); !strings.Contains(text, string(domain.ReasonApprovalUnavailable)) {
			t.Errorf("refusal = %q, want it to name %s", text, domain.ReasonApprovalUnavailable)
		}
		select {
		case got := <-requests:
			t.Fatalf("upstream was called: %+v", got)
		default:
		}
	})

	// The operator can decide otherwise, and then the same call goes through.
	t.Run("approval can be turned off", func(t *testing.T) {
		res := callAs(t, nil, "--allow-mutations", "--approval", "never")
		if res.IsError {
			t.Fatalf("a mutation failed with approval disabled: %+v", res.Content)
		}
		select {
		case <-requests:
		default:
			t.Fatal("upstream received no request")
		}
	})
}

// resultText is whatever the server put in the result's content blocks.
func resultText(res *mcp.CallToolResult) string {
	var text strings.Builder
	for _, content := range res.Content {
		if block, ok := content.(*mcp.TextContent); ok {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

// TestServeSearchModeEndToEnd is feature 002's checkpoint over the real
// transport: a catalog published as five tools, searched, described and called,
// with the mutation gate behaving exactly as it does in tools mode (T111).
func TestServeSearchModeEndToEnd(t *testing.T) {
	bin := buildBinary(t)
	ctx := t.Context()

	type received struct{ method, uri, body string }
	requests := make(chan received, 8)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		requests <- received{method: r.Method, uri: r.URL.RequestURI(), body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"p-1"}`))
	}))
	defer api.Close()

	open := func(t *testing.T, extra ...string) *mcp.ClientSession {
		t.Helper()
		args := append([]string{"serve", miniSpecPath(t), "--lax", "--mode=search",
			"--base-url", api.URL, "--log-level", "warn"}, extra...)
		cmd := exec.Command(bin, args...)
		var stderr syncBuffer
		cmd.Stderr = &stderr

		client := mcp.NewClient(&mcp.Implementation{Name: "search-e2e", Version: "v0"}, approving())
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}, nil)
		if err != nil {
			t.Fatalf("connect: %v\nstderr:\n%s", err, stderr.String())
		}
		t.Cleanup(func() {
			if closeErr := session.Close(); closeErr != nil {
				t.Errorf("close: %v", closeErr)
			}
		})
		return session
	}

	t.Run("five tools, search, describe, read call", func(t *testing.T) {
		session := open(t)

		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		names := map[string]bool{}
		for _, tool := range tools.Tools {
			names[tool.Name] = true
		}
		for _, want := range []string{"ping", "search_operations", "list_tags", "describe_operation", "call_read_operation"} {
			if !names[want] {
				t.Errorf("%s missing: %v", want, names)
			}
		}
		if names["get_pet"] || names["call_mutating_operation"] {
			t.Errorf("unexpected tools published: %v", names)
		}

		// Search, then describe, then call -- the path a model actually walks.
		found, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "search_operations",
			Arguments: map[string]any{"query": "get a pet by id"},
		})
		if err != nil || found.IsError {
			t.Fatalf("search_operations: %v %+v", err, found.Content)
		}
		var search struct {
			Results []struct {
				Key    string `json:"key"`
				Effect string `json:"effect"`
			} `json:"results"`
		}
		decodeStructured(t, found, &search)
		if len(search.Results) == 0 {
			t.Fatal("search found nothing")
		}
		id := search.Results[0].Key
		if id != "default:GET:/pets/{petId}" {
			t.Errorf("top hit = %q", id)
		}

		described, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "describe_operation",
			Arguments: map[string]any{"id": id},
		})
		if err != nil || described.IsError {
			t.Fatalf("describe_operation: %v %+v", err, described.Content)
		}
		var describe struct {
			Callable    bool           `json:"callable"`
			InputSchema map[string]any `json:"inputSchema"`
		}
		decodeStructured(t, described, &describe)
		if !describe.Callable || describe.InputSchema == nil {
			t.Fatalf("describe = %+v", describe)
		}

		called, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "call_read_operation",
			Arguments: map[string]any{
				"id":        id,
				"arguments": map[string]any{"path": map[string]any{"petId": "p 1/2"}},
			},
		})
		if err != nil || called.IsError {
			t.Fatalf("call_read_operation: %v %+v", err, called.Content)
		}
		got := <-requests
		if got.method != http.MethodGet || got.uri != "/pets/p%201%2F2" {
			t.Errorf("upstream received %+v", got)
		}
	})

	t.Run("mutation blocked by default", func(t *testing.T) {
		session := open(t)
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "call_read_operation",
			Arguments: map[string]any{
				"id":        "default:POST:/pets",
				"arguments": map[string]any{"body": map[string]any{"name": "Murka"}},
			},
		})
		if err == nil && !res.IsError {
			t.Fatal("a POST was called through call_read_operation")
		}
		select {
		case got := <-requests:
			t.Fatalf("upstream was called: %+v", got)
		default:
		}
	})

	t.Run("mutation executes when allowed", func(t *testing.T) {
		session := open(t, "--allow-mutations")
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "call_mutating_operation",
			Arguments: map[string]any{
				"id":        "default:POST:/pets",
				"arguments": map[string]any{"body": map[string]any{"name": "Murka"}},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("call_mutating_operation: %v %+v", err, res.Content)
		}
		got := <-requests
		if got.method != http.MethodPost || got.body != `{"name":"Murka"}` {
			t.Errorf("upstream received %+v", got)
		}
	})
}

// decodeStructured reads a tool result's structured content into target.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, target any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}
