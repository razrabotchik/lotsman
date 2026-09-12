package catalog

import (
	"encoding/json"
	"sort"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// ReportSchemaVersion is the contract version of the machine-readable report
// (contracts/cli.md). Within a version fields are only added, never
// repurposed, so a CI consumer that ignores unknown fields keeps working.
const ReportSchemaVersion = "1"

// Report is lotsman's account of what it made of a document: what it found,
// what it will publish, what it will actually run, and why not for the rest.
//
// It is a first-class product, not a by-product of serving (FR-12a). An API
// owner can run it in a pull request and learn what an agent would be able to
// do, without ever starting an MCP server.
type Report struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Totals        Totals                    `json:"totals"`
	ByReason      map[domain.ReasonCode]int `json:"byReason,omitempty"`
	Estimate      Estimate                  `json:"catalog"`
	Security      SecuritySummary           `json:"security"`
	Operations    []OperationVerdict        `json:"operations"`
}

// Totals counts operations along the axes that matter to different readers:
// translation (found/supported/partial/rejected), publication and execution
// (published/executable/policyBlocked/capabilityBlocked), and effect.
//
// They are separate numbers on purpose. "Rejected" is lotsman refusing to
// translate; "policyBlocked" is the operator refusing to allow; and
// "capabilityBlocked" is lotsman not being finished yet. Collapsing them into
// one "unavailable" count would hide which of the three a reader can fix.
type Totals struct {
	Found      int `json:"found"`
	Supported  int `json:"supported"`
	Partial    int `json:"partial"`
	Rejected   int `json:"rejected"`
	Published  int `json:"published"`
	Executable int `json:"executable"`
	// CapabilityBlocked is supported but not runnable by this build.
	CapabilityBlocked int `json:"capabilityBlocked"`
	// PolicyBlocked is runnable but not permitted by the current policy.
	PolicyBlocked int `json:"policyBlocked"`
	// Excluded is supported and translatable, but removed from the surface by
	// the operator: a disabled override or a tag filter. It is its own number
	// because it is the only one of these a reader fixes by editing their own
	// configuration rather than lotsman's.
	Excluded int                   `json:"excluded"`
	ByEffect map[domain.Effect]int `json:"byEffect,omitempty"`
}

// Estimate sizes the published catalog. serializedBytes is what the tool list
// costs a model's context, and it is the number that decides the mode (FR-52):
// 65 Kubernetes tools weigh more than 600 DigitalOcean ones, so an operation
// count cannot tell them apart.
type Estimate struct {
	// Mode is the mode actually in force.
	Mode Mode `json:"mode"`
	// Recommended is what the measurement says the catalog needs. It can
	// differ from Mode -- an operator may pin tools mode for a catalog that
	// exceeds the budget, and a report that hid that would be useless.
	Recommended     Mode `json:"recommendedMode"`
	ToolCount       int  `json:"toolCount"`
	SerializedBytes int  `json:"serializedBytesEstimate"`
	// ThresholdBytes is the budget Recommended was decided against.
	ThresholdBytes int    `json:"thresholdBytes"`
	OverBudget     bool   `json:"overBudget"`
	Digest         string `json:"digest"`
}

// SecuritySummary states the posture a reader would otherwise have to infer
// from the absence of warnings.
type SecuritySummary struct {
	// RemoteRefs reports whether any reference outside the root document was
	// resolved. Always false in this build: they are refused.
	RemoteRefs bool `json:"remoteRefs"`
	// Redirects reports whether the HTTP client follows redirects. Always
	// false: the redirect guard is installed from the first real call.
	Redirects bool `json:"redirects"`
	// UnknownMutationsBlocked reports whether an operation whose effect could
	// not be determined is refused before the network.
	UnknownMutationsBlocked bool `json:"unknownMutationsBlocked"`
}

// OperationVerdict is one operation's line in the report.
type OperationVerdict struct {
	Key      domain.OperationKey `json:"key"`
	Method   string              `json:"method"`
	Path     string              `json:"pathTemplate"`
	ToolName string              `json:"toolName,omitempty"`
	Support  domain.SupportLevel `json:"support"`
	// Published means an MCP client can see the tool; Executable means a call
	// would actually be attempted. The first never implies the second.
	Published bool `json:"published"`
	// Excluded means the operator removed it from the surface. It is not a
	// refusal: the operation is supported, and publishing it again is one
	// configuration line away.
	Excluded   bool                  `json:"excluded,omitempty"`
	Executable bool                  `json:"executable"`
	Effect            domain.EffectDecision `json:"effect"`
	ExecutionBlockers []domain.ReasonCode   `json:"executionBlockers,omitempty"`
	PolicyBlockers    []domain.ReasonCode   `json:"policyBlockers,omitempty"`
	// AuthProfiles names the configured credentials the operation would use.
	AuthProfiles []string `json:"authProfiles,omitempty"`
	Reasons      []Reason `json:"reasons,omitempty"`
}

// Reason is a machine-readable refusal with enough provenance to act on:
// the code to branch on, the text to read, and where in the document to look.
type Reason struct {
	Code    domain.ReasonCode `json:"code"`
	Detail  string            `json:"detail,omitempty"`
	Pointer string            `json:"pointer,omitempty"`
}

