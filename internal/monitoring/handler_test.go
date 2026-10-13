package monitoring

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/environments"
	"github.com/vipul2902/clouddeploy/internal/kube"
)

func TestDriftReasons(t *testing.T) {
	img := "clouddeploy/web-1:abc1234"
	latest := &deployments.Deployment{ID: "dep-1", Image: &img, Config: deployments.Config{Replicas: 2,
		Resources: environments.Resources{CPURequest: "100m", CPULimit: "500m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}}}
	inSync := kube.Health{Status: kube.StatusHealthy, LiveDeploymentID: "dep-1", Replicas: kube.Replicas{Desired: 2},
		// "0.5" CPU == "500m": quantities compare semantically, not as strings.
		Live: kube.LiveSpec{Image: img, CPURequest: "100m", CPULimit: "0.5", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}}

	if r := driftReasons(inSync, latest); len(r) != 0 {
		t.Fatalf("in sync reported drift: %v", r)
	}
	scaled := inSync
	scaled.Replicas.Desired = 3
	edited := inSync
	edited.Live.MemoryLimit = "1Gi"
	edited.Live.Image = "nginx:latest"
	other := inSync
	other.LiveDeploymentID = "dep-2"
	for name, c := range map[string]struct {
		h    kube.Health
		want []string
	}{
		"kubectl scale":            {scaled, []string{"replicas"}},
		"kubectl edit":             {edited, []string{"image", "resources"}},
		"rollout of other release": {other, []string{"release"}},
	} {
		got := strings.Join(driftReasons(c.h, latest), " | ")
		for _, w := range c.want {
			if !strings.Contains(got, w+":") {
				t.Errorf("%s: reasons %q missing %s", name, got, w)
			}
		}
	}
	if r := driftReasons(kube.Health{Status: kube.StatusNotDeployed}, latest); len(r) != 0 {
		t.Errorf("not deployed reported drift: %v", r)
	}
}

func TestParseLogQuery(t *testing.T) {
	cases := map[string]string{
		"tail=0": "tail", "tail=1001": "tail", "since=48h": "since", "since=-1m": "since",
		"since=abc": "since", "follow=true&previous=true": "previous",
	}
	for q, field := range cases {
		if _, _, errs := parseLogQuery(httptest.NewRequest("GET", "/?"+q, nil)); errs[field] == "" {
			t.Errorf("?%s: no error on %s", q, field)
		}
	}
	o, follow, errs := parseLogQuery(httptest.NewRequest("GET", "/?tail=50&since=15m&follow=true", nil))
	if len(errs) != 0 || o.TailLines != 50 || o.Since.Minutes() != 15 || !follow {
		t.Errorf("valid query: %+v %v %v", o, follow, errs)
	}
}
