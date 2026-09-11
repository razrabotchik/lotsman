package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
)

// connect wires a client to a server over in-memory transports.
func connect(t *testing.T, opts mcpserver.Options) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpserver.New(opts).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Wait() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func TestListToolsPublishesPing(t *testing.T) {
	session := connect(t, mcpserver.Options{})

	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(res.Tools) != 1 {
		t.Fatalf("got %d tools, want exactly the hardcoded ping", len(res.Tools))
	}

	tool := res.Tools[0]
	if tool.Name != "ping" {
		t.Errorf("tool name = %q, want ping", tool.Name)
	}
	if len(tool.Name) > 64 {
		t.Errorf("tool name is %d chars, MCP client budget is 64", len(tool.Name))
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Error("ping must carry readOnlyHint: it performs no I/O")
	}
	if tool.InputSchema == nil {
		t.Fatal("ping has no input schema; clients reject tools without one")
	}
	// Over the wire the schema is plain JSON (mcp.Tool.InputSchema is `any`),
	// which is what a client such as Claude Desktop has to make sense of.
	schema := decodeSchema(t, tool.InputSchema)
	if schema.Type != "object" {
		t.Errorf("input schema type = %q, want object", schema.Type)
	}
	// The optional message must not be required: an agent may call ping bare.
	if len(schema.Required) != 0 {
		t.Errorf("input schema requires %v, want no required properties", schema.Required)
	}
	if _, ok := schema.Properties["message"]; !ok {
		t.Errorf("input schema has no message property: %+v", schema.Properties)
	}
}

func TestCallPingEchoesAndReportsIdentity(t *testing.T) {
	fixed := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	session := connect(t, mcpserver.OptionsForTest(fixed))

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "ping",
		Arguments: map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("ping reported an error: %+v", res.Content)
	}

	var out mcpserver.PingOutput
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", raw, err)
	}

	want := mcpserver.PingOutput{
		Pong:            true,
		Server:          buildinfo.Name,
		Version:         buildinfo.Version(),
		ProtocolVersion: buildinfo.MCPProtocolVersion,
		ReceivedAt:      "2026-09-11T12:00:00Z",
		Echo:            "hello",
	}
	if out != want {
		t.Errorf("ping output = %+v, want %+v", out, want)
	}
}

// TestListToolsPublishesCatalogGETTools covers T011's Step 3 checkpoint:
// a parsed spec's GET operations must be visible in tools/list, while
// non-GET operations stay unpublished until the mutation policy gate (T019).
func TestListToolsPublishesCatalogGETTools(t *testing.T) {
	cat := catalog.Catalog{
		Tools: []catalog.Tool{
			{Name: "getPet", OperationKey: "ns:GET:/pets/{petId}", Method: "GET", PathTemplate: "/pets/{petId}", Description: "Get a pet."},
			{Name: "createPet", OperationKey: "ns:POST:/pets", Method: "POST", PathTemplate: "/pets"},
		},
	}
	session := connect(t, mcpserver.Options{Catalog: &cat})

	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(res.Tools) != 2 { // ping + getPet, not createPet
		t.Fatalf("got %d tools, want ping + getPet only: %+v", len(res.Tools), res.Tools)
	}

	names := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool
	}
	if _, ok := names["ping"]; !ok {
		t.Error("ping missing from tools/list")
	}
	getPet, ok := names["getPet"]
	if !ok {
		t.Fatal("getPet missing from tools/list")
	}
	if getPet.Description != "Get a pet." {
		t.Errorf("getPet description = %q, want %q", getPet.Description, "Get a pet.")
	}
	if getPet.Annotations == nil || !getPet.Annotations.ReadOnlyHint {
		t.Error("getPet must carry readOnlyHint: it is a GET")
	}
	if _, ok := names["createPet"]; ok {
		t.Error("createPet (POST) must not be published yet")
	}
}

// TestCallCatalogToolReturnsNotImplemented guards the honesty contract: a
// visible-but-unexecutable tool must say so explicitly (isError), never
// approximate a result.
func TestCallCatalogToolReturnsNotImplemented(t *testing.T) {
	cat := catalog.Catalog{
		Tools: []catalog.Tool{{Name: "getPet", OperationKey: "ns:GET:/pets/{petId}", Method: "GET", PathTemplate: "/pets/{petId}"}},
	}
	session := connect(t, mcpserver.Options{Catalog: &cat})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "getPet"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("getPet call succeeded, want isError: %+v", res.StructuredContent)
	}
}

func TestCallPingRejectsUnknownArgument(t *testing.T) {
	session := connect(t, mcpserver.Options{})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "ping",
		Arguments: map[string]any{"smuggled": "value"},
	})
	// Either a protocol error or an error result is acceptable; silently
	// dropping an unknown argument is not (data-model.md invariant 4).
	if err != nil {
		return
	}
	if !res.IsError {
		t.Errorf("unknown argument accepted: %+v", res.StructuredContent)
	}
}

func TestServeStdioLogsToTheProvidedWriterOnly(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- mcpserver.ServeStdio(ctx, mcpserver.Options{Logger: logger}) }()

	// Under `go test` stdin is already at EOF, so the session ends immediately;
	// that is a clean client disconnect, not a failure.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeStdio: %v (logs: %s)", err, logs.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeStdio did not return after stdin EOF")
	}

	out := logs.String()
	for _, want := range []string{
		"serving mcp over stdio",
		"mcp_protocol=" + buildinfo.MCPProtocolVersion,
		"client disconnected",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("startup logs lack %q:\n%s", want, out)
		}
	}
}

// toolSchema is the subset of a published JSON Schema the tests assert on.
type toolSchema struct {
	Type       string                     `json:"type"`
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
}

func decodeSchema(t *testing.T, v any) toolSchema {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var s toolSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode schema %s: %v", raw, err)
	}
	return s
}

func TestSDKLoggerDemotesCleanDisconnect(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	sdk := mcpserver.SDKLoggerForTest(logger)
	sdk.Error("server session ended with error", "error", mcpserver.CleanDisconnectErrForTest())
	sdk.Error("something actually broke", "error", errors.New("boom"))

	out := logs.String()
	if strings.Contains(out, "server session ended with error") {
		t.Errorf("clean disconnect was logged at info level:\n%s", out)
	}
	if !strings.Contains(out, "something actually broke") {
		t.Errorf("a real error was swallowed:\n%s", out)
	}
	if !strings.Contains(out, "component=mcp-sdk") {
		t.Errorf("sdk logs are not tagged with their origin:\n%s", out)
	}
}

func TestSDKLoggerKeepsCleanDisconnectAtDebug(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mcpserver.SDKLoggerForTest(logger).Error("server session ended with error",
		"error", mcpserver.CleanDisconnectErrForTest())

	if out := logs.String(); !strings.Contains(out, "level=DEBUG") {
		t.Errorf("clean disconnect should stay visible at debug level:\n%s", out)
	}
}
