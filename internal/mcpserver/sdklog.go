package mcpserver

import (
	"context"
	"log/slog"
)

// sdkLogger wraps the logger handed to the MCP SDK.
//
// The SDK logs the end of a session at ERROR level even when the client simply
// closed stdin mid-flight ("server is closing: EOF"), which is how desktop
// clients quit. Reporting that as an error trains operators to ignore errors,
// so it is demoted to DEBUG; everything else passes through untouched, tagged
// with its origin.
func sdkLogger(base *slog.Logger) *slog.Logger {
	return slog.New(&demoteCleanDisconnect{handler: base.Handler()}).With("component", "mcp-sdk")
}

type demoteCleanDisconnect struct{ handler slog.Handler }

func (h *demoteCleanDisconnect) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

//nolint:gocritic // slog.Handler fixes this signature: Record is passed by value.
func (h *demoteCleanDisconnect) Handle(ctx context.Context, rec slog.Record) error {
	if rec.Level < slog.LevelError || !isCleanDisconnectRecord(&rec) {
		return h.handler.Handle(ctx, rec)
	}
	if !h.handler.Enabled(ctx, slog.LevelDebug) {
		return nil
	}
	demoted := slog.NewRecord(rec.Time, slog.LevelDebug, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		demoted.AddAttrs(a)
		return true
	})
	return h.handler.Handle(ctx, demoted)
}

func (h *demoteCleanDisconnect) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &demoteCleanDisconnect{handler: h.handler.WithAttrs(attrs)}
}

func (h *demoteCleanDisconnect) WithGroup(name string) slog.Handler {
	return &demoteCleanDisconnect{handler: h.handler.WithGroup(name)}
}

// isCleanDisconnectRecord reports whether a record carries an "error" attribute
// that is really an ordinary client disconnect.
func isCleanDisconnectRecord(rec *slog.Record) bool {
	clean := false
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key != "error" {
			return true
		}
		if err, ok := a.Value.Any().(error); ok && isCleanDisconnect(err) {
			clean = true
		}
		return false
	})
	return clean
}
