package database

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppRole is the group role with the application's privileges (migration
// 00010): row access only, audit log append-only, no DDL.
const AppRole = "clouddeploy_app"

var roleNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// EnsureAppUser creates (or updates the password of) a LOGIN role that is a
// member of AppRole. It must run as a role with CREATEROLE, e.g. the
// migrate job's schema owner.
//
// The password is sent as a SCRAM-SHA-256 verifier, never in plain text:
// DDL can end up in server logs (log_statement=ddl), and a verifier there
// is not a usable password.
func EnsureAppUser(ctx context.Context, pool *pgxpool.Pool, name, password string) error {
	if !roleNameRE.MatchString(name) || name == AppRole {
		return fmt.Errorf("invalid role name %q", name)
	}
	if err := validPassword(password); err != nil {
		return err
	}
	verifier, err := scramVerifier(password)
	if err != nil {
		return err
	}
	ident := pgx.Identifier{name}.Sanitize()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_roles WHERE rolname = $1)`, name).Scan(&exists); err != nil {
		return err
	}
	// The verifier holds only base64 characters, '$' and ':', so quoting it
	// as a literal is safe; the name is a sanitized identifier.
	stmt := fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' IN ROLE %s`, ident, verifier, AppRole)
	if exists {
		stmt = fmt.Sprintf(`ALTER ROLE %s LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '%s'`, ident, verifier)
	}
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("create or update role %s: %w", name, err)
	}
	if exists {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`GRANT %s TO %s`, AppRole, ident)); err != nil {
			return fmt.Errorf("grant %s to %s: %w", AppRole, name, err)
		}
	}
	return nil
}

// validPassword requires 16-128 printable ASCII characters. SCRAM expects
// SASLprep-normalized passwords; ASCII needs no normalization, which keeps
// the verifier computed here identical to what PostgreSQL would compute.
func validPassword(p string) error {
	if len(p) < 16 || len(p) > 128 {
		return errors.New("password must be 16-128 characters")
	}
	for _, c := range p {
		if c < 0x21 || c > 0x7e {
			return errors.New("password must be printable ASCII without spaces")
		}
	}
	return nil
}

// scramVerifier returns the SCRAM-SHA-256 verifier PostgreSQL stores in
// pg_authid (RFC 5802 / RFC 7677):
// SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>.
func scramVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return scramVerifierWith(password, salt, 4096)
}

func scramVerifierWith(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(storedKey[:]), b64(serverKey)), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// ExcessPrivileges lists what the connected role can do beyond the
// application's needs: superuser, role or database creation, bypassing row
// security, or owning the schema's tables (owners can ALTER and DROP them).
// An empty result means the connection is least-privilege.
func ExcessPrivileges(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var super, createRole, createDB, bypassRLS, ownsTables bool
	err := pool.QueryRow(ctx, `
		SELECT r.rolsuper, r.rolcreaterole, r.rolcreatedb, r.rolbypassrls,
		       EXISTS (SELECT 1 FROM pg_tables WHERE schemaname = 'public' AND tableowner = current_user)
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&super, &createRole, &createDB, &bypassRLS, &ownsTables)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range []struct {
		on   bool
		what string
	}{
		{super, "superuser"}, {createRole, "can create roles"}, {createDB, "can create databases"},
		{bypassRLS, "bypasses row-level security"}, {ownsTables, "owns the schema's tables (can ALTER/DROP them)"},
	} {
		if c.on {
			out = append(out, c.what)
		}
	}
	return out, nil
}

// RequireLeastPrivilege checks the connection with ExcessPrivileges. With
// strict (production) excess privileges are a startup error; otherwise they
// are logged as a warning, since local development usually connects as the
// database owner.
func RequireLeastPrivilege(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, strict bool) error {
	excess, err := ExcessPrivileges(ctx, pool)
	if err != nil {
		return fmt.Errorf("check database privileges: %w", err)
	}
	if len(excess) == 0 {
		return nil
	}
	msg := "database role has more privileges than the application needs: " + strings.Join(excess, ", ") +
		" (connect as a member of " + AppRole + "; see docs/security.md)"
	if strict {
		return errors.New(msg)
	}
	logger.WarnContext(ctx, msg)
	return nil
}
