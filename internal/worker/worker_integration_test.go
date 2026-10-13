//go:build integration

package worker_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/httpapi/apitest"
	"github.com/vipul2902/clouddeploy/internal/worker"
)

// testExecutor stands in for the external build/deploy systems so tests can
// drive success, failure, hangs and panics. It counts calls per deployment.
type testExecutor struct {
	build  func(ctx context.Context, job deployments.Job) (string, error)
	deploy func(ctx context.Context, job deployments.Job) error

	mu      sync.Mutex
	builds  map[string]int
	deploys map[string]int
}

func newExec() *testExecutor {
	return &testExecutor{builds: map[string]int{}, deploys: map[string]int{},
		build: func(_ context.Context, j deployments.Job) (string, error) {
			return j.ProjectName + ":" + j.Deployment.CommitSHA, nil
		},
		deploy: func(context.Context, deployments.Job) error { return nil },
	}
}

func (e *testExecutor) Build(ctx context.Context, job deployments.Job, log worker.LogFunc) (string, error) {
	e.mu.Lock()
	e.builds[job.Deployment.ID]++
	e.mu.Unlock()
	log("info", "test build")
	return e.build(ctx, job)
}

func (e *testExecutor) Deploy(ctx context.Context, job deployments.Job, _ string, log worker.LogFunc) error {
	e.mu.Lock()
	e.deploys[job.Deployment.ID]++
	e.mu.Unlock()
	log("info", "test deploy")
	return e.deploy(ctx, job)
}

func (e *testExecutor) counts(id string) (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.builds[id], e.deploys[id]
}

type fixture struct {
	api  *apitest.API
	user apitest.User
	svc  *deployments.Service
}

func setup(t *testing.T) fixture {
	t.Helper()
	api := apitest.New(t)
	return fixture{api: api, user: api.Register("dev@example.com"), svc: deployments.NewService(api.Pool)}
}

// queue creates a project with a development environment and deploys it.
func (f fixture) queue(t *testing.T, name string) string {
	t.Helper()
	pid := f.api.Do(http.MethodPost, "/api/v1/projects", f.user.Token, map[string]string{
		"organization_id": f.user.OrgID, "name": name, "repository_url": "https://github.com/acme/" + name,
	}).Str("id")
	f.api.Do(http.MethodPost, "/api/v1/projects/"+pid+"/environments", f.user.Token, map[string]string{"name": "development"})
	res := f.api.Do(http.MethodPost, "/api/v1/deployments", f.user.Token, map[string]string{
		"project_id": pid, "environment": "development", "commit_sha": "abc1234",
	})
	if res.Status != http.StatusAccepted {
		t.Fatalf("deploy %s: %d %v", name, res.Status, res.Body)
	}
	return res.Str("id")
}

