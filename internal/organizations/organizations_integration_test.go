//go:build integration

package organizations_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/httpapi/apitest"
	"github.com/vipul2902/clouddeploy/internal/organizations"
)

func TestCreateAndListOrganizations(t *testing.T) {
	api := apitest.New(t)
	alice := api.Register("alice@example.com")

	res := api.Do(http.MethodPost, "/api/v1/organizations", alice.Token, map[string]string{"name": "Platform Team", "slug": "platform"})
	if res.Status != http.StatusCreated || res.Str("role") != "admin" || res.Str("slug") != "platform" {
		t.Fatalf("create: %d %v", res.Status, res.Body)
	}
	if res := api.Do(http.MethodPost, "/api/v1/organizations", alice.Token, map[string]string{"name": "Dup", "slug": "platform"}); res.Status != http.StatusConflict {
		t.Errorf("duplicate slug: %d", res.Status)
	}
	if res := api.Do(http.MethodPost, "/api/v1/organizations", alice.Token, map[string]string{"name": "Bad", "slug": "Not Valid"}); res.Status != http.StatusUnprocessableEntity {
		t.Errorf("invalid slug: %d", res.Status)
	}

	list := api.Do(http.MethodGet, "/api/v1/organizations", alice.Token, nil)
	if len(list.Data()) != 2 {
		t.Errorf("want 2 orgs (registration + created), got %d", len(list.Data()))
	}
	if res := api.Do(http.MethodGet, "/api/v1/organizations", "", nil); res.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated list: %d", res.Status)
	}
}

func TestNonMemberGets404(t *testing.T) {
	api := apitest.New(t)
	alice := api.Register("alice@example.com")
	mallory := api.Register("mallory@example.com")

	for _, path := range []string{"/api/v1/organizations/" + alice.OrgID, "/api/v1/organizations/" + alice.OrgID + "/members"} {
		if res := api.Do(http.MethodGet, path, mallory.Token, nil); res.Status != http.StatusNotFound {
			t.Errorf("GET %s as non-member: %d, want 404", path, res.Status)
		}
	}
	// Malformed IDs are 404, not 500.
	if res := api.Do(http.MethodGet, "/api/v1/organizations/not-a-uuid", alice.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("malformed id: %d", res.Status)
	}
}

func TestMemberManagementRBAC(t *testing.T) {
	api := apitest.New(t)
	admin := api.Register("admin@example.com")
	dev := api.Register("dev@example.com")
	viewer := api.Register("viewer@example.com")
	members := "/api/v1/organizations/" + admin.OrgID + "/members"

	// Admin adds members.
	for _, m := range []struct{ email, role string }{{dev.Email, "developer"}, {viewer.Email, "viewer"}} {
		if res := api.Do(http.MethodPost, members, admin.Token, map[string]string{"email": m.email, "role": m.role}); res.Status != http.StatusCreated {
			t.Fatalf("add %s: %d %v", m.email, res.Status, res.Body)
		}
	}
	if res := api.Do(http.MethodPost, members, admin.Token, map[string]string{"email": dev.Email, "role": "viewer"}); res.ErrCode() != "ALREADY_MEMBER" {
		t.Errorf("re-add: %v", res.Body)
	}
	if res := api.Do(http.MethodPost, members, admin.Token, map[string]string{"email": "ghost@example.com", "role": "viewer"}); res.ErrCode() != "USER_NOT_FOUND" {
		t.Errorf("unknown user: %v", res.Body)
	}
	if res := api.Do(http.MethodPost, members, admin.Token, map[string]string{"email": "x@example.com", "role": "owner"}); res.Status != http.StatusUnprocessableEntity {
		t.Errorf("invalid role: %d", res.Status)
	}

	// Viewer can list members but a developer cannot manage them.
	if res := api.Do(http.MethodGet, members, viewer.Token, nil); res.Status != http.StatusOK || len(res.Data()) != 3 {
		t.Errorf("viewer list members: %d, %d members", res.Status, len(res.Data()))
	}
	if res := api.Do(http.MethodPatch, members+"/"+viewer.ID, dev.Token, map[string]string{"role": "admin"}); res.Status != http.StatusForbidden {
		t.Errorf("developer promoting: %d, want 403", res.Status)
	}

	// Admin promotes and removes.
	if res := api.Do(http.MethodPatch, members+"/"+viewer.ID, admin.Token, map[string]string{"role": "developer"}); res.Str("role") != "developer" {
		t.Errorf("promote: %v", res.Body)
	}
	if res := api.Do(http.MethodDelete, members+"/"+viewer.ID, admin.Token, nil); res.Status != http.StatusNoContent {
		t.Errorf("remove: %d", res.Status)
	}
	if res := api.Do(http.MethodGet, "/api/v1/organizations/"+admin.OrgID, viewer.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("removed member still has access: %d", res.Status)
	}
}

func TestLastAdminCannotBeRemovedOrDemoted(t *testing.T) {
	api := apitest.New(t)
	admin := api.Register("admin@example.com")
	path := "/api/v1/organizations/" + admin.OrgID + "/members/" + admin.ID

	if res := api.Do(http.MethodPatch, path, admin.Token, map[string]string{"role": "viewer"}); res.ErrCode() != "LAST_ADMIN" {
		t.Errorf("demote last admin: %d %v", res.Status, res.Body)
	}
	if res := api.Do(http.MethodDelete, path, admin.Token, nil); res.ErrCode() != "LAST_ADMIN" {
		t.Errorf("remove last admin: %d %v", res.Status, res.Body)
	}
}

// Two admins demote each other at the same moment. Without the org-row lock
// both would see "2 admins" and both succeed, leaving zero admins.
func TestConcurrentDemotionKeepsOneAdmin(t *testing.T) {
	api := apitest.New(t)
	a := api.Register("a@example.com")
	b := api.Register("b@example.com")
	api.AddMember(a.OrgID, b.ID, "admin")
	svc := organizations.NewService(api.Pool)

	for range 10 { // repeat to give a race a real chance to show up
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, target := range []string{a.ID, b.ID} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = svc.UpdateRole(context.Background(), a.OrgID, target, auth.RoleViewer)
			}()
		}
		wg.Wait()

		var admins int
		if err := api.Pool.QueryRow(t.Context(),
			`SELECT count(*) FROM organization_members WHERE organization_id = $1 AND role = 'admin'`, a.OrgID).Scan(&admins); err != nil {
			t.Fatal(err)
		}
		if admins != 1 {
			t.Fatalf("admins after concurrent demotion = %d, want 1 (errs: %v)", admins, errs)
		}
		if !errors.Is(errs[0], organizations.ErrLastAdmin) && !errors.Is(errs[1], organizations.ErrLastAdmin) {
			t.Fatalf("expected one ErrLastAdmin, got %v", errs)
		}
		// Reset: make both admins again.
		if _, err := api.Pool.Exec(t.Context(), `UPDATE organization_members SET role = 'admin' WHERE organization_id = $1`, a.OrgID); err != nil {
			t.Fatal(err)
		}
	}
}
