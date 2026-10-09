//go:build integration

package projects_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/httpapi/apitest"
)

func createProject(t *testing.T, api *apitest.API, token, orgID, name string) apitest.Response {
	t.Helper()
	return api.Do(http.MethodPost, "/api/v1/projects", token, map[string]string{
		"organization_id": orgID, "name": name, "repository_url": "https://github.com/acme/" + name,
	})
}

func TestProjectCRUD(t *testing.T) {
	api := apitest.New(t)
	u := api.Register("alice@example.com")

	res := createProject(t, api, u.Token, u.OrgID, "billing-api")
	if res.Status != http.StatusCreated || res.Str("default_branch") != "main" || res.Header.Get("ETag") == "" {
		t.Fatalf("create: %d %v", res.Status, res.Body)
	}
	id := res.Str("id")
	if res.Header.Get("Location") != "/api/v1/projects/"+id {
		t.Errorf("Location = %q", res.Header.Get("Location"))
	}

	if res := createProject(t, api, u.Token, u.OrgID, "billing-api"); res.ErrCode() != "PROJECT_NAME_TAKEN" {
		t.Errorf("duplicate name: %d %v", res.Status, res.Body)
	}

	get := api.Do(http.MethodGet, "/api/v1/projects/"+id, u.Token, nil)
	if get.Status != http.StatusOK || get.Str("name") != "billing-api" {
		t.Fatalf("get: %d %v", get.Status, get.Body)
	}

	upd := api.Do(http.MethodPatch, "/api/v1/projects/"+id, u.Token, map[string]string{"default_branch": "develop"})
	if upd.Status != http.StatusOK || upd.Str("default_branch") != "develop" || upd.Str("name") != "billing-api" {
		t.Fatalf("patch: %d %v", upd.Status, upd.Body)
	}

	if res := api.Do(http.MethodDelete, "/api/v1/projects/"+id, u.Token, nil); res.Status != http.StatusNoContent {
		t.Fatalf("delete: %d", res.Status)
	}
	if res := api.Do(http.MethodGet, "/api/v1/projects/"+id, u.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("get after delete: %d", res.Status)
	}
	// Soft delete frees the name.
	if res := createProject(t, api, u.Token, u.OrgID, "billing-api"); res.Status != http.StatusCreated {
		t.Errorf("recreate after delete: %d %v", res.Status, res.Body)
	}
}

