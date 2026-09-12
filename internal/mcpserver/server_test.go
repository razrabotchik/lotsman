package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/mcpserver"
	"github.com/razrabotchik/lotsman/internal/policy"
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
func TestListToolsPublishesEverySupportedOperation(t *testing.T) {
	cat := catalog.Catalog{
		Tools: []catalog.Tool{
			{
				Name: "getPet", OperationKey: "ns:GET:/pets/{petId}", Method: "GET",
				PathTemplate: "/pets/{petId}", Description: "Get a pet.",
				Effect: domain.EffectDecision{Effect: domain.EffectRead},
			},
			{
				Name: "createPet", OperationKey: "ns:POST:/pets", Method: "POST", PathTemplate: "/pets",
				Effect:         domain.EffectDecision{Effect: domain.EffectUnknown},
				PolicyBlockers: []domain.ReasonCode{domain.ReasonPolicyUnknownEffectBlocked},
				PolicyMessage:  "effect could not be determined",
			},
		},
	}
	session := connect(t, mcpserver.Options{Catalog: &cat})

	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		names[tool.Name] = tool
	}
	for _, want := range []string{"ping", "getPet", "createPet"} {
		if _, ok := names[want]; !ok {
			t.Errorf("%s missing from tools/list: got %v", want, res.Tools)
		}
	}

	getPet := names["getPet"]
	if getPet.Description != "Get a pet." {
		t.Errorf("getPet description = %q", getPet.Description)
	}
	if getPet.Annotations == nil || !getPet.Annotations.ReadOnlyHint {
		t.Error("getPet must carry readOnlyHint: its effect is read")
	}
	if getPet.Annotations.DestructiveHint != nil && *getPet.Annotations.DestructiveHint {
		t.Error("a read operation must not be advertised as destructive")
	}

	// FR-42: anything that is not a known read is advertised conservatively,
	// including an operation whose effect could not be determined.
	createPet := names["createPet"]
	if createPet.Annotations == nil || createPet.Annotations.ReadOnlyHint {
		t.Error("createPet must not carry readOnlyHint")
	}
	if createPet.Annotations.DestructiveHint == nil || !*createPet.Annotations.DestructiveHint {
		t.Error("an unknown effect must be advertised as potentially destructive")
	}
}

// A published tool is not a permitted one: a policy-blocked call must refuse
// before the network and say what would change it.
func TestCallPolicyBlockedToolRefusesBeforeNetwork(t *testing.T) {
	transport := &countingTransport{}
	cat := catalog.Catalog{Tools: []catalog.Tool{{
		Name: "createPet", Method: "POST", PathTemplate: "/pets",
		Effect:         domain.EffectDecision{Effect: domain.EffectUnknown},
		Executable:     false,
		PolicyBlockers: []domain.ReasonCode{domain.ReasonPolicyUnknownEffectBlocked},
		PolicyMessage:  "enable execution.allowMutations",
	}}}
	session := connect(t, mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport},
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "createPet"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a policy-blocked tool executed")
	}
	text := ""
	for _, content := range res.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	for _, want := range []string{string(domain.ReasonPolicyUnknownEffectBlocked), "allowMutations"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal %q does not mention %q", text, want)
		}
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
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

// TestCallCatalogToolExecutesParameterlessGET covers T012's checkpoint: a
// parameterless GET tool must hit a live server through lotsman, not just
// report itself as not-implemented.
func TestCallCatalogToolExecutesParameterlessGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/widgets" {
			t.Errorf("upstream got path %s, want /widgets", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"widgets":[]}`))
	}))
	defer srv.Close()

	cat := catalog.Catalog{
		Tools: []catalog.Tool{{Name: "listWidgets", Method: "GET", PathTemplate: "/widgets", Servers: []string{srv.URL}, Executable: true}},
	}
	session := connect(t, mcpserver.Options{Catalog: &cat, HTTPClient: srv.Client(), BaseURL: srv.URL})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "listWidgets"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("listWidgets reported an error: %+v", res.Content)
	}

	var out struct {
		Status      int    `json:"status"`
		ContentType string `json:"contentType"`
		Body        string `json:"body"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", raw, err)
	}
	if out.Status != http.StatusOK || out.Body != `{"widgets":[]}` {
		t.Errorf("result = %+v", out)
	}
}

