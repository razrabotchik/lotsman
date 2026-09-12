package openapi

import (
	"log/slog"
)

// parserLogger is the logger handed to libopenapi.
//
// It discards by default, and that is a deliberate inversion of the obvious
// arrangement. Everything the parser can tell an operator reaches lotsman as a
// returned error and becomes a Diagnostic with a JSON Pointer and a position;
// its log is the same information again, in its own vocabulary, without
// provenance. One exploded specification produced 662 log lines about "the
// rolodex" next to 662 diagnostics saying which reference could not be
// followed and where -- the second set is the one an operator can act on.
//
// Options.ParserLog reinstates the parser's own log for debugging. It must
// never be a logger that writes to stdout: on stdio transport that corrupts
// the JSON-RPC stream (ADR-0001).
func parserLogger(debug *slog.Logger) *slog.Logger {
	if debug != nil {
		return debug
	}
	return slog.New(slog.DiscardHandler)
}