// buildReport assembles the report from every enumerated operation -- not
// only the published ones, since the operations a reader most needs to see
// are the ones that did not make it.
func buildReport(operations []domain.Operation, tools []Tool, excluded map[domain.OperationKey]domain.ReasonCode, digest string, opts Options) Report {
	report := Report{
		SchemaVersion: ReportSchemaVersion,
		ByReason:      map[domain.ReasonCode]int{},
		Estimate:      estimate(tools, digest, opts),
		Security: SecuritySummary{
			UnknownMutationsBlocked: !opts.Policy.AllowMutations,
		},
		Totals: Totals{Found: len(operations), ByEffect: map[domain.Effect]int{}},
	}

	byKey := make(map[domain.OperationKey]*Tool, len(tools))
	for i := range tools {
		byKey[tools[i].OperationKey] = &tools[i]
	}

	for i := range operations {
		op := &operations[i]
		verdict := OperationVerdict{
			Key:               op.Key,
			Method:            op.Method,
			Path:              op.PathTemplate,
			Support:           op.Support.Level,
			Effect:            op.Effect,
			ExecutionBlockers: append([]domain.ReasonCode(nil), op.ExecutionBlockers...),
			Reasons:           reasonsFor(op),
		}
		if reason, gone := excluded[op.Key]; gone {
			verdict.Excluded = true
			verdict.Reasons = append(verdict.Reasons, Reason{Code: reason})
		}
		if tool, published := byKey[op.Key]; published {
			verdict.ToolName = tool.Name
			verdict.Published = true
			verdict.Executable = tool.Executable
			verdict.PolicyBlockers = append([]domain.ReasonCode(nil), tool.PolicyBlockers...)
			verdict.AuthProfiles = append([]string(nil), tool.AuthProfiles...)
			// The tool's blockers are the operation's plus the ones that could
			// only be decided with the configuration in hand (auth), which is
			// what a reader needs to see.
			verdict.ExecutionBlockers = append([]domain.ReasonCode(nil), tool.ExecutionBlockers...)
		}

		report.Totals.ByEffect[op.Effect.Effect]++
		switch op.Support.Level {
		case domain.SupportSupported:
			report.Totals.Supported++
		case domain.SupportPartiallySupported:
			report.Totals.Partial++
		case domain.SupportRejected:
			report.Totals.Rejected++
		}
		if verdict.Published {
			report.Totals.Published++
		}
		switch {
		case verdict.Executable:
			report.Totals.Executable++
		case len(verdict.PolicyBlockers) > 0:
			report.Totals.PolicyBlocked++
		case verdict.Excluded:
			report.Totals.Excluded++
		case verdict.Published:
			report.Totals.CapabilityBlocked++
		}

		for _, reason := range verdict.Reasons {
			report.ByReason[reason.Code]++
		}
		for _, code := range verdict.ExecutionBlockers {
			report.ByReason[code]++
		}
		for _, code := range verdict.PolicyBlockers {
			report.ByReason[code]++
		}

		report.Operations = append(report.Operations, verdict)
	}

	sort.Slice(report.Operations, func(i, j int) bool {
		return report.Operations[i].Key < report.Operations[j].Key
	})
	return report
}

// reasonsFor collects an operation's refusal reasons, preferring the
// diagnostic (which carries a message and a pointer) and falling back to the
// bare support code when no diagnostic explained it.
func reasonsFor(op *domain.Operation) []Reason {
	var reasons []Reason
	explained := map[domain.ReasonCode]bool{}
	for _, d := range op.Diagnostics {
		if d.Severity != domain.SeverityError || d.Code == "" {
			continue
		}
		reasons = append(reasons, Reason{Code: d.Code, Detail: d.Message, Pointer: d.Pointer})
		explained[d.Code] = true
	}
	for _, code := range op.Support.Reasons {
		if !explained[code] {
			reasons = append(reasons, Reason{Code: code})
		}
	}
	return reasons
}

// estimate measures the published catalog and states which mode it needs.
func estimate(tools []Tool, digest string, opts Options) Estimate {
	bytes := serializedBytes(tools)
	threshold := opts.maxSerializedBytes()

	out := Estimate{
		Mode:            opts.mode(),
		ToolCount:       len(tools),
		SerializedBytes: bytes,
		ThresholdBytes:  threshold,
		OverBudget:      bytes > threshold,
		Digest:          digest,
	}
	out.Recommended = ModeTools
	if out.OverBudget {
		out.Recommended = ModeSearch
	}
	return out
}

// serializedBytes estimates what the published tool list costs a model's
// context: name, description and input schema, which is what tools/list
// actually carries. Marshal errors are impossible for these types.
func serializedBytes(tools []Tool) int {
	type wire struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		InputSchema map[string]any `json:"inputSchema"`
	}
	payload := make([]wire, 0, len(tools))
	for i := range tools {
		payload = append(payload, wire{
			Name:        tools[i].Name,
			Description: tools[i].Description,
			InputSchema: tools[i].InputSchema,
		})
	}
	encoded, _ := json.Marshal(payload)
	return len(encoded)
}
