package deployments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/audit"
	"github.com/vipul2902/clouddeploy/internal/environments"
	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/projects"
)

// Service implements deployment use cases. Authorization happens in the
// handler; the service enforces integrity (state machine, idempotency,
// one active deployment per environment).
type Service struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewService returns a deployment service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool, now: time.Now} }

// querier is satisfied by both the pool and a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

const selectDeployment = `
	SELECT d.id, d.project_id, pr.name, d.environment_id, e.name, d.commit_sha, d.image, d.status,
	       d.triggered_by, u.name, d.rollback_of, d.error_message, d.config,
	       d.created_at, d.started_at, d.completed_at
	FROM deployments d
	JOIN environments e ON e.id = d.environment_id
	JOIN projects pr ON pr.id = d.project_id
	LEFT JOIN users u ON u.id = d.triggered_by`

func (s *Service) scan(r pgx.CollectableRow) (Deployment, error) {
	var d Deployment
	var triggerID, triggerName *string
	err := r.Scan(&d.ID, &d.ProjectID, &d.ProjectName, &d.EnvironmentID, &d.Environment, &d.CommitSHA, &d.Image, &d.Status,
		&triggerID, &triggerName, &d.RollbackOf, &d.ErrorMessage, &d.Config,
		&d.CreatedAt, &d.StartedAt, &d.CompletedAt)
	if triggerID != nil && triggerName != nil {
		d.TriggeredBy = &Trigger{ID: *triggerID, Name: *triggerName}
	}
	if d.Config.Variables == nil {
		d.Config.Variables = []environments.Variable{}
	}
	d.computeDuration(s.now())
	return d, err
}

func (s *Service) getWith(ctx context.Context, q querier, id string) (Deployment, error) {
	rows, err := q.Query(ctx, selectDeployment+` WHERE d.id = $1`, id)
	if err != nil {
		return Deployment{}, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, s.scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	return d, err
}

// Get returns a deployment by ID (no authorization; see handler).
func (s *Service) Get(ctx context.Context, id string) (Deployment, error) {
	return s.getWith(ctx, s.pool, id)
}

// CreateParams describes a deployment request after authorization.
type CreateParams struct {
	ActorID        string
	Project        projects.Project
	Environment    environments.Environment
	CommitSHA      string
	IdempotencyKey string // optional

	// Rollbacks: restore Config (the target's snapshot) instead of
	// snapshotting the environment's current configuration.
	RollbackOf string
	Config     *Config
	// AuditAction overrides "deployment.created" (e.g. "deployment.rollback").
	AuditAction   string
	AuditMetadata map[string]any
}

// Create queues a new deployment. It returns replayed=true when an earlier
// request with the same idempotency key already created it.
//
// Guarantees, all enforced by the database:
//   - same Idempotency-Key + environment -> the same deployment, even for
//     concurrent retries (unique index deployments_idempotency_key);
//   - at most one active deployment per environment
//     (unique index deployments_one_active_per_env);
//   - a project cannot be deleted while a deployment is being created
//     (FOR SHARE here vs FOR UPDATE in project deletion).
func (s *Service) Create(ctx context.Context, p CreateParams) (Deployment, bool, error) {
	if p.IdempotencyKey != "" {
		if d, err := s.replay(ctx, p); !errors.Is(err, ErrNotFound) {
			return d, err == nil, err
		}
	}

	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) (err error) {
		var alive bool
		if err := tx.QueryRow(ctx,
			`SELECT true FROM projects WHERE id = $1 AND deleted_at IS NULL FOR SHARE`, p.Project.ID).Scan(&alive); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProjectGone
			}
			return err
		}
		var cfg Config
		if p.Config != nil {
			cfg = *p.Config
		} else if cfg, err = snapshot(ctx, tx, p.Environment); err != nil {
			return err
		}
		// triggered_by is NULL for system-initiated deployments (auto-rollback).
		if err := tx.QueryRow(ctx, `
			INSERT INTO deployments (project_id, environment_id, commit_sha, triggered_by, idempotency_key, config, rollback_of)
			VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, ''), $6, NULLIF($7, '')::uuid) RETURNING id`,
			p.Project.ID, p.Environment.ID, p.CommitSHA, p.ActorID, p.IdempotencyKey, cfg, p.RollbackOf).Scan(&id); err != nil {
			return err
		}
		action := p.AuditAction
		if action == "" {
			action = "deployment.created"
		}
		md := map[string]any{"project": p.Project.Name, "environment": p.Environment.Name, "commit_sha": p.CommitSHA}
		if p.RollbackOf != "" {
			md["rollback_of"] = p.RollbackOf
		}
		for k, v := range p.AuditMetadata {
			md[k] = v
		}
		if err := audit.Record(ctx, tx, audit.Actor{UserID: p.ActorID, OrganizationID: p.Project.OrganizationID},
			action, "deployment", id, md); err != nil {
			return err
		}
		// Wake the workers. Delivered only if this transaction commits, so a
		// worker can never be told about a deployment it cannot see.
		_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, QueueChannel, id)
		return err
	})

	switch {
	case isUnique(err, "deployments_idempotency_key"):
		// A concurrent request with the same key won the race: replay it.
		d, err := s.replay(ctx, p)
		return d, err == nil, err
	case isUnique(err, "deployments_one_active_per_env"):
		// Concurrent retries with the same key violate BOTH unique indexes and
		// Postgres may report either one. If a deployment with our key exists,
		// this was a retry of the same request: replay it.
		if p.IdempotencyKey != "" {
			if d, rerr := s.replay(ctx, p); !errors.Is(rerr, ErrNotFound) {
				return d, rerr == nil, rerr
			}
		}
		var active string
		if err := s.pool.QueryRow(ctx, `SELECT id FROM deployments WHERE environment_id = $1
			AND status IN ('QUEUED','BUILDING','DEPLOYING')`, p.Environment.ID).Scan(&active); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Deployment{}, false, err
		}
		return Deployment{}, false, &InProgressError{ActiveID: active}
	case err != nil:
		return Deployment{}, false, err
	}
	d, err := s.Get(ctx, id)
	return d, false, err
}

