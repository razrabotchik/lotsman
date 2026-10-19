package audit

import "time"

// SchemaVersion is the version of the event shape below.
//
// It ships with the first event on the report's precedent: anything anyone
// may parse is a promise, and a version is cheap before there are readers and
// expensive afterwards.
//
// Version 2 narrowed `refused` to what its name says and added `failed` and
// `outcome_unknown` for the calls version 1 wrongly recorded as refused: a
// request that left the process and never came back (ADR-0019).
//
// Version 3 added `files`: what a call read from this machine's disk and sent
// to the API (feature 009). A record of an upload that does not say which
// bytes left is not a record of the upload.
const SchemaVersion = 3

// Decision is how a call ended.
type Decision string

// Decisions. They are deliberately distinguishable: "refused", "waiting to be
// approved" and "sent, and nobody knows what happened" all look the same to a
// caller who only sees that no answer came back -- and only the first two
// promise that the API did nothing.
const (
	// DecisionExecuted means a request reached the upstream and an answer
	// came back. It says nothing about whether the API liked it: a 4xx is an
	// executed call with a status (FR-39). Reason is set when the answer
	// arrived and its body could not be read.
	DecisionExecuted Decision = "executed"
	// DecisionRefused means lotsman stopped the call, and nothing reached
	// the upstream. Reason says which control did it.
	DecisionRefused Decision = "refused"
	// DecisionInputRequired means the call is waiting for a human to confirm
	// it and the client will make it again (FR-44). Nothing reached the
	// network, and nothing was denied either.
	DecisionInputRequired Decision = "input_required"
	// DecisionFailed means lotsman tried to send the request and could not:
	// no connection, or one that broke before the request was written.
	// Nothing reached the upstream, so nothing took effect.
	DecisionFailed Decision = "failed"
	// DecisionOutcomeUnknown means the request was written and no response
	// came back. The upstream may have acted on it; for a mutation, that is
	// the one fact that matters before trying again.
	DecisionOutcomeUnknown Decision = "outcome_unknown"
)

// Event is one completed call.
type Event struct {
	SchemaVersion int       `json:"schemaVersion"`
	Time          time.Time `json:"time"`

	// Operation is the catalog key; Tool is the published name. Both, because
	// a reader chasing an incident has one of them and wants the other.
	Operation string `json:"operation"`
	Tool      string `json:"tool"`
	Effect    string `json:"effect"`

	// Subject is who asked, when the transport authenticated them (FR-81).
	// It is recorded and never enforced: matching policy on a subject is
	// RBAC, and §8 defers RBAC to the gateway layer. An operator who saw a
	// subject in a rule would reasonably assume the rest of it exists.
	//
	// The subject, never the token. One identifies a caller in a record
	// somebody may read months later; the other is a credential.
	Subject string `json:"subject,omitempty"`

	Decision Decision `json:"decision"`
	// Reason is the machine-readable class of whatever stopped the call,
	// empty when nothing did.
	Reason string `json:"reason,omitempty"`

	// Origin is the scheme, host and port that would have been called —
	// never the query, which is where an API key travels when a document
	// puts it there.
	Origin string `json:"origin,omitempty"`
	Method string `json:"method,omitempty"`
	// Path is the *template*, not the expanded path. The expansion carries
	// the caller's arguments, and an argument is data lotsman was trusted
	// with rather than data it may write down.
	Path string `json:"path,omitempty"`

	// Refreshed reports that a credential was re-minted and the request made
	// again (FR-35's *audit metadata*). That it happened, never what the new
	// credential is.
	Refreshed bool `json:"refreshed,omitempty"`

	Status        int   `json:"status,omitempty"`
	DurationMs    int64 `json:"durationMs"`
	ResponseBytes int   `json:"responseBytes,omitempty"`
	Truncated     bool  `json:"truncated,omitempty"`

	// Files are the local files this call sent (feature 009), in part order.
	// Empty for every call that sent none.
	Files []FileRecord `json:"files,omitempty"`
}

// FileRecord is one file a call read from this machine and sent.
//
// The name, never the path: a path describes the host and the operator's
// directory layout, and the basename is what identifies the thing that left.
// The hash is of the bytes actually streamed, which is why it is here and not
// in the approval prompt -- it is known afterwards, and a hash computed from a
// second read would describe a different file (ADR-0022 decision 7). It is
// empty for a call that was refused before anything was sent.
type FileRecord struct {
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"contentType,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
}

// Sink receives events. Recording must never fail a call: a runtime that
// stops working because it cannot describe what it is doing has its
// priorities backwards, so there is no error to return.
type Sink interface {
	Record(Event)
}

// Discard is the sink for a process nobody is auditing.
type Discard struct{}

// Record throws the event away.
func (Discard) Record(Event) {}

// Multi fans one event out to several sinks, in order.
type Multi []Sink

// Record hands the event to each sink.
//
//nolint:gocritic // hugeParam: an Event travels by value so that a sink cannot edit the record the next sink in a Multi is about to write.
func (m Multi) Record(event Event) {
	for _, sink := range m {
		if sink != nil {
			sink.Record(event)
		}
	}
}
