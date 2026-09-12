package mcpserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/auth"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/egress"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
	"github.com/razrabotchik/lotsman/internal/requestbuild"
	"github.com/razrabotchik/lotsman/internal/response"
)

// runner is one published operation and everything needed to call it: the
// validator compiled from its published schema, the client carrying its
// credentials, and the egress policy.
//
// It exists so that tools mode and search mode are two front doors onto one
// call path. A meta-tool that built requests its own way would be a second
// security boundary, and a second boundary is one that will disagree with the
// first eventually -- on the day it matters.
type runner struct {
	tool      *catalog.Tool
	validator *argvalidate.Validator
	client    *http.Client
	baseURL   string
	egress    *egress.Policy

	// approval asks a human before a call that changes something. It runs
	// after the gate, never instead of it (FR-44a).
	approval approver

	// refusal, when set, is why this operation cannot be called at all. The
	// operation is still published -- discovery is not permission, and a model
	// that can read the refusal is better off than one guessing why an
	// endpoint it can see in the docs is missing.
	refusal      string
	refusalClass errs.Class
}

// callable reports whether the operation can actually be called.
func (r *runner) callable() bool { return r.refusal == "" }

// call runs the runtime order (docs/pipeline.md stages 5-8) for validated
// arguments: validate, build, check egress, ask, send, shape.
//
// invocation is the tool call being served: it carries the client to ask for
// approval and the answer, if this is the retry after one was asked for. A
// nil invocation is a caller with no client, which cannot be asked and
// therefore fails closed exactly like a client that declared no capability.
func (r *runner) call(ctx context.Context, invocation *mcp.CallToolRequest, args map[string]any) (response.Result, error) {
	if !r.callable() {
		return response.Result{}, errs.Errorf(r.refusalClass, "lotsman: %s %s %s",
			r.tool.Method, r.tool.PathTemplate, r.refusal)
	}

	// Every error leaving a call passes through redaction: an argument, a URL
	// or an upstream message may quote a credential.
	if err := r.validator.Validate(args); err != nil {
		return response.Result{}, redact.Error(err)
	}

	op := requestbuild.Operation{
		Method:       r.tool.Method,
		PathTemplate: r.tool.PathTemplate,
		Servers:      r.tool.Servers,
		Parameters:   r.tool.Input.Parameters,
		Body:         r.tool.Input.Body,
	}
	req, err := requestbuild.Build(ctx, &op, requestbuild.Arguments(args), requestbuild.Options{BaseURL: r.baseURL})
	if err != nil {
		return response.Result{}, redact.Error(err)
	}
	// Defence in depth: an unexpanded template would mean a placeholder
	// reached the wire as a literal, which is a guess about the API.
	if strings.ContainsAny(req.URL.EscapedPath(), "{}") {
		return response.Result{}, errs.Errorf(errs.ClassInternal,
			"lotsman: %s %s: unexpanded path template", r.tool.Method, r.tool.PathTemplate)
	}
	if denied := r.egress.CheckTarget(req.URL); denied != nil {
		return response.Result{}, denied
	}

	// Last: everything that could refuse this call without troubling a human
	// already has, so a prompt is only ever shown for a call that would
	// otherwise happen -- and the only thing a yes does is send it.
	ask, err := r.approval.check(invocation, r.tool, args)
	if err != nil {
		return response.Result{}, err
	}
	if ask != nil {
		return response.Result{}, &pending{result: ask}
	}

	//nolint:bodyclose // response.FromHTTP closes resp.Body on every path;
	// bodyclose cannot see through the call.
	resp, err := r.client.Do(req)
	if err != nil {
		// A transport error can carry the request URL, and an API key may live
		// in a query parameter.
		return response.Result{}, redact.Error(
			errs.Errorf(errs.ClassUpstream, "lotsman: %s %s: %w", r.tool.Method, r.tool.PathTemplate, err))
	}
	result, err := response.FromHTTP(resp)
	if err != nil {
		return response.Result{}, redact.Error(err)
	}
	return result, nil
}

// newRunners prepares one runner per published tool, in catalog order.
func newRunners(tools []catalog.Tool, client *http.Client, baseURL string, outbound *egress.Policy, log logger, approval approver) []runner {
	runners := make([]runner, 0, len(tools))
	for i := range tools {
		tool := &tools[i]
		prepared := runner{tool: tool, baseURL: baseURL, egress: outbound, approval: approval}

		switch {
		case len(tool.PolicyBlockers) > 0:
			// A policy refusal is the operator's decision, not a gap in
			// lotsman: say which, and say what would change it.
			prepared.refusalClass = errs.ClassPolicy
			prepared.refusal = "blocked by policy (" + reasonCodes(tool.PolicyBlockers) + "): " + tool.PolicyMessage

		case !tool.Executable:
			prepared.refusalClass = errs.ClassUnsupported
			prepared.refusal = "not executable by this runtime"
			if len(tool.ExecutionBlockers) > 0 {
				prepared.refusal += ": " + reasonCodes(tool.ExecutionBlockers)
			}

		default:
			// A schema that will not compile cannot be validated against, and
			// an unvalidated argument must never reach a URL.
			validator, err := argvalidate.Compile(tool.Name, publishedSchema(tool))
			if err != nil {
				log.Error("tool input schema did not compile; publishing it as not executable",
					"tool", tool.Name, "operation", string(tool.OperationKey), "error", err)
				prepared.refusalClass = errs.ClassSpecInvalid
				prepared.refusal = "not executable: " + string(domain.ReasonInvalidSchema)
				break
			}
			prepared.validator = validator
			// The credential is applied by the innermost round tripper, after
			// every other layer has seen the request without it.
			prepared.client = auth.Client(client, tool.AuthBinding)
		}

		runners = append(runners, prepared)
	}
	return runners
}

// logger is the narrow slice of *slog.Logger this package needs, which keeps
// the runner testable without a logging framework.
type logger interface {
	Error(msg string, args ...any)
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}

// find locates a runner by the identifier a caller has in hand: the tool name
// a client sees, or the operation key a report prints.
func find(runners []runner, id string) *runner {
	for i := range runners {
		if runners[i].tool.Name == id || string(runners[i].tool.OperationKey) == id {
			return &runners[i]
		}
	}
	return nil
}