// TestCallCatalogToolUpstreamErrorIsError checks FR-39 through the whole
// stack: an upstream 4xx must surface as isError with the real status and
// body, not a generic Go error string.
func TestCallCatalogToolUpstreamErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	cat := catalog.Catalog{
		Tools: []catalog.Tool{{Name: "listWidgets", Method: "GET", PathTemplate: "/widgets", Servers: []string{srv.URL}, Executable: true}},
	}
	session := connect(t, mcpserver.Options{Catalog: &cat, HTTPClient: srv.Client(), BaseURL: srv.URL})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "listWidgets"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want isError for an upstream 403")
	}

	var out struct {
		Status int `json:"status"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured content %s: %v", raw, err)
	}
	if out.Status != http.StatusForbidden {
		t.Errorf("Status = %d, want 403 (the real upstream status, not a generic error)", out.Status)
	}
}

// TestCallCatalogToolWithPathParamsStillNotImplemented guards the boundary
// executeHandler introduces: only genuinely parameterless GETs execute.
// widgetTool builds the catalog entry the way catalog.Build would, so the
// published schema and the serializer see the same parameters.
func widgetTool(server string, params ...domain.Parameter) catalog.Tool {
	cat := catalog.Build("sha256:test", []domain.Operation{{
		Key:          "ns:GET:/widgets/{widgetId}",
		Method:       "GET",
		PathTemplate: "/widgets/{widgetId}",
		Servers:      []string{server},
		Input:        domain.InputModel{Parameters: params},
		Effect:       domain.EffectDecision{Effect: domain.EffectRead, Source: domain.EffectSourceHTTPMethod, Confidence: domain.ConfidenceInferred},
		Support:      domain.SupportStatus{Level: domain.SupportSupported},
	}}, catalog.Options{})
	return cat.Tools[0]
}

func widgetIDParam() domain.Parameter {
	return domain.Parameter{
		Name: "widgetId", In: domain.LocationPath, Required: true,
		Style: domain.StyleSimple, Schema: domain.Schema{"type": "string"},
	}
}

// TestCallCatalogToolExecutesWithParameters is Step 5's checkpoint: a tool
// with a path and a query parameter reaches the live API with both values
// serialized, instead of reporting itself as not implemented.
func TestCallCatalogToolExecutesWithParameters(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tool := widgetTool(srv.URL, widgetIDParam(), domain.Parameter{
		Name: "tags", In: domain.LocationQuery, Style: domain.StyleForm, Explode: true,
		Schema: domain.Schema{"type": "array", "items": map[string]any{"type": "string"}},
	})
	session := connect(t, mcpserver.Options{
		Catalog:    &catalog.Catalog{Tools: []catalog.Tool{tool}},
		HTTPClient: srv.Client(), BaseURL: srv.URL,
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: tool.Name,
		Arguments: map[string]any{
			"path":  map[string]any{"widgetId": "w 1/2"},
			"query": map[string]any{"tags": []any{"red", "blue"}},
		},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("call failed: %+v", res.Content)
	}
	if want := "/widgets/w%201%2F2?tags=red&tags=blue"; gotURL != want {
		t.Errorf("upstream URL = %q, want %q", gotURL, want)
	}
}

func TestCallCatalogToolPublishesGroupedInputSchema(t *testing.T) {
	tool := widgetTool("https://api.example.com", widgetIDParam())
	session := connect(t, mcpserver.Options{Catalog: &catalog.Catalog{Tools: []catalog.Tool{tool}}})

	res, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	for _, published := range res.Tools {
		if published.Name != tool.Name {
			continue
		}
		schema, ok := published.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("InputSchema = %T, want an object", published.InputSchema)
		}
		properties, _ := schema["properties"].(map[string]any)
		if _, ok := properties[domain.GroupPath]; !ok {
			t.Errorf("published schema has no %q group: %+v", domain.GroupPath, schema)
		}
		if schema["additionalProperties"] != false {
			t.Errorf("published schema allows extra arguments: %+v", schema)
		}
		return
	}
	t.Fatalf("%s missing from tools/list", tool.Name)
}

// A bad argument must be refused before anything is built, let alone sent.
func TestCallCatalogToolRejectsBadArgumentsBeforeNetwork(t *testing.T) {
	for _, tt := range []struct {
		name string
		args map[string]any
	}{
		{"missing required path parameter", map[string]any{}},
		{"unknown argument", map[string]any{
			"path":  map[string]any{"widgetId": "w-1"},
			"query": map[string]any{"admin": true},
		}},
		{"wrong type", map[string]any{"path": map[string]any{"widgetId": 7}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := &countingTransport{}
			tool := widgetTool("https://api.example.com", widgetIDParam())
			session := connect(t, mcpserver.Options{
				Catalog: &catalog.Catalog{Tools: []catalog.Tool{tool}},
				BaseURL: "https://api.example.com", HTTPClient: &http.Client{Transport: transport},
			})

			res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: tt.args})
			if err == nil && !res.IsError {
				t.Fatalf("invalid arguments were accepted: %+v", res.StructuredContent)
			}
			if transport.calls != 0 {
				t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
			}
		})
	}
}

type countingTransport struct {
	calls int
}

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, errors.New("unexpected network call")
}

func TestCallCatalogToolBlockedBeforeNetworkWithoutAuthorizedBaseURL(t *testing.T) {
	transport := &countingTransport{}
	cat := catalog.Catalog{Tools: []catalog.Tool{{
		Name: "listWidgets", Method: "GET", PathTemplate: "/widgets",
		Servers: []string{"https://spec-controlled.example"}, Executable: true,
	}}}
	session := connect(t, mcpserver.Options{Catalog: &cat, HTTPClient: &http.Client{Transport: transport}})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "listWidgets"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatal("call without explicit base URL succeeded")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
}

func TestCallCatalogToolWithCapabilityBlockerNeverReachesNetwork(t *testing.T) {
	transport := &countingTransport{}
	cat := catalog.Catalog{Tools: []catalog.Tool{{
		Name: "listWidgets", Method: "GET", PathTemplate: "/widgets", Executable: false,
		ExecutionBlockers: []domain.ReasonCode{domain.ReasonParametersNotImplemented},
	}}}
	session := connect(t, mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport},
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "listWidgets"})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatal("blocked operation succeeded")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
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

// TestCallCatalogToolEnforcesFormatsTheSDKTreatsAsAnnotations shows why
// lotsman validates even though the SDK also validates against the same
// published schema: JSON Schema treats "format" as an annotation, and the
// SDK's validator follows that, so a value that is not the uuid the spec
// declared would otherwise reach the API.
func TestCallCatalogToolEnforcesFormatsTheSDKTreatsAsAnnotations(t *testing.T) {
	transport := &countingTransport{}
	tool := widgetTool("https://api.example.com", domain.Parameter{
		Name: "widgetId", In: domain.LocationPath, Required: true,
		Style: domain.StyleSimple, Schema: domain.Schema{"type": "string", "format": "uuid"},
	})
	session := connect(t, mcpserver.Options{
		Catalog: &catalog.Catalog{Tools: []catalog.Tool{tool}},
		BaseURL: "https://api.example.com", HTTPClient: &http.Client{Transport: transport},
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      tool.Name,
		Arguments: map[string]any{"path": map[string]any{"widgetId": "not-a-uuid"}},
	})
	if err == nil && !res.IsError {
		t.Fatalf("a value violating format: uuid was accepted: %+v", res.StructuredContent)
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
}

// TestCallParameterlessCatalogToolWithoutArguments pins the same thing one
// layer up, through the real tool registration: a catalog-built tool with no
// parameters is callable with no arguments.
func TestCallParameterlessCatalogToolWithoutArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cat := catalog.Build("sha256:test", []domain.Operation{{
		Key: "ns:GET:/health", Method: "GET", PathTemplate: "/health",
		Servers: []string{srv.URL},
		Effect:  domain.EffectDecision{Effect: domain.EffectRead, Source: domain.EffectSourceHTTPMethod, Confidence: domain.ConfidenceInferred},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}}, catalog.Options{})
	session := connect(t, mcpserver.Options{Catalog: &cat, HTTPClient: srv.Client(), BaseURL: srv.URL})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: cat.Tools[0].Name})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("a parameterless tool rejected a call with no arguments: %+v", res.Content)
	}
}

// createPetOperation is a POST with a JSON body, the shape Step 6 gates.
func createPetOperation(server string) domain.Operation {
	return domain.Operation{
		Key:          "ns:POST:/pets",
		Method:       "POST",
		PathTemplate: "/pets",
		Servers:      []string{server},
		Effect: domain.EffectDecision{
			Effect: domain.EffectUnknown, Source: domain.EffectSourceHTTPMethod, Confidence: domain.ConfidenceInferred,
		},
		Input: domain.InputModel{Body: &domain.BodySpec{
			MediaType: "application/json",
			Required:  true,
			Schema: domain.Schema{
				"type":                 "object",
				"properties":           map[string]any{"name": map[string]any{"type": "string"}},
				"required":             []any{"name"},
				"additionalProperties": false,
			},
		}},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}
}

// TestMutationBlockedByDefault is the first half of Step 6's checkpoint: with
// the default policy, a POST is published but never reaches the network.
func TestMutationBlockedByDefault(t *testing.T) {
	transport := &countingTransport{}
	cat := catalog.Build("sha256:test", []domain.Operation{createPetOperation("https://api.example.com")}, catalog.Options{})
	session := connect(t, mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport},
	})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !res.IsError {
		t.Fatal("a mutation executed under the default read-only policy")
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
}

// TestMutationExecutesWhenAllowed is the second half: the same operation, the
// same arguments, with mutations enabled -- the body reaches the API.
func TestMutationExecutesWhenAllowed(t *testing.T) {
	type received struct {
		method      string
		contentType string
		body        string
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{method: r.Method, contentType: r.Header.Get("Content-Type"), body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	cat := catalog.Build("sha256:test", []domain.Operation{createPetOperation(srv.URL)},
		catalog.Options{Policy: policy.Config{AllowMutations: true}})
	session := connect(t, mcpserver.Options{Catalog: &cat, HTTPClient: srv.Client(), BaseURL: srv.URL})

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{"name": "Murka"}},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("an allowed mutation failed: %+v", res.Content)
	}

	select {
	case upstream := <-got:
		if upstream.method != http.MethodPost {
			t.Errorf("method = %s, want POST", upstream.method)
		}
		if upstream.contentType != "application/json" {
			t.Errorf("Content-Type = %q", upstream.contentType)
		}
		if upstream.body != `{"name":"Murka"}` {
			t.Errorf("body = %s", upstream.body)
		}
	default:
		t.Fatal("upstream received no request")
	}
}

// Enabling mutations does not disable validation: the body is still checked
// against the published schema before anything is sent.
func TestAllowedMutationStillValidatesItsBody(t *testing.T) {
	transport := &countingTransport{}
	cat := catalog.Build("sha256:test", []domain.Operation{createPetOperation("https://api.example.com")},
		catalog.Options{Policy: policy.Config{AllowMutations: true}})
	session := connect(t, mcpserver.Options{
		Catalog: &cat, BaseURL: "https://api.example.com",
		HTTPClient: &http.Client{Transport: transport},
	})

	for _, args := range []map[string]any{
		{"body": map[string]any{"nickname": "Murka"}}, // undeclared field
		{"body": map[string]any{}},                    // missing required field
		{},                                            // missing required body
	} {
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: cat.Tools[0].Name, Arguments: args})
		if err == nil && !res.IsError {
			t.Errorf("invalid body %v was accepted", args)
		}
	}
	if transport.calls != 0 {
		t.Fatalf("RoundTrip calls = %d, want zero", transport.calls)
	}
}

// A recursive schema has to survive the whole publication path: the SDK
// resolves the published schema, lotsman compiles the same document for
// validation, and both must handle "#/$defs" without inlining anything.
func TestRecursiveSchemaSurvivesPublicationAndValidation(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	node := domain.Schema{
		"type": "object",
		"properties": map[string]any{
			"label": map[string]any{"type": "string"},
			"child": map[string]any{"$ref": "#/$defs/Node"},
		},
		"required":             []any{"label"},
		"additionalProperties": false,
	}
	op := domain.Operation{
		Key: "ns:POST:/nodes", Method: "POST", PathTemplate: "/nodes", Servers: []string{srv.URL},
		Effect: domain.EffectDecision{Effect: domain.EffectUnknown},
		Input: domain.InputModel{
			Body: &domain.BodySpec{MediaType: "application/json", Required: true,
				Schema: domain.Schema{"$ref": "#/$defs/Node"}},
			Defs: domain.SchemaDefs{"Node": node},
		},
		Support: domain.SupportStatus{Level: domain.SupportSupported},
	}
	cat := catalog.Build("sha256:test", []domain.Operation{op},
		catalog.Options{Policy: policy.Config{AllowMutations: true}})
	session := connect(t, mcpserver.Options{Catalog: &cat, HTTPClient: srv.Client(), BaseURL: srv.URL})

	// A well-formed nested value is accepted and sent.
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{
			"label": "root",
			"child": map[string]any{"label": "leaf"},
		}},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if res.IsError {
		t.Fatalf("a recursive schema broke the call: %+v", res.Content)
	}
	if gotBody != `{"child":{"label":"leaf"},"label":"root"}` {
		t.Errorf("upstream body = %s", gotBody)
	}

	// And the recursion is validated at depth, not just at the top level.
	bad, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: cat.Tools[0].Name,
		Arguments: map[string]any{"body": map[string]any{
			"label": "root",
			"child": map[string]any{"nope": true},
		}},
	})
	if err == nil && !bad.IsError {
		t.Error("an invalid nested node was accepted")
	}
}
