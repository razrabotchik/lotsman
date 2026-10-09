package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/argvalidate"
	"github.com/razrabotchik/lotsman/internal/audit"
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

	// audit receives one event per completed call.
	audit audit.Sink

	// minter re-mints a credential that a 401 says has gone stale (FR-35).
	// Nil when nothing in this operation's binding can be re-minted.
	minter *auth.Minter

	// calls bounds the calls in flight across the process.
	calls *CallLimit

	// maxResponseBytes bounds what a response may spend of this process's
	// memory (FR-36). Zero means the response package's documented default.
	maxResponseBytes int

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
	// §7.5's last stage. It wraps the call rather than living inside it so
	// that there is no way out of a call that skips the record: every return
	// below, including the ones added later, passes through here.
	started := time.Now()
	release, err := r.calls.acquire()
	if err != nil {
		r.record(ctx, started, response.Result{}, outcome{}, err)
		return response.Result{}, err
	}
	defer release()
	result, how, err := r.execute(ctx, invocation, args)
	r.record(ctx, started, result, how, err)
	return result, err
}

// outcome is what the call path learned along the way that the record needs
// and the caller does not. It is returned rather than stored on the runner,
// which is shared between concurrent calls.
type outcome struct {
	// refreshed reports that a credential was re-minted and the request made
	// again (FR-35).
	refreshed bool
}

// record writes what happened (§7.4). It never fails a call: a runtime that
// stops working because it cannot describe itself has its priorities
// backwards.
func (r *runner) record(ctx context.Context, started time.Time, result response.Result, how outcome, err error) {
	if r.audit == nil {
		return
	}
	event := audit.Event{
		SchemaVersion: audit.SchemaVersion,
		Time:          started,
		Operation:     string(r.tool.OperationKey),
		Tool:          r.tool.Name,
		Effect:        string(r.tool.Effect.Effect),
		Subject:       subjectOf(ctx),
		Decision:      audit.DecisionExecuted,
		Origin:        originOf(r.base()),
		Method:        r.tool.Method,
		Path:          r.tool.SafePath(),
		Status:        result.Status,
		DurationMs:    time.Since(started).Milliseconds(),
		ResponseBytes: result.ReceivedBytes,
		Truncated:     result.Truncated,
		Refreshed:     how.refreshed,
	}
	var ask *pending
	var lost *sendFailure
	var unread *unreadBody
	switch {
	case err == nil:
	case errors.As(err, &ask):
		// Waiting for a human is not a refusal and not an execution: nothing
		// reached the network, and nothing was denied either (FR-44).
		event.Decision = audit.DecisionInputRequired
	case errors.As(err, &unread):
		// The API answered; only its body was lost. The call happened, and
		// the status says how it went.
		event.Status = unread.status
		event.Reason = string(errs.ClassOf(err))
	case errors.As(err, &lost):
		// Sent, or tried to be, and no answer: not a refusal, because no
		// control stopped it -- and recording one would promise the API did
		// nothing, which is exactly what is not known (ADR-0019).
		event.Decision = audit.DecisionFailed
		if lost.written {
			event.Decision = audit.DecisionOutcomeUnknown
		}
		event.Reason = string(errs.ClassOf(err))
		event.Status = 0
	default:
		event.Decision = audit.DecisionRefused
		event.Reason = string(errs.ClassOf(err))
		event.Status = 0
	}
	r.audit.Record(event)
}

