//go:build integration

package environments_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/httpapi/apitest"
)

type fixture struct {
	api              *apitest.API
	admin, dev, view apitest.User
	projectID        string
	base             string // /api/v1/projects/{id}/environments
}

func setup(t *testing.T) fixture {
	t.Helper()
	api := apitest.New(t)
	f := fixture{api: api, admin: api.Register("admin@example.com"), dev: api.Register("dev@example.com"), view: api.Register("viewer@example.com")}
	api.AddMember(f.admin.OrgID, f.dev.ID, "developer")
	api.AddMember(f.admin.OrgID, f.view.ID, "viewer")
	res := api.Do(http.MethodPost, "/api/v1/projects", f.admin.Token, map[string]string{
		"organization_id": f.admin.OrgID, "name": "billing-api", "repository_url": "https://github.com/acme/billing-api",
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("create project: %d %v", res.Status, res.Body)
	}
	f.projectID = res.Str("id")
	f.base = "/api/v1/projects/" + f.projectID + "/environments"
	return f
}

func TestEnvironmentLifecycle(t *testing.T) {
	f := setup(t)

	res := f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "development"})
	if res.Status != http.StatusCreated || res.Str("namespace") != "billing-api-dev" || res.Str("resources", "memory_limit") != "256Mi" {
		t.Fatalf("create with defaults: %d %v", res.Status, res.Body)
	}
	if res := f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "development"}); res.ErrCode() != "ENVIRONMENT_EXISTS" {
		t.Errorf("duplicate env: %v", res.Body)
	}
	f.api.Do(http.MethodPost, f.base, f.admin.Token, map[string]any{"name": "production", "replicas": 3})
	f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "staging"})

	// Listed in promotion order regardless of creation order.
	list := f.api.Do(http.MethodGet, f.base, f.view.Token, nil)
	var names []string
	for _, e := range list.Data() {
		names = append(names, e["name"].(string))
	}
	if fmt.Sprint(names) != "[development staging production]" {
		t.Errorf("order = %v", names)
	}

	upd := f.api.Do(http.MethodPatch, f.base+"/development", f.dev.Token, map[string]any{
		"replicas":  2,
		"resources": map[string]string{"cpu_request": "250m", "cpu_limit": "1", "memory_request": "128Mi", "memory_limit": "512Mi"},
	})
	if upd.Status != http.StatusOK || upd.Body["replicas"].(float64) != 2 || upd.Str("resources", "cpu_limit") != "1" {
		t.Fatalf("update: %d %v", upd.Status, upd.Body)
	}

	bad := f.api.Do(http.MethodPatch, f.base+"/development", f.dev.Token, map[string]any{
		"replicas":  50,
		"resources": map[string]string{"cpu_request": "2", "cpu_limit": "1", "memory_request": "1Gi", "memory_limit": "9Gi"},
	})
	details, _ := bad.Body["error"].(map[string]any)["details"].(map[string]any)
	for _, field := range []string{"replicas", "resources.cpu_request", "resources.memory_limit"} {
		if _, ok := details[field]; !ok {
			t.Errorf("missing validation error for %s: %v", field, details)
		}
	}

	if res := f.api.Do(http.MethodGet, f.base+"/qa", f.view.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("unknown env name: %d", res.Status)
	}
	if res := f.api.Do(http.MethodDelete, f.base+"/staging", f.admin.Token, nil); res.Status != http.StatusNoContent {
		t.Errorf("delete: %d", res.Status)
	}
}

func TestProductionRequiresAdmin(t *testing.T) {
	f := setup(t)

	if res := f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "production"}); res.Status != http.StatusForbidden {
		t.Fatalf("developer creating production: %d", res.Status)
	}
	f.api.Do(http.MethodPost, f.base, f.admin.Token, map[string]any{"name": "production"})
	f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "staging"})

	cases := []struct {
		name         string
		token        string
		method, path string
		body         any
		want         int
	}{
		{"dev edits staging", f.dev.Token, http.MethodPatch, f.base + "/staging", map[string]any{"replicas": 2}, http.StatusOK},
		{"dev cannot edit prod", f.dev.Token, http.MethodPatch, f.base + "/production", map[string]any{"replicas": 2}, http.StatusForbidden},
		{"dev cannot set prod var", f.dev.Token, http.MethodPut, f.base + "/production/variables/LOG_LEVEL", map[string]string{"value": "debug"}, http.StatusForbidden},
		{"dev reads prod", f.dev.Token, http.MethodGet, f.base + "/production", nil, http.StatusOK},
		{"dev cannot delete staging", f.dev.Token, http.MethodDelete, f.base + "/staging", nil, http.StatusForbidden},
		{"viewer cannot edit staging", f.view.Token, http.MethodPatch, f.base + "/staging", map[string]any{"replicas": 1}, http.StatusForbidden},
		{"viewer reads vars", f.view.Token, http.MethodGet, f.base + "/staging/variables", nil, http.StatusOK},
		{"admin edits prod", f.admin.Token, http.MethodPatch, f.base + "/production", map[string]any{"replicas": 3}, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if res := f.api.Do(c.method, c.path, c.token, c.body); res.Status != c.want {
				t.Errorf("%d, want %d (%v)", res.Status, c.want, res.Body)
			}
		})
	}

	outsider := f.api.Register("outsider@example.com")
	if res := f.api.Do(http.MethodGet, f.base, outsider.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("outsider: %d, want 404", res.Status)
	}
}

