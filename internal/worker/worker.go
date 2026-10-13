// Package worker processes queued deployments asynchronously.
//
// Lifecycle of one job:
//
//	claim (QUEUED -> BUILDING, lease) -> Build -> DEPLOYING -> Deploy -> RUNNING
//	                          │ heartbeat renews the lease; if the deployment
//	                          │ was cancelled or reaped, the job ctx is cancelled
//	any error -> FAILED (unless the user cancelled: CANCELLED is kept)
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/metrics"
	"github.com/vipul2902/clouddeploy/internal/tracing"
)

// Config tunes the worker.
type Config struct {
	ID            string        // unique per process; recorded as claimed_by
	Concurrency   int           // max deployments processed at once
	PollInterval  time.Duration // fallback when no NOTIFY arrives
	LeaseTTL      time.Duration // how long a claim survives without heartbeats
	ReapInterval  time.Duration // how often expired leases are failed
	ShutdownGrace time.Duration // time in-flight jobs get to finish on shutdown
	BuildTimeout  time.Duration
	DeployTimeout time.Duration
	// Metrics records deployment metrics; nil disables them.
	Metrics *metrics.Deployments
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	host, _ := os.Hostname()
	return Config{
		ID:            fmt.Sprintf("%s-%d", host, os.Getpid()),
		Concurrency:   2,
		PollInterval:  5 * time.Second,
		LeaseTTL:      30 * time.Second,
		ReapInterval:  15 * time.Second,
		ShutdownGrace: 20 * time.Second,
		BuildTimeout:  15 * time.Minute,
		DeployTimeout: 10 * time.Minute,
	}
}

// Worker claims and executes deployments.
type Worker struct {
	cfg    Config
	pool   *pgxpool.Pool // for LISTEN; nil disables notifications (poll only)
	svc    *deployments.Service
	exec   Executor
	logger *slog.Logger
	wake   chan struct{}
}

// New returns a worker.
func New(pool *pgxpool.Pool, svc *deployments.Service, exec Executor, cfg Config, logger *slog.Logger) *Worker {
	return &Worker{cfg: cfg, pool: pool, svc: svc, exec: exec, logger: logger.With("worker_id", cfg.ID),
		wake: make(chan struct{}, 1)}
}

// Run processes deployments until ctx is cancelled, then shuts down
// gracefully: no new claims; in-flight jobs get ShutdownGrace to finish,
// after which they are interrupted and recorded as FAILED.
func (w *Worker) Run(ctx context.Context) error {
	// Jobs run on a context that survives ctx's cancellation, so a shutdown
	// signal stops claiming without killing work mid-step. hardStop ends
	// them once the grace period is exhausted.
	jobBase, hardStop := context.WithCancel(context.WithoutCancel(ctx))
	defer hardStop()

	var bg sync.WaitGroup // listener + reaper: stop immediately on ctx
	bg.Add(2)
	go func() { defer bg.Done(); w.listen(ctx) }()
	go func() { defer bg.Done(); w.reapLoop(ctx) }()

	var loops sync.WaitGroup
	for range w.cfg.Concurrency {
		loops.Add(1)
		go func() { defer loops.Done(); w.loop(ctx, jobBase) }()
	}
	w.logger.Info("worker started", "concurrency", w.cfg.Concurrency)

	<-ctx.Done()
	w.logger.Info("shutdown requested; draining in-flight deployments", "grace", w.cfg.ShutdownGrace)
	done := make(chan struct{})
	go func() { loops.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(w.cfg.ShutdownGrace):
		w.logger.Warn("grace period exhausted; interrupting in-flight deployments")
		hardStop()
		<-done
	}
	bg.Wait()
	w.logger.Info("worker stopped")
	return nil
}

// loop claims and processes jobs one at a time until ctx is cancelled.
func (w *Worker) loop(ctx, jobBase context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		job, err := w.svc.Claim(ctx, w.cfg.ID, w.cfg.LeaseTTL)
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Error("claim failed", "err", err, "retry_in", backoff)
			}
			sleep(ctx, backoff)
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if job == nil {
			select { // idle: wait for a NOTIFY or the poll interval
			case <-ctx.Done():
			case <-w.wake:
			case <-time.After(w.cfg.PollInterval):
			}
			continue
		}
		// There may be more queued work: let another idle loop look too.
		w.signal()
		w.process(jobBase, *job)
	}
}

