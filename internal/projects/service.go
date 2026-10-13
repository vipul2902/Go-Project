package projects

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
	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/httpx"
)

// Service implements project use cases. Every method takes the caller's
// user ID and enforces their organization role.
type Service struct {
	pool *pgxpool.Pool
}

// NewService returns a project service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

const projectColumns = `p.id, p.organization_id, p.name, p.repository_url, p.default_branch, p.created_at, p.updated_at`

func scanProject(r pgx.CollectableRow) (Project, error) {
	var p Project
	err := r.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.RepositoryURL, &p.DefaultBranch, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// checkRole maps membership to an error: no membership hides existence
// (ErrNotFound); insufficient role is ErrForbidden.
func checkRole(role *auth.Role, need auth.Role) error {
	if role == nil {
		return ErrNotFound
	}
	if !role.Allows(need) {
		return ErrForbidden
	}
	return nil
}

// Create registers a project in an organization (developer or admin).
func (s *Service) Create(ctx context.Context, userID string, in CreateInput) (Project, error) {
	var role *auth.Role
	err := s.pool.QueryRow(ctx,
		`SELECT role FROM organization_members WHERE organization_id = $1 AND user_id = $2`,
		in.OrganizationID, userID).Scan(&role)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Project{}, err
	}
	if err := checkRole(role, auth.RoleDeveloper); err != nil {
		return Project{}, err
	}

	var p Project
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			INSERT INTO projects AS p (organization_id, name, repository_url, default_branch)
			VALUES ($1, $2, $3, $4) RETURNING `+projectColumns,
			in.OrganizationID, in.Name, in.RepositoryURL, in.DefaultBranch)
		if err != nil {
			return err
		}
		if p, err = pgx.CollectExactlyOneRow(rows, scanProject); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Actor{UserID: userID, OrganizationID: p.OrganizationID},
			"project.created", "project", p.ID, map[string]any{"name": p.Name, "repository_url": p.RepositoryURL})
	})
	if isUnique(err, "projects_org_name_key") {
		return Project{}, ErrNameTaken
	}
	return p, err
}

// Get returns a live project the caller can view.
func (s *Service) Get(ctx context.Context, userID, id string) (Project, error) {
	p, role, err := s.getWithRole(ctx, userID, id)
	if err != nil {
		return Project{}, err
	}
	return p, checkRole(role, auth.RoleViewer)
}

// Access returns a live project and the caller's role in its organization,
// for packages that own project sub-resources (environments, deployments).
// Missing projects and non-members both yield ErrNotFound.
func (s *Service) Access(ctx context.Context, userID, id string) (Project, auth.Role, error) {
	p, role, err := s.getWithRole(ctx, userID, id)
	if err != nil {
		return Project{}, "", err
	}
	if role == nil {
		return Project{}, "", ErrNotFound
	}
	return p, *role, nil
}

// getWithRole loads a project together with the caller's role in its
// organization in a single query.
func (s *Service) getWithRole(ctx context.Context, userID, id string) (Project, *auth.Role, error) {
	var p Project
	var role *auth.Role
	err := s.pool.QueryRow(ctx, `
		SELECT `+projectColumns+`, m.role
		FROM projects p
		LEFT JOIN organization_members m ON m.organization_id = p.organization_id AND m.user_id = $1
		WHERE p.id = $2 AND p.deleted_at IS NULL`, userID, id).
		Scan(&p.ID, &p.OrganizationID, &p.Name, &p.RepositoryURL, &p.DefaultBranch, &p.CreatedAt, &p.UpdatedAt, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, nil, ErrNotFound
	}
	return p, role, err
}

// ListFilter narrows a project listing.
type ListFilter struct {
	OrganizationID string // optional
	NamePrefix     string // optional
	Sort           string // a key of sorts
}

// List returns one page of projects in organizations the caller belongs to,
// using keyset pagination: "rows after (sort value, id) of the last row".
// Unlike OFFSET, cost does not grow with page depth and concurrent inserts
// cannot cause skipped or duplicated rows.
func (s *Service) List(ctx context.Context, userID string, f ListFilter, page httpx.Page) ([]Project, *string, error) {
	spec := sorts[f.Sort]
	args := []any{userID}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }

	var where strings.Builder
	where.WriteString("p.deleted_at IS NULL")
	if f.OrganizationID != "" {
		where.WriteString(" AND p.organization_id = " + arg(f.OrganizationID))
	}
	if f.NamePrefix != "" {
		where.WriteString(" AND p.name LIKE " + arg(escapeLike(f.NamePrefix)+"%") + ` ESCAPE '\'`)
	}
	if page.Cursor != "" {
		var c cursor
		if err := httpx.DecodeCursor(page.Cursor, &c); err != nil || c.Sort != f.Sort || c.ID == "" {
			return nil, nil, errInvalidCursor
		}
		op := ">"
		if spec.desc {
			op = "<"
		}
		// Row-value comparison: (col, id) > (v, id) handles ties on col.
		fmt.Fprintf(&where, " AND (%s, p.id) %s (%s::%s, %s::uuid)", spec.column, op, arg(c.Value), spec.cast, arg(c.ID))
	}
	dir := "ASC"
	if spec.desc {
		dir = "DESC"
	}

	// Fetch one extra row to learn whether another page exists.
	query := fmt.Sprintf(`
		SELECT %s FROM projects p
		JOIN organization_members m ON m.organization_id = p.organization_id AND m.user_id = $1
		WHERE %s ORDER BY %s %s, p.id %s LIMIT %s`,
		projectColumns, where.String(), spec.column, dir, dir, arg(page.Limit+1))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	ps, err := pgx.CollectRows(rows, scanProject)
	if err != nil {
		return nil, nil, err
	}
	if len(ps) <= page.Limit {
		return ps, nil, nil
	}
	ps = ps[:page.Limit]
	last := ps[len(ps)-1]
	next := httpx.EncodeCursor(cursor{Sort: f.Sort, Value: sortValue(last, f.Sort), ID: last.ID})
	return ps, &next, nil
}