// execute runs the runtime order for validated arguments.
func (r *runner) execute(ctx context.Context, invocation *mcp.CallToolRequest, args map[string]any) (response.Result, outcome, error) {
	var how outcome
	if !r.callable() {
		return response.Result{}, how, errs.Errorf(r.refusalClass, "lotsman: %s %s %s",
			r.tool.Method, r.tool.SafePath(), r.refusal)
	}

	// Every error leaving a call passes through redaction: an argument, a URL
	// or an upstream message may quote a credential.
	if err := r.validator.Validate(args); err != nil {
		return response.Result{}, how, redact.Error(err)
	}

	op := requestbuild.Operation{
		Method:       r.tool.Method,
		PathTemplate: r.tool.PathTemplate,
		Servers:      r.tool.Servers,
		Parameters:   r.tool.Input.Parameters,
		Body:         r.tool.Input.Body,
	}
	// build makes the request from scratch. A retry re-runs this rather than
	// re-sending the first request with a header patched (§7.5): the body of
	// a sent request has been read, and an unrepeatable request is not a
	// request lotsman should try to repeat.
	build := func() (*http.Request, error) {
		req, err := requestbuild.Build(ctx, &op, requestbuild.Arguments(args), requestbuild.Options{BaseURL: r.baseURL})
		if err != nil {
			return nil, redact.Error(err)
		}
		// Defence in depth: an unexpanded template would mean a placeholder
		// reached the wire as a literal, which is a guess about the API.
		if strings.ContainsAny(req.URL.EscapedPath(), "{}") {
			return nil, errs.Errorf(errs.ClassInternal,
				"lotsman: %s %s: unexpanded path template", r.tool.Method, r.tool.SafePath())
		}
		if denied := r.egress.CheckTarget(req.URL); denied != nil {
			return nil, denied
		}
		return req, nil
	}

	req, err := build()
	if err != nil {
		return response.Result{}, how, err
	}

	// Last: everything that could refuse this call without troubling a human
	// already has, so a prompt is only ever shown for a call that would
	// otherwise happen -- and the only thing a yes does is send it.
	ask, err := r.approval.check(invocation, r.tool, r.base(), args)
	if err != nil {
		return response.Result{}, how, err
	}
	if ask != nil {
		return response.Result{}, how, &pending{result: ask}
	}

	resp, err := r.send(req)
	if err != nil {
		return response.Result{}, how, err
	}

	// FR-35: one refresh and one retry, on a 401, and only for a credential
	// this runtime can provably re-mint.
	//
	// A 401 is not a generic transport error: the API answered, and it
	// answered that the credential was not accepted -- so the request
	// provably did not take effect, which is the only reason repeating a
	// mutation is defensible at all (§7.5).
	if resp.StatusCode == http.StatusUnauthorized && r.refreshable() != "" {
		drain(resp)
		r.minter.Invalidate(r.refreshable())
		how.refreshed = true

		retried, buildErr := build()
		if buildErr != nil {
			return response.Result{}, how, buildErr
		}
		//nolint:bodyclose // response.FromHTTP below closes it; the reassignment is what bodyclose cannot follow.
		resp, err = r.send(retried)
		if err != nil {
			return response.Result{}, how, err
		}
	}

	status := resp.StatusCode
	result, err := response.FromHTTP(resp, r.maxResponseBytes)
	if err != nil {
		return response.Result{}, how, &unreadBody{status: status, err: redact.Error(errs.Errorf(errs.ClassUpstream,
			"lotsman: %s %s: the API answered %d and its body could not be read: %w",
			r.tool.Method, r.tool.SafePath(), status, err))}
	}
	return result, how, nil
}

// base is where this call goes: the base URL the request builder resolves,
// with the operator's --base-url taking precedence over the document's
// servers. It is asked of the same function Build asks, so the approval
// prompt and the audit record cannot name a different place from the one the
// request is sent to -- which is what they did when they read the document's
// servers directly and --base-url pointed somewhere else. Empty when nothing
// resolves; the call itself is refused for that reason.
func (r *runner) base() string {
	base, err := requestbuild.BaseURL(r.tool.Servers, requestbuild.Options{BaseURL: r.baseURL})
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(base, "/")
}

// originOf reduces a base URL to the scheme, host and port the audit record
// promises: a base path is the document's detail, not where the call went.
func originOf(base string) string {
	parsed, err := url.Parse(base)
	if base == "" || err != nil {
		return ""
	}
	return redact.String(parsed.Scheme + "://" + parsed.Host)
}

// send performs one request. Every caller hands the response to
// response.FromHTTP or to drain, and both of those close it.
//
// A failure says how far the request got, because that decides what the
// caller may do next. A control that stopped it -- the dial guard, a
// credential that could not be applied -- is a refusal and keeps its own
// class. Anything else is a *sendFailure, which records whether the request
// was written: a mutation that was written and never answered may have
// happened, and a caller told otherwise will repeat it.
func (r *runner) send(req *http.Request) (*http.Response, error) {
	var progress sendProgress
	resp, err := r.client.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), progress.trace())))
	if err == nil {
		return resp, nil
	}
	switch {
	case errors.Is(err, egress.ErrDenied):
		// The dial guard refused every address: nothing was sent.
		return nil, redact.Error(errs.Wrap(errs.ClassPolicy,
			fmt.Errorf("lotsman: %s %s: %w", r.tool.Method, r.tool.SafePath(), err)))
	case errs.ClassOf(err) == errs.ClassAuth:
		// The credential could not be applied, which happens before the
		// request reaches the transport. Reporting it as upstream would send
		// an operator to the API when the fix is the secret.
		return nil, redact.Error(err)
	}

	failure := &sendFailure{written: !progress.provablyUnsent()}
	if failure.written {
		// A transport error can carry the request URL, and an API key may live
		// in a query parameter.
		failure.err = errs.Errorf(errs.ClassUpstream, "lotsman: %s %s: the request was sent and no response came back: %w",
			r.tool.Method, r.tool.SafePath(), err)
		if !r.tool.Effect.IsRead() {
			failure.err = errs.Errorf(errs.ClassUpstream, "%w; the API may have acted on it -- "+
				"check its state before calling %s again", failure.err, r.tool.Name)
		}
	} else {
		failure.err = errs.Errorf(errs.ClassUpstream, "lotsman: %s %s: the request could not be sent, "+
			"and nothing reached the API: %w", r.tool.Method, r.tool.SafePath(), err)
	}
	failure.err = redact.Error(failure.err)
	return nil, failure
}

