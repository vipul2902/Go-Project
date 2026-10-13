//go:build integration

package deployments_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/audit"
	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/httpapi/apitest"
)

type fixture struct {
	api                         *apitest.API
	admin, dev, viewer, outside apitest.User
	projectID                   string
}

func setup(t *testing.T) fixture {
	t.Helper()
	api := apitest.New(t)
	f := fixture{
		api: api, admin: api.Register("admin@example.com"), dev: api.Register("dev@example.com"),
		viewer: api.Register("viewer@example.com"), outside: api.Register("outsider@example.com"),
	}
	api.AddMember(f.admin.OrgID, f.dev.ID, "developer")
	api.AddMember(f.admin.OrgID, f.viewer.ID, "viewer")
	f.projectID = api.Do(http.MethodPost, "/api/v1/projects", f.admin.Token, map[string]string{
		"organization_id": f.admin.OrgID, "name": "billing-api", "repository_url": "https://github.com/acme/billing-api",
	}).Str("id")
	envs := "/api/v1/projects/" + f.projectID + "/environments"
	for _, env := range []string{"development", "production"} {
		if res := api.Do(http.MethodPost, envs, f.admin.Token, map[string]string{"name": env}); res.Status != http.StatusCreated {
			t.Fatalf("create env %s: %d %v", env, res.Status, res.Body)
		}
	}
	api.Do(http.MethodPut, envs+"/development/variables/LOG_LEVEL", f.admin.Token, map[string]string{"value": "debug"})
	api.Do(http.MethodPut, envs+"/development/variables/DB_PASSWORD", f.admin.Token, map[string]string{"secret_ref": "db/password"})
	return f
}

func (f fixture) deploy(token, env, sha string, headers ...string) apitest.Response {
	return f.api.Do(http.MethodPost, "/api/v1/deployments", token,
		map[string]string{"project_id": f.projectID, "environment": env, "commit_sha": sha}, headers...)
}

func TestCreateDeploymentQueuesWithSnapshot(t *testing.T) {
	f := setup(t)
	res := f.deploy(f.dev.Token, "development", "ABC1234") // upper-case SHA is normalized
	if res.Status != http.StatusAccepted {
		t.Fatalf("create: %d %v", res.Status, res.Body)
	}
	if res.Str("status") != "QUEUED" || res.Str("commit_sha") != "abc1234" || res.Str("environment") != "development" {
		t.Errorf("unexpected deployment: %v", res.Body)
	}
	if res.Str("triggered_by", "id") != f.dev.ID || res.Header.Get("Location") != "/api/v1/deployments/"+res.Str("id") {
		t.Errorf("trigger/location wrong: %v %s", res.Body["triggered_by"], res.Header.Get("Location"))
	}
	cfg := res.Body["config"].(map[string]any)
	if cfg["namespace"] != "billing-api-dev" || len(cfg["variables"].([]any)) != 2 {
		t.Fatalf("snapshot: %v", cfg)
	}
	secret := cfg["variables"].([]any)[0].(map[string]any) // sorted: DB_PASSWORD first
	if secret["key"] != "DB_PASSWORD" || secret["value"] != nil || secret["secret_ref"] != "db/password" {
		t.Errorf("secret in snapshot: %v", secret)
	}

	// The snapshot is immutable: later config changes do not rewrite history.
	f.api.Do(http.MethodPut, "/api/v1/projects/"+f.projectID+"/environments/development/variables/LOG_LEVEL",
		f.dev.Token, map[string]string{"value": "info"})
	got := f.api.Do(http.MethodGet, "/api/v1/deployments/"+res.Str("id"), f.viewer.Token, nil)
	vars := got.Body["config"].(map[string]any)["variables"].([]any)
	if vars[1].(map[string]any)["value"] != "debug" {
		t.Errorf("snapshot changed after env update: %v", vars[1])
	}
}

func TestDeploymentAuthorizationAndValidation(t *testing.T) {
	f := setup(t)
	cases := []struct {
		name  string
		token string
		env   string
		sha   string
		want  int
		code  string
	}{
		{"viewer cannot deploy", f.viewer.Token, "development", "abc1234", http.StatusForbidden, "FORBIDDEN"},
		{"developer cannot deploy to production", f.dev.Token, "production", "abc1234", http.StatusForbidden, "FORBIDDEN"},
		{"outsider sees no project", f.outside.Token, "development", "abc1234", http.StatusNotFound, "PROJECT_NOT_FOUND"},
		{"missing environment", f.dev.Token, "staging", "abc1234", http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"bad sha", f.dev.Token, "development", "not-a-sha", http.StatusUnprocessableEntity, "VALIDATION_FAILED"},
		{"admin deploys to production", f.admin.Token, "production", "abc1234", http.StatusAccepted, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := f.deploy(c.token, c.env, c.sha)
			if res.Status != c.want || res.ErrCode() != c.code {
				t.Errorf("%d %s, want %d %s (%v)", res.Status, res.ErrCode(), c.want, c.code, res.Body)
			}
		})
	}
}

