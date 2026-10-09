package organizations

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/slug"
)

// Handler exposes organization endpoints.
type Handler struct {
	svc *Service
}

// NewHandler returns the organizations HTTP handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers endpoints; all require authentication.
func (h *Handler) Routes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	handle := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, requireAuth(fn)) }
	handle("GET /api/v1/organizations", h.list)
	handle("POST /api/v1/organizations", h.create)
	handle("GET /api/v1/organizations/{orgID}", h.get)
	handle("GET /api/v1/organizations/{orgID}/members", h.listMembers)
	handle("POST /api/v1/organizations/{orgID}/members", h.addMember)
	handle("PATCH /api/v1/organizations/{orgID}/members/{userID}", h.updateMember)
	handle("DELETE /api/v1/organizations/{orgID}/members/{userID}", h.removeMember)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	orgs, err := h.svc.ListForUser(r.Context(), auth.UserIDFrom(r.Context()))
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[Organization]{Data: orgs})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	errs := map[string]string{}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 100 {
		errs["name"] = "must be 1-100 characters"
	}
	if in.Slug == "" {
		in.Slug = slug.WithSuffix(in.Name)
	} else if !slug.Valid(in.Slug) {
		errs["slug"] = "must be lowercase letters, digits and hyphens (1-63 chars)"
	}
	if len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	o, err := h.svc.Create(r.Context(), auth.UserIDFrom(r.Context()), in.Name, in.Slug)
	if errors.Is(err, ErrSlugTaken) {
		httpx.WriteError(w, r, http.StatusConflict, "SLUG_TAKEN", "an organization with this slug already exists")
		return
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, o)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if o, ok := h.authorize(w, r, auth.RoleViewer); ok {
		httpx.WriteJSON(w, http.StatusOK, o)
	}
}

func (h *Handler) listMembers(w http.ResponseWriter, r *http.Request) {
	o, ok := h.authorize(w, r, auth.RoleViewer)
	if !ok {
		return
	}
	ms, err := h.svc.Members(r.Context(), o.ID)
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[Member]{Data: ms})
}

func (h *Handler) addMember(w http.ResponseWriter, r *http.Request) {
	o, ok := h.authorize(w, r, auth.RoleAdmin)
	if !ok {
		return
	}
	var in struct {
		Email string    `json:"email"`
		Role  auth.Role `json:"role"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if !in.Role.Valid() {
		httpx.WriteValidationError(w, r, map[string]string{"role": "must be admin, developer or viewer"})
		return
	}
	m, err := h.svc.AddMember(r.Context(), o.ID, strings.ToLower(strings.TrimSpace(in.Email)), in.Role)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, m)
}

func (h *Handler) updateMember(w http.ResponseWriter, r *http.Request) {
	o, ok := h.authorize(w, r, auth.RoleAdmin)
	if !ok {
		return
	}
	userID, ok := httpx.PathUUID(w, r, "userID")
	if !ok {
		return
	}
	var in struct {
		Role auth.Role `json:"role"`
	}
	if !httpx.DecodeJSON(w, r, &in) {
		return
	}
	if !in.Role.Valid() {
		httpx.WriteValidationError(w, r, map[string]string{"role": "must be admin, developer or viewer"})
		return
	}
	m, err := h.svc.UpdateRole(r.Context(), o.ID, userID, in.Role)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

func (h *Handler) removeMember(w http.ResponseWriter, r *http.Request) {
	o, ok := h.authorize(w, r, auth.RoleAdmin)
	if !ok {
		return
	}
	userID, ok := httpx.PathUUID(w, r, "userID")
	if !ok {
		return
	}
	if err := h.svc.RemoveMember(r.Context(), o.ID, userID); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authorize loads the {orgID} organization for the caller and checks their
// role: non-members get 404 (existence is hidden), members lacking the role
// get 403.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request, need auth.Role) (Organization, bool) {
	orgID, ok := httpx.PathUUID(w, r, "orgID")
	if !ok {
		return Organization{}, false
	}
	o, err := h.svc.Get(r.Context(), auth.UserIDFrom(r.Context()), orgID)
	if err != nil {
		h.writeErr(w, r, err)
		return Organization{}, false
	}
	if !o.Role.Allows(need) {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "your role does not permit this action")
		return Organization{}, false
	}
	return o, true
}

func (h *Handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "organization not found")
	case errors.Is(err, ErrMemberMissing):
		httpx.WriteError(w, r, http.StatusNotFound, httpx.CodeNotFound, "member not found")
	case errors.Is(err, ErrUserNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "USER_NOT_FOUND", "no user with this email; they must register first")
	case errors.Is(err, ErrAlreadyMember):
		httpx.WriteError(w, r, http.StatusConflict, "ALREADY_MEMBER", "user is already a member")
	case errors.Is(err, ErrLastAdmin):
		httpx.WriteError(w, r, http.StatusConflict, "LAST_ADMIN", "organization must keep at least one admin")
	default:
		httpx.WriteInternalError(w, r, err)
	}
}
