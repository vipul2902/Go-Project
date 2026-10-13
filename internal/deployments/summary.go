package deployments

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/httpx"
)

// Summary is the dashboard overview across every project the user can see.
type Summary struct {
	Projects     int            `json:"projects"`
	Environments int            `json:"environments"`
	Active       int            `json:"active_deployments"`
	Last30Days   map[Status]int `json:"last_30_days"`
	// SuccessRate = RUNNING / (RUNNING + FAILED) over 30 days; null with no
	// finished deployments (cancellations are not counted either way).
	SuccessRate *float64     `json:"success_rate_30d"`
	Recent      []Deployment `json:"recent_deployments"`
}

// Summary computes the dashboard overview in a few aggregate queries rather
// than making the client page through every list.
func (s *Service) Summary(ctx context.Context, userID string) (Summary, error) {
	sum := Summary{Last30Days: map[Status]int{}}
	for st := range transitions {
		sum.Last30Days[st] = 0
	}
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM projects p JOIN organization_members m ON m.organization_id = p.organization_id AND m.user_id = $1
		    WHERE p.deleted_at IS NULL),
		  (SELECT count(*) FROM environments e JOIN projects p ON p.id = e.project_id
		    JOIN organization_members m ON m.organization_id = p.organization_id AND m.user_id = $1 WHERE p.deleted_at IS NULL)`,
		userID).Scan(&sum.Projects, &sum.Environments)
	if err != nil {
		return Summary{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT d.status, count(*) FROM deployments d
		JOIN projects p ON p.id = d.project_id AND p.deleted_at IS NULL
		JOIN organization_members m ON m.organization_id = p.organization_id AND m.user_id = $1
		WHERE d.created_at > now() - interval '30 days'
		GROUP BY d.status`, userID)
	if err != nil {
		return Summary{}, err
	}
	counts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (struct {
		st Status
		n  int
	}, error) {
		var c struct {
			st Status
			n  int
		}
		err := r.Scan(&c.st, &c.n)
		return c, err
	})
	if err != nil {
		return Summary{}, err
	}
	for _, c := range counts {
		sum.Last30Days[c.st] = c.n
		if c.st.Active() {
			sum.Active += c.n
		}
	}
	if done := sum.Last30Days[StatusRunning] + sum.Last30Days[StatusFailed]; done > 0 {
		rate := float64(sum.Last30Days[StatusRunning]) / float64(done)
		sum.SuccessRate = &rate
	}
	recent, _, err := s.List(ctx, userID, ListFilter{}, httpx.Page{Limit: 8})
	if err != nil {
		return Summary{}, err
	}
	sum.Recent = recent
	return sum, nil
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	sum, err := h.svc.Summary(r.Context(), auth.UserIDFrom(r.Context()))
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sum)
}
