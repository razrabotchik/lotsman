package mcpserver

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/audit"
	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/errs"
)

// A call the runner never sees still happened. The SDK validates arguments
// against the published schema before any handler runs, and answers a call to
// a tool that does not exist without one; search mode refuses an id it cannot
// resolve before it has a runner to ask. None of those reached the record,
// which made the audit log -- and the counters built from it -- a list of
// the calls that got far enough, rather than of the calls that were made.

// reachedKey marks a call's context so the runner can say it took the call.
type reachedKey struct{}

type reached struct{ ok atomic.Bool }

// markReached is called by the runner when it records a call.
func markReached(ctx context.Context) {
	if mark, ok := ctx.Value(reachedKey{}).(*reached); ok {
		mark.ok.Store(true)
	}
}

// auditUnreached records every operation call that ended before a runner
// recorded it, as a refusal: nothing was sent.
func auditUnreached(sink audit.Sink, runners []runner) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if method != "tools/call" || !ok || call.Params == nil || !callsAnOperation(call.Params.Name) {
				return next(ctx, method, req)
			}
			mark := &reached{}
			started := time.Now()
			result, err := next(context.WithValue(ctx, reachedKey{}, mark), method, req)
			if !mark.ok.Load() {
				sink.Record(unreachedEvent(ctx, started, call, runners))
			}
			return result, err
		}
	}
}

// callsAnOperation reports whether a tool name is a call of an API operation:
// a catalog tool, one of the two search-mode call tools, or a name nothing
// answers to. The other built-ins describe; they do not call.
func callsAnOperation(name string) bool {
	switch name {
	case "call_read_operation", "call_mutating_operation":
		return true
	}
	return !catalog.IsReservedToolName(name)
}

// unreachedEvent describes the call as far as it can be identified: by its
// tool name in tools mode, by the id it asked for in search mode.
func unreachedEvent(ctx context.Context, started time.Time, call *mcp.CallToolRequest, runners []runner) audit.Event {
	event := audit.Event{
		SchemaVersion: audit.SchemaVersion,
		Time:          started,
		Tool:          call.Params.Name,
		Subject:       subjectOf(ctx),
		Decision:      audit.DecisionRefused,
		// Every refusal that happens before a runner is about the call
		// itself: arguments the schema rejects, a tool or an id that does
		// not exist.
		Reason:     string(errs.ClassUsage),
		DurationMs: time.Since(started).Milliseconds(),
	}
	id := call.Params.Name
	if catalog.IsReservedToolName(id) {
		// A search-mode call tool names its operation in its arguments; a
		// catalog tool is its own id.
		var envelope struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(call.Params.Arguments, &envelope)
		id = envelope.ID
	}
	if prepared := find(runners, id); prepared != nil {
		event.Operation = string(prepared.tool.OperationKey)
		event.Effect = string(prepared.tool.Effect.Effect)
		event.Origin = originOf(prepared.base())
		event.Method = prepared.tool.Method
		event.Path = prepared.tool.SafePath()
	}
	return event
}
