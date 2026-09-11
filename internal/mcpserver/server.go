package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
)

// instructions are sent to the client at initialize time. They state the one
// promise that matters before request execution exists: nothing is
// approximated.
const instructions = "lotsman exposes an OpenAPI specification as MCP tools. " +
	"Operations that cannot be translated safely are not published and never " +
	"execute approximately; mutations are blocked unless explicitly allowed. " +
	"GET tools from a parsed spec are visible but do not yet execute (T012); " +
	"calling one returns an explicit not-implemented error rather than a guess."

// Options configures a server. The zero value is usable: logging is
// discarded and only the ping tool is served.
type Options struct {
	// Logger receives server activity. It MUST NOT write to stdout: on stdio
	// transport stdout carries protocol frames only.
	Logger *slog.Logger

	// Catalog, when set, publishes its GET tools alongside ping. Other
	// methods wait for the mutation policy gate (T019) before publication.
	Catalog *catalog.Catalog

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
	addCatalogTools(srv, catalogGETTools(opts.Catalog))
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

// addCatalogTools registers each catalog tool with a handler that reports
// "not implemented" honestly rather than approximating a call: request
// execution lands in T012. The tools are visible and schema-checkable
// (Step 3's checkpoint) before they are callable.
func addCatalogTools(srv *mcp.Server, tools []catalog.Tool) {
	for _, t := range tools {
		tool := &mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:  true,
				OpenWorldHint: ptr(true),
			},
		}
		mcp.AddTool(srv, tool, notImplementedHandler(t))
	}
}

// notImplementedHandler never touches the network: it exists so a tool can
// be listed and schema-validated before T012 wires real execution.
func notImplementedHandler(t catalog.Tool) mcp.ToolHandlerFor[struct{}, any] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return nil, nil, fmt.Errorf("lotsman: %s %s is not executable yet (request execution lands in T012)", t.Method, t.PathTemplate)
	}
}

func ptr[T any](v T) *T { return &v }
