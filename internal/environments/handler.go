package environments

import (
	"errors"
	"net/http"

	"github.com/vipul2902/clouddeploy/internal/audit"
	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/projects"
)

// Handler exposes environment and variable endpoints under a project.
type Handler struct {
	svc      *Service
	projects *projects.Service
}

// NewHandler returns the environments HTTP handler.
func NewHandler(svc *Service, projectSvc *projects.Service) *Handler {
	return &Handler{svc: svc, projects: projectSvc}
}

const base = "/api/v1/projects/{id}/environments"

// Routes registers endpoints; all require authentication.
func (h *Handler) Routes(mux httpx.Router, requireAuth func(http.Handler) http.Handler) {
	handle := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, requireAuth(fn)) }
	handle("GET "+base, h.list)
	handle("POST "+base, h.create)
	handle("GET "+base+"/{env}", h.get)
	handle("PATCH "+base+"/{env}", h.update)
	handle("DELETE "+base+"/{env}", h.delete)
	handle("GET "+base+"/{env}/variables", h.listVariables)
	handle("PUT "+base+"/{env}/variables/{key}", h.setVariable)
	handle("DELETE "+base+"/{env}/variables/{key}", h.deleteVariable)
}

// project resolves {id} and the caller's role; non-members get 404.
func (h *Handler) project(w http.ResponseWriter, r *http.Request) (projects.Project, auth.Role, bool) {
	id, ok := httpx.PathUUID(w, r, "id")
	if !ok {
		return projects.Project{}, "", false
	}
	p, role, err := h.projects.Access(r.Context(), auth.UserIDFrom(r.Context()), id)
	if errors.Is(err, projects.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found")
		return projects.Project{}, "", false
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return projects.Project{}, "", false
	}
	return p, role, true
}

func allow(w http.ResponseWriter, r *http.Request, have, need auth.Role) bool {
	if have.Allows(need) {
		return true
	}
	msg := "your role does not permit this action"
	if need == auth.RoleAdmin {
		msg = "changing production configuration requires the admin role"
	}
	httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, msg)
	return false
}

// environment resolves {id} and {env}, checks the caller has `need` (a
// function of the environment name), and loads the environment. It also
// returns the audit actor (caller + organization) for mutations.
func (h *Handler) environment(w http.ResponseWriter, r *http.Request, need func(string) auth.Role) (Environment, audit.Actor, bool) {
	p, role, ok := h.project(w, r)
	if !ok {
		return Environment{}, audit.Actor{}, false
	}
	name := r.PathValue("env")
	if !ValidName(name) {
		writeErr(w, r, ErrNotFound)
		return Environment{}, audit.Actor{}, false
	}
	if !allow(w, r, role, need(name)) {
		return Environment{}, audit.Actor{}, false
	}
	e, err := h.svc.Get(r.Context(), p.ID, name)
	if err != nil {
		writeErr(w, r, err)
		return Environment{}, audit.Actor{}, false
	}
	return e, actorFor(r, p), true
}

// actorFor builds the audit actor for the caller acting within p's organization.
func actorFor(r *http.Request, p projects.Project) audit.Actor {
	return audit.Actor{UserID: auth.UserIDFrom(r.Context()), OrganizationID: p.OrganizationID}
}

func viewer(string) auth.Role { return auth.RoleViewer }

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, _, ok := h.project(w, r) // any member may view
	if !ok {
		return
	}
	envs, err := h.svc.List(r.Context(), p.ID)
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[Environment]{Data: envs})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, role, ok := h.project(w, r)
	if !ok {
		return
	}
	var in CreateInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if errs := in.normalize(p.Name); len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	if !allow(w, r, role, RequiredRole(in.Name)) {
		return
	}
	e, err := h.svc.Create(r.Context(), actorFor(r, p), p.ID, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+p.ID+"/environments/"+e.Name)
	httpx.WriteJSON(w, http.StatusCreated, e)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if e, _, ok := h.environment(w, r, viewer); ok {
		httpx.WriteJSON(w, http.StatusOK, e)
	}
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	e, actor, ok := h.environment(w, r, RequiredRole)
	if !ok {
		return
	}
	var in UpdateInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if errs := in.validate(); len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	updated, err := h.svc.Update(r.Context(), actor, e.ProjectID, e.Name, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, updated)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	e, actor, ok := h.environment(w, r, func(string) auth.Role { return auth.RoleAdmin })
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), actor, e.ProjectID, e.Name); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listVariables(w http.ResponseWriter, r *http.Request) {
	e, _, ok := h.environment(w, r, viewer)
	if !ok {
		return
	}
	vars, err := h.svc.Variables(r.Context(), e.ID)
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[Variable]{Data: vars})
}

func (h *Handler) setVariable(w http.ResponseWriter, r *http.Request) {
	e, actor, ok := h.environment(w, r, RequiredRole)
	if !ok {
		return
	}
	key := r.PathValue("key")
	var in VariableInput
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if errs := validateVariable(key, in); len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	v, created, err := h.svc.SetVariable(r.Context(), actor, e, key, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, v)
}

func (h *Handler) deleteVariable(w http.ResponseWriter, r *http.Request) {
	e, actor, ok := h.environment(w, r, RequiredRole)
	if !ok {
		return
	}
	if err := h.svc.DeleteVariable(r.Context(), actor, e, r.PathValue("key")); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "ENVIRONMENT_NOT_FOUND", "environment not found")
	case errors.Is(err, ErrVariableNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "VARIABLE_NOT_FOUND", "variable not found")
	case errors.Is(err, ErrExists):
		httpx.WriteError(w, r, http.StatusConflict, "ENVIRONMENT_EXISTS", "this project already has that environment")
	case errors.Is(err, ErrNamespaceTaken):
		httpx.WriteValidationError(w, r, map[string]string{"namespace": "is already used by another environment"})
	case errors.Is(err, ErrHasDeployments):
		httpx.WriteError(w, r, http.StatusConflict, "ENVIRONMENT_HAS_DEPLOYMENTS", "environment has deployment history and cannot be deleted")
	case errors.Is(err, ErrTooManyVariables):
		httpx.WriteError(w, r, http.StatusConflict, "TOO_MANY_VARIABLES", "an environment can have at most 100 variables")
	default:
		httpx.WriteInternalError(w, r, err)
	}
}
