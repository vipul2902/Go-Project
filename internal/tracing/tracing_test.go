package tracing

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInjectExtractRoundTrip(t *testing.T) {
	_, _, _ = Setup(context.Background(), "test", "v") // installs the W3C propagator
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "api request")
	tp2 := Inject(ctx)
	if !strings.HasPrefix(tp2, "00-"+span.SpanContext().TraceID().String()+"-") {
		t.Fatalf("traceparent %q does not carry the trace id", tp2)
	}
	_, child := tp.Tracer("t").Start(Extract(context.Background(), tp2), "worker job")
	if child.SpanContext().TraceID() != span.SpanContext().TraceID() {
		t.Error("extracted context does not continue the trace")
	}
	if Inject(context.Background()) != "" || Extract(context.Background(), "") == nil {
		t.Error("no span: nothing to inject")
	}
}

func TestLogHandlerAddsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(LogHandler{Handler: slog.NewJSONHandler(&buf, nil)}).With("service", "x")
	ctx, span := sdktrace.NewTracerProvider().Tracer("t").Start(context.Background(), "op")
	logger.InfoContext(ctx, "inside")
	logger.InfoContext(context.Background(), "outside")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if !strings.Contains(lines[0], `"trace_id":"`+span.SpanContext().TraceID().String()+`"`) {
		t.Errorf("trace_id missing: %s", lines[0])
	}
	if strings.Contains(lines[1], "trace_id") {
		t.Errorf("trace_id on a log without a span: %s", lines[1])
	}
}

func TestOperationAndTruncate(t *testing.T) {
	for sql, want := range map[string]string{"SELECT 1": "select", "  UPDATE x": "update", "WITH a AS (...) SELECT": "with", "": "query"} {
		if got := operation(sql); got != want {
			t.Errorf("operation(%q) = %q, want %q", sql, got, want)
		}
	}
	if got := truncate("a\n\t  b", 10); got != "a b" {
		t.Errorf("truncate = %q", got)
	}
}

// With an OTLP endpoint configured, Setup must succeed: a semconv schema
// version that differs from the SDK's default resource fails at startup
// ("conflicting Schema URL"), which only shows up with tracing enabled.
func TestSetupWithExporterEnabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // nothing listens; export is async
	shutdown, enabled, err := Setup(context.Background(), "clouddeploy-test", "v0")
	if err != nil || !enabled {
		t.Fatalf("Setup: enabled=%v err=%v", enabled, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = shutdown(ctx) // export fails (no collector); shutdown must still return
}
