//go:build integration

package worker_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vipul2902/clouddeploy/internal/deployments"
)

// rbFixture is one project with a development environment, deployed by a
// running worker whose deploy step can be told to fail.
type rbFixture struct {
	fixture
	projectID string
	exec      *testExecutor
	failSHA   map[string]bool // commits whose deploy step fails
}

func setupRollback(t *testing.T) *rbFixture {
	t.Helper()
	f := &rbFixture{fixture: setup(t), exec: newExec(), failSHA: map[string]bool{}}
	f.exec.deploy = func(_ context.Context, j deployments.Job) error {
		if f.failSHA[j.Deployment.CommitSHA] {
			return errors.New("rollout failed: CrashLoopBackOff")
		}
		return nil
	}
	f.projectID = f.api.Do(http.MethodPost, "/api/v1/projects", f.user.Token, map[string]string{
		"organization_id": f.user.OrgID, "name": "web", "repository_url": "https://github.com/acme/web",
	}).Str("id")
	f.api.Do(http.MethodPost, "/api/v1/projects/"+f.projectID+"/environments", f.user.Token, map[string]any{"name": "development", "replicas": 1})
	f.start(t, fastConfig("w1"), f.exec)
	return f
}

func (f *rbFixture) env() string {
	return "/api/v1/projects/" + f.projectID + "/environments/development"
}

// deployOK deploys sha and waits for the given terminal status.
func (f *rbFixture) deployAndWait(t *testing.T, sha string, want deployments.Status) deployments.Deployment {
	t.Helper()
	res := f.api.Do(http.MethodPost, "/api/v1/deployments", f.user.Token, map[string]string{
		"project_id": f.projectID, "environment": "development", "commit_sha": sha,
	})
	if res.Status != http.StatusAccepted {
		t.Fatalf("deploy %s: %d %v", sha, res.Status, res.Body)
	}
	return f.waitFor(t, res.Str("id"), 5*time.Second, want)
}

func TestManualRollbackRestoresSnapshot(t *testing.T) {
	f := setupRollback(t)
	v1 := f.deployAndWait(t, "aaaaaaa", deployments.StatusRunning)

	// Config changes between releases: v2 runs with 3 replicas and a new var.
	f.api.Do(http.MethodPatch, f.env(), f.user.Token, map[string]any{"replicas": 3})
	f.api.Do(http.MethodPut, f.env()+"/variables/FEATURE_X", f.user.Token, map[string]string{"value": "on"})
	f.deployAndWait(t, "bbbbbbb", deployments.StatusRunning)

	res := f.api.Do(http.MethodPost, "/api/v1/deployments/"+v1.ID+"/rollback", f.user.Token, nil)
	if res.Status != http.StatusAccepted || res.Str("rollback_of") != v1.ID || res.Str("commit_sha") != "aaaaaaa" {
		t.Fatalf("rollback: %d %v", res.Status, res.Body)
	}
	cfg := res.Body["config"].(map[string]any)
	if cfg["replicas"].(float64) != 1 || len(cfg["variables"].([]any)) != 0 {
		t.Errorf("rollback did not restore v1's snapshot: %v", cfg)
	}
	warnings := strings.Join(toStrings(res.Body["warnings"]), " | ")
	if !strings.Contains(warnings, "replicas: environment has 3, this release uses 1") || !strings.Contains(warnings, "FEATURE_X") {
		t.Errorf("warnings = %q", warnings)
	}

	rb := f.waitFor(t, res.Str("id"), 5*time.Second, deployments.StatusRunning, deployments.StatusFailed)
	if rb.Status != deployments.StatusRunning {
		t.Fatalf("rollback deployment: %s %v", rb.Status, rb.ErrorMessage)
	}
	// The environment's own settings were not touched.
	if env := f.api.Do(http.MethodGet, f.env(), f.user.Token, nil); env.Body["replicas"].(float64) != 3 {
		t.Errorf("environment replicas changed to %v", env.Body["replicas"])
	}
	if got := f.auditActions(t, rb.ID); got[0] != "deployment.rollback" {
		t.Errorf("audit = %v", got)
	}
}

