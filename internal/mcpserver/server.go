package mcpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/egress"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/policy"
	"github.com/razrabotchik/lotsman/internal/redact"
	"github.com/razrabotchik/lotsman/internal/requestbuild"
	"github.com/razrabotchik/lotsman/internal/response"
)

// instructions are sent to the client at initialize time. They state the one
// promise that matters before full parameter/policy support exists: nothing
// is approximated.
const instructions = "lotsman exposes an OpenAPI specification as MCP tools. " +
	"Operations that cannot be translated safely are not published. Supported " +
	"operations with runtime blockers may remain discoverable but never execute " +
	"approximately; mutations are blocked unless explicitly allowed. " +
	"A tool executes only when its operation is fully translated and its effect " +
	"is permitted by policy; blocked tools return an explicit error before any " +
	"network call. Read operations are allowed by default, everything else " +
	"requires mutations to be enabled. During M0, real HTTP execution also " +
	"requires an explicit --base-url."

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
		return egress.Client(o.HTTPClient)
	}
	return egress.Client(&http.Client{Timeout: defaultTimeout})
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
	addCatalogTools(srv, catalogTools(opts.Catalog), opts.httpClient(), opts.BaseURL, opts.logger())
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
		"tools", 1+len(catalogTools(opts.Catalog)))
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

// catalogTools returns the catalog's tools, or nil for a nil Catalog.
//
// Every supported operation is published, whatever its method: publication is
// discovery, and the mutation gate decides execution (ADR-0005). A tool the
// policy blocks is listed with conservative annotations and refuses before the
// network, which tells a model what exists and what it may not do -- rather
// than leaving it to guess why an endpoint it can see in the docs is missing.
func catalogTools(cat *catalog.Catalog) []catalog.Tool {
	if cat == nil {
		return nil
	}
	return cat.Tools
}

// addCatalogTools registers each catalog tool. Publication supports discovery;
// only tools explicitly marked executable receive a network-capable handler.
//
// tools is owned by the caller and never mutated afterwards, so the handlers
// below may hold a pointer into it (a Catalog is immutable once built).
func addCatalogTools(srv *mcp.Server, tools []catalog.Tool, client *http.Client, baseURL string, log *slog.Logger) {
	for i := range tools {
		t := &tools[i]
		// Annotations are derived conservatively from the effect and are hints
		// for the client's UX only: the gate below does not read them, and
		// nothing a client does with them can relax it (FR-42, FR-43).
		hints := policy.AnnotationsFor(t.Effect)
		tool := &mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			// The published schema is the contract: the client sees it, the
			// SDK checks arguments against it, and lotsman validates against
			// the very same document before serializing anything.
			InputSchema: publishedSchema(t),
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    hints.ReadOnly,
				DestructiveHint: ptr(hints.Destructive),
				OpenWorldHint:   ptr(true),
			},
		}
		if len(t.PolicyBlockers) > 0 {
			// A policy refusal is the operator's decision, not a gap in
			// lotsman: say which, and say what would change it.
			mcp.AddTool(srv, tool, refusalHandler(t, errs.ClassPolicy,
				"blocked by policy ("+reasonCodes(t.PolicyBlockers)+"): "+t.PolicyMessage))
			continue
		}
		if !t.Executable {
			reason := "not executable by this runtime"
			if len(t.ExecutionBlockers) > 0 {
				reason += ": " + reasonCodes(t.ExecutionBlockers)
			}
			mcp.AddTool(srv, tool, refusalHandler(t, errs.ClassUnsupported, reason))
			continue
		}

		// A schema that will not compile cannot be validated against, and an
		// unvalidated argument must never reach a URL: publish the tool, but
		// only as a refusal.
		validator, err := argvalidate.Compile(t.Name, publishedSchema(t))
		if err != nil {
			log.Error("tool input schema did not compile; publishing it as not executable",
				"tool", t.Name, "operation", string(t.OperationKey), "error", err)
			mcp.AddTool(srv, tool, refusalHandler(t, errs.ClassSpecInvalid, "not executable: "+string(domain.ReasonInvalidSchema)))
			continue
		}

		// The credential is applied by the innermost round tripper, after
		// every other layer has seen the request without it.
		mcp.AddTool(srv, tool, executeHandler(t, validator, auth.Client(client, t.AuthBinding), baseURL))
	}
}

