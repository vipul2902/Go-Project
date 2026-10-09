// Package organizations manages organizations and their memberships.
package organizations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/auth"
)

// Domain errors.
var (
	ErrNotFound      = errors.New("organization not found")
	ErrMemberMissing = errors.New("member not found")
	ErrUserNotFound  = errors.New("user not found")
	ErrSlugTaken     = errors.New("slug already taken")
	ErrAlreadyMember = errors.New("user is already a member")
	// ErrLastAdmin protects against an organization nobody can administer.
	ErrLastAdmin = errors.New("organization must keep at least one admin")
)

// Organization is an organization as seen by one user (Role is theirs).
type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	Role      auth.Role `json:"role"`
}

// Member is one organization membership.
type Member struct {
	UserID   string    `json:"user_id"`
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Role     auth.Role `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// Service implements organization use cases.
type Service struct {
	pool *pgxpool.Pool
}

// NewService returns an organization service.
func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Create makes a new organization with the creator as admin.
func (s *Service) Create(ctx context.Context, userID, name, slug string) (Organization, error) {
	o := Organization{Name: name, Slug: slug, Role: auth.RoleAdmin}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id, created_at`,
			name, slug).Scan(&o.ID, &o.CreatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`, o.ID, userID)
		return err
	})
	if isUnique(err, "organizations_slug_key") {
		return Organization{}, ErrSlugTaken
	}
	if err != nil {
		return Organization{}, fmt.Errorf("create organization: %w", err)
	}
	return o, nil
}

// ListForUser returns the organizations userID belongs to.
func (s *Service) ListForUser(ctx context.Context, userID string) ([]Organization, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.name, o.slug, o.created_at, m.role
		FROM organizations o JOIN organization_members m ON m.organization_id = o.id
		WHERE m.user_id = $1 ORDER BY o.name, o.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanOrg)
}

// Get returns the organization with the caller's role. Non-members get
// ErrNotFound so they cannot discover which organizations exist.
func (s *Service) Get(ctx context.Context, userID, orgID string) (Organization, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.name, o.slug, o.created_at, m.role
		FROM organizations o JOIN organization_members m ON m.organization_id = o.id AND m.user_id = $1
		WHERE o.id = $2`, userID, orgID)
	if err != nil {
		return Organization{}, err
	}
	o, err := pgx.CollectExactlyOneRow(rows, scanOrg)
	if errors.Is(err, pgx.ErrNoRows) {
		return Organization{}, ErrNotFound
	}
	return o, err
}

// Members lists an organization's members.
func (s *Service) Members(ctx context.Context, orgID string) ([]Member, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, m.role, m.created_at
		FROM organization_members m JOIN users u ON u.id = m.user_id
		WHERE m.organization_id = $1 ORDER BY u.email`, orgID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanMember)
}

// AddMember adds an existing user (by email) to the organization.
func (s *Service) AddMember(ctx context.Context, orgID, email string, role auth.Role) (Member, error) {
	rows, err := s.pool.Query(ctx, `
		WITH ins AS (
			INSERT INTO organization_members (organization_id, user_id, role)
			SELECT $1, id, $3 FROM users WHERE email = $2
			RETURNING user_id, role, created_at
		)
		SELECT u.id, u.email, u.name, ins.role, ins.created_at FROM ins JOIN users u ON u.id = ins.user_id`,
		orgID, email, role)
	if err != nil {
		return Member{}, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, scanMember)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Member{}, ErrUserNotFound
	case isUnique(err, "organization_members_pkey"):
		return Member{}, ErrAlreadyMember
	}
	return m, err
}

// UpdateRole changes a member's role, refusing to demote the last admin.
func (s *Service) UpdateRole(ctx context.Context, orgID, userID string, role auth.Role) (Member, error) {
	var m Member
	err := s.withAdminGuard(ctx, orgID, userID, role != auth.RoleAdmin, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH upd AS (
				UPDATE organization_members SET role = $3
				WHERE organization_id = $1 AND user_id = $2 RETURNING user_id, role, created_at
			)
			SELECT u.id, u.email, u.name, upd.role, upd.created_at FROM upd JOIN users u ON u.id = upd.user_id`,
			orgID, userID, role)
		if err != nil {
			return err
		}
		m, err = pgx.CollectExactlyOneRow(rows, scanMember)
		return err
	})
	return m, err
}

// RemoveMember removes a member, refusing to remove the last admin.
func (s *Service) RemoveMember(ctx context.Context, orgID, userID string) error {
	return s.withAdminGuard(ctx, orgID, userID, true, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM organization_members WHERE organization_id = $1 AND user_id = $2`, orgID, userID)
		return err
	})
}

// withAdminGuard runs change in a transaction that first locks the
// organization row. The lock serializes all membership changes for one org,
// so "count admins, then demote" cannot interleave with a concurrent demotion
// (which under READ COMMITTED could otherwise leave zero admins).
func (s *Service) withAdminGuard(ctx context.Context, orgID, userID string, losesAdmin bool, change func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM organizations WHERE id = $1 FOR UPDATE`, orgID); err != nil {
			return err
		}
		var current auth.Role
		err := tx.QueryRow(ctx,
			`SELECT role FROM organization_members WHERE organization_id = $1 AND user_id = $2`, orgID, userID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMemberMissing
		}
		if err != nil {
			return err
		}
		if current == auth.RoleAdmin && losesAdmin {
			var admins int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM organization_members WHERE organization_id = $1 AND role = 'admin'`, orgID).Scan(&admins); err != nil {
				return err
			}
			if admins <= 1 {
				return ErrLastAdmin
			}
		}
		return change(tx)
	})
}

func scanOrg(r pgx.CollectableRow) (Organization, error) {
	var o Organization
	err := r.Scan(&o.ID, &o.Name, &o.Slug, &o.CreatedAt, &o.Role)
	return o, err
}

func scanMember(r pgx.CollectableRow) (Member, error) {
	var m Member
	err := r.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt)
	return m, err
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}
