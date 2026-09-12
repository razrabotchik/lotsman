package mcpserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/razrabotchik/lotsman/internal/catalog"
	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/redact"
)

// Interactive approval (FR-44-46) asks a human before a call that changes
// something happens.
//
// It is a UX mechanism and never a security boundary (FR-44a, Constitution
// III). The protocol cannot promise a person saw the prompt: a client may
// answer it automatically, and nothing here can tell the difference. By the
// time anything below runs, the gate has already allowed the call on the
// strength of the effect policy and the operator's rules -- approval can only
// stop a call that policy permitted, never permit one that policy stopped.

// approvalRequestID names the one input request lotsman ever makes. The
// client echoes it back with the answer.
const approvalRequestID = "lotsman.approval"

// approvalSummaryBytes bounds the argument summary in the prompt. A prompt
// nobody can read is a prompt everybody accepts.
const approvalSummaryBytes = 512

// approver asks the client for confirmation according to the configured mode.
type approver struct {
	mode config.Approval
	log  logger
}

// required reports whether this call needs confirmation at all. Reads never
// prompt: the policy that let them through is the one that classified them,
// and a prompt on every GET is how an operator learns to accept without
// reading.
func (a approver) required(tool *catalog.Tool) bool {
	return a.mode != config.ApprovalNever && !tool.Effect.IsRead()
}

// pending is the sentinel a call returns when it cannot finish until the
// client has answered. It carries the input-required result the handler must
// hand back; the client fulfils the request and calls the tool again, and the
// second call arrives with the answer attached (SEP-2322).
//
// It is an error rather than a return value because that is what the call
// path already has a channel for, and because every caller must decide what
// to do with it: a front door that ignored it would send the request without
// asking.
type pending struct{ result *mcp.CallToolResult }

func (p *pending) Error() string { return "approval required" }

