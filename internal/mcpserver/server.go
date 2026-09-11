package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/requestbuild"
	"github.com/razrabotchik/lotsman/internal/response"
)

// instructions are sent to the client at initialize time. They state the one
// promise that matters before full parameter/policy support exists: nothing
// is approximated.
const instructions = "lotsman exposes an OpenAPI specification as MCP tools. " +
	"Operations that cannot be translated safely are not published and never " +
	"execute approximately; mutations are blocked unless explicitly allowed. " +
	"Parameterless GET tools execute for real; GET tools that need path " +
	"parameters are visible but not callable yet (T014-018), and return an " +
	"explicit not-implemented error rather than a guess."

// defaultTimeout bounds an upstream call. No config wiring exists yet
// (T032 adds full connect/TLS/header/body budgets); this is the v0
// hardcoded default, matching the example config in docs/spec.md
// (execution.timeout).
const defaultTimeout = 30 * time.Second

// Options configures a server. The zero value is usable: logging is
// discarded and only the ping tool is served.
type Options struct {
	// Logger receives server activity. It MUST NOT write to stdout: on stdio
	// transport stdout carries protocol frames only.
	Logger *slog.Logger

	// Catalog, when set, publishes its GET tools alongside ping. Other
	// methods wait for the mutation policy gate (T019) before publication.
	Catalog *catalog.Catalog

	// BaseURL overrides every tool's own servers (FR-30's --base-url).
	BaseURL string

	// HTTPClient executes tool calls; a default with defaultTimeout is used
	// when nil. Tests inject one pointed at an httptest server.
	HTTPClient *http.Client

	// now is the clock used by tool handlers; tests override it.
	now func() time.Time
}

func (o Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (o Options) clock() func() time.Time {
	if o.now != nil {
		return o.now
	}
	return time.Now
}

func (o Options) httpClient() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: defaultTimeout}
}

// New builds the MCP server and registers its tools: the hardcoded ping tool
// (it proves the transport, the tool registration path and the client's
// schema handling before any OpenAPI document is involved -- see
// docs/adr/0004-mcp-client-notes.md) plus, when a Catalog is supplied, its
// GET tools.
func New(opts Options) *mcp.Server {
	info := buildinfo.Get()
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    info.Name,
		Title:   "lotsman",
		Version: info.Version,
	}, &mcp.ServerOptions{
		Instructions: instructions,
		Logger:       sdkLogger(opts.logger()),
	})
	addPing(srv, opts.clock())
	addCatalogTools(srv, catalogGETTools(opts.Catalog), opts.httpClient(), opts.BaseURL)
	return srv
}

// ServeStdio runs the server on stdin/stdout until the context is cancelled or
// the client disconnects. A clean disconnect is not an error.
func ServeStdio(ctx context.Context, opts Options) error {
	log := opts.logger()
	info := buildinfo.Get()
	log.Info("serving mcp over stdio",
		"version", info.Version,
		"commit", info.Commit,
		"mcp_sdk", info.MCPSDKVersion,
		"mcp_protocol", info.MCPProtocolVersion,
		"tools", 1+len(catalogGETTools(opts.Catalog)))
	err := New(opts).Run(ctx, &mcp.StdioTransport{})
	switch {
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		log.Info("shutting down on signal")
		return nil
	case isCleanDisconnect(err):
		log.Info("client disconnected")
		return nil
	default:
		return err
	}
}

// codeServerClosing is the JSON-RPC code the SDK reports when the session is
// torn down while requests are still in flight. A client that closes stdin
// right after its last call produces exactly this ("server is closing: EOF"),
// so it is a normal end of session, not a failure. The SDK exposes the code on
// the wire but no sentinel error for it — see docs/adr/0004-mcp-client-notes.md.
const codeServerClosing = -32004

// isCleanDisconnect reports whether err is the ordinary end of a stdio session.
func isCleanDisconnect(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	var wire *jsonrpc.Error
	return errors.As(err, &wire) && wire.Code == codeServerClosing
}

