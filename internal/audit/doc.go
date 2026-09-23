// Package audit records what lotsman did, for someone reading afterwards.
//
// It has been a package comment since 001 on purpose: an event format with
// one writer and no reader is an abstraction, and the shape of the record is
// decided by what the reader needs. The HTTP profile is the first deployment
// where nobody is watching the terminal, so this is where the reader arrives
// and where the type does.
//
// An event carries the operation key, the effect, the decision, the origin
// without its query, the status, the timing and the sizes (docs/spec.md §7.4)
// — never arguments, never bodies, never a credential (Constitution VI). It
// is written at the last stage of the execution pipeline (§7.5), which is the
// only place that knows how the call ended.
package audit
