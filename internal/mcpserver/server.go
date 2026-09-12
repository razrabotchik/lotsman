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

	"github.com/razrabotchik/lotsman/internal/buildinfo"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/egress"
	"github.com/razrabotchik/lotsman/internal/policy"
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
	"requires mutations to be enabled. A mutating call may also require the " +
	"user's confirmation, requested as an input request; a client that cannot " +
	"be asked is refused before the network. Confirmation is a safety prompt, " +
	"not an authorization: it can stop a call policy allowed, never permit one " +
	"policy stopped. During M0, real HTTP execution also requires an explicit " +
	"--base-url."

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

	// Egress is the outbound network policy: which origins may be called and
	// under what budgets. The zero value allows nothing, which is the correct
	// default for a runtime whose targets are named by an untrusted document.
	Egress egress.Policy

	// HTTPClient executes tool calls; a default with defaultTimeout is used
	// when nil. Tests inject one pointed at an httptest server.
	HTTPClient *http.Client

	// Approval is when a mutating call asks the client to confirm (FR-44).
	// The empty value is the documented default, `always`.
	Approval config.Approval

	// now is the clock used by tool handlers; tests override it.
	now func() time.Time
}

func (o *Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

// mode is how the catalog is published. A nil catalog serves ping only, which
// is tools mode by default.
func (o *Options) mode() catalog.Mode {
	if o.Catalog == nil {
		return catalog.ModeTools
	}
	return o.Catalog.Mode
}

// mutationsAllowed reports whether any published operation is permitted to
// mutate. It is read from the catalog rather than from a flag: the catalog is
// what the policy was applied to, and a second source of truth about the same
// decision is a disagreement waiting to happen.
func (o *Options) mutationsAllowed() bool {
	if o.Catalog == nil {
		return false
	}
	for i := range o.Catalog.Tools {
		tool := &o.Catalog.Tools[i]
		if !tool.Effect.IsRead() && len(tool.PolicyBlockers) == 0 {
			return true
		}
	}
	return false
}

// approval is the configured approval mode, defaulting to the documented
// `always`. A zero Options must not mean "never ask": the safe default has to
// be the one you get by not saying anything.
func (o *Options) approval() config.Approval {
	if o.Approval == "" {
		return config.ApprovalAlways
	}
	return o.Approval
}

func (o *Options) clock() func() time.Time {
	if o.now != nil {
		return o.now
	}
	return time.Now
}

func (o *Options) httpClient() *http.Client {
	outbound := o.egressPolicy()
	return outbound.Client(o.HTTPClient)
}

// egressPolicy falls back to authorizing exactly the operator's --base-url
// when no allowlist was configured. That is the documented M0 behaviour: one
// origin, named on the command line, and nothing else.
func (o *Options) egressPolicy() egress.Policy {
	if len(o.Egress.AllowedOrigins) > 0 {
		return o.Egress
	}
	derived, err := egress.PolicyFromBaseURL(o.BaseURL, o.Egress.Budget, o.Egress.AllowPrivateNetworks)
	if err != nil {
		// An unusable base URL authorizes nothing, which CheckTarget reports
		// per call with the reason.
		return egress.Policy{Budget: o.Egress.Budget}
	}
	return derived
}

// New builds the MCP server and registers its tools: the hardcoded ping tool
// (it proves the transport, the tool registration path and the client's
// schema handling before any OpenAPI document is involved -- see
// docs/adr/0004-mcp-client-notes.md) plus, when a Catalog is supplied, its
// GET tools.
func New(opts *Options) *mcp.Server {
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

	outbound := opts.egressPolicy()
	runners := newRunners(catalogTools(opts.Catalog), opts.httpClient(), opts.BaseURL, &outbound,
		opts.logger(), approver{mode: opts.approval(), log: opts.logger()})

	// Two front doors, one call path. In search mode the catalog is too large
	// to publish as tools, so five meta-tools stand in front of the same
	// runners -- not in front of a second, parallel way to make a request.
	if opts.mode() == catalog.ModeSearch {
		addSearchTools(srv, opts.Catalog, runners, opts.mutationsAllowed())
		return srv
	}
	addCatalogTools(srv, runners)
	return srv
}

// ServeStdio runs the server on stdin/stdout until the context is cancelled or
// the client disconnects. A clean disconnect is not an error.
func ServeStdio(ctx context.Context, opts *Options) error {
	log := opts.logger()
	info := buildinfo.Get()
	log.Info("serving mcp over stdio",
		"version", info.Version,
		"commit", info.Commit,
		"mcp_sdk", info.MCPSDKVersion,
		"mcp_protocol", info.MCPProtocolVersion,
		"mode", string(opts.mode()),
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
func addCatalogTools(srv *mcp.Server, runners []runner) {
	for i := range runners {
		prepared := &runners[i]
		t := prepared.tool
		// Annotations are derived conservatively from the effect and are hints
		// for the client's UX only: the gate does not read them, and nothing a
		// client does with them can relax it (FR-42, FR-43).
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
		mcp.AddTool(srv, tool, executeHandler(prepared))
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

// executeHandler is the tools-mode front door onto runner.call.
//
// An upstream 4xx/5xx is not a Go error: it is a successful tool call that
// reports isError=true with the upstream status and body, so the model sees
// what the API actually said (FR-39) instead of a generic failure message.
func executeHandler(prepared *runner) mcp.ToolHandlerFor[map[string]any, response.Result] {
	return func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (*mcp.CallToolResult, response.Result, error) {
		result, err := prepared.call(ctx, req, args)
		if err != nil {
			// An approval request is not a failure: it is the first half of a
			// call the client will make again once it has an answer.
			var ask *pending
			if errors.As(err, &ask) {
				return ask.result, response.Result{}, nil
			}
			return nil, response.Result{}, err
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
