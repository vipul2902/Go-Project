package deployments

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vipul2902/clouddeploy/internal/audit"
	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/environments"
	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/projects"
)

// HeaderIdempotencyKey is the request header carrying the client's key.
const HeaderIdempotencyKey = "Idempotency-Key"

// Handler exposes deployment endpoints.
type Handler struct {
	svc          *Service
	projects     *projects.Service
	environments *environments.Service
}

// NewHandler returns the deployments HTTP handler.
func NewHandler(svc *Service, projectSvc *projects.Service, envSvc *environments.Service) *Handler {
	return &Handler{svc: svc, projects: projectSvc, environments: envSvc}
}

// Routes registers endpoints; all require authentication.
func (h *Handler) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	handle := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, requireAuth(fn)) }
	handle("POST /api/v1/deployments", h.create)
	handle("GET /api/v1/deployments", h.list)
	handle("GET /api/v1/deployments/{id}", h.get)
	handle("POST /api/v1/deployments/{id}/cancel", h.cancel)
	handle("GET /api/v1/deployments/{id}/logs", h.logs)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(HeaderIdempotencyKey)
	if key != "" && !ValidIdempotencyKey(key) {
		httpx.WriteValidationError(w, r, map[string]string{"Idempotency-Key": "must be 1-128 chars of A-Z a-z 0-9 _ . : -"})
		return
	}
	var in CreateInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if errs := in.validate(httpx.IsUUID); len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}

	userID := auth.UserIDFrom(r.Context())
	p, role, err := h.projects.Access(r.Context(), userID, in.ProjectID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Deploying is a change to the environment: same rule as editing its
	// configuration (production requires admin).
	if !role.Allows(environments.RequiredRole(in.Environment)) {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "your role does not permit deploying to "+in.Environment)
		return
	}
	env, err := h.environments.Get(r.Context(), p.ID, in.Environment)
	if errors.Is(err, environments.ErrNotFound) {
		httpx.WriteValidationError(w, r, map[string]string{"environment": "does not exist for this project; create it first"})
		return
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}

	d, replayed, err := h.svc.Create(r.Context(), CreateParams{
		ActorID: userID, Project: p, Environment: env, CommitSHA: in.CommitSHA, IdempotencyKey: key,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/deployments/"+d.ID)
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.WriteJSON(w, http.StatusOK, d)
		return
	}
	// 202: the deployment is accepted and queued; it completes asynchronously.
	httpx.WriteJSON(w, http.StatusAccepted, d)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, errs := httpx.ParsePage(r)
	q := r.URL.Query()
	f := ListFilter{ProjectID: q.Get("project_id"), Environment: q.Get("environment"), Status: Status(q.Get("status"))}
	if f.ProjectID != "" && !httpx.IsUUID(f.ProjectID) {
		errs["project_id"] = "must be a UUID"
	}
	if f.Environment != "" && !environments.ValidName(f.Environment) {
		errs["environment"] = "must be development, staging or production"
	}
	if f.Status != "" && !f.Status.Valid() {
		errs["status"] = "must be one of QUEUED, BUILDING, DEPLOYING, RUNNING, FAILED, CANCELLED"
	}
	if len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	ds, next, err := h.svc.List(r.Context(), auth.UserIDFrom(r.Context()), f, page)
	if errors.Is(err, ErrInvalidCursor) {
		httpx.WriteValidationError(w, r, map[string]string{"cursor": "invalid"})
		return
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[Deployment]{Data: ds, NextCursor: next})
}

