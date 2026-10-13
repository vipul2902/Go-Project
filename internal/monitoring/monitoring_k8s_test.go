//go:build k8s

// End-to-end monitoring tests: real API (production wiring), real Postgres,
// real cluster. Run with make test-k8s.
package monitoring_test

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/httpapi/apitest"
	"github.com/vipul2902/clouddeploy/internal/kube"
)

const tickerImage = "cdtest/monitor-ticker:1"

func buildTicker(t *testing.T) {
	t.Helper()
	cmd := exec.Command("docker", "build", "-q", "-t", tickerImage, "-")
	cmd.Stdin = strings.NewReader("FROM rancher/mirrored-library-busybox:1.37.0\nRUN mkdir -p /www && echo ok > /www/healthz\n" +
		"CMD [\"sh\",\"-c\",\"httpd -p 8080 -h /www; i=0; while true; do echo tick $i; i=$((i+1)); sleep 1; done\"]\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
}

func TestMonitoringEndToEnd(t *testing.T) {
	buildTicker(t)
	cs, err := kube.NewClientset(os.Getenv("KUBE_CONTEXT"))
	if err != nil {
		t.Fatal(err)
	}
	api := apitest.New(t, apitest.WithKube(cs))
	admin := api.Register("admin@example.com")
	viewer := api.Register("viewer@example.com")
	outsider := api.Register("outsider@example.com")
	api.AddMember(admin.OrgID, viewer.ID, "viewer")

	b := make([]byte, 3)
	_, _ = rand.Read(b)
	ns := "cdmon-" + hex.EncodeToString(b)
	t.Cleanup(func() { _ = cs.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{}) })

	pid := api.Do(http.MethodPost, "/api/v1/projects", admin.Token, map[string]string{
		"organization_id": admin.OrgID, "name": "ticker", "repository_url": "https://github.com/acme/ticker",
	}).Str("id")
	env := "/api/v1/projects/" + pid + "/environments/development"
	api.Do(http.MethodPost, "/api/v1/projects/"+pid+"/environments", admin.Token, map[string]any{
		"name": "development", "namespace": ns, "replicas": 1, "health_check_path": "/healthz",
		"resources": map[string]string{"cpu_request": "10m", "cpu_limit": "100m", "memory_request": "16Mi", "memory_limit": "32Mi"},
	})
	if res := api.Do(http.MethodGet, env+"/health", viewer.Token, nil); res.Str("status") != "not_deployed" {
		t.Fatalf("before deploy: %v", res.Body)
	}

	// Deploy for real: a deployment record, rolled out by the Kubernetes deployer.
	svc := deployments.NewService(api.Pool)
	id := api.Do(http.MethodPost, "/api/v1/deployments", admin.Token, map[string]string{
		"project_id": pid, "environment": "development", "commit_sha": "abc1234",
	}).Str("id")
	job, err := svc.Claim(t.Context(), "test", time.Minute)
	if err != nil || job == nil || job.Deployment.ID != id {
		t.Fatalf("claim: %v %v", job, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := kube.NewDeployer(cs).Deploy(ctx, *job, tickerImage, func(string, string) {}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	_ = svc.SetImage(t.Context(), id, tickerImage)
	_, _ = svc.Transition(t.Context(), id, deployments.StatusDeploying, "")
	if _, err := svc.Complete(t.Context(), id, deployments.StatusRunning, ""); err != nil {
		t.Fatal(err)
	}

	health := api.Do(http.MethodGet, env+"/health", viewer.Token, nil)
	if health.Str("status") != "healthy" || health.Body["drift"] != false || health.Str("live_deployment_id") != id {
		t.Fatalf("healthy: %v", health.Body)
	}

	// Out-of-band change is reported as drift.
	if _, err := cs.AppsV1().Deployments(ns).UpdateScale(t.Context(), "ticker", scaleTo(t, cs, ns, 2), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	drift := api.Do(http.MethodGet, env+"/health", viewer.Token, nil)
	if drift.Body["drift"] != true || !strings.Contains(strings.Join(toStrings(drift.Body["drift_reasons"]), " "), "replicas: cluster wants 2, deployed with 1") {
		t.Errorf("drift not reported: %v", drift.Body["drift_reasons"])
	}

	// Logs: developers+ only; outsiders cannot see the environment at all.
	if res := api.Do(http.MethodGet, env+"/logs", viewer.Token, nil); res.Status != http.StatusForbidden {
		t.Errorf("viewer logs: %d", res.Status)
	}
	if res := api.Do(http.MethodGet, env+"/health", outsider.Token, nil); res.Status != http.StatusNotFound {
		t.Errorf("outsider health: %d", res.Status)
	}
	time.Sleep(2 * time.Second)
	logs := api.Do(http.MethodGet, env+"/logs?tail=5", admin.Token, nil)
	if lines := logs.Data(); len(lines) == 0 || !strings.HasPrefix(lines[0]["line"].(string), "tick ") {
		t.Errorf("logs: %v", logs.Body)
	}

	// Live follow over SSE receives new lines as the app prints them.
	req, _ := http.NewRequest(http.MethodGet, api.URL+env+"/logs?follow=true&tail=1", nil)
	req.Header.Set("Authorization", "Bearer "+admin.Token)
	fctx, fcancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer fcancel()
	resp, err := http.DefaultClient.Do(req.WithContext(fctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == "event: log" {
			events++
		}
	}
	if events < 3 {
		t.Errorf("followed only %d log events in 4s", events)
	}
}

func scaleTo(t *testing.T, cs kubernetes.Interface, ns string, n int32) *autoscalingv1.Scale {
	t.Helper()
	s, err := cs.AppsV1().Deployments(ns).GetScale(context.Background(), "ticker", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s.Spec.Replicas = n
	return s
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