func TestOneActiveDeploymentPerEnvironment(t *testing.T) {
	f := setup(t)
	first := f.deploy(f.dev.Token, "development", "aaaaaaa")
	second := f.deploy(f.dev.Token, "development", "bbbbbbb")
	if second.Status != http.StatusConflict || second.ErrCode() != "DEPLOYMENT_IN_PROGRESS" {
		t.Fatalf("second deploy: %d %v", second.Status, second.Body)
	}
	details := second.Body["error"].(map[string]any)["details"].(map[string]any)
	if details["active_deployment_id"] != first.Str("id") {
		t.Errorf("active id = %v, want %s", details["active_deployment_id"], first.Str("id"))
	}
	if second.Str("error", "request_id") == "" {
		t.Error("error response missing request_id")
	}
	// Other environments are independent.
	if res := f.deploy(f.admin.Token, "production", "bbbbbbb"); res.Status != http.StatusAccepted {
		t.Errorf("deploy to other env: %d", res.Status)
	}
}

func TestIdempotency(t *testing.T) {
	f := setup(t)
	first := f.deploy(f.dev.Token, "development", "abc1234", "Idempotency-Key", "click-42")
	if first.Status != http.StatusAccepted {
		t.Fatalf("first: %d %v", first.Status, first.Body)
	}
	// The user double-clicks / the client retries after a timeout.
	retry := f.deploy(f.dev.Token, "development", "abc1234", "Idempotency-Key", "click-42")
	if retry.Status != http.StatusOK || retry.Str("id") != first.Str("id") || retry.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry: %d id=%s replayed=%q", retry.Status, retry.Str("id"), retry.Header.Get("Idempotent-Replayed"))
	}
	// Same key, different request: refuse rather than silently replay.
	if res := f.deploy(f.dev.Token, "development", "fffffff", "Idempotency-Key", "click-42"); res.ErrCode() != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("key reuse: %d %v", res.Status, res.Body)
	}
	if res := f.deploy(f.dev.Token, "development", "abc1234", "Idempotency-Key", "bad key!"); res.Status != http.StatusUnprocessableEntity {
		t.Errorf("invalid key: %d", res.Status)
	}
	assertDeploymentCount(t, f, 1)
}

// Ten identical requests arrive at once (aggressive client retries). All
// must resolve to the same deployment; exactly one creates it.
func TestConcurrentIdempotentRequests(t *testing.T) {
	f := setup(t)
	const n = 10
	var wg sync.WaitGroup
	results := make(chan apitest.Response, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.deploy(f.dev.Token, "development", "abc1234", "Idempotency-Key", "same-key")
		}()
	}
	wg.Wait()
	close(results)
	ids := map[string]bool{}
	created := 0
	for res := range results {
		switch res.Status {
		case http.StatusAccepted:
			created++
		case http.StatusOK:
		default:
			t.Errorf("unexpected %d %v", res.Status, res.Body)
		}
		ids[res.Str("id")] = true
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("created=%d distinct ids=%d; want 1 and 1", created, len(ids))
	}
	assertDeploymentCount(t, f, 1)
}

