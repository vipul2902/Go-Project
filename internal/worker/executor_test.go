package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/environments"
)

func TestDryRunNeverClaimsRealWorkAndHidesSecrets(t *testing.T) {
	plain, ref := "debug", "db/password"
	job := deployments.Job{ProjectName: "web", RepositoryURL: "https://github.com/acme/web",
		Deployment: deployments.Deployment{CommitSHA: "abc1234", Config: deployments.Config{Namespace: "web-dev", Replicas: 2,
			Variables: []environments.Variable{{Key: "LOG_LEVEL", Value: &plain}, {Key: "DB_PASSWORD", Secret: true, SecretRef: &ref}}}}}
	var lines []string
	log := func(_, m string) { lines = append(lines, m) }

	image, err := DryRun{}.Build(context.Background(), job, log)
	if err != nil || image != "web:abc1234" {
		t.Fatalf("build: %q %v", image, err)
	}
	if err := (DryRun{}).Deploy(context.Background(), job, image, log); err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "[dry-run]") {
			t.Errorf("line not marked dry-run: %q", l)
		}
	}
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "debug") || !strings.Contains(all, "<secret db/password>") {
		t.Errorf("variable values must not be logged; secret refs shown as refs:\n%s", all)
	}
	// Cancellation is honoured.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (DryRun{}).Build(ctx, job, log); err == nil {
		t.Error("cancelled build returned no error")
	}
}
