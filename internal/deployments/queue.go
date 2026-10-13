package deployments

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/vipul2902/clouddeploy/internal/audit"
)

// QueueChannel is the Postgres NOTIFY channel signalled when a deployment is
// queued. NOTIFY is transactional: listeners only hear about deployments
// whose creating transaction committed.
const QueueChannel = "deployment_queued"

// Job is a claimed deployment plus the project facts an executor needs.
type Job struct {
	Deployment     Deployment
	OrganizationID string
	ProjectName    string
	RepositoryURL  string
	// TraceParent continues the trace of the request that queued the job.
	TraceParent string
}

// Claim atomically takes the oldest QUEUED deployment, moves it to BUILDING
// and leases it to workerID. It returns nil when the queue is empty.
//
// Exactly-once claiming comes from the row lock plus Postgres re-checking
// status = 'QUEUED' after any lock wait. SKIP LOCKED adds liveness: a
// concurrent claimer skips rows another claimer holds and takes the next job
// instead of blocking (and possibly then finding nothing).
// TestClaimSkipsLockedRowsWithoutBlocking fails without it.
func (s *Service) Claim(ctx context.Context, workerID string, lease time.Duration) (*Job, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		UPDATE deployments SET
			status           = 'BUILDING',
			started_at       = clock_timestamp(),
			claimed_by       = $1,
			lease_expires_at = clock_timestamp() + $2 * interval '1 millisecond'
		WHERE id = (
			SELECT id FROM deployments WHERE status = 'QUEUED'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		RETURNING id`, workerID, lease.Milliseconds()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	job := &Job{Deployment: d}
	err = s.pool.QueryRow(ctx, `
		SELECT p.organization_id, p.name, p.repository_url, coalesce(d.trace_context, '')
		FROM projects p JOIN deployments d ON d.project_id = p.id WHERE d.id = $1`, d.ID).
		Scan(&job.OrganizationID, &job.ProjectName, &job.RepositoryURL, &job.TraceParent)
	return job, err
}

// Heartbeat renews the lease. It reports false when the worker no longer
// owns an active deployment — it was cancelled, or reaped after the lease
// lapsed — and the worker must stop working on it.
func (s *Service) Heartbeat(ctx context.Context, id, workerID string, lease time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE deployments SET lease_expires_at = clock_timestamp() + $3 * interval '1 millisecond'
		WHERE id = $1 AND claimed_by = $2 AND status IN ('BUILDING', 'DEPLOYING')`,
		id, workerID, lease.Milliseconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetImage records the built image reference.
func (s *Service) SetImage(ctx context.Context, id, image string) error {
	_, err := s.pool.Exec(ctx, `UPDATE deployments SET image = $2 WHERE id = $1`, id, image)
	return err
}

// Complete moves a deployment to a terminal state (RUNNING or FAILED) and
// records the outcome in the audit log, atomically. The actor is the system
// (no user).
func (s *Service) Complete(ctx context.Context, id string, to Status, errMsg string) (Deployment, error) {
	if !to.Terminal() {
		return Deployment{}, errors.New("Complete requires a terminal status")
	}
	action := map[Status]string{
		StatusRunning: "deployment.succeeded", StatusFailed: "deployment.failed", StatusCancelled: "deployment.cancelled",
	}[to]
	var d Deployment
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if d, err = s.transition(ctx, tx, id, to, errMsg); err != nil {
			return err
		}
		return recordSystem(ctx, tx, d, action, map[string]any{"status": to, "error": errMsg})
	})
	return d, err
}

// LeaseLostMessage is recorded on deployments failed by the reaper.
const LeaseLostMessage = "worker lost: lease expired (the worker crashed or was killed); retry with a new deployment"

// ReapExpired fails active deployments whose lease has expired. The state
// machine allows BUILDING/DEPLOYING -> FAILED. Retrying automatically would
// be unsafe until every step is proven idempotent, so a human redeploys.
// Safe to run concurrently from several workers: each row is updated once.
func (s *Service) ReapExpired(ctx context.Context) ([]string, error) {
	var reaped []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE deployments SET status = 'FAILED', completed_at = clock_timestamp(), error_message = $1
			WHERE status IN ('BUILDING', 'DEPLOYING') AND lease_expires_at < clock_timestamp()
			RETURNING id`, LeaseLostMessage)
		if err != nil {
			return err
		}
		if reaped, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		for _, id := range reaped {
			d, err := s.getWith(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := appendLog(ctx, tx, id, "error", LeaseLostMessage); err != nil {
				return err
			}
			if err := recordSystem(ctx, tx, d, "deployment.failed", map[string]any{"status": StatusFailed, "error": LeaseLostMessage}); err != nil {
				return err
			}
		}
		return nil
	})
	return reaped, err
}

func recordSystem(ctx context.Context, q querier, d Deployment, action string, md map[string]any) error {
	var orgID string
	if err := q.QueryRow(ctx, `SELECT organization_id FROM projects WHERE id = $1`, d.ProjectID).Scan(&orgID); err != nil {
		return err
	}
	return audit.Record(ctx, q, audit.Actor{OrganizationID: orgID}, action, "deployment", d.ID, md)
}

// LogLine is one line of a deployment's pipeline log.
type LogLine struct {
	ID        int64     `json:"id"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

const maxLogMessage = 4096

// AppendLog adds a line to the deployment's pipeline log.
func (s *Service) AppendLog(ctx context.Context, id, level, message string) error {
	return appendLog(ctx, s.pool, id, level, message)
}

func appendLog(ctx context.Context, q querier, id, level, message string) error {
	_, err := q.Exec(ctx, `INSERT INTO deployment_logs (deployment_id, level, message) VALUES ($1, $2, $3)`,
		id, level, truncateMessage(message, maxLogMessage))
	return err
}

// truncateMessage caps message at max bytes without splitting a multi-byte
// UTF-8 character (Postgres rejects invalid UTF-8 in text columns).
func truncateMessage(message string, max int) string {
	if len(message) <= max {
		return message
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + " …[truncated]"
}

// Logs returns log lines with id > after, oldest first. Clients tail a
// running deployment by passing the last id they have seen.
func (s *Service) Logs(ctx context.Context, id string, after int64, limit int) ([]LogLine, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, level, message, created_at FROM deployment_logs
		WHERE deployment_id = $1 AND id > $2 ORDER BY id LIMIT $3`, id, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[LogLine])
}

// LatestSucceeded returns the environment's most recent RUNNING deployment,
// or nil if it has never deployed successfully. Monitoring compares it with
// the release live in the cluster to detect drift.
func (s *Service) LatestSucceeded(ctx context.Context, environmentID string) (*Deployment, error) {
	rows, err := s.pool.Query(ctx, selectDeployment+`
		WHERE d.environment_id = $1 AND d.status = 'RUNNING' ORDER BY d.completed_at DESC LIMIT 1`, environmentID)
	if err != nil {
		return nil, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, s.scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}

// QueueStats reports queue state for metrics: queued and active counts and
// the age of the oldest queued deployment (one query, at scrape time).
func (s *Service) QueueStats(ctx context.Context) (queued, active int, oldest time.Duration, err error) {
	var oldestSecs float64
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'QUEUED'),
		       count(*) FILTER (WHERE status IN ('BUILDING', 'DEPLOYING')),
		       coalesce(extract(epoch FROM now() - min(created_at) FILTER (WHERE status = 'QUEUED')), 0)
		FROM deployments WHERE status IN ('QUEUED', 'BUILDING', 'DEPLOYING')`).Scan(&queued, &active, &oldestSecs)
	return queued, active, time.Duration(oldestSecs * float64(time.Second)), err
}