var errInvalidCursor = errors.New("invalid cursor")

// Update applies a partial update (developer or admin). If ifMatch is set,
// the update only succeeds when the project is unchanged since the client
// read it (optimistic concurrency); otherwise ErrPreconditionFailed.
func (s *Service) Update(ctx context.Context, userID, id string, in UpdateInput, ifMatch *time.Time) (Project, error) {
	before, role, err := s.getWithRole(ctx, userID, id)
	if err != nil {
		return Project{}, err
	}
	if err := checkRole(role, auth.RoleDeveloper); err != nil {
		return Project{}, err
	}

	var p Project
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE projects AS p SET
				name           = COALESCE($3, name),
				repository_url = COALESCE($4, repository_url),
				default_branch = COALESCE($5, default_branch)
			WHERE id = $1 AND deleted_at IS NULL
			  AND ($2::timestamptz IS NULL OR updated_at = $2)
			RETURNING `+projectColumns, id, ifMatch, in.Name, in.RepositoryURL, in.DefaultBranch)
		if err != nil {
			return err
		}
		if p, err = pgx.CollectExactlyOneRow(rows, scanProject); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Actor{UserID: userID, OrganizationID: p.OrganizationID},
			"project.updated", "project", p.ID, changes(before, p))
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows) && ifMatch != nil:
		return Project{}, ErrPreconditionFailed
	case errors.Is(err, pgx.ErrNoRows):
		return Project{}, ErrNotFound // deleted concurrently
	case isUnique(err, "projects_org_name_key"):
		return Project{}, ErrNameTaken
	}
	return p, err
}

// changes describes which fields an update modified, as {field: {from, to}}.
func changes(before, after Project) map[string]any {
	out := map[string]any{}
	for field, pair := range map[string][2]string{
		"name":           {before.Name, after.Name},
		"repository_url": {before.RepositoryURL, after.RepositoryURL},
		"default_branch": {before.DefaultBranch, after.DefaultBranch},
	} {
		if pair[0] != pair[1] {
			out[field] = map[string]string{"from": pair[0], "to": pair[1]}
		}
	}
	return out
}

// Delete soft-deletes a project (admin only). Deployment history and audit
// records keep referencing it.
func (s *Service) Delete(ctx context.Context, userID, id string) error {
	_, role, err := s.getWithRole(ctx, userID, id)
	if err != nil {
		return err
	}
	if err := checkRole(role, auth.RoleAdmin); err != nil {
		return err
	}
	// Lock the project row: deployment creation holds FOR SHARE on it, so a
	// deployment cannot be created between our check and the delete.
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var p Project
		rows, err := tx.Query(ctx, `SELECT `+projectColumns+` FROM projects p WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id)
		if err != nil {
			return err
		}
		if p, err = pgx.CollectExactlyOneRow(rows, scanProject); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployments WHERE project_id = $1
			AND status IN ('QUEUED', 'BUILDING', 'DEPLOYING'))`, id).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrActiveDeployment
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET deleted_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Actor{UserID: userID, OrganizationID: p.OrganizationID},
			"project.deleted", "project", id, map[string]any{"name": p.Name})
	})
}

// escapeLike escapes LIKE wildcards so user input matches literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