func fastConfig(id string) worker.Config {
	return worker.Config{
		ID: id, Concurrency: 2, PollInterval: 50 * time.Millisecond, LeaseTTL: 600 * time.Millisecond,
		ReapInterval: 100 * time.Millisecond, ShutdownGrace: 3 * time.Second,
		BuildTimeout: 10 * time.Second, DeployTimeout: 10 * time.Second,
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// start runs a worker until the test ends (or stop is called) and returns stop.
func (f fixture) start(t *testing.T, cfg worker.Config, exec worker.Executor) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = worker.New(f.api.Pool, f.svc, exec, cfg, quiet).Run(ctx)
	}()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

func (f fixture) waitFor(t *testing.T, id string, timeout time.Duration, want ...deployments.Status) deployments.Deployment {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		d, err := f.svc.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if d.Status == w {
				return d
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("deployment %s is %s after %v; want %v", id, d.Status, timeout, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (f fixture) auditActions(t *testing.T, id string) []string {
	t.Helper()
	rows, _ := f.api.Pool.Query(t.Context(), `SELECT action FROM audit_logs WHERE resource_id = $1 ORDER BY id`, id)
	var out []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		out = append(out, a)
	}
	return out
}

func TestHappyPath(t *testing.T) {
	f := setup(t)
	exec := newExec()
	f.start(t, fastConfig("w1"), exec)
	id := f.queue(t, "api")

	d := f.waitFor(t, id, 5*time.Second, deployments.StatusRunning, deployments.StatusFailed)
	if d.Status != deployments.StatusRunning || d.Image == nil || *d.Image != "api:abc1234" || d.CompletedAt == nil {
		t.Fatalf("deployment: %+v", d)
	}
	if b, dep := exec.counts(id); b != 1 || dep != 1 {
		t.Errorf("builds=%d deploys=%d", b, dep)
	}
	if got := strings.Join(f.auditActions(t, id), ","); got != "deployment.created,deployment.succeeded" {
		t.Errorf("audit = %s", got)
	}

	// Pipeline logs are exposed over the API and can be tailed with ?after.
	logs := f.api.Do(http.MethodGet, "/api/v1/deployments/"+id+"/logs", f.user.Token, nil)
	var msgs []string
	for _, l := range logs.Data() {
		msgs = append(msgs, l["message"].(string))
	}
	joined := strings.Join(msgs, " | ")
	for _, want := range []string{"claimed by worker w1", "test build", "test deploy", "deployment succeeded"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs missing %q: %s", want, joined)
		}
	}
	next := fmt.Sprint(int64(logs.Body["next_after"].(float64)))
	if tail := f.api.Do(http.MethodGet, "/api/v1/deployments/"+id+"/logs?after="+next, f.user.Token, nil); len(tail.Data()) != 0 {
		t.Errorf("tail after last id returned %d lines", len(tail.Data()))
	}
}

func TestBuildFailure(t *testing.T) {
	f := setup(t)
	exec := newExec()
	exec.build = func(context.Context, deployments.Job) (string, error) {
		return "", errors.New("compile error in main.go")
	}
	f.start(t, fastConfig("w1"), exec)
	id := f.queue(t, "api")

	d := f.waitFor(t, id, 5*time.Second, deployments.StatusFailed, deployments.StatusRunning)
	if d.Status != deployments.StatusFailed || d.ErrorMessage == nil || *d.ErrorMessage != "build: compile error in main.go" {
		t.Fatalf("deployment: %s %v", d.Status, d.ErrorMessage)
	}
	if _, dep := exec.counts(id); dep != 0 {
		t.Error("deploy ran after a failed build")
	}
	if got := f.auditActions(t, id); got[len(got)-1] != "deployment.failed" {
		t.Errorf("audit = %v", got)
	}
}

func TestExecutorPanicFailsDeployment(t *testing.T) {
	f := setup(t)
	exec := newExec()
	exec.deploy = func(context.Context, deployments.Job) error { panic("nil map write") }
	f.start(t, fastConfig("w1"), exec)
	id := f.queue(t, "api")
	d := f.waitFor(t, id, 5*time.Second, deployments.StatusFailed, deployments.StatusRunning)
	if d.Status != deployments.StatusFailed || !strings.Contains(*d.ErrorMessage, "panicked") {
		t.Fatalf("deployment: %s %v", d.Status, d.ErrorMessage)
	}
	// The worker survived the panic and keeps processing.
	exec.deploy = func(context.Context, deployments.Job) error { return nil }
	id2 := f.queue(t, "api2")
	if d := f.waitFor(t, id2, 5*time.Second, deployments.StatusRunning, deployments.StatusFailed); d.Status != deployments.StatusRunning {
		t.Fatalf("after panic: %s", d.Status)
	}
}

// A user cancels while the build is running. The heartbeat notices, the
// build's context is cancelled, and CANCELLED is not overwritten by FAILED.
func TestCancelDuringBuildStopsExecutor(t *testing.T) {
	f := setup(t)
	exec := newExec()
	started, stopped := make(chan struct{}), make(chan time.Time, 1)
	exec.build = func(ctx context.Context, _ deployments.Job) (string, error) {
		close(started)
		<-ctx.Done() // a well-behaved long step honours cancellation
		stopped <- time.Now()
		return "", ctx.Err()
	}
	f.start(t, fastConfig("w1"), exec)
	id := f.queue(t, "api")

	<-started
	cancelledAt := time.Now()
	if res := f.api.Do(http.MethodPost, "/api/v1/deployments/"+id+"/cancel", f.user.Token, nil); res.Status != http.StatusOK {
		t.Fatalf("cancel: %d %v", res.Status, res.Body)
	}
	select {
	case at := <-stopped:
		if lag := at.Sub(cancelledAt); lag > time.Second {
			t.Errorf("build stopped %v after cancel; heartbeat too slow", lag)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("build was not cancelled")
	}
	time.Sleep(200 * time.Millisecond) // let the worker finish its bookkeeping
	d := f.waitFor(t, id, time.Second, deployments.StatusCancelled, deployments.StatusFailed)
	if d.Status != deployments.StatusCancelled || *d.ErrorMessage != "cancelled by user" {
		t.Fatalf("status %s (%v); cancellation was overwritten", d.Status, *d.ErrorMessage)
	}
	if _, dep := exec.counts(id); dep != 0 {
		t.Error("deploy ran after cancellation")
	}
}

// Three worker processes compete for twelve jobs; each job must be
// processed exactly once.
func TestEachJobProcessedExactlyOnce(t *testing.T) {
	f := setup(t)
	exec := newExec()
	exec.build = func(_ context.Context, j deployments.Job) (string, error) {
		time.Sleep(30 * time.Millisecond) // widen the window for double claims
		return j.ProjectName + ":x", nil
	}
	var ids []string
	for i := range 12 {
		ids = append(ids, f.queue(t, fmt.Sprintf("svc-%02d", i)))
	}
	for _, w := range []string{"wa", "wb", "wc"} {
		cfg := fastConfig(w)
		cfg.Concurrency = 3
		f.start(t, cfg, exec)
	}
	claimedBy := map[string]bool{}
	for _, id := range ids {
		if d := f.waitFor(t, id, 10*time.Second, deployments.StatusRunning, deployments.StatusFailed); d.Status != deployments.StatusRunning {
			t.Fatalf("%s: %s", id, d.Status)
		}
		if b, dep := exec.counts(id); b != 1 || dep != 1 {
			t.Errorf("%s built %d times, deployed %d times", id, b, dep)
		}
		var by string
		_ = f.api.Pool.QueryRow(t.Context(), `SELECT claimed_by FROM deployments WHERE id = $1`, id).Scan(&by)
		claimedBy[by] = true
	}
	if len(claimedBy) < 2 {
		t.Logf("note: only %d worker(s) got jobs; work was not spread (acceptable, not an error)", len(claimedBy))
	}
}

// With polling effectively off, only Postgres NOTIFY can wake the worker.
func TestNotifyWakesIdleWorker(t *testing.T) {
	f := setup(t)
	cfg := fastConfig("w1")
	cfg.PollInterval = time.Hour
	f.start(t, cfg, newExec())
	time.Sleep(300 * time.Millisecond) // worker is now idle, listening

	start := time.Now()
	id := f.queue(t, "api")
	f.waitFor(t, id, 3*time.Second, deployments.StatusRunning)
	t.Logf("queued -> running in %v via NOTIFY", time.Since(start).Round(time.Millisecond))
}

// A worker claims a deployment and dies (no heartbeats). The reaper of a
// live worker fails it once the lease expires.
func TestReaperFailsDeploymentOfDeadWorker(t *testing.T) {
	f := setup(t)
	id := f.queue(t, "api")
	job, err := f.svc.Claim(t.Context(), "dead-worker", 300*time.Millisecond)
	if err != nil || job == nil || job.Deployment.ID != id {
		t.Fatalf("claim: %v %v", job, err)
	}
	f.start(t, fastConfig("survivor"), newExec())

	d := f.waitFor(t, id, 3*time.Second, deployments.StatusFailed, deployments.StatusRunning)
	if d.Status != deployments.StatusFailed || *d.ErrorMessage != deployments.LeaseLostMessage {
		t.Fatalf("status %s: %v", d.Status, d.ErrorMessage)
	}
	if got := f.auditActions(t, id); got[len(got)-1] != "deployment.failed" {
		t.Errorf("audit = %v", got)
	}
	// The environment is free again for a new deployment.
	if res := f.api.Do(http.MethodPost, "/api/v1/deployments", f.user.Token, map[string]string{
		"project_id": job.Deployment.ProjectID, "environment": "development", "commit_sha": "def5678",
	}); res.Status != http.StatusAccepted {
		t.Errorf("redeploy after reap: %d", res.Status)
	}
}

// SIGTERM mid-build: the worker stops claiming but lets the job finish.
func TestShutdownDrainsInFlightJob(t *testing.T) {
	f := setup(t)
	exec := newExec()
	started := make(chan struct{})
	exec.build = func(ctx context.Context, j deployments.Job) (string, error) {
		close(started)
		select {
		case <-time.After(500 * time.Millisecond):
			return j.ProjectName + ":x", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	stop := f.start(t, fastConfig("w1"), exec)
	id := f.queue(t, "api")
	<-started
	stop() // returns only after draining
	if d, _ := f.svc.Get(t.Context(), id); d.Status != deployments.StatusRunning {
		t.Fatalf("after graceful shutdown: %s %v", d.Status, d.ErrorMessage)
	}
}

// SIGTERM with a job that will not finish within the grace period: it is
// interrupted and recorded as FAILED with an explanation, not left hanging.
func TestShutdownGraceExceeded(t *testing.T) {
	f := setup(t)
	exec := newExec()
	started := make(chan struct{})
	exec.build = func(ctx context.Context, _ deployments.Job) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	cfg := fastConfig("w1")
	cfg.ShutdownGrace = 200 * time.Millisecond
	stop := f.start(t, cfg, exec)
	id := f.queue(t, "api")
	<-started

	begin := time.Now()
	stop()
	if took := time.Since(begin); took > 2*time.Second {
		t.Errorf("shutdown took %v", took)
	}
	d, _ := f.svc.Get(t.Context(), id)
	if d.Status != deployments.StatusFailed || !strings.HasPrefix(*d.ErrorMessage, "interrupted") {
		t.Fatalf("status %s: %v", d.Status, d.ErrorMessage)
	}
}

// SKIP LOCKED is about liveness, not correctness: without it, a claimer
// blocks behind another claimer's locked row (and may then come back
// empty-handed even though other jobs are waiting). While the oldest job is
// locked by an in-flight claim, a second claim must return the NEXT job
// immediately.
func TestClaimSkipsLockedRowsWithoutBlocking(t *testing.T) {
	f := setup(t)
	first := f.queue(t, "first")
	second := f.queue(t, "second")

	tx, err := f.api.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	// Simulate another worker mid-claim holding the oldest queued row.
	if _, err := tx.Exec(t.Context(), `SELECT id FROM deployments WHERE id = $1 FOR UPDATE`, first); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	begin := time.Now()
	job, err := f.svc.Claim(ctx, "w2", time.Minute)
	if err != nil {
		t.Fatalf("claim blocked or failed after %v: %v", time.Since(begin), err)
	}
	if job == nil || job.Deployment.ID != second {
		t.Fatalf("claimed %v, want the second (unlocked) job", job)
	}
	t.Logf("claimed the next job in %v while the oldest was locked", time.Since(begin).Round(time.Millisecond))
}
