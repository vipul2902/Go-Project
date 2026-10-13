//go:build integration

package worker_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/metrics"
	"github.com/vipul2902/clouddeploy/internal/tracing"
)

// One trace follows a deployment from the HTTP request, through the
// Postgres queue, into the worker's build and deploy steps.
func TestTraceSpansAPIQueueAndWorker(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	_, _, _ = tracing.Setup(context.Background(), "test", "v") // W3C propagator

	f := setup(t)
	f.start(t, fastConfig("w1"), newExec())
	id := f.queue(t, "api")
	f.waitFor(t, id, 5*time.Second, deployments.StatusRunning)
	time.Sleep(200 * time.Millisecond) // let the worker end its span

	spans := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		spans[s.Name()] = s
	}
	api, job := spans["POST /api/v1/deployments"], spans["deployment development"]
	if api == nil || job == nil {
		names := []string{}
		for n := range spans {
			names = append(names, n)
		}
		t.Fatalf("missing spans; have %v", names)
	}
	if job.SpanContext().TraceID() != api.SpanContext().TraceID() {
		t.Fatal("worker span is not in the API request's trace")
	}
	for _, step := range []string{"build", "deploy"} {
		s := spans[step]
		if s == nil || s.Parent().SpanID() != job.SpanContext().SpanID() {
			t.Errorf("%s span missing or not a child of the deployment span", step)
		}
	}
	if spans["db insert"] == nil {
		t.Error("no database span for the deployment insert")
	}
}

func TestDeploymentMetrics(t *testing.T) {
	f := setup(t)
	reg := prometheus.NewRegistry()
	m := metrics.NewDeployments(reg)
	metrics.NewQueueCollector(reg, func() (int, int, time.Duration, error) { return f.svc.QueueStats(context.Background()) })

	// Before any worker runs, two deployments wait in the queue.
	a, b := f.queue(t, "ok-app"), f.queue(t, "bad-app")
	if v := gaugeValue(t, reg, "deployment_queue_depth"); v != 2 {
		t.Errorf("queue depth = %v, want 2", v)
	}

	exec := newExec()
	exec.build = func(_ context.Context, j deployments.Job) (string, error) {
		if j.ProjectName == "bad-app" {
			return "", errors.New("compile error")
		}
		return "img", nil
	}
	cfg := fastConfig("w1")
	cfg.Metrics = m
	f.start(t, cfg, exec)
	f.waitFor(t, a, 5*time.Second, deployments.StatusRunning)
	f.waitFor(t, b, 5*time.Second, deployments.StatusFailed)
	time.Sleep(200 * time.Millisecond)

	exp := `
deployment_failures_total{environment="development",stage="build"} 1
deployment_total{environment="development",outcome="failed"} 1
deployment_total{environment="development",outcome="succeeded"} 1
`
	if got := nonZero(t, reg, "deployment_total", "deployment_failures_total"); got != strings.TrimSpace(exp) {
		t.Errorf("non-zero series:\n%s\nwant:\n%s", got, strings.TrimSpace(exp))
	}
	// Every known series exists from startup, at 0 if unused (so increase()
	// sees the first event).
	if n := testutil.CollectAndCount(m.TotalCollector()); n != 9 {
		t.Errorf("deployment_total has %d series, want 9 (3 environments x 3 outcomes)", n)
	}
	if v := gaugeValue(t, reg, "deployment_queue_depth"); v != 0 {
		t.Errorf("queue depth after processing = %v", v)
	}
	if v := gaugeValue(t, reg, "worker_deployments_in_flight"); v != 0 {
		t.Errorf("in-flight gauge leaked: %v", v)
	}
}

func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

// nonZero renders the non-zero samples of the named counters, sorted.
func nonZero(t *testing.T, reg *prometheus.Registry, names ...string) string {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, mf := range mfs {
		if !slices.Contains(names, mf.GetName()) {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.GetCounter().GetValue() == 0 {
				continue
			}
			var labels []string
			for _, l := range m.GetLabel() {
				labels = append(labels, l.GetName()+`="`+l.GetValue()+`"`)
			}
			out = append(out, mf.GetName()+"{"+strings.Join(labels, ",")+"} "+strconv.FormatFloat(m.GetCounter().GetValue(), 'f', -1, 64))
		}
	}
	slices.Sort(out)
	return strings.Join(out, "\n")
}
