// Package reload republishes a catalog without restarting the process
// (FR-72, docs/spec.md §7.6).
//
// The requirement names its own failure mode: a broken candidate must never
// destroy a working catalog. So a reload is not an edit. A candidate is built
// and validated in full, beside the catalog already serving; only when it is
// complete does one pointer move, and a call that has already started keeps
// the catalog it started on.
//
// What moves is the whole server, not its tool list. Adding and removing
// tools on a live server would be two critical sections with a gap between
// them, and a call arriving in that gap would find no tool -- a half-applied
// reload, which is worse than none. Swapping the value a transport looks up
// per request has no such gap.
//
// That also fixes where reload is available: a transport that resolves its
// server once per session cannot be handed a new one. See Holder.Server.
package reload