func assertDeploymentCount(t *testing.T, f fixture, want int) {
	t.Helper()
	var n int
	if err := f.api.Pool.QueryRow(t.Context(), `SELECT count(*) FROM deployments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Errorf("deployments in DB = %d, want %d", n, want)
	}
}

func TestStateMachineLifecycle(t *testing.T) {
	f := setup(t)
	svc := deployments.NewService(f.api.Pool)
	ctx := context.Background()
	id := f.deploy(f.dev.Token, "development", "abc1234").Str("id")

	// Skipping states is rejected by the database compare-and-swap.
	var te *deployments.TransitionError
	if _, err := svc.Transition(ctx, id, deployments.StatusRunning, ""); !errors.As(err, &te) || te.From != deployments.StatusQueued {
		t.Fatalf("QUEUED -> RUNNING: err = %v", err)
	}

	for _, to := range []deployments.Status{deployments.StatusBuilding, deployments.StatusDeploying, deployments.StatusRunning} {
		d, err := svc.Transition(ctx, id, to, "")
		if err != nil || d.Status != to {
			t.Fatalf("-> %s: %v (status %s)", to, err, d.Status)
		}
	}
	d, _ := svc.Get(ctx, id)
	if d.StartedAt == nil || d.CompletedAt == nil || d.DurationSeconds == nil || *d.DurationSeconds < 0 {
		t.Errorf("timestamps not set: started=%v completed=%v duration=%v", d.StartedAt, d.CompletedAt, d.DurationSeconds)
	}
	// Terminal: nothing moves it again.
	if _, err := svc.Transition(ctx, id, deployments.StatusFailed, "late failure"); !errors.As(err, &te) {
		t.Errorf("RUNNING -> FAILED: err = %v", err)
	}
	// A finished deployment frees the environment.
	if res := f.deploy(f.dev.Token, "development", "bbbbbbb"); res.Status != http.StatusAccepted {
		t.Errorf("next deployment: %d", res.Status)
	}
	if _, err := svc.Transition(ctx, "00000000-0000-4000-8000-00000000dead", deployments.StatusBuilding, ""); !errors.Is(err, deployments.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestFailureRecordsErrorMessage(t *testing.T) {
	f := setup(t)
	svc := deployments.NewService(f.api.Pool)
	id := f.deploy(f.dev.Token, "development", "abc1234").Str("id")
	svc.Transition(t.Context(), id, deployments.StatusBuilding, "")
	svc.Transition(t.Context(), id, deployments.StatusDeploying, "")
	d, err := svc.Transition(t.Context(), id, deployments.StatusFailed, "rollout timed out")
	if err != nil || d.Status != deployments.StatusFailed || d.ErrorMessage == nil || *d.ErrorMessage != "rollout timed out" {
		t.Fatalf("DEPLOYING -> FAILED: %v %+v", err, d)
	}
}

// A user cancels while the worker tries to advance the same deployment.
// Exactly one transition may win; the loser sees an invalid transition.
func TestCancelRacesWorker(t *testing.T) {
	f := setup(t)
	svc := deployments.NewService(f.api.Pool)
	for i := range 20 {
		id := f.deploy(f.dev.Token, "development", fmt.Sprintf("%07x", i+1)).Str("id")
		var wg sync.WaitGroup
		var cancelErr, workerErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, cancelErr = svc.Cancel(t.Context(), audit.Actor{UserID: f.dev.ID, OrganizationID: f.admin.OrgID}, id)
		}()
		go func() {
			defer wg.Done()
			_, workerErr = svc.Transition(t.Context(), id, deployments.StatusBuilding, "")
		}()
		wg.Wait()

		var te *deployments.TransitionError
		d, _ := svc.Get(t.Context(), id)
		switch {
		case cancelErr == nil && workerErr == nil:
			// Valid serial order: worker moved to BUILDING, then cancel won.
			if d.Status != deployments.StatusCancelled {
				t.Fatalf("both succeeded but status = %s", d.Status)
			}
		case cancelErr == nil && errors.As(workerErr, &te):
			if d.Status != deployments.StatusCancelled {
				t.Fatalf("cancel won but status = %s", d.Status)
			}
		default:
			t.Fatalf("iteration %d: cancel=%v worker=%v status=%s", i, cancelErr, workerErr, d.Status)
		}
	}
}

func TestCancelEndpoint(t *testing.T) {
	f := setup(t)
	id := f.deploy(f.dev.Token, "development", "abc1234").Str("id")
	path := "/api/v1/deployments/" + id + "/cancel"

	if res := f.api.Do(http.MethodPost, path, f.viewer.Token, nil); res.Status != http.StatusForbidden {
		t.Errorf("viewer cancel: %d", res.Status)
	}
	if res := f.api.Do(http.MethodPost, path, f.outside.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("outsider cancel: %d", res.Status)
	}
	res := f.api.Do(http.MethodPost, path, f.dev.Token, nil)
	if res.Status != http.StatusOK || res.Str("status") != "CANCELLED" || res.Str("completed_at") == "" {
		t.Fatalf("cancel: %d %v", res.Status, res.Body)
	}
	if res := f.api.Do(http.MethodPost, path, f.dev.Token, nil); res.ErrCode() != "INVALID_TRANSITION" {
		t.Errorf("double cancel: %d %v", res.Status, res.Body)
	}
}

func TestListDeployments(t *testing.T) {
	f := setup(t)
	svc := deployments.NewService(f.api.Pool)
	var ids []string
	for i := range 5 {
		id := f.deploy(f.dev.Token, "development", fmt.Sprintf("%07d", i)).Str("id")
		ids = append(ids, id)
		svc.Transition(t.Context(), id, deployments.StatusFailed, "boom") // free the env
	}
	f.deploy(f.admin.Token, "production", "1234567")

	all := f.api.Do(http.MethodGet, "/api/v1/deployments?project_id="+f.projectID+"&limit=4", f.viewer.Token, nil)
	if len(all.Data()) != 4 || all.Data()[0]["environment"] != "production" {
		t.Fatalf("newest first: %v", all.Data())
	}
	next, _ := all.Body["next_cursor"].(string)
	rest := f.api.Do(http.MethodGet, "/api/v1/deployments?project_id="+f.projectID+"&limit=4&cursor="+next, f.viewer.Token, nil)
	if len(rest.Data()) != 2 || rest.Body["next_cursor"] != nil || rest.Data()[1]["id"] != ids[0] {
		t.Errorf("second page: %v", rest.Data())
	}

	failed := f.api.Do(http.MethodGet, "/api/v1/deployments?status=FAILED&environment=development", f.viewer.Token, nil)
	if len(failed.Data()) != 5 {
		t.Errorf("status filter: %d", len(failed.Data()))
	}
	if res := f.api.Do(http.MethodGet, "/api/v1/deployments", f.outside.Token, nil); len(res.Data()) != 0 {
		t.Errorf("outsider sees %d deployments", len(res.Data()))
	}
	for _, q := range []string{"status=DONE", "environment=qa", "project_id=x", "cursor=zz"} {
		if res := f.api.Do(http.MethodGet, "/api/v1/deployments?"+q, f.viewer.Token, nil); res.Status != http.StatusUnprocessableEntity {
			t.Errorf("?%s: %d", q, res.Status)
		}
	}
}

func TestProjectDeleteBlockedByActiveDeployment(t *testing.T) {
	f := setup(t)
	id := f.deploy(f.dev.Token, "development", "abc1234").Str("id")
	if res := f.api.Do(http.MethodDelete, "/api/v1/projects/"+f.projectID, f.admin.Token, nil); res.ErrCode() != "DEPLOYMENT_IN_PROGRESS" {
		t.Fatalf("delete with active deployment: %d %v", res.Status, res.Body)
	}
	f.api.Do(http.MethodPost, "/api/v1/deployments/"+id+"/cancel", f.dev.Token, nil)
	if res := f.api.Do(http.MethodDelete, "/api/v1/projects/"+f.projectID, f.admin.Token, nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete after cancel: %d", res.Status)
	}
	// History of a deleted project is no longer listed or retrievable.
	if res := f.api.Do(http.MethodGet, "/api/v1/deployments/"+id, f.admin.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("get after project delete: %d", res.Status)
	}
}

func TestAuditTrail(t *testing.T) {
	f := setup(t)
	id := f.deploy(f.dev.Token, "development", "abc1234").Str("id")
	f.api.Do(http.MethodPost, "/api/v1/deployments/"+id+"/cancel", f.dev.Token, nil)
	// A failed action (duplicate project name) must leave no audit entry.
	f.api.Do(http.MethodPost, "/api/v1/projects", f.admin.Token, map[string]string{
		"organization_id": f.admin.OrgID, "name": "billing-api", "repository_url": "https://github.com/acme/x",
	})

	auditPath := "/api/v1/organizations/" + f.admin.OrgID + "/audit"
	if res := f.api.Do(http.MethodGet, auditPath, f.dev.Token, nil); res.Status != http.StatusForbidden {
		t.Errorf("developer reading audit: %d", res.Status)
	}
	res := f.api.Do(http.MethodGet, auditPath+"?limit=100", f.admin.Token, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("audit: %d %v", res.Status, res.Body)
	}
	var actions []string
	for _, e := range res.Data() {
		actions = append(actions, e["action"].(string))
	}
	// Newest first: every audited mutation from setup and this test, and
	// nothing for the failed duplicate-project request. (apitest.AddMember
	// inserts memberships directly, so it produces no entries.)
	want := []string{
		"deployment.cancelled", "deployment.created",
		"variable.set", "variable.set",
		"environment.created", "environment.created",
		"project.created", "auth.registered",
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Errorf("actions =\n  %v\nwant\n  %v", actions, want)
	}

	cancelled := res.Data()[0]
	if cancelled["actor_email"] != "dev@example.com" || cancelled["resource_id"] != id || cancelled["ip_address"] != "127.0.0.1" {
		t.Errorf("cancel entry: %v", cancelled)
	}
	if md := cancelled["metadata"].(map[string]any); md["request_id"] == nil {
		t.Errorf("request_id missing from metadata: %v", md)
	}
	// Variable values never reach the audit log.
	for _, e := range res.Data() {
		if e["action"] == "variable.set" && strings.Contains(fmt.Sprint(e["metadata"]), "debug") {
			t.Errorf("variable value leaked into audit: %v", e["metadata"])
		}
	}
	filtered := f.api.Do(http.MethodGet, auditPath+"?action=variable.set", f.admin.Token, nil)
	if len(filtered.Data()) != 2 {
		t.Errorf("action filter: %d entries", len(filtered.Data()))
	}
}
