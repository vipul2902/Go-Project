// Package tracing sets up OpenTelemetry tracing and the helpers that carry
// a trace across the deployment queue and into logs.
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Setup installs the global tracer provider and W3C propagator. Tracing is
// enabled only when OTEL_EXPORTER_OTLP_ENDPOINT is set (the standard
// OpenTelemetry variable); otherwise the no-op provider stays in place and
// tracing costs nothing. The returned function flushes buffered spans.
func Setup(ctx context.Context, service, version string) (shutdown func(context.Context) error, enabled bool, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, false, nil
	}
	exp, err := otlptracehttp.New(ctx) // reads OTEL_EXPORTER_OTLP_* settings
	if err != nil {
		return nil, false, fmt.Errorf("otlp exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(service), semconv.ServiceVersion(version)))
	if err != nil {
		return nil, false, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res),
		// Keep every trace in development; sample in production via the
		// standard OTEL_TRACES_SAMPLER / OTEL_TRACES_SAMPLER_ARG variables.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, true, nil
}

// Tracer returns the named tracer from the global provider.
func Tracer(name string) trace.Tracer { return otel.Tracer("github.com/vipul2902/clouddeploy/" + name) }

// Inject serializes the span context in ctx (W3C traceparent) for storage,
// e.g. on a queued deployment, so the worker can continue the trace.
func Inject(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// Extract restores a span context serialized by Inject.
func Extract(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}

// End records err on span (if any) and ends it.
func End(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// LogHandler adds trace_id and span_id to every log record written inside a
// traced operation, so logs and traces can be joined in either direction.
type LogHandler struct{ slog.Handler }

// Handle implements slog.Handler.
func (h LogHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h LogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return LogHandler{h.Handler.WithAttrs(as)}
}

// WithGroup implements slog.Handler.
func (h LogHandler) WithGroup(name string) slog.Handler { return LogHandler{h.Handler.WithGroup(name)} }

// PGXTracer creates a span per database query (pgx.QueryTracer). Statements
// are recorded with their placeholders only; argument values are never
// attached, since they may contain personal data or secrets.
type PGXTracer struct{}

type spanKey struct{}

// TraceQueryStart implements pgx.QueryTracer.
func (PGXTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !trace.SpanFromContext(ctx).SpanContext().IsValid() {
		return ctx // only trace queries that belong to a traced operation
	}
	ctx, span := Tracer("database").Start(ctx, "db "+operation(data.SQL), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.statement", truncate(data.SQL, 1000))))
	return context.WithValue(ctx, spanKey{}, span)
}

// TraceQueryEnd implements pgx.QueryTracer.
func (PGXTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if span, ok := ctx.Value(spanKey{}).(trace.Span); ok {
		span.SetAttributes(attribute.Int64("db.rows_affected", data.CommandTag.RowsAffected()))
		End(span, data.Err)
	}
}

// operation is the SQL verb, used as the span name (low cardinality).
func operation(sql string) string {
	f := strings.Fields(sql)
	if len(f) == 0 {
		return "query"
	}
	if strings.EqualFold(f[0], "WITH") {
		return "with"
	}
	return strings.ToLower(f[0])
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