// PingInput is the argument schema of the ping tool.
type PingInput struct {
	Message string `json:"message,omitempty" jsonschema:"optional text echoed back verbatim"`
}

// PingOutput is the structured result of the ping tool.
type PingOutput struct {
	Pong            bool   `json:"pong"`
	Server          string `json:"server"`
	Version         string `json:"version"`
	ProtocolVersion string `json:"protocolVersion"`
	ReceivedAt      string `json:"receivedAt"`
	Echo            string `json:"echo,omitempty"`
}

func addPing(srv *mcp.Server, now func() time.Time) {
	tool := &mcp.Tool{
		Name:        "ping",
		Title:       "Ping lotsman",
		Description: "Check that lotsman is reachable. Returns the server version and echoes an optional message. Performs no network I/O.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  ptr(false),
		},
	}

	mcp.AddTool(srv, tool, func(_ context.Context, _ *mcp.CallToolRequest, in PingInput) (*mcp.CallToolResult, PingOutput, error) {
		info := buildinfo.Get()
		return nil, PingOutput{
			Pong:            true,
			Server:          info.Name,
			Version:         info.Version,
			ProtocolVersion: info.MCPProtocolVersion,
			ReceivedAt:      now().UTC().Format(time.RFC3339),
			Echo:            in.Message,
		}, nil
	})
}

// catalogGETTools returns cat's GET tools, or nil for a nil Catalog. Other
// methods are not published yet: executing them needs the mutation policy
// gate (T019), which does not exist.
func catalogGETTools(cat *catalog.Catalog) []catalog.Tool {
	if cat == nil {
		return nil
	}
	var out []catalog.Tool
	for _, t := range cat.Tools {
		if t.Method == "GET" {
			out = append(out, t)
		}
	}
	return out
}

// addCatalogTools registers each catalog tool. A tool whose path template
// has no "{...}" placeholder is genuinely callable (T012: parameterless GET
// execution); everything else stays visible but honestly not-implemented
// rather than approximating a call with parameters this pipeline stage
// cannot supply (T014-018 add them).
func addCatalogTools(srv *mcp.Server, tools []catalog.Tool, client *http.Client, baseURL string) {
	for _, t := range tools {
		tool := &mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:  true,
				OpenWorldHint: ptr(true),
			},
		}
		if strings.Contains(t.PathTemplate, "{") {
			mcp.AddTool(srv, tool, notImplementedHandler(t, "needs parameters, not supported yet (T014-018)"))
			continue
		}
		mcp.AddTool(srv, tool, executeHandler(t, client, baseURL))
	}
}

// notImplementedHandler never touches the network: it exists so a tool can
// be listed and schema-validated before it is genuinely callable.
func notImplementedHandler(t catalog.Tool, reason string) mcp.ToolHandlerFor[struct{}, any] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, fmt.Errorf("lotsman: %s %s %s", t.Method, t.PathTemplate, reason)
	}
}

// executeHandler runs the runtime call order for a parameterless GET
// (docs/pipeline.md stages 6-8, minus auth/egress -- those land with
// T029-31/T032): build the request, execute it, shape the response.
//
// An upstream 4xx/5xx is not a Go error: it is a successful tool call that
// reports isError=true with the upstream status and body, so the model sees
// what the API actually said (FR-39) instead of a generic failure message.
func executeHandler(t catalog.Tool, client *http.Client, baseURL string) mcp.ToolHandlerFor[struct{}, response.Result] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, response.Result, error) {
		req, err := requestbuild.Build(ctx, t.Method, t.PathTemplate, t.Servers, requestbuild.Options{BaseURL: baseURL})
		if err != nil {
			return nil, response.Result{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, response.Result{}, fmt.Errorf("lotsman: %s %s: %w", t.Method, t.PathTemplate, err)
		}
		result, err := response.FromHTTP(resp)
		if err != nil {
			return nil, response.Result{}, err
		}
		var res *mcp.CallToolResult
		if result.IsError {
			res = &mcp.CallToolResult{IsError: true}
		}
		return res, result, nil
	}
}

func ptr[T any](v T) *T { return &v }
