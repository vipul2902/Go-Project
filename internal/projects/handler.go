package projects

import (
	"errors"
	"net/http"
	"time"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/httpx"
)

// Handler exposes project endpoints.
type Handler struct {
	svc *Service
}

// NewHandler returns the projects HTTP handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers endpoints; all require authentication.
func (h *Handler) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	handle := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, requireAuth(fn)) }
	handle("GET /api/v1/projects", h.list)
	handle("POST /api/v1/projects", h.create)
	handle("GET /api/v1/projects/{id}", h.get)
	handle("PATCH /api/v1/projects/{id}", h.update)
	handle("DELETE /api/v1/projects/{id}", h.delete)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, errs := httpx.ParsePage(r)
	q := r.URL.Query()
	f := ListFilter{OrganizationID: q.Get("organization_id"), NamePrefix: q.Get("q"), Sort: q.Get("sort")}
	if f.Sort == "" {
		f.Sort = DefaultSort
	}
	if _, ok := sorts[f.Sort]; !ok {
		errs["sort"] = "must be one of name, -name, created_at, -created_at"
	}
	if f.OrganizationID != "" && !httpx.IsUUID(f.OrganizationID) {
		errs["organization_id"] = "must be a UUID"
	}
	if len(f.NamePrefix) > 63 {
		errs["q"] = "must be at most 63 characters"
	}
	if len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}

	ps, next, err := h.svc.List(r.Context(), auth.UserIDFrom(r.Context()), f, page)
	if errors.Is(err, errInvalidCursor) {
		httpx.WriteValidationError(w, r, map[string]string{"cursor": "invalid or does not match sort"})
		return
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[Project]{Data: ps, NextCursor: next})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	errs := in.validate()
	if in.OrganizationID != "" && !httpx.IsUUID(in.OrganizationID) {
		errs["organization_id"] = "must be a UUID"
	}
	if len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	p, err := h.svc.Create(r.Context(), auth.UserIDFrom(r.Context()), in)
	if errors.Is(err, ErrNotFound) {
		// The org does not exist or the caller is not a member.
		httpx.WriteValidationError(w, r, map[string]string{"organization_id": "organization not found"})
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("ETag", p.ETag())
	w.Header().Set("Location", "/api/v1/projects/"+p.ID)
	httpx.WriteJSON(w, http.StatusCreated, p)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id")
	if !ok {
		return
	}
	p, err := h.svc.Get(r.Context(), auth.UserIDFrom(r.Context()), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("ETag", p.ETag())
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id")
	if !ok {
		return
	}
	var ifMatch *time.Time
	if v := r.Header.Get("If-Match"); v != "" {
		t, ok := ParseETag(v)
		if !ok {
			httpx.WriteError(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "invalid If-Match header")
			return
		}
		ifMatch = &t
	}
	var in UpdateInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if errs := in.validate(); len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	p, err := h.svc.Update(r.Context(), auth.UserIDFrom(r.Context()), id, in, ifMatch)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("ETag", p.ETag())
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), auth.UserIDFrom(r.Context()), id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found")
	case errors.Is(err, ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "your role does not permit this action")
	case errors.Is(err, ErrNameTaken):
		httpx.WriteError(w, r, http.StatusConflict, "PROJECT_NAME_TAKEN", "a project with this name already exists in the organization")
	case errors.Is(err, ErrActiveDeployment):
		httpx.WriteError(w, r, http.StatusConflict, "DEPLOYMENT_IN_PROGRESS", "cancel or wait for the active deployment before deleting the project")
	case errors.Is(err, ErrPreconditionFailed):
		httpx.WriteError(w, r, http.StatusPreconditionFailed, "PRECONDITION_FAILED", "project was modified since you read it; reload and retry")
	default:
		httpx.WriteInternalError(w, r, err)
	}
}
