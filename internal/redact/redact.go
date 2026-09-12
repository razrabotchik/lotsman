package redact

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// Placeholder replaces a redacted value. It names what was removed so a reader
// knows something was there, without saying what.
const Placeholder = "[redacted]"

// minLength is the shortest value worth redacting. A one-character "secret"
// would turn every log line into confetti, and a credential that short is not
// one.
const minLength = 6

// Registry remembers the secret values this process has resolved, so they can
// be removed from anything it writes.
//
// It holds the values in memory, which is the same place the process already
// holds them after reading an environment variable or a file: the registry
// adds no new exposure, and it is what makes redaction possible at all --
// matching a value requires having it.
type Registry struct {
	mu     sync.RWMutex
	values map[string]struct{}
}

// Default is the process-wide registry. Credential providers add to it as they
// resolve; log handlers and result shapers read from it.
var Default = &Registry{values: map[string]struct{}{}}

// Add registers a resolved secret. Short values are ignored (see minLength).
func (r *Registry) Add(value string) {
	if len(value) < minLength {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[value] = struct{}{}
}

// String removes every registered secret from s.
func (r *Registry) String(s string) string {
	if s == "" {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for value := range r.values {
		if strings.Contains(s, value) {
			s = strings.ReplaceAll(s, value, Placeholder)
		}
	}
	return s
}

// Error redacts an error's message while keeping the error chain intact, so
// errors.Is and the class survive redaction.
func (r *Registry) Error(err error) error {
	if err == nil {
		return nil
	}
	redacted := r.String(err.Error())
	if redacted == err.Error() {
		return err
	}
	return &redactedError{err: err, message: redacted}
}

type redactedError struct {
	err     error
	message string
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.err }

// Add registers a secret with the default registry.
func Add(value string) { Default.Add(value) }

// String redacts using the default registry.
func String(s string) string { return Default.String(s) }

// Error redacts using the default registry.
func Error(err error) error { return Default.Error(err) }

// Handler wraps a slog handler, redacting messages and attribute values.
//
// It sits at the outermost layer on purpose: whatever any package logs, and
// however it got there, the bytes leaving the process pass through here.
type Handler struct {
	inner    slog.Handler
	registry *Registry
}

// NewHandler wraps inner with redaction against the default registry.
func NewHandler(inner slog.Handler) slog.Handler {
	return &Handler{inner: inner, registry: Default}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

//nolint:gocritic // hugeParam: slog.Handler fixes this signature: Record is passed by value.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, h.registry.String(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		clean.AddAttrs(h.redactAttr(attr))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *Handler) redactAttr(attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, h.registry.String(value.String()))
	case slog.KindGroup:
		attrs := value.Group()
		clean := make([]any, 0, len(attrs))
		for _, nested := range attrs {
			clean = append(clean, h.redactAttr(nested))
		}
		return slog.Group(attr.Key, clean...)
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return slog.Any(attr.Key, h.registry.Error(err))
		}
		// Anything else is rendered by the handler; redact its rendering.
		return slog.String(attr.Key, h.registry.String(value.String()))
	default:
		return attr
	}
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		clean = append(clean, h.redactAttr(attr))
	}
	return &Handler{inner: h.inner.WithAttrs(clean), registry: h.registry}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), registry: h.registry}
}