// load fetches {id} and the caller's role in its project; deployments of
// projects the caller cannot see are reported as not found.
func (h *Handler) load(w http.ResponseWriter, r *http.Request) (Deployment, projects.Project, auth.Role, bool) {
	id, ok := httpx.PathUUID(w, r, "id")
	if !ok {
		return Deployment{}, projects.Project{}, "", false
	}
	d, err := h.svc.Get(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return Deployment{}, projects.Project{}, "", false
	}
	p, role, err := h.projects.Access(r.Context(), auth.UserIDFrom(r.Context()), d.ProjectID)
	if errors.Is(err, projects.ErrNotFound) {
		writeErr(w, r, ErrNotFound)
		return Deployment{}, projects.Project{}, "", false
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return Deployment{}, projects.Project{}, "", false
	}
	return d, p, role, true
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if d, _, _, ok := h.load(w, r); ok {
		httpx.WriteJSON(w, http.StatusOK, d)
	}
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	d, p, role, ok := h.load(w, r)
	if !ok {
		return
	}
	if !role.Allows(environments.RequiredRole(d.Environment)) {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "your role does not permit cancelling deployments to "+d.Environment)
		return
	}
	d, err := h.svc.Cancel(r.Context(), audit.Actor{UserID: auth.UserIDFrom(r.Context()), OrganizationID: p.OrganizationID}, d.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// logs returns pipeline log lines after ?after=<id> (default 0), oldest
// first, up to ?limit (default 200, max 1000). next_after is the id to pass
// on the next poll.
func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	d, _, _, ok := h.load(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	after, limit := int64(0), 200
	errs := map[string]string{}
	if v := q.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs["after"] = "must be a non-negative integer"
		}
		after = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			errs["limit"] = "must be between 1 and 1000"
		}
		limit = n
	}
	if len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	if q.Get("follow") == "true" {
		// Resume where an EventSource left off after a reconnect.
		if v, err := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64); err == nil && v > after {
			after = v
		}
		h.followLogs(w, r, d.ID, after)
		return
	}
	lines, err := h.svc.Logs(r.Context(), d.ID, after, limit)
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	if lines == nil {
		lines = []LogLine{}
	}
	next := after
	if len(lines) > 0 {
		next = lines[len(lines)-1].ID
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Data      []LogLine `json:"data"`
		NextAfter int64     `json:"next_after"`
		Status    Status    `json:"status"`
	}{lines, next, d.Status})
}

// followLogs streams pipeline log lines as Server-Sent Events: one "log"
// event per line (id = line id), periodic heartbeats, and a final "end"
// event once the deployment reaches a terminal state and every line has
// been sent. The worker writes lines to Postgres, so the stream polls it.
func (h *Handler) followLogs(w http.ResponseWriter, r *http.Request, id string, after int64) {
	sse, err := httpx.StartSSE(w)
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), httpx.MaxStreamDuration)
	defer cancel()
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	lastWrite := time.Now()
	for {
		// Read status BEFORE lines: if it is terminal now, every line the
		// worker wrote is already committed, so draining them is complete.
		d, err := h.svc.Get(ctx, id)
		if err != nil {
			return
		}
		for {
			lines, err := h.svc.Logs(ctx, id, after, 500)
			if err != nil {
				return
			}
			for _, l := range lines {
				if sse.Event("log", strconv.FormatInt(l.ID, 10), l) != nil {
					return // client went away
				}
				after = l.ID
				lastWrite = time.Now()
			}
			if len(lines) < 500 {
				break
			}
		}
		if d.Status.Terminal() {
			_ = sse.Event("end", "", map[string]any{"status": d.Status, "error_message": d.ErrorMessage})
			return
		}
		if time.Since(lastWrite) > 15*time.Second {
			if sse.Heartbeat() != nil {
				return
			}
			lastWrite = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inProgress *InProgressError
	var transition *TransitionError
	switch {
	case errors.As(err, &inProgress):
		httpx.WriteErrorDetails(w, r, http.StatusConflict, "DEPLOYMENT_IN_PROGRESS",
			"another deployment is in progress for this environment",
			map[string]string{"active_deployment_id": inProgress.ActiveID})
	case errors.As(err, &transition):
		httpx.WriteError(w, r, http.StatusConflict, "INVALID_TRANSITION",
			"deployment is "+string(transition.From)+" and cannot become "+string(transition.To))
	case errors.Is(err, ErrIdempotencyMismatch):
		httpx.WriteError(w, r, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED",
			"this Idempotency-Key was already used for a different deployment request")
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "DEPLOYMENT_NOT_FOUND", "deployment not found")
	case errors.Is(err, projects.ErrNotFound), errors.Is(err, ErrProjectGone):
		httpx.WriteError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found")
	default:
		httpx.WriteInternalError(w, r, err)
	}
}
