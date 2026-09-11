package domain

import "strings"

// OperationKey is the stable identity of an operation: independent of
// operationId, because real specs duplicate or omit it.
type OperationKey string

// NewOperationKey builds the canonical "{namespace}:{METHOD}:{normalizedPath}"
// key (data-model.md). Method is upper-cased so callers do not have to agree
// on a case convention.
func NewOperationKey(namespace, method, pathTemplate string) OperationKey {
	return OperationKey(namespace + ":" + strings.ToUpper(method) + ":" + pathTemplate)
}

// Operation is the enumeration-stage IR: identity, method, path template and
// diagnostics. Later pipeline stages add inputs, security, effect and doc
// metadata (data-model.md); this is deliberately the minimal slice needed by
// Step 2 (operations enumeration).
type Operation struct {
	Key OperationKey `json:"key"`
	// SourceOperationID is the operationId as authored, kept for diagnostics
	// only -- it is never part of Key because it is unreliable in real specs.
	SourceOperationID string        `json:"sourceOperationId,omitempty"`
	Method            string        `json:"method"`
	PathTemplate      string        `json:"pathTemplate"`
	Support           SupportStatus `json:"support"`
	Diagnostics       []Diagnostic  `json:"diagnostics,omitempty"`
}

// Severity classifies a Diagnostic.
type Severity string

// Diagnostic severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// ReasonCode is a machine-readable diagnostic or rejection code
// (data-model.md), stable across releases so consumers can branch on it.
type ReasonCode string

// ReasonPathParameterMismatch marks a path template placeholder that has no
// matching required `in: path` parameter -- the operation cannot be called
// safely because its request path cannot be built.
const ReasonPathParameterMismatch ReasonCode = "path_parameter_mismatch"

// Diagnostic records a problem found while normalizing an operation, with
// enough provenance to point back at the source document.
type Diagnostic struct {
	Severity Severity   `json:"severity"`
	Code     ReasonCode `json:"code,omitempty"`
	Pointer  string     `json:"pointer,omitempty"` // JSON Pointer into the source document
	Line     int        `json:"line,omitempty"`
	Col      int        `json:"col,omitempty"`
	Message  string     `json:"message"`
}

// SupportLevel classifies whether an operation can be published as a tool.
type SupportLevel string

// Support levels (data-model.md). An operation absent Level == Supported is
// excluded from the catalog by default.
const (
	SupportSupported          SupportLevel = "supported"
	SupportPartiallySupported SupportLevel = "partially_supported"
	SupportRejected           SupportLevel = "rejected"
)

// SupportStatus is the verdict for one operation plus the reasons behind it.
type SupportStatus struct {
	Level   SupportLevel `json:"level"`
	Reasons []ReasonCode `json:"reasons,omitempty"`
}
