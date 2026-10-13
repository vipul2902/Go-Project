// Package monitoring exposes the live health and application logs of
// deployed environments, read from Kubernetes.
package monitoring

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/environments"
	"github.com/vipul2902/clouddeploy/internal/httpx"
	"github.com/vipul2902/clouddeploy/internal/kube"
	"github.com/vipul2902/clouddeploy/internal/projects"
)

// Handler serves monitoring endpoints. A nil inspector (no cluster access)
// makes every endpoint answer 503 instead of failing the whole API.
type Handler struct {
	projects     *projects.Service
	environments *environments.Service
	deployments  *deployments.Service
	inspector    *kube.Inspector
}

// NewHandler returns the monitoring handler.
func NewHandler(p *projects.Service, e *environments.Service, d *deployments.Service, in *kube.Inspector) *Handler {
	return &Handler{projects: p, environments: e, deployments: d, inspector: in}
}

const base = "/api/v1/projects/{id}/environments/{env}"

// Routes registers endpoints; all require authentication.
func (h *Handler) Routes(mux httpx.Router, requireAuth func(http.Handler) http.Handler) {
	mux.Handle("GET "+base+"/health", requireAuth(http.HandlerFunc(h.health)))
	mux.Handle("GET "+base+"/logs", requireAuth(http.HandlerFunc(h.logs)))
}

// resolve authorizes the caller for the environment (role `need`) and
// returns the cluster target. The namespace comes from the database record,
// never from the request: that, not Kubernetes RBAC, is what keeps one
// tenant from reading another's pods.
func (h *Handler) resolve(w http.ResponseWriter, r *http.Request, need auth.Role) (environments.Environment, kube.Target, bool) {
	if h.inspector == nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "MONITORING_UNAVAILABLE", "this API instance has no access to the cluster")
		return environments.Environment{}, kube.Target{}, false
	}
	id, ok := httpx.PathUUID(w, r, "id")
	if !ok {
		return environments.Environment{}, kube.Target{}, false
	}
	p, role, err := h.projects.Access(r.Context(), auth.UserIDFrom(r.Context()), id)
	if errors.Is(err, projects.ErrNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "PROJECT_NOT_FOUND", "project not found")
		return environments.Environment{}, kube.Target{}, false
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return environments.Environment{}, kube.Target{}, false
	}
	if !role.Allows(need) {
		httpx.WriteError(w, r, http.StatusForbidden, httpx.CodeForbidden, "your role does not permit this action")
		return environments.Environment{}, kube.Target{}, false
	}
	name := r.PathValue("env")
	env, err := h.environments.Get(r.Context(), p.ID, name)
	if errors.Is(err, environments.ErrNotFound) || !environments.ValidName(name) {
		httpx.WriteError(w, r, http.StatusNotFound, "ENVIRONMENT_NOT_FOUND", "environment not found")
		return environments.Environment{}, kube.Target{}, false
	}
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return environments.Environment{}, kube.Target{}, false
	}
	return env, kube.Target{Namespace: env.Namespace, Name: p.Name, Instance: p.Name + "-" + env.Name}, true
}

// healthResponse adds platform knowledge to the cluster's view.
type healthResponse struct {
	Environment string `json:"environment"`
	Namespace   string `json:"namespace"`
	kube.Health
	// LatestSucceeded is the last deployment CloudDeploy completed. Drift
	// lists how the cluster differs from it: a different release (a rollout
	// failing or in progress) or out-of-band changes (kubectl scale/edit).
	LatestSucceeded *deployments.Deployment `json:"latest_succeeded_deployment"`
	Drift           bool                    `json:"drift"`
	DriftReasons    []string                `json:"drift_reasons"`
	CheckedAt       time.Time               `json:"checked_at"`
}

// driftReasons compares the live cluster spec with the deployment
// CloudDeploy last completed successfully.
func driftReasons(live kube.Health, latest *deployments.Deployment) []string {
	reasons := []string{}
	if live.Status == kube.StatusNotDeployed || latest == nil {
		return reasons
	}
	c := latest.Config
	add := func(cond bool, format string, args ...any) {
		if cond {
			reasons = append(reasons, fmt.Sprintf(format, args...))
		}
	}
	add(live.LiveDeploymentID != latest.ID, "release: cluster runs deployment %s, last successful is %s", live.LiveDeploymentID, latest.ID)
	add(live.Replicas.Desired != int32(c.Replicas), "replicas: cluster wants %d, deployed with %d", live.Replicas.Desired, c.Replicas)
	add(latest.Image != nil && live.Live.Image != *latest.Image, "image: cluster runs %s, deployed %s", live.Live.Image, deref(latest.Image))
	add(!sameQuantity(live.Live.CPULimit, c.Resources.CPULimit) || !sameQuantity(live.Live.MemoryLimit, c.Resources.MemoryLimit) ||
		!sameQuantity(live.Live.CPURequest, c.Resources.CPURequest) || !sameQuantity(live.Live.MemoryRequest, c.Resources.MemoryRequest),
		"resources: cluster requests/limits differ from the deployed configuration")
	return reasons
}

