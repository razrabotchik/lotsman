package main_test

import (
	"bufio"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	client := mcp.NewClient(&mcp.Implementation{Name: "lotsman-e2e", Version: "v0"}, nil)
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