func TestNamespaceUniqueAndReserved(t *testing.T) {
	f := setup(t)
	other := f.api.Do(http.MethodPost, "/api/v1/projects", f.admin.Token, map[string]string{
		"organization_id": f.admin.OrgID, "name": "web", "repository_url": "https://github.com/acme/web",
	}).Str("id")

	f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "development", "namespace": "shared-ns"})
	res := f.api.Do(http.MethodPost, "/api/v1/projects/"+other+"/environments", f.dev.Token, map[string]any{"name": "development", "namespace": "shared-ns"})
	if res.Status != http.StatusUnprocessableEntity {
		t.Errorf("namespace collision across projects: %d %v", res.Status, res.Body)
	}
	if res := f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "staging", "namespace": "kube-system"}); res.Status != http.StatusUnprocessableEntity {
		t.Errorf("kube-system accepted: %d", res.Status)
	}
}

func TestVariables(t *testing.T) {
	f := setup(t)
	f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "development"})
	vars := f.base + "/development/variables"

	if res := f.api.Do(http.MethodPut, vars+"/LOG_LEVEL", f.dev.Token, map[string]string{"value": "info"}); res.Status != http.StatusCreated {
		t.Fatalf("create var: %d %v", res.Status, res.Body)
	}
	if res := f.api.Do(http.MethodPut, vars+"/LOG_LEVEL", f.dev.Token, map[string]string{"value": "debug"}); res.Status != http.StatusOK || res.Str("value") != "debug" {
		t.Fatalf("replace var: %d %v", res.Status, res.Body)
	}

	// The guardrail: credentials cannot be stored as plain values...
	res := f.api.Do(http.MethodPut, vars+"/DB_PASSWORD", f.dev.Token, map[string]string{"value": "hunter2"})
	if res.Status != http.StatusUnprocessableEntity {
		t.Fatalf("plaintext password accepted: %d", res.Status)
	}
	// ...only as references.
	res = f.api.Do(http.MethodPut, vars+"/DB_PASSWORD", f.dev.Token, map[string]string{"secret_ref": "db-credentials/password"})
	if res.Status != http.StatusCreated || res.Body["secret"] != true || res.Body["value"] != nil {
		t.Fatalf("secret ref: %d %v", res.Status, res.Body)
	}

	list := f.api.Do(http.MethodGet, vars, f.view.Token, nil).Data()
	if len(list) != 2 || list[0]["key"] != "DB_PASSWORD" || list[0]["value"] != nil || list[0]["secret_ref"] != "db-credentials/password" {
		t.Errorf("list: %v", list)
	}

	if res := f.api.Do(http.MethodPut, vars+"/lower", f.dev.Token, map[string]string{"value": "x"}); res.Status != http.StatusUnprocessableEntity {
		t.Errorf("bad key: %d", res.Status)
	}
	if res := f.api.Do(http.MethodDelete, vars+"/LOG_LEVEL", f.dev.Token, nil); res.Status != http.StatusNoContent {
		t.Errorf("delete var: %d", res.Status)
	}
	if res := f.api.Do(http.MethodDelete, vars+"/LOG_LEVEL", f.dev.Token, nil); res.ErrCode() != "VARIABLE_NOT_FOUND" {
		t.Errorf("delete missing var: %v", res.Body)
	}
}

func TestVariableCapUnderConcurrency(t *testing.T) {
	f := setup(t)
	envID := f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "development"}).Str("id")
	// Fill to 98 directly, then race 10 concurrent creates for the last 2 slots.
	if _, err := f.api.Pool.Exec(t.Context(), `
		INSERT INTO environment_variables (environment_id, key, value)
		SELECT $1, 'VAR_' || g, 'x' FROM generate_series(1, 98) g`, envID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- f.api.Do(http.MethodPut, fmt.Sprintf("%s/development/variables/RACE_%d", f.base, i), f.dev.Token, map[string]string{"value": "x"}).Status
		}()
	}
	wg.Wait()
	close(statuses)
	created, rejected := 0, 0
	for s := range statuses {
		switch s {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			rejected++
		default:
			t.Errorf("unexpected status %d", s)
		}
	}
	var total int
	_ = f.api.Pool.QueryRow(t.Context(), `SELECT count(*) FROM environment_variables WHERE environment_id = $1`, envID).Scan(&total)
	if created != 2 || rejected != 8 || total != 100 {
		t.Fatalf("created=%d rejected=%d total=%d; want 2, 8, 100", created, rejected, total)
	}
}

func TestEnvironmentWithDeploymentsCannotBeDeleted(t *testing.T) {
	f := setup(t)
	envID := f.api.Do(http.MethodPost, f.base, f.dev.Token, map[string]any{"name": "development"}).Str("id")
	if _, err := f.api.Pool.Exec(t.Context(),
		`INSERT INTO deployments (project_id, environment_id, commit_sha) VALUES ($1, $2, 'abc1234')`, f.projectID, envID); err != nil {
		t.Fatal(err)
	}
	if res := f.api.Do(http.MethodDelete, f.base+"/development", f.admin.Token, nil); res.ErrCode() != "ENVIRONMENT_HAS_DEPLOYMENTS" {
		t.Errorf("delete with history: %d %v", res.Status, res.Body)
	}
}