// sameQuantity compares Kubernetes quantities semantically ("0.5" == "500m").
func sameQuantity(a, b string) bool {
	qa, err1 := resource.ParseQuantity(a)
	qb, err2 := resource.ParseQuantity(b)
	return err1 == nil && err2 == nil && qa.Cmp(qb) == 0
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// health: GET .../health (viewer).
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	env, target, ok := h.resolve(w, r, auth.RoleViewer)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	live, err := h.inspector.Health(ctx, target)
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadGateway, "CLUSTER_UNAVAILABLE", "could not read the environment's state from the cluster")
		return
	}
	latest, err := h.deployments.LatestSucceeded(r.Context(), env.ID)
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	resp := healthResponse{Environment: env.Name, Namespace: env.Namespace, Health: live, LatestSucceeded: latest, CheckedAt: time.Now().UTC()}
	resp.DriftReasons = driftReasons(live, latest)
	resp.Drift = len(resp.DriftReasons) > 0
	httpx.WriteJSON(w, http.StatusOK, resp)
}

// logs: GET .../logs?tail=100&since=15m&pod=&previous=true&follow=true
// (developer: application output often contains sensitive data).
func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	_, target, ok := h.resolve(w, r, auth.RoleDeveloper)
	if !ok {
		return
	}
	opts, follow, errs := parseLogQuery(r)
	if len(errs) > 0 {
		httpx.WriteValidationError(w, r, errs)
		return
	}
	if follow {
		h.follow(w, r, target, opts)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	lines, err := h.inspector.Logs(ctx, target, opts)
	if errors.Is(err, kube.ErrPodNotFound) {
		httpx.WriteError(w, r, http.StatusNotFound, "POD_NOT_FOUND", "pod not found in this environment")
		return
	}
	if err != nil {
		httpx.WriteError(w, r, http.StatusBadGateway, "CLUSTER_UNAVAILABLE", "could not read logs from the cluster")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.List[kube.LogLine]{Data: lines})
}

func (h *Handler) follow(w http.ResponseWriter, r *http.Request, target kube.Target, opts kube.LogOptions) {
	sse, err := httpx.StartSSE(w)
	if err != nil {
		httpx.WriteInternalError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), httpx.MaxStreamDuration)
	defer cancel()

	// Heartbeats keep idle connections alive; they share the writer with
	// log events, so both go through one goroutine-safe path.
	events := make(chan kube.LogLine)
	done := make(chan error, 1)
	go func() {
		done <- h.inspector.Follow(ctx, target, opts, func(l kube.LogLine) {
			select {
			case events <- l:
			case <-ctx.Done():
			}
		})
	}()
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	for {
		select {
		case l := <-events:
			if sse.Event("log", "", l) != nil {
				return
			}
		case <-beat.C:
			if sse.Heartbeat() != nil {
				return
			}
		case err := <-done:
			reason := "streams closed"
			if errors.Is(err, kube.ErrPodNotFound) {
				reason = "pod not found"
			}
			_ = sse.Event("end", "", map[string]string{"reason": reason})
			return
		case <-ctx.Done():
			return
		}
	}
}

func parseLogQuery(r *http.Request) (kube.LogOptions, bool, map[string]string) {
	q := r.URL.Query()
	o := kube.LogOptions{TailLines: 100, Pod: q.Get("pod"), Previous: q.Get("previous") == "true"}
	errs := map[string]string{}
	if v := q.Get("tail"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > kube.MaxTailLines {
			errs["tail"] = "must be between 1 and 1000"
		}
		o.TailLines = n
	}
	if v := q.Get("since"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > kube.MaxSince {
			errs["since"] = `must be a duration up to 24h, e.g. "15m"`
		}
		o.Since = d
	}
	follow := q.Get("follow") == "true"
	if follow && o.Previous {
		errs["previous"] = "cannot follow a previous (terminated) container"
	}
	return o, follow, errs
}
