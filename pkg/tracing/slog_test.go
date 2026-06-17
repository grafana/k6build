package tracing

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestSlogHandlerInjectsTraceContext(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	log := slog.New(NewSlogHandler(slog.NewTextHandler(buf, nil)))

	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	log.InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, "trace_id="+traceID.String()) {
		t.Errorf("expected trace_id in log output, got: %s", out)
	}
	if !strings.Contains(out, "span_id="+spanID.String()) {
		t.Errorf("expected span_id in log output, got: %s", out)
	}
}

func TestSlogHandlerNoSpanContext(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	log := slog.New(NewSlogHandler(slog.NewTextHandler(buf, nil)))

	log.Info("hello")

	out := buf.String()
	if strings.Contains(out, "trace_id") {
		t.Errorf("did not expect trace_id without an active span, got: %s", out)
	}
}

func TestSlogHandlerWithAttrsPreservesCorrelation(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	// Logger.With derives a handler via WithAttrs; correlation must survive.
	log := slog.New(NewSlogHandler(slog.NewTextHandler(buf, nil))).With("platform", "linux/amd64")

	traceID, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	spanID, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	log.InfoContext(ctx, "hello")

	out := buf.String()
	if !strings.Contains(out, "trace_id="+traceID.String()) {
		t.Errorf("expected trace_id to survive With(), got: %s", out)
	}
}
