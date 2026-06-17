package tracing

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// SlogHandler wraps a slog.Handler and, when a record is emitted with a context
// carrying an active span, adds "trace_id" and "span_id" attributes. This
// correlates log lines with traces so a log line can be linked to its trace.
type SlogHandler struct {
	inner slog.Handler
}

// NewSlogHandler wraps inner with trace/span correlation.
func NewSlogHandler(inner slog.Handler) *SlogHandler {
	return &SlogHandler{inner: inner}
}

// Enabled implements slog.Handler.
func (h *SlogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler, injecting trace_id/span_id from ctx when present.
func (h *SlogHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, record)
}

// WithAttrs implements slog.Handler, preserving the wrapper so correlation
// survives loggers derived via Logger.With.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SlogHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler, preserving the wrapper.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	return &SlogHandler{inner: h.inner.WithGroup(name)}
}