// replay returns the deployment previously created with p's idempotency key,
// or ErrIdempotencyMismatch if that key was used for a different commit.
func (s *Service) replay(ctx context.Context, p CreateParams) (Deployment, error) {
	rows, err := s.pool.Query(ctx, selectDeployment+` WHERE d.environment_id = $1 AND d.idempotency_key = $2`,
		p.Environment.ID, p.IdempotencyKey)
	if err != nil {
		return Deployment{}, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, s.scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, err
	}
	if d.CommitSHA != p.CommitSHA || deref(d.RollbackOf) != p.RollbackOf {
		return Deployment{}, ErrIdempotencyMismatch
	}
	return d, nil
}

// snapshot captures the environment's current configuration.
func snapshot(ctx context.Context, q querier, env environments.Environment) (Config, error) {
	rows, err := q.Query(ctx, `
		SELECT key, value, is_secret, secret_ref, updated_at FROM environment_variables
		WHERE environment_id = $1 ORDER BY key`, env.ID)
	if err != nil {
		return Config{}, err
	}
	vars, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (environments.Variable, error) {
		var v environments.Variable
		err := r.Scan(&v.Key, &v.Value, &v.Secret, &v.SecretRef, &v.UpdatedAt)
		return v, err
	})
	if err != nil {
		return Config{}, err
	}
	return Config{Namespace: env.Namespace, Replicas: env.Replicas, Resources: env.Resources,
		Port: env.Port, HealthCheckPath: env.HealthCheckPath, Variables: vars}, nil
}

// Transition moves a deployment to `to` if the state machine allows it from
// its current status. It is a single compare-and-swap UPDATE, so concurrent
// transitions (e.g. a user cancelling while the worker advances) cannot both
// succeed: the loser gets a *TransitionError naming the actual state.
func (s *Service) Transition(ctx context.Context, id string, to Status, errMsg string) (Deployment, error) {
	return s.transition(ctx, s.pool, id, to, errMsg)
}