func TestProjectRBAC(t *testing.T) {
	api := apitest.New(t)
	admin := api.Register("admin@example.com")
	dev := api.Register("dev@example.com")
	viewer := api.Register("viewer@example.com")
	outsider := api.Register("outsider@example.com")
	api.AddMember(admin.OrgID, dev.ID, "developer")
	api.AddMember(admin.OrgID, viewer.ID, "viewer")

	id := createProject(t, api, dev.Token, admin.OrgID, "web").Str("id")
	if id == "" {
		t.Fatal("developer could not create project")
	}
	path := "/api/v1/projects/" + id
	patch := map[string]string{"default_branch": "develop"}

	cases := []struct {
		who          string
		token        string
		method, path string
		body         any
		want         int
	}{
		{"viewer reads", viewer.Token, http.MethodGet, path, nil, http.StatusOK},
		{"viewer cannot create", viewer.Token, http.MethodPost, "/api/v1/projects", map[string]string{"organization_id": admin.OrgID, "name": "x", "repository_url": "https://g.com/x"}, http.StatusForbidden},
		{"viewer cannot update", viewer.Token, http.MethodPatch, path, patch, http.StatusForbidden},
		{"developer updates", dev.Token, http.MethodPatch, path, patch, http.StatusOK},
		{"developer cannot delete", dev.Token, http.MethodDelete, path, nil, http.StatusForbidden},
		{"outsider cannot see", outsider.Token, http.MethodGet, path, nil, http.StatusNotFound},
		{"outsider cannot update", outsider.Token, http.MethodPatch, path, patch, http.StatusNotFound},
		{"outsider cannot create in org", outsider.Token, http.MethodPost, "/api/v1/projects", map[string]string{"organization_id": admin.OrgID, "name": "x", "repository_url": "https://g.com/x"}, http.StatusUnprocessableEntity},
		{"no token", "", http.MethodGet, path, nil, http.StatusUnauthorized},
		{"admin deletes", admin.Token, http.MethodDelete, path, nil, http.StatusNoContent},
	}
	for _, c := range cases {
		t.Run(c.who, func(t *testing.T) {
			if res := api.Do(c.method, c.path, c.token, c.body); res.Status != c.want {
				t.Errorf("%s %s: %d, want %d (%v)", c.method, c.path, res.Status, c.want, res.Body)
			}
		})
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	api := apitest.New(t)
	u := api.Register("alice@example.com")
	id := createProject(t, api, u.Token, u.OrgID, "api").Str("id")
	path := "/api/v1/projects/" + id

	// Two clients read the same version.
	etag := api.Do(http.MethodGet, path, u.Token, nil).Header.Get("ETag")

	first := api.Do(http.MethodPatch, path, u.Token, map[string]string{"default_branch": "one"}, "If-Match", etag)
	if first.Status != http.StatusOK || first.Header.Get("ETag") == etag {
		t.Fatalf("first write: %d, etag changed=%v", first.Status, first.Header.Get("ETag") != etag)
	}
	// Second client's stale write is rejected instead of silently overwriting.
	second := api.Do(http.MethodPatch, path, u.Token, map[string]string{"default_branch": "two"}, "If-Match", etag)
	if second.Status != http.StatusPreconditionFailed {
		t.Fatalf("stale write: %d, want 412", second.Status)
	}
	// Retrying with the fresh ETag succeeds.
	if res := api.Do(http.MethodPatch, path, u.Token, map[string]string{"default_branch": "two"}, "If-Match", first.Header.Get("ETag")); res.Status != http.StatusOK {
		t.Fatalf("fresh write: %d", res.Status)
	}
}

func TestListPaginationSortingFiltering(t *testing.T) {
	api := apitest.New(t)
	u := api.Register("alice@example.com")
	other := api.Register("bob@example.com")
	for i := range 7 {
		createProject(t, api, u.Token, u.OrgID, fmt.Sprintf("svc-%02d", i))
	}
	createProject(t, api, u.Token, u.OrgID, "web_frontend-x") // invalid name: not created
	createProject(t, api, u.Token, u.OrgID, "web")
	createProject(t, api, other.Token, other.OrgID, "secret-project") // other org

	// Page through by name, 3 at a time; collect everything.
	var names []string
	cursor := ""
	for pages := 0; ; pages++ {
		q := url.Values{"sort": {"name"}, "limit": {"3"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		res := api.Do(http.MethodGet, "/api/v1/projects?"+q.Encode(), u.Token, nil)
		if res.Status != http.StatusOK {
			t.Fatalf("list: %d %v", res.Status, res.Body)
		}
		for _, p := range res.Data() {
			names = append(names, p["name"].(string))
		}
		next, _ := res.Body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
	}
	want := []string{"svc-00", "svc-01", "svc-02", "svc-03", "svc-04", "svc-05", "svc-06", "web"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("names = %v, want %v (no other-org projects, no gaps or dupes)", names, want)
	}

	// Descending sort and prefix filter.
	res := api.Do(http.MethodGet, "/api/v1/projects?sort=-name&q=svc&limit=2", u.Token, nil)
	if d := res.Data(); len(d) != 2 || d[0]["name"] != "svc-06" || d[1]["name"] != "svc-05" {
		t.Errorf("desc+filter: %v", d)
	}
	// LIKE wildcards in q are literal, not patterns.
	if res := api.Do(http.MethodGet, "/api/v1/projects?q=%25", u.Token, nil); len(res.Data()) != 0 {
		t.Errorf("q=%% matched %d projects; wildcard not escaped", len(res.Data()))
	}
	// Filter by an org the user is not in returns nothing (not an error).
	if res := api.Do(http.MethodGet, "/api/v1/projects?organization_id="+other.OrgID, u.Token, nil); len(res.Data()) != 0 {
		t.Errorf("other org leaked: %v", res.Data())
	}

	// Bad parameters are 422, and a cursor cannot be reused with a different sort.
	for _, q := range []string{"limit=0", "limit=101", "sort=password", "cursor=garbage", "organization_id=nope"} {
		if res := api.Do(http.MethodGet, "/api/v1/projects?"+q, u.Token, nil); res.Status != http.StatusUnprocessableEntity {
			t.Errorf("?%s: %d, want 422", q, res.Status)
		}
	}
	firstPage := api.Do(http.MethodGet, "/api/v1/projects?sort=name&limit=1", u.Token, nil)
	nc, _ := firstPage.Body["next_cursor"].(string)
	if res := api.Do(http.MethodGet, "/api/v1/projects?sort=-created_at&cursor="+nc, u.Token, nil); res.Status != http.StatusUnprocessableEntity {
		t.Errorf("cursor reused across sorts: %d", res.Status)
	}
}

func TestCreatedAtCursorIsStable(t *testing.T) {
	api := apitest.New(t)
	u := api.Register("alice@example.com")
	for i := range 5 {
		createProject(t, api, u.Token, u.OrgID, fmt.Sprintf("p%d", i))
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		path := "/api/v1/projects?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		res := api.Do(http.MethodGet, path, u.Token, nil)
		for _, p := range res.Data() {
			if seen[p["id"].(string)] {
				t.Fatalf("duplicate across pages: %v", p["name"])
			}
			seen[p["id"].(string)] = true
		}
		// Insert mid-pagination: a new (newest) project must not shift pages.
		if cursor == "" {
			createProject(t, api, u.Token, u.OrgID, "inserted-later")
		}
		next, _ := res.Body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 {
		t.Errorf("saw %d projects, want the 5 that existed when paging started", len(seen))
	}
}