// check decides what happens to a call that needs approval:
//
//	nil, nil     -- proceed, either because no approval is needed or because it was given
//	result, nil  -- the client must be asked; return this result and wait to be called again
//	nil, err     -- refused before the network
//
// On protocol 2026-07-28 a server cannot open an elicitation while it is
// serving a request; the answer is a multi-round-trip input request, which is
// exactly what FR-44 specifies ("input-required/MRTR"). The SDK fulfils it
// against the client's elicitation handler and re-invokes this tool, so the
// whole call -- validation, request build, egress -- runs again from the top
// with the answer in hand.
func (a approver) check(req *mcp.CallToolRequest, tool *catalog.Tool, args map[string]any) (*mcp.CallToolResult, error) {
	if !a.required(tool) {
		return nil, nil
	}

	if answered, action := a.answer(req); answered {
		if action == "accept" {
			a.log.Info("approved", "operation", string(tool.OperationKey), "effect", string(tool.Effect.Effect))
			return nil, nil
		}
		a.log.Warn("approval declined", "operation", string(tool.OperationKey), "action", action)
		return nil, errs.Errorf(errs.ClassPolicy,
			"lotsman: %s was not approved (action %q, %s); nothing was sent",
			tool.OperationKey, action, domain.ReasonApprovalDeclined)
	}

	if !clientCanBeAsked(req.Session) {
		// FR-45: required approval plus a client that cannot be asked is a
		// refusal, not an exception. `client-capability` is the mode for an
		// operator who has decided otherwise, and has written it down.
		if a.mode == config.ApprovalClientCapability {
			a.log.Warn("approval skipped: the client declared no elicitation capability",
				"operation", string(tool.OperationKey), "effect", string(tool.Effect.Effect),
				"mode", string(a.mode))
			return nil, nil
		}
		return nil, errs.Errorf(errs.ClassPolicy,
			"lotsman: %s requires approval (effect %s, execution.interactiveApproval=%s) and this client "+
				"declared no elicitation capability; nothing was sent (%s)",
			tool.OperationKey, tool.Effect.Effect, a.mode, domain.ReasonApprovalUnavailable)
	}

	a.log.Info("requesting approval",
		"operation", string(tool.OperationKey), "effect", string(tool.Effect.Effect))
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{approvalRequestID: &mcp.ElicitParams{
			Mode:    "form",
			Message: approvalMessage(tool, args),
			// The answer lotsman needs is the action itself -- accept,
			// decline or cancel -- so the form asks for no fields. A prompt
			// that also collected data would be a second way to influence a
			// request whose arguments have already been validated.
			RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	}, nil
}

// answer reads the client's reply, if this is the second call.
//
// Nothing here is evidence that a human saw anything: the reply is whatever
// the client sent, and a client may produce one by itself. That is FR-44a,
// and it is why this runs after the gate rather than in place of it.
func (a approver) answer(req *mcp.CallToolRequest) (answered bool, action string) {
	if req == nil || req.Params == nil {
		return false, ""
	}
	response, replied := req.Params.InputResponses[approvalRequestID]
	if !replied {
		return false, ""
	}
	result, ok := response.(*mcp.ElicitResult)
	if !ok {
		// A reply of the wrong shape is not consent.
		return true, "malformed"
	}
	return true, result.Action
}

// clientCanBeAsked reports whether the client declared the elicitation
// capability at initialize time. Asking one that did not would fail at the
// transport, which is a different error from the one an operator needs to
// read.
func clientCanBeAsked(session *mcp.ServerSession) bool {
	if session == nil {
		return false
	}
	params := session.InitializeParams()
	return params != nil && params.Capabilities != nil && params.Capabilities.Elicitation != nil
}

// approvalMessage is the FR-46 payload: what the call is, what it does, where
// it goes, and a redacted summary of its arguments. It is deliberately not
// the request: a full URL and a full body are what a human stops reading.
func approvalMessage(tool *catalog.Tool, args map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Allow %s?\n", tool.Name)
	if tool.Description != "" {
		fmt.Fprintf(&b, "%s\n", tool.Description)
	}
	fmt.Fprintf(&b, "operation %s\n", tool.OperationKey)
	fmt.Fprintf(&b, "effect    %s (%s, %s)\n", tool.Effect.Effect, tool.Effect.Source, tool.Effect.Confidence)
	fmt.Fprintf(&b, "target    %s %s%s\n", tool.Method, origin(tool), tool.PathTemplate)
	if summary := argumentSummary(args); summary != "" {
		fmt.Fprintf(&b, "arguments %s\n", summary)
	}
	return b.String()
}

// origin is the target origin, without the path. It comes from the tool's own
// servers list, which is what the runner would use; the operator's --base-url
// override is not shown because the runner may still refuse it at the egress
// check, and a prompt naming an origin nothing was sent to would be worse
// than none.
func origin(tool *catalog.Tool) string {
	if len(tool.Servers) == 0 {
		return ""
	}
	return redact.String(strings.TrimSuffix(tool.Servers[0], "/"))
}

// argumentSummary lists what was passed, with values shown only where a human
// needs them to answer the question at all.
//
// Path and query values are the *which*: approving the deletion of "a
// droplet" is not a decision. Body values are the *what*, they are unbounded,
// and they are the likeliest place for something nobody wants on a screen --
// so the body is summarized by its field names only.
func argumentSummary(args map[string]any) string {
	var parts []string
	for _, group := range []string{domain.GroupPath, domain.GroupQuery, domain.GroupHeaders, domain.GroupCookies} {
		values, ok := args[group].(map[string]any)
		if !ok || len(values) == 0 {
			continue
		}
		if group == domain.GroupHeaders || group == domain.GroupCookies {
			// A header or a cookie is where a credential travels.
			parts = append(parts, group+": "+strings.Join(sortedKeys(values), ", "))
			continue
		}
		pairs := make([]string, 0, len(values))
		for _, name := range sortedKeys(values) {
			pairs = append(pairs, fmt.Sprintf("%s=%v", name, values[name]))
		}
		parts = append(parts, group+": "+strings.Join(pairs, ", "))
	}
	if body, ok := args[domain.GroupBody].(map[string]any); ok && len(body) > 0 {
		parts = append(parts, "body: "+strings.Join(sortedKeys(body), ", "))
	} else if _, present := args[domain.GroupBody]; present {
		parts = append(parts, "body: present")
	}

	summary := redact.String(strings.Join(parts, "; "))
	if len(summary) > approvalSummaryBytes {
		summary = summary[:approvalSummaryBytes] + "…"
	}
	return summary
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
