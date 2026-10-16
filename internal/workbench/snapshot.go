package workbench

import (
	"strings"

	"github.com/razrabotchik/lotsman/internal/config"
	"github.com/razrabotchik/lotsman/internal/domain"
)

// Snapshot is the pipeline's verdict on one configuration.
type Snapshot struct {
	// SpecError is set when the specification could not be read or parsed at
	// all; Operations is then empty.
	SpecError string `json:"specError,omitempty"`
	// ConfigError is set when the configuration itself does not validate.
	// The snapshot is then of the document without any configuration.
	ConfigError string    `json:"configError,omitempty"`
	Problems    []Problem `json:"problems,omitempty"`
	// Operations are in catalog order.
	Operations []Operation                `json:"operations"`
	Profiles   []Profile                  `json:"profiles,omitempty"`
	Components []config.ComponentOverride `json:"components,omitempty"`
	Overlays   []string                   `json:"overlays,omitempty"`
}

// Problem is a correction that could not be applied as written.
type Problem struct {
	Section string `json:"section"`
	Index   int    `json:"index"`
	Message string `json:"message"`
	// Match names the operationOverrides entry, Schema the componentOverrides
	// one, so the workbench can offer to remove a stale correction.
	Match  *config.Match `json:"match,omitempty"`
	Schema string        `json:"schema,omitempty"`
}

// Profile is a configured credential, by name and scheme only: the
// workbench never sees a secret or even its reference.
type Profile struct {
	Name   string `json:"name"`
	Scheme string `json:"scheme"`
}

// Reason is a refusal with where in the document it applies.
type Reason struct {
	Code    string `json:"code"`
	Detail  string `json:"detail,omitempty"`
	Pointer string `json:"pointer,omitempty"`
}

// Body is the request body as published.
type Body struct {
	MediaType string         `json:"mediaType"`
	Required  bool           `json:"required"`
	Schema    map[string]any `json:"schema,omitempty"`
}

// Parameter is a declared parameter with its published schema, if any.
type Parameter struct {
	domain.DeclaredParameter
	Schema map[string]any `json:"schema,omitempty"`
}

// Operation is one operation as a configuration leaves it.
type Operation struct {
	Key          string   `json:"key"`
	Method       string   `json:"method"`
	Path         string   `json:"path"`
	OperationID  string   `json:"operationId,omitempty"`
	Summary      string   `json:"summary,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	Status       Status   `json:"status"`
	Support      string   `json:"support"`
	Reasons      []Reason `json:"reasons,omitempty"`
	Effect       string   `json:"effect"`
	EffectSource string   `json:"effectSource"`
	Published    bool     `json:"published"`
	Excluded     bool     `json:"excluded"`
	Executable   bool     `json:"executable"`
	Blockers     []string `json:"blockers,omitempty"`
	AuthProfiles []string `json:"authProfiles,omitempty"`

	MediaTypes    []string    `json:"mediaTypes,omitempty"`
	Parameters    []Parameter `json:"parameters,omitempty"`
	Body          *Body       `json:"body,omitempty"`
	BodyComponent string      `json:"bodyComponent,omitempty"`
	// InputSchema is the published tool input schema; nil when unpublished.
	InputSchema map[string]any `json:"inputSchema,omitempty"`

	Corrections []domain.Correction `json:"corrections,omitempty"`
	// Override is the operationOverrides entry that names this operation.
	Override *config.OperationOverride `json:"override,omitempty"`
}

// Status is what an operation needs from the operator.
type Status string

// Statuses, in the order the workbench shows them.
const (
	// StatusDecision: refused for a reason a correction can lift.
	StatusDecision Status = "decision"
	// StatusUnsupported: refused for a reason only new code or a fixed
	// document can lift.
	StatusUnsupported Status = "unsupported"
	// StatusBlocked: translated, but not callable — policy, a decision to
	// disable it, or a capability lotsman does not have yet.
	StatusBlocked Status = "blocked"
	// StatusWorks: translated and callable.
	StatusWorks Status = "works"
)

// decidable are the refusals a correction can lift (spec §1).
var decidable = map[string]bool{
	string(domain.ReasonUnsupportedParameterStyle):  true,
	string(domain.ReasonUnsupportedParameterSchema): true,
	string(domain.ReasonAmbiguousSecurity):          true,
}

// Classify sets op.Status from the rest of op.
func Classify(op *Operation) {
	switch {
	case op.Support == string(domain.SupportRejected):
		op.Status = StatusDecision
		for _, reason := range op.Reasons {
			if !decidableReason(reason.Code, op.MediaTypes) {
				op.Status = StatusUnsupported
				break
			}
		}
	case op.Excluded || !op.Executable:
		op.Status = StatusBlocked
		for _, blocker := range op.Blockers {
			if blocker == string(domain.ReasonAmbiguousSecurity) {
				op.Status = StatusDecision
			}
		}
	default:
		op.Status = StatusWorks
	}
}

func decidableReason(code string, mediaTypes []string) bool {
	if decidable[code] || strings.HasPrefix(code, "policy_") {
		return true
	}
	if code == string(domain.ReasonUnsupportedMediaType) {
		sendable := 0
		for _, mt := range mediaTypes {
			if Sendable(mt) {
				sendable++
			}
		}
		return sendable > 0
	}
	return false
}

// Sendable reports whether lotsman can send a body of this media type. It
// mirrors the adapter's rule; the snapshot says which types an operation
// offers, and the form greys out the rest.
func Sendable(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
