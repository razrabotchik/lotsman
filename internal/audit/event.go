package audit

import "time"

// SchemaVersion is the version of the event shape below.
//
// It ships with the first event on the report's precedent: anything anyone
// may parse is a promise, and a version is cheap before there are readers and
// expensive afterwards.
const SchemaVersion = 1

// Decision is how a call ended.
type Decision string

// Decisions. These are the three outcomes a call can have, and they are
// deliberately distinguishable: "refused" and "waiting to be approved" look
// the same to a caller who only sees that nothing happened.
const (
	// DecisionExecuted means a request reached the upstream and an answer
	// came back. It says nothing about whether the API liked it: a 4xx is an
	// executed call with a status (FR-39).
	DecisionExecuted Decision = "executed"
	// DecisionRefused means lotsman stopped the call. Reason says which
	// control did it.
	DecisionRefused Decision = "refused"
	// DecisionInputRequired means the call is waiting for a human to confirm
	// it and the client will make it again (FR-44). Nothing reached the
	// network, and nothing was denied either.
	DecisionInputRequired Decision = "input_required"
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

	Decision Decision `json:"decision"`
	// Reason is the machine-readable class of a refusal, empty otherwise.
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

	Status        int   `json:"status,omitempty"`
	DurationMs    int64 `json:"durationMs"`
	ResponseBytes int   `json:"responseBytes,omitempty"`
	Truncated     bool  `json:"truncated,omitempty"`
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
