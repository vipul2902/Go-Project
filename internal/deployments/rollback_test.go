package deployments

import (
	"strings"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/environments"
)

func sp(s string) *string { return &s }

func TestConfigDifferences(t *testing.T) {
	res := environments.Resources{CPURequest: "100m", CPULimit: "500m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}
	snap := Config{Replicas: 2, Resources: res, Port: 8080, Variables: []environments.Variable{
		{Key: "LOG_LEVEL", Value: sp("info")},
		{Key: "DB_PASSWORD", Secret: true, SecretRef: sp("db/password")},
		{Key: "OLD_FLAG", Value: sp("on")},
	}}
	env := environments.Environment{Replicas: 2, Resources: res, Port: 8080}

	if d := ConfigDifferences(env, snap, snap.Variables); len(d) != 0 {
		t.Fatalf("identical config reported differences: %v", d)
	}

	env.Replicas = 5
	env.Resources.MemoryLimit = "1Gi"
	current := []environments.Variable{
		{Key: "LOG_LEVEL", Value: sp("debug")},                              // changed value
		{Key: "DB_PASSWORD", Secret: true, SecretRef: sp("db-v2/password")}, // changed reference
		{Key: "NEW_FEATURE", Value: sp("true")},                             // added
	} // OLD_FLAG removed
	got := strings.Join(ConfigDifferences(env, snap, current), "\n")
	for _, want := range []string{
		"replicas: environment has 5, this release uses 2",
		"resources:",
		"added since this release: [NEW_FEATURE]",
		"removed since this release: [OLD_FLAG]",
		"changed since this release: [DB_PASSWORD LOG_LEVEL]",
		"next regular deployment",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Values never appear, only keys.
	for _, secretish := range []string{"debug", "info", "db-v2", "true"} {
		if strings.Contains(got, secretish) {
			t.Errorf("value %q leaked into warnings:\n%s", secretish, got)
		}
	}
}
