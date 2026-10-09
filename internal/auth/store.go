package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errNotFound = errors.New("not found")

// store holds the auth package's SQL. All queries are parameterized.
type store struct {
	pool *pgxpool.Pool
}

func (s store) createUserWithOrg(ctx context.Context, email, name, hash, orgName, slug string) (User, error) {
	u := User{Email: email, Name: name}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3) RETURNING id`,
			email, name, hash).Scan(&u.ID); err != nil {
			return err
		}
		var orgID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id`,
			orgName, slug).Scan(&orgID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'admin')`,
			orgID, u.ID)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "users_email_key" {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

func (s store) userByEmail(ctx context.Context, email string) (User, string, error) {
	var u User
	var hash string
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, name, password_hash FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email, &u.Name, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", errNotFound
	}
	return u, hash, err
}

func (s store) userByID(ctx context.Context, id string) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `SELECT id, email, name FROM users WHERE id = $1`, id).Scan(&u.ID, &u.Email, &u.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, errNotFound
	}
	return u, err
}

func (s store) memberships(ctx context.Context, userID string) ([]Membership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.name, o.slug, m.role
		FROM organization_members m JOIN organizations o ON o.id = m.organization_id
		WHERE m.user_id = $1 ORDER BY o.name`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Membership, error) {
		var m Membership
		err := r.Scan(&m.OrganizationID, &m.OrganizationName, &m.OrganizationSlug, &m.Role)
		return m, err
	})
}

func (s store) roleIn(ctx context.Context, userID, orgID string) (Role, error) {
	var role Role
	err := s.pool.QueryRow(ctx,
		`SELECT role FROM organization_members WHERE user_id = $1 AND organization_id = $2`,
		userID, orgID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	return role, err
}

// insertFamilyRoot stores the first token of a new family (a new login
// session). The family ID is the root token's own ID.
func (s store) insertFamilyRoot(ctx context.Context, userID string, hash []byte, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (id, user_id, token_hash, family_id, expires_at)
		SELECT id, $1, $2, id, $3 FROM (SELECT gen_random_uuid() AS id) t`,
		userID, hash, expires)
	return err
}

// rotateRefreshToken atomically validates the presented token, revokes it and
// inserts its replacement. SELECT ... FOR UPDATE serializes concurrent
// refreshes of the same token: the second one sees it revoked.
func (s store) rotateRefreshToken(ctx context.Context, oldHash, newHash []byte, expires time.Time) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) // no-op after Commit

	var id, userID, familyID string
	var expiresAt time.Time
	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, user_id, family_id, expires_at, revoked_at
		FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE`, oldHash).
		Scan(&id, &userID, &familyID, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidRefresh
	}
	if err != nil {
		return "", err
	}

	if revokedAt != nil {
		// Reuse of a rotated token: revoke the family and COMMIT that
		// revocation before reporting the error.
		if _, err := tx.Exec(ctx,
			`UPDATE refresh_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL`, familyID); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", ErrRefreshReuse
	}
	if time.Now().After(expiresAt) {
		return "", ErrInvalidRefresh
	}

	var newID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO refresh_tokens (user_id, token_hash, family_id, expires_at)
		VALUES ($1, $2, $3, $4) RETURNING id`, userID, newHash, familyID, expires).Scan(&newID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now(), replaced_by = $2 WHERE id = $1`, id, newID); err != nil {
		return "", err
	}
	return userID, tx.Commit(ctx)
}

func (s store) revokeFamilyOf(ctx context.Context, hash []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE revoked_at IS NULL
		  AND family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1)`, hash)
	return err
}