func TestRollbackValidation(t *testing.T) {
	f := setupRollback(t)
	v1 := f.deployAndWait(t, "aaaaaaa", deployments.StatusRunning)
	f.failSHA["ccccccc"] = true
	bad := f.deployAndWait(t, "ccccccc", deployments.StatusFailed)
	// The failure itself auto-rolled back to v1; wait for that to finish.
	time.Sleep(500 * time.Millisecond)
	latest, _ := f.svc.LatestSucceeded(t.Context(), v1.EnvironmentID)
	f.waitFor(t, latest.ID, 5*time.Second, deployments.StatusRunning)

	cases := []struct {
		name, path, token string
		want              int
		code              string
	}{
		{"target that failed", "/api/v1/deployments/" + bad.ID + "/rollback", f.user.Token, http.StatusUnprocessableEntity, "INVALID_ROLLBACK_TARGET"},
		{"target is the current release", "/api/v1/deployments/" + latest.ID + "/rollback", f.user.Token, http.StatusConflict, "ALREADY_CURRENT"},
		{"unknown deployment", "/api/v1/deployments/00000000-0000-4000-8000-0000000000aa/rollback", f.user.Token, http.StatusNotFound, "DEPLOYMENT_NOT_FOUND"},
	}
	outsider := f.api.Register("outsider@example.com")
	cases = append(cases, struct {
		name, path, token string
		want              int
		code              string
	}{"outsider", "/api/v1/deployments/" + v1.ID + "/rollback", outsider.Token, http.StatusNotFound, "DEPLOYMENT_NOT_FOUND"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if res := f.api.Do(http.MethodPost, c.path, c.token, nil); res.Status != c.want || res.ErrCode() != c.code {
				t.Errorf("%d %s, want %d %s", res.Status, res.ErrCode(), c.want, c.code)
			}
		})
	}
}

func TestRollbackToPreviousAndIdempotency(t *testing.T) {
	f := setupRollback(t)
	if res := f.api.Do(http.MethodPost, f.env()+"/rollback", f.user.Token, nil); res.ErrCode() != "NO_PREVIOUS_RELEASE" {
		t.Fatalf("no releases yet: %v", res.Body)
	}
	v1 := f.deployAndWait(t, "aaaaaaa", deployments.StatusRunning)
	if res := f.api.Do(http.MethodPost, f.env()+"/rollback", f.user.Token, nil); res.ErrCode() != "NO_PREVIOUS_RELEASE" {
		t.Fatalf("single release: %v", res.Body)
	}
	f.deployAndWait(t, "bbbbbbb", deployments.StatusRunning)

	first := f.api.Do(http.MethodPost, f.env()+"/rollback", f.user.Token, nil, "Idempotency-Key", "undo-1")
	if first.Status != http.StatusAccepted || first.Str("rollback_of") != v1.ID {
		t.Fatalf("rollback to previous: %d %v", first.Status, first.Body)
	}
	retry := f.api.Do(http.MethodPost, f.env()+"/rollback", f.user.Token, nil, "Idempotency-Key", "undo-1")
	if retry.Status != http.StatusOK || retry.Str("id") != first.Str("id") {
		t.Errorf("retry: %d %s", retry.Status, retry.Str("id"))
	}
	f.waitFor(t, first.Str("id"), 5*time.Second, deployments.StatusRunning)
}