func (w *Worker) signal() {
	select {
	case w.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// process runs one claimed job to a terminal state.
func (w *Worker) process(base context.Context, job deployments.Job) {
	d := job.Deployment
	// Continue the trace of the API request that queued this deployment:
	// one trace covers request -> queue -> build -> rollout.
	traced, span := tracing.Tracer("worker").Start(tracing.Extract(base, job.TraceParent), "deployment "+d.Environment,
		trace.WithAttributes(
			attribute.String("deployment.id", d.ID), attribute.String("deployment.environment", d.Environment),
			attribute.String("project.name", job.ProjectName), attribute.String("vcs.commit_sha", d.CommitSHA),
			attribute.Bool("deployment.rollback", d.RollbackOf != nil)))
	if d.StartedAt != nil {
		span.SetAttributes(attribute.Float64("deployment.queue_wait_seconds", d.StartedAt.Sub(d.CreatedAt).Seconds()))
	}
	w.cfg.Metrics.Started()
	var failStage string
	defer func() {
		w.cfg.Metrics.Done()
		w.recordOutcome(base, d, failStage, span)
		span.End()
	}()
	logger := w.logger.With("deployment_id", d.ID, "environment", d.Environment)
	if sc := span.SpanContext(); sc.IsValid() {
		// Every log line about this job joins its trace, including lines
		// logged without a context (fail, autoRollback, heartbeat).
		logger = logger.With("trace_id", sc.TraceID().String())
	}
	jobCtx, cancelJob := context.WithCancel(traced)
	defer cancelJob()

	// lost is set when the heartbeat finds we no longer own the deployment
	// (cancelled by a user, or reaped). Its state must then be left alone.
	var lost atomic.Bool
	hbDone := make(chan struct{})
	go func() { defer close(hbDone); w.heartbeat(jobCtx, d.ID, &lost, cancelJob, logger) }()
	defer func() { cancelJob(); <-hbDone }()

	logLine := func(level, msg string) {
		if err := detached(base, func(ctx context.Context) error { return w.svc.AppendLog(ctx, d.ID, level, msg) }); err != nil {
			logger.Warn("append deployment log failed", "err", err)
		}
	}

	defer func() {
		if p := recover(); p != nil {
			failStage = "panic"
			logger.Error("executor panicked", "panic", p)
			w.fail(base, job, &lost, errors.New("internal error: executor panicked"), logLine, logger)
		}
	}()

	logger.Info("deployment claimed")
	logLine("info", fmt.Sprintf("claimed by worker %s; building %s", w.cfg.ID, d.CommitSHA))

	buildCtx, cancelBuild := context.WithTimeout(jobCtx, w.cfg.BuildTimeout)
	image, err := w.step(buildCtx, "build", func(ctx context.Context) (string, error) { return w.exec.Build(ctx, job, logLine) })
	cancelBuild()
	if err != nil {
		failStage = "build"
		w.fail(base, job, &lost, fmt.Errorf("build: %w", err), logLine, logger) // nothing applied: no rollback
		return
	}
	if err := detached(base, func(ctx context.Context) error { return w.svc.SetImage(ctx, d.ID, image) }); err != nil {
		w.fail(base, job, &lost, fmt.Errorf("record image: %w", err), logLine, logger)
		return
	}
	if err := detached(base, func(ctx context.Context) error {
		_, err := w.svc.Transition(ctx, d.ID, deployments.StatusDeploying, "")
		return err
	}); err != nil {
		w.fail(base, job, &lost, err, logLine, logger)
		return
	}
	logLine("info", "image "+image+" ready; deploying")

	if d.RollbackOf != nil {
		logLine("info", fmt.Sprintf("rolling back to deployment %s (commit %s) with its original configuration", *d.RollbackOf, d.CommitSHA))
	}
	deployCtx, cancelDeploy := context.WithTimeout(jobCtx, w.cfg.DeployTimeout)
	_, err = w.step(deployCtx, "deploy", func(ctx context.Context) (string, error) { return "", w.exec.Deploy(ctx, job, image, logLine) })
	cancelDeploy()
	if err != nil {
		failStage = "deploy"
		// The cluster was changed and the rollout failed: restore the last
		// good release (if the environment allows it). Not after a user
		// cancel, a lost lease, or a shutdown: those are not release failures.
		if w.fail(base, job, &lost, fmt.Errorf("deploy: %w", err), logLine, logger) {
			w.autoRollback(base, d.ID, d.Environment, logLine, logger)
		}
		return
	}
	if err := w.complete(base, d.ID, deployments.StatusRunning, ""); err != nil {
		w.fail(base, job, &lost, err, logLine, logger)
		return
	}
	logLine("info", "deployment succeeded")
	logger.Info("deployment succeeded")
}

// fail records a failure unless the deployment is no longer ours. Database
// writes use a detached context so they still happen during shutdown. It
// reports whether it recorded a genuine release failure (not a cancel, lost
// lease or shutdown interruption).
func (w *Worker) fail(base context.Context, job deployments.Job, lost *atomic.Bool, cause error, logLine LogFunc, logger *slog.Logger) bool {
	var te *deployments.TransitionError
	switch {
	case lost.Load() || errors.As(cause, &te):
		// Cancelled by a user (or reaped) while we worked: its terminal
		// state was already written by whoever ended it. Do not overwrite.
		logger.Info("deployment ended elsewhere; stopping", "cause", cause)
		logLine("info", "stopped: the deployment was cancelled or taken over")
		return false
	case base.Err() != nil:
		cause = errors.New("interrupted: the worker shut down before the deployment finished; retry with a new deployment")
		logLine("error", cause.Error())
		_ = w.complete(base, job.Deployment.ID, deployments.StatusFailed, cause.Error())
		return false
	}
	logLine("error", cause.Error())
	if err := w.complete(base, job.Deployment.ID, deployments.StatusFailed, cause.Error()); err != nil {
		if !errors.As(err, &te) {
			logger.Error("record failure", "err", err, "cause", cause)
		}
		return false
	}
	logger.Warn("deployment failed", "cause", cause)
	return true
}

// autoRollback queues a rollback to the last good release and records the
// outcome in the failed deployment's log. The new deployment is processed
// like any other (by whichever worker claims it).
func (w *Worker) autoRollback(base context.Context, failedID, env string, logLine LogFunc, logger *slog.Logger) {
	var rb *deployments.Deployment
	var skipped string
	err := detached(base, func(ctx context.Context) (err error) {
		rb, skipped, err = w.svc.AutoRollback(ctx, failedID)
		return err
	})
	switch {
	case err != nil:
		logger.Error("auto-rollback failed", "err", err)
		logLine("error", "auto-rollback could not be queued: "+err.Error())
	case rb == nil:
		w.cfg.Metrics.AutoRollback(env, "skipped")
		logLine("warn", "auto-rollback skipped: "+skipped)
	default:
		w.cfg.Metrics.AutoRollback(env, "queued")
		logger.Warn("auto-rollback queued", "rollback_deployment_id", rb.ID, "restores", *rb.RollbackOf)
		logLine("warn", fmt.Sprintf("auto-rollback queued: deployment %s restores deployment %s (commit %s)", rb.ID, *rb.RollbackOf, rb.CommitSHA))
	}
}

// heartbeat renews the lease every LeaseTTL/3 until ctx ends. If the
// deployment is no longer ours, it marks lost and cancels the job, which
// aborts the executor's current step promptly.
func (w *Worker) heartbeat(ctx context.Context, id string, lost *atomic.Bool, cancel context.CancelFunc, logger *slog.Logger) {
	t := time.NewTicker(w.cfg.LeaseTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		alive, err := w.svc.Heartbeat(ctx, id, w.cfg.ID, w.cfg.LeaseTTL)
		switch {
		case err != nil && ctx.Err() == nil:
			// Keep working; if the database stays unreachable the lease
			// expires and the reaper takes over — the safe outcome.
			logger.Warn("heartbeat failed", "err", err)
		case err == nil && !alive:
			logger.Info("deployment no longer owned (cancelled or reaped); aborting")
			lost.Store(true)
			cancel()
			return
		}
	}
}

// reapLoop periodically fails deployments whose worker died.
func (w *Worker) reapLoop(ctx context.Context) {
	t := time.NewTicker(w.cfg.ReapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ids, err := w.svc.ReapExpired(ctx)
		if err != nil && ctx.Err() == nil {
			w.logger.Error("reap expired leases", "err", err)
		}
		for _, id := range ids {
			w.logger.Warn("reaped deployment with expired lease", "deployment_id", id)
			if d, err := w.svc.Get(ctx, id); err == nil {
				w.cfg.Metrics.Failed(d.Environment, "lease_lost")
				w.cfg.Metrics.Finished(d.Environment, "failed", duration(d))
			}
		}
	}
}

// detached runs a bookkeeping write on a short-lived context that is not
// cancelled with base, so state is still recorded while shutting down.
func detached(base context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(base), 10*time.Second)
	defer cancel()
	return fn(ctx)
}

func (w *Worker) complete(base context.Context, id string, to deployments.Status, msg string) error {
	return detached(base, func(ctx context.Context) error {
		_, err := w.svc.Complete(ctx, id, to, msg)
		return err
	})
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// step runs one pipeline step in its own span and records its duration.
func (w *Worker) step(ctx context.Context, name string, fn func(context.Context) (string, error)) (string, error) {
	ctx, span := tracing.Tracer("worker").Start(ctx, name)
	start := time.Now()
	out, err := fn(ctx)
	w.cfg.Metrics.Step(name, err, time.Since(start))
	tracing.End(span, err)
	return out, err
}

// recordOutcome reads the deployment's final state from the database (the
// source of truth, whoever wrote it: this worker, a user's cancel, the
// reaper) and records metrics and span status from it.
func (w *Worker) recordOutcome(base context.Context, d deployments.Deployment, failStage string, span trace.Span) {
	var final deployments.Deployment
	if err := detached(base, func(ctx context.Context) (err error) { final, err = w.svc.Get(ctx, d.ID); return err }); err != nil {
		return
	}
	outcome := map[deployments.Status]string{deployments.StatusRunning: "succeeded", deployments.StatusFailed: "failed", deployments.StatusCancelled: "cancelled"}[final.Status]
	if outcome == "" {
		return // not terminal (should not happen)
	}
	span.SetAttributes(attribute.String("deployment.outcome", outcome))
	if outcome == "failed" {
		if failStage == "" {
			failStage = "interrupted"
		}
		span.SetStatus(codes.Error, deref(final.ErrorMessage))
		w.cfg.Metrics.Failed(final.Environment, failStage)
	}
	w.cfg.Metrics.Finished(final.Environment, outcome, duration(final))
}

func duration(d deployments.Deployment) time.Duration {
	if d.StartedAt == nil || d.CompletedAt == nil {
		return 0
	}
	return d.CompletedAt.Sub(*d.StartedAt)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