// sendFailure is a request that went to the transport and came back without
// a response.
type sendFailure struct {
	err error
	// written is false only when the request provably never left: the
	// transport took a connection and failed before writing to it, or never
	// got one. Everything else -- including a transport that cannot say --
	// counts as written, because the safe mistake is to doubt a call that
	// did not happen, never to deny one that did.
	written bool
}

func (f *sendFailure) Error() string { return f.err.Error() }
func (f *sendFailure) Unwrap() error { return f.err }

// unreadBody is an answer whose status arrived and whose body did not.
type unreadBody struct {
	status int
	err    error
}

func (u *unreadBody) Error() string { return u.err.Error() }
func (u *unreadBody) Unwrap() error { return u.err }

// sendProgress follows one request through net/http's transport.
type sendProgress struct {
	// asked is set when the transport went looking for a connection, which
	// is how a net/http transport announces itself. A transport that never
	// says so is one whose progress cannot be read.
	asked atomic.Bool
	// wrote is set when the request headers were written to a connection.
	wrote atomic.Bool
}

func (p *sendProgress) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:      func(string) { p.asked.Store(true) },
		WroteHeaders: func() { p.wrote.Store(true) },
	}
}

// provablyUnsent reports that nothing of the request reached a connection: a
// request whose headers were never written cannot have been acted on.
func (p *sendProgress) provablyUnsent() bool {
	return p.asked.Load() && !p.wrote.Load()
}

// drain discards a response whose body nobody will read, so the connection
// can be reused instead of dropped.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

// refreshable names the credential this operation presents that can be
// re-minted, or empty when none can.
//
// "Can provably refresh" is FR-35's condition and it is a property of the
// scheme: an API key read from a file cannot be renewed by asking anyone, so
// a 401 against one is the API's own answer and goes to the caller unchanged.
func (r *runner) refreshable() string {
	if r.minter == nil {
		return ""
	}
	for i := range r.tool.AuthBinding.Credentials {
		credential := &r.tool.AuthBinding.Credentials[i]
		if auth.Mints(&credential.Profile) {
			return credential.Name
		}
	}
	return ""
}

// subjectOf reports who the transport authenticated, or empty when nothing
// did.
//
// Read from the context the SDK carries rather than passed down from the
// transport: a value that had to be threaded through every caller would be a
// value some caller eventually forgets, and the record would quietly stop
// saying who asked.
func subjectOf(ctx context.Context) string {
	if info := sdkauth.TokenInfoFromContext(ctx); info != nil {
		return info.UserID
	}
	return ""
}

// newRunners prepares one runner per published tool, in catalog order.
//
// Everything it needs comes from the resolved options, so the options are what
// it takes. The earlier form passed eight values the caller derived from them
// one by one -- `opts.logger()` here, an approver built from `opts.approval()`
// there -- and nothing made those derivations agree: a caller could hand the
// runners one logger and the approval prompt another, and both would compile.
func newRunners(opts *Options, outbound *egress.Policy) []runner {
	tools := catalogTools(opts.Catalog)
	log := opts.logger()
	// One minter for the whole catalog: a token belongs to the credential,
	// not to the operation, so two hundred tools bound to one profile hold
	// one token between them.
	client := opts.httpClient()
	minter := auth.NewMinter(client, outbound)
	calls := opts.Calls
	if calls == nil {
		calls = NewCallLimit(0)
	}
	runners := make([]runner, 0, len(tools))
	for i := range tools {
		tool := &tools[i]
		prepared := runner{
			tool: tool, baseURL: opts.BaseURL, egress: outbound,
			approval:         approver{mode: opts.approval(), log: log},
			audit:            opts.auditSink(),
			maxResponseBytes: opts.MaxResponseBytes,
			calls:            calls,
		}

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
			if tool.AuthMessage != "" {
				// A credential problem is the one blocker an operator can fix
				// today, so it is the one worth spelling out rather than coding.
				prepared.refusal += " (" + tool.AuthMessage + ")"
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
			prepared.client = auth.Client(client, tool.AuthBinding, minter)
			prepared.minter = minter
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
