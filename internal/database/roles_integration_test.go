//go:build integration

package database_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/database"
	"github.com/vipul2902/clouddeploy/internal/database/dbtest"
)

// connectAs opens a pool to the test database as another login role.
func connectAs(t *testing.T, user, password string) (*pgxpool.Pool, error) {
	t.Helper()
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, password)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return database.Open(ctx, u.String(), database.PoolConfig{MaxConns: 2, MaxConnLifetime: time.Minute})
}

func TestAppRoleIsLeastPrivilege(t *testing.T) {
	owner := dbtest.New(t)
	ctx := t.Context()
	const user = "cd_it_app"
	password := "it-only-" + time.Now().Format("150405.000000")
	if err := database.EnsureAppUser(ctx, owner, user, password); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), `DROP ROLE IF EXISTS cd_it_app`) })

	app, err := connectAs(t, user, password)
	if err != nil {
		t.Fatalf("login as app role: %v", err)
	}
	defer app.Close()

	// What the application needs works.
	for _, q := range []string{
		`SELECT count(*) FROM users`,
		`UPDATE users SET name = name WHERE false`,
		`DELETE FROM refresh_tokens WHERE false`,
		`SELECT count(*) FROM audit_logs`,
	} {
		if _, err := app.Exec(ctx, q); err != nil {
			t.Errorf("app role cannot %q: %v", q, err)
		}
	}
	// Everything else is denied with insufficient_privilege (42501). The
	// WHERE false still requires the privilege, so no data is touched.
	for _, q := range []string{
		`UPDATE audit_logs SET action = action WHERE false`, // audit trail is append-only
		`DELETE FROM audit_logs WHERE false`,
		`TRUNCATE audit_logs`,
		`DROP TABLE users`, // DDL belongs to the owner
		`ALTER TABLE users ADD COLUMN x int`,
		`SELECT * FROM goose_db_version`, // migration history
		`CREATE TABLE evil (id int)`,
	} {
		_, err := app.Exec(ctx, q)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("app role %q: err = %v, want insufficient_privilege", q, err)
		}
	}

	if excess, err := database.ExcessPrivileges(ctx, app); err != nil || len(excess) != 0 {
		t.Errorf("app role excess privileges = %v, %v; want none", excess, err)
	}
	if excess, _ := database.ExcessPrivileges(ctx, owner); len(excess) == 0 {
		t.Error("owner connection should be reported as over-privileged")
	}
}

func TestEnsureAppUserRotatesPassword(t *testing.T) {
	owner := dbtest.New(t)
	ctx := t.Context()
	const user = "cd_it_rotate"
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), `DROP ROLE IF EXISTS cd_it_rotate`) })

	first, second := "first-password-0000001", "second-password-000002"
	if err := database.EnsureAppUser(ctx, owner, user, first); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureAppUser(ctx, owner, user, second); err != nil {
		t.Fatal(err)
	}
	if p, err := connectAs(t, user, first); err == nil {
		p.Close()
		t.Error("old password still works after rotation")
	}
	p, err := connectAs(t, user, second)
	if err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	p.Close()
}

func TestEnsureAppUserRejectsBadInput(t *testing.T) {
	owner := dbtest.New(t)
	for name, c := range map[string][2]string{
		"injection in name": {`x"; DROP TABLE users; --`, "long-enough-password-1"},
		"the group role":    {database.AppRole, "long-enough-password-1"},
		"uppercase":         {"App", "long-enough-password-1"},
		"short password":    {"cd_ok", "short"},
		"space in password": {"cd_ok", "has a space in it 12345"},
	} {
		if err := database.EnsureAppUser(t.Context(), owner, c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