// publishedSchema is the tool's input schema, or the most restrictive schema
// there is when a catalog somehow arrives without one. Falling back to
// "object with no properties" keeps a catalog bug from either panicking the
// SDK's schema inference or publishing a tool that accepts anything.
func publishedSchema(t *catalog.Tool) map[string]any {
	if len(t.InputSchema) > 0 {
		return t.InputSchema
	}
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

// refusalHandler never touches the network: it exists so a tool can be listed
// and schema-validated while still being impossible to call.
func refusalHandler(t *catalog.Tool, class errs.Class, reason string) mcp.ToolHandlerFor[map[string]any, any] {
	return func(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
		return nil, nil, errs.Errorf(class, "lotsman: %s %s %s", t.Method, t.PathTemplate, reason)
	}
}

// executeHandler runs the runtime call order (docs/pipeline.md stages 5-8):
// validate the arguments, build the request, apply the M0 egress floor,
// execute it and shape the response. Auth and full egress policy land later.
//
// An upstream 4xx/5xx is not a Go error: it is a successful tool call that
// reports isError=true with the upstream status and body, so the model sees
// what the API actually said (FR-39) instead of a generic failure message.
func executeHandler(t *catalog.Tool, validator *argvalidate.Validator, client *http.Client, baseURL string) mcp.ToolHandlerFor[map[string]any, response.Result] {
	op := requestbuild.Operation{
		Method:       t.Method,
		PathTemplate: t.PathTemplate,
		Servers:      t.Servers,
		Parameters:   t.Input.Parameters,
		Body:         t.Input.Body,
	}
	return func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, response.Result, error) {
		// Every error leaving a handler passes through redaction: an argument,
		// a URL or an upstream message may quote a credential, and this is the
		// boundary where anything lotsman says reaches a client.
		if err := validator.Validate(args); err != nil {
			return nil, response.Result{}, redact.Error(err)
		}
		req, err := requestbuild.Build(ctx, &op, requestbuild.Arguments(args), requestbuild.Options{BaseURL: baseURL})
		if err != nil {
			return nil, response.Result{}, redact.Error(err)
		}
		// Defence in depth: an unexpanded template would mean a placeholder
		// reached the wire as a literal, which is a guess about the API.
		if strings.ContainsAny(req.URL.EscapedPath(), "{}") {
			return nil, response.Result{}, errs.Errorf(errs.ClassInternal,
				"lotsman: %s %s: unexpanded path template", t.Method, t.PathTemplate)
		}
		if denied := egress.CheckTarget(req.URL, baseURL); denied != nil {
			return nil, response.Result{}, denied
		}
		//nolint:bodyclose // response.FromHTTP closes resp.Body on every path;
		// bodyclose cannot see through the call.
		resp, err := client.Do(req)
		if err != nil {
			// A transport error can carry the request URL, and an API key may
			// live in a query parameter.
			return nil, response.Result{}, redact.Error(
				errs.Errorf(errs.ClassUpstream, "lotsman: %s %s: %w", t.Method, t.PathTemplate, err))
		}
		result, err := response.FromHTTP(resp)
		if err != nil {
			return nil, response.Result{}, redact.Error(err)
		}
		var res *mcp.CallToolResult
		if result.IsError {
			res = &mcp.CallToolResult{IsError: true}
		}
		return res, result, nil
	}
}

func reasonCodes(codes []domain.ReasonCode) string {
	parts := make([]string, len(codes))
	for i, code := range codes {
		parts[i] = string(code)
	}
	return strings.Join(parts, ", ")
}

func ptr[T any](v T) *T { return &v }
