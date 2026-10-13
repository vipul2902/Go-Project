// Package audit records who did what, when, and from where.
//
// Entries are written with the same database handle (usually a transaction)
// as the change they describe, so an action and its audit record commit or
// roll back together: there is never an action without a record, or a record
// for an action that did not happen.
package audit

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/middleware"
)

// Querier is satisfied by *pgxpool.Pool and pgx.Tx, so callers choose
// whether the record joins a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Actor identifies who acted and in which organization ("" = none).
type Actor struct {
	UserID         string
	OrganizationID string
}

// Record writes one audit entry. action is "<resource>.<verb>", e.g.
// "deployment.created". metadata must never contain secrets or passwords.
func Record(ctx context.Context, q Querier, actor Actor, action, resourceType, resourceID string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	if id := middleware.RequestIDFrom(ctx); id != "" {
		metadata["request_id"] = id
	}
	_, err := q.Exec(ctx, `
		INSERT INTO audit_logs (organization_id, actor_id, action, resource_type, resource_id, metadata, ip_address)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, $3, $4, NULLIF($5, ''), $6, $7)`,
		actor.OrganizationID, actor.UserID, action, resourceType, resourceID, metadata, clientIP(ctx))
	return err
}

func clientIP(ctx context.Context) *netip.Addr {
	if ip, ok := middleware.ClientIPFrom(ctx); ok {
		return &ip
	}
	return nil
}

// Entry is an audit record as returned by the API.
type Entry struct {
	ID           int64          `json:"id"`
	ActorID      *string        `json:"actor_id"`
	ActorEmail   *string        `json:"actor_email"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   *string        `json:"resource_id"`
	Metadata     map[string]any `json:"metadata"`
	IPAddress    *string        `json:"ip_address"`
	CreatedAt    time.Time      `json:"created_at"`
}

// Filter narrows an audit listing.
type Filter struct {
	Action       string
	ResourceType string
}

type cursor struct {
	ID int64 `json:"id"`
}

// List returns an organization's audit entries, newest first. IDs are
// monotonic, so keyset pagination on id alone is stable.
func List(ctx context.Context, pool *pgxpool.Pool, orgID string, f Filter, page httpx.Page) ([]Entry, *string, error) {
	var after *int64
	if page.Cursor != "" {
		var c cursor
		if err := httpx.DecodeCursor(page.Cursor, &c); err != nil || c.ID <= 0 {
			return nil, nil, ErrInvalidCursor
		}
		after = &c.ID
	}
	rows, err := pool.Query(ctx, `
		SELECT a.id, a.actor_id, u.email, a.action, a.resource_type, a.resource_id, a.metadata, host(a.ip_address), a.created_at
		FROM audit_logs a LEFT JOIN users u ON u.id = a.actor_id
		WHERE a.organization_id = $1
		  AND ($2 = '' OR a.action = $2)
		  AND ($3 = '' OR a.resource_type = $3)
		  AND ($4::bigint IS NULL OR a.id < $4)
		ORDER BY a.id DESC LIMIT $5`,
		orgID, f.Action, f.ResourceType, after, page.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Entry, error) {
		var e Entry
		err := r.Scan(&e.ID, &e.ActorID, &e.ActorEmail, &e.Action, &e.ResourceType, &e.ResourceID, &e.Metadata, &e.IPAddress, &e.CreatedAt)
		return e, err
	})
	if err != nil || len(entries) <= page.Limit {
		return entries, nil, err
	}
	entries = entries[:page.Limit]
	next := httpx.EncodeCursor(cursor{ID: entries[len(entries)-1].ID})
	return entries, &next, nil
}

// ErrInvalidCursor is returned for malformed cursors.
var ErrInvalidCursor = errors.New("invalid cursor")