// A release fails during rollout; the worker restores the last good one.
func TestAutoRollbackAfterFailedRollout(t *testing.T) {
	f := setupRollback(t)
	v1 := f.deployAndWait(t, "aaaaaaa", deployments.StatusRunning)
	f.failSHA["bbbbbbb"] = true
	v2 := f.deployAndWait(t, "bbbbbbb", deployments.StatusFailed)

	var rb deployments.Deployment
	deadline := time.Now().Add(5 * time.Second)
	for {
		latest, _ := f.svc.LatestSucceeded(t.Context(), v1.EnvironmentID)
		if latest != nil && latest.RollbackOf != nil {
			rb = *latest
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no auto-rollback reached RUNNING")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if *rb.RollbackOf != v1.ID || rb.CommitSHA != "aaaaaaa" || rb.TriggeredBy != nil {
		t.Errorf("auto-rollback: rollback_of=%v sha=%s triggered_by=%v", *rb.RollbackOf, rb.CommitSHA, rb.TriggeredBy)
	}
	logs, _ := f.svc.Logs(t.Context(), v2.ID, 0, 100)
	if !strings.Contains(logLines(logs), "auto-rollback queued: deployment "+rb.ID) {
		t.Errorf("failed deployment's log does not mention the rollback:\n%s", logLines(logs))
	}
	if got := f.auditActions(t, rb.ID); got[0] != "deployment.auto_rollback" {
		t.Errorf("audit = %v", got)
	}
	// Retrying the auto-rollback (e.g. a worker retry) does not queue another.
	again, _, err := f.svc.AutoRollback(t.Context(), v2.ID)
	if err != nil || again == nil || again.ID != rb.ID {
		t.Errorf("repeat AutoRollback = %v, %v; want the same deployment %s", again, err, rb.ID)
	}
}

func TestNoAutoRollbackWhen(t *testing.T) {
	t.Run("the build failed (nothing was applied)", func(t *testing.T) {
		f := setupRollback(t)
		f.deployAndWait(t, "aaaaaaa", deployments.StatusRunning)
		f.exec.build = func(context.Context, deployments.Job) (string, error) { return "", errors.New("compile error") }
		bad := f.deployAndWait(t, "bbbbbbb", deployments.StatusFailed)
		assertNoRollbackAfter(t, f, bad)
	})
	t.Run("the environment disabled it", func(t *testing.T) {
		f := setupRollback(t)
		f.api.Do(http.MethodPatch, f.env(), f.user.Token, map[string]any{"auto_rollback": false})
		f.deployAndWait(t, "aaaaaaa", deployments.StatusRunning)
		f.failSHA["bbbbbbb"] = true
		bad := f.deployAndWait(t, "bbbbbbb", deployments.StatusFailed)
		assertNoRollbackAfter(t, f, bad)
		logs, _ := f.svc.Logs(t.Context(), bad.ID, 0, 100)
		if !strings.Contains(logLines(logs), "auto-rollback skipped: auto-rollback is disabled") {
			t.Errorf("log:\n%s", logLines(logs))
		}
	})
	t.Run("there was never a good release", func(t *testing.T) {
		f := setupRollback(t)
		f.failSHA["aaaaaaa"] = true
		bad := f.deployAndWait(t, "aaaaaaa", deployments.StatusFailed)
		assertNoRollbackAfter(t, f, bad)
	})
	t.Run("the failing deployment was itself a rollback (no loops)", func(t *testing.T) {
		f := setupRollback(t)
		v1 := f.deployAndWait(t, "aaaaaaa", deployments.StatusRunning)
		f.deployAndWait(t, "bbbbbbb", deployments.StatusRunning)
		f.failSHA["aaaaaaa"] = true // restoring v1 now fails too
		res := f.api.Do(http.MethodPost, "/api/v1/deployments/"+v1.ID+"/rollback", f.user.Token, nil)
		rb := f.waitFor(t, res.Str("id"), 5*time.Second, deployments.StatusFailed)
		assertNoRollbackAfter(t, f, rb)
	})
}

func assertNoRollbackAfter(t *testing.T, f *rbFixture, failed deployments.Deployment) {
	t.Helper()
	time.Sleep(400 * time.Millisecond) // give a (wrong) rollback time to appear
	var n int
	_ = f.api.Pool.QueryRow(t.Context(), `SELECT count(*) FROM deployments WHERE created_at > $1`, failed.CreatedAt).Scan(&n)
	if n != 0 {
		t.Errorf("%d deployment(s) created after %s; want no auto-rollback", n, failed.ID)
	}
}

func logLines(ls []deployments.LogLine) string {
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(l.Message + "\n")
	}
	return b.String()
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