func (s *Service) transition(ctx context.Context, q querier, id string, to Status, errMsg string) (Deployment, error) {
	if !to.Valid() {
		return Deployment{}, fmt.Errorf("unknown status %q", to)
	}
	// clock_timestamp(), not now(): now() is the *transaction start* time. If
	// this UPDATE waited on a concurrent transition's row lock, now() would
	// predate that transition's started_at and violate the
	// completed_at >= started_at CHECK (found by TestCancelRacesWorker).
	tag, err := q.Exec(ctx, `
		UPDATE deployments SET
			status        = $2,
			started_at    = CASE WHEN $2 = 'BUILDING' THEN clock_timestamp() ELSE started_at END,
			completed_at  = CASE WHEN $2 = ANY($4::text[]) THEN clock_timestamp() ELSE completed_at END,
			error_message = COALESCE(NULLIF($3, ''), error_message)
		WHERE id = $1 AND status = ANY($5::text[])`,
		id, to, errMsg, terminalStatuses(), predecessors(to))
	if err != nil {
		return Deployment{}, err
	}
	if tag.RowsAffected() == 0 {
		var current Status
		err := q.QueryRow(ctx, `SELECT status FROM deployments WHERE id = $1`, id).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return Deployment{}, ErrNotFound
		}
		if err != nil {
			return Deployment{}, err
		}
		return Deployment{}, &TransitionError{From: current, To: to}
	}
	return s.getWith(ctx, q, id)
}

func terminalStatuses() []string {
	var out []string
	for st := range transitions {
		if st.Terminal() {
			out = append(out, string(st))
		}
	}
	return out
}

// Cancel cancels an active deployment and records who did it.
func (s *Service) Cancel(ctx context.Context, actor audit.Actor, id string) (Deployment, error) {
	var d Deployment
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if d, err = s.transition(ctx, tx, id, StatusCancelled, "cancelled by user"); err != nil {
			return err
		}
		return audit.Record(ctx, tx, actor, "deployment.cancelled", "deployment", id, nil)
	})
	return d, err
}

// ListFilter narrows a deployment listing.
type ListFilter struct {
	ProjectID   string
	Environment string
	Status      Status
}

type cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"id"`
}

// ErrInvalidCursor is returned for malformed cursors.
var ErrInvalidCursor = errors.New("invalid cursor")

// List returns deployments of live projects the user can see, newest first,
// with keyset pagination on (created_at, id).
func (s *Service) List(ctx context.Context, userID string, f ListFilter, page httpx.Page) ([]Deployment, *string, error) {
	args := []any{userID}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	where := []string{"p.deleted_at IS NULL"}
	if f.ProjectID != "" {
		where = append(where, "d.project_id = "+arg(f.ProjectID))
	}
	if f.Environment != "" {
		where = append(where, "e.name = "+arg(f.Environment))
	}
	if f.Status != "" {
		where = append(where, "d.status = "+arg(string(f.Status)))
	}
	if page.Cursor != "" {
		var c cursor
		if err := httpx.DecodeCursor(page.Cursor, &c); err != nil || c.ID == "" {
			return nil, nil, ErrInvalidCursor
		}
		where = append(where, fmt.Sprintf("(d.created_at, d.id) < (%s, %s::uuid)", arg(c.CreatedAt), arg(c.ID)))
	}
	query := selectDeployment + `
		JOIN projects p ON p.id = d.project_id
		JOIN organization_members m ON m.organization_id = p.organization_id AND m.user_id = $1
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY d.created_at DESC, d.id DESC LIMIT ` + arg(page.Limit+1)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	ds, err := pgx.CollectRows(rows, s.scan)
	if err != nil || len(ds) <= page.Limit {
		return ds, nil, err
	}
	ds = ds[:page.Limit]
	last := ds[len(ds)-1]
	next := httpx.EncodeCursor(cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	return ds, &next, nil
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
