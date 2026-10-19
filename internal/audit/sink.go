package audit

import "log/slog"

// LogSink writes events through the process logger.
//
// Not a second writer to stderr of its own: the logger already carries the
// two properties an audit record must not lose. Its outermost handler is the
// redaction filter (FR-61), so an event that somehow quoted a credential is
// scrubbed on the way out, and it is bound to stderr, so FR-68's rule that
// stdout belongs to the protocol holds without being restated here. A sink
// that opened its own file would have to re-derive both and would eventually
// get one of them wrong.
type LogSink struct {
	log *slog.Logger
}

// Log builds a sink over a logger. A nil logger records nothing rather than
// panicking somewhere deep inside a call.
func Log(log *slog.Logger) Sink {
	if log == nil {
		return Discard{}
	}
	return LogSink{log: log}
}

// Record writes one event.
//
// At info, because an audit record is not a diagnostic: an operator who
// raised the level to quiet the debug chatter has not asked to stop being
// told what was called.
//
//nolint:gocritic // hugeParam: an Event travels by value so that a sink cannot edit the record the next sink in a Multi is about to write.
func (s LogSink) Record(event Event) {
	attrs := []any{
		"schemaVersion", event.SchemaVersion,
		"operation", event.Operation,
		"tool", event.Tool,
		"effect", event.Effect,
		"subject", event.Subject,
		"decision", string(event.Decision),
		"reason", event.Reason,
		"origin", event.Origin,
		"method", event.Method,
		"path", event.Path,
		"refreshed", event.Refreshed,
		"status", event.Status,
		"durationMs", event.DurationMs,
		"responseBytes", event.ResponseBytes,
		"truncated", event.Truncated,
	}
	// One attribute per file, so a line stays greppable: the name that left,
	// its size, its type and the hash of the bytes that were streamed. Never
	// the path (ADR-0022 decision 8).
	for i := range event.Files {
		file := &event.Files[i]
		attrs = append(attrs, "file", fileAttr(file))
	}
	s.log.Info("audit", attrs...)
}

// fileAttr renders one file record for a log line.
func fileAttr(file *FileRecord) slog.Value {
	values := []slog.Attr{
		slog.String("name", file.Name),
		slog.Int64("bytes", file.Bytes),
	}
	if file.ContentType != "" {
		values = append(values, slog.String("contentType", file.ContentType))
	}
	if file.SHA256 != "" {
		values = append(values, slog.String("sha256", file.SHA256))
	}
	return slog.GroupValue(values...)
}
