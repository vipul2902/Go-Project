package environments

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service implements environment and variable storage. Authorization is
// done by the handler (via projects.Service.Access) before calling it.
type Service struct {
	pool *pgxpool.Pool
}

// NewService returns an environment service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

const envColumns = `id, project_id, name, namespace, replicas, cpu_request, cpu_limit, memory_request, memory_limit, created_at, updated_at`

func scanEnv(r pgx.CollectableRow) (Environment, error) {
	var e Environment
	err := r.Scan(&e.ID, &e.ProjectID, &e.Name, &e.Namespace, &e.Replicas,
		&e.Resources.CPURequest, &e.Resources.CPULimit, &e.Resources.MemoryRequest, &e.Resources.MemoryLimit,
		&e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// List returns a project's environments in promotion order.
func (s *Service) List(ctx context.Context, projectID string) ([]Environment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+envColumns+` FROM environments WHERE project_id = $1
		ORDER BY array_position(ARRAY['development','staging','production'], name)`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEnv)
}

// Get returns one environment by name.
func (s *Service) Get(ctx context.Context, projectID, name string) (Environment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+envColumns+` FROM environments WHERE project_id = $1 AND name = $2`, projectID, name)
	if err != nil {
		return Environment{}, err
	}
	return one(rows, ErrNotFound)
}

// Create adds an environment; input must already be normalized.
func (s *Service) Create(ctx context.Context, projectID string, in CreateInput) (Environment, error) {
	r := in.Resources
	rows, err := s.pool.Query(ctx, `
		INSERT INTO environments (project_id, name, namespace, replicas, cpu_request, cpu_limit, memory_request, memory_limit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+envColumns,
		projectID, in.Name, in.Namespace, *in.Replicas, r.CPURequest, r.CPULimit, r.MemoryRequest, r.MemoryLimit)
	if err != nil {
		return Environment{}, err
	}
	e, err := one(rows, ErrNotFound)
	switch {
	case pgErrIs(err, "23505", "environments_project_id_name_key"):
		return Environment{}, ErrExists
	case pgErrIs(err, "23505", "environments_namespace_key"):
		return Environment{}, ErrNamespaceTaken
	}
	return e, err
}

// Update applies a validated partial update.
func (s *Service) Update(ctx context.Context, projectID, name string, in UpdateInput) (Environment, error) {
	var cpuReq, cpuLim, memReq, memLim *string
	if r := in.Resources; r != nil {
		cpuReq, cpuLim, memReq, memLim = &r.CPURequest, &r.CPULimit, &r.MemoryRequest, &r.MemoryLimit
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE environments SET
			replicas       = COALESCE($3, replicas),
			cpu_request    = COALESCE($4, cpu_request),
			cpu_limit      = COALESCE($5, cpu_limit),
			memory_request = COALESCE($6, memory_request),
			memory_limit   = COALESCE($7, memory_limit)
		WHERE project_id = $1 AND name = $2 RETURNING `+envColumns,
		projectID, name, in.Replicas, cpuReq, cpuLim, memReq, memLim)
	if err != nil {
		return Environment{}, err
	}
	return one(rows, ErrNotFound)
}

// Delete removes an environment and its variables. Environments with
// deployment history cannot be deleted (the FK protects the history).
func (s *Service) Delete(ctx context.Context, projectID, name string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM environments WHERE project_id = $1 AND name = $2`, projectID, name)
	if pgErrIs(err, "23503", "") {
		return ErrHasDeployments
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Variables lists an environment's variables by key.
func (s *Service) Variables(ctx context.Context, envID string) ([]Variable, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT key, value, is_secret, secret_ref, updated_at FROM environment_variables
		WHERE environment_id = $1 ORDER BY key`, envID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanVar)
}

// SetVariable creates or replaces a variable (input must be validated) and
// reports whether it was newly created. The environment row is locked so
// the per-environment variable cap cannot be exceeded by concurrent writes.
func (s *Service) SetVariable(ctx context.Context, envID, key string, in VariableInput) (Variable, bool, error) {
	var v Variable
	var created bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM environments WHERE id = $1 FOR UPDATE`, envID); err != nil {
			return err
		}
		var others int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM environment_variables WHERE environment_id = $1 AND key <> $2`, envID, key).Scan(&others); err != nil {
			return err
		}
		if others >= MaxVariablesPerEnv {
			return ErrTooManyVariables
		}
		// xmax = 0 is true only for a freshly inserted row (an updated row
		// version carries the updating transaction's ID): insert vs update
		// in one statement.
		return tx.QueryRow(ctx, `
			INSERT INTO environment_variables (environment_id, key, value, is_secret, secret_ref)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (environment_id, key) DO UPDATE
			SET value = EXCLUDED.value, is_secret = EXCLUDED.is_secret, secret_ref = EXCLUDED.secret_ref
			RETURNING key, value, is_secret, secret_ref, updated_at, (xmax = 0)`,
			envID, key, in.Value, in.SecretRef != nil, in.SecretRef).
			Scan(&v.Key, &v.Value, &v.Secret, &v.SecretRef, &v.UpdatedAt, &created)
	})
	return v, created, err
}

// DeleteVariable removes a variable.
func (s *Service) DeleteVariable(ctx context.Context, envID, key string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM environment_variables WHERE environment_id = $1 AND key = $2`, envID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrVariableNotFound
	}
	return nil
}

func scanVar(r pgx.CollectableRow) (Variable, error) {
	var v Variable
	err := r.Scan(&v.Key, &v.Value, &v.Secret, &v.SecretRef, &v.UpdatedAt)
	return v, err
}

func one(rows pgx.Rows, notFound error) (Environment, error) {
	e, err := pgx.CollectExactlyOneRow(rows, scanEnv)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, notFound
	}
	return e, err
}

// pgErrIs matches a Postgres error code and, if non-empty, constraint name.
func pgErrIs(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && (constraint == "" || pgErr.ConstraintName == constraint)
}
