package environments

import (
	"strings"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/auth"
)

func TestParseCPU(t *testing.T) {
	good := map[string]int64{"100m": 100, "10m": 10, "0.5": 500, "1": 1000, "2.25": 2250, "4": 4000}
	for q, want := range good {
		if got, err := ParseCPU(q); err != nil || got != want {
			t.Errorf("ParseCPU(%q) = %d, %v; want %d", q, got, err, want)
		}
	}
	for _, q := range []string{"", "5m", "4001m", "8", "1.2345", "abc", "-1", "100M", "1e3"} {
		if _, err := ParseCPU(q); err == nil {
			t.Errorf("ParseCPU(%q) accepted", q)
		}
	}
}

func TestParseMemory(t *testing.T) {
	good := map[string]int64{"64Mi": 64 << 20, "1Gi": 1 << 30, "8Gi": 8 << 30, "100M": 100e6, "16777216": 16 << 20}
	for q, want := range good {
		if got, err := ParseMemory(q); err != nil || got != want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d", q, got, err, want)
		}
	}
	for _, q := range []string{"", "1Mi", "9Gi", "1Ti", "1.5Gi", "lots", "64mi"} {
		if _, err := ParseMemory(q); err == nil {
			t.Errorf("ParseMemory(%q) accepted", q)
		}
	}
}

func TestResourcesRequestMustNotExceedLimit(t *testing.T) {
	errs := map[string]string{}
	Resources{CPURequest: "1", CPULimit: "500m", MemoryRequest: "1Gi", MemoryLimit: "256Mi"}.validate(errs)
	if errs["resources.cpu_request"] == "" || errs["resources.memory_request"] == "" {
		t.Errorf("request > limit accepted: %v", errs)
	}
	errs = map[string]string{}
	DefaultResources.validate(errs)
	if len(errs) != 0 {
		t.Errorf("defaults invalid: %v", errs)
	}
}

func TestCreateInputDefaultsAndNamespace(t *testing.T) {
	in := CreateInput{Name: Production}
	if errs := in.normalize("billing-api"); len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if in.Namespace != "billing-api-prod" || *in.Replicas != 1 || in.Resources.CPULimit != "500m" {
		t.Errorf("defaults not applied: %+v", in)
	}

	long := strings.Repeat("a", 63)
	if ns := DefaultNamespace(long, Staging); len(ns) > 63 || !strings.HasSuffix(ns, "-staging") {
		t.Errorf("namespace not truncated: %q (%d)", ns, len(ns))
	}

	for ns, want := range map[string]bool{"kube-system": false, "kube-anything": false, "default": false, "clouddeploy": false, "Bad_NS": false, "team-a-dev": true} {
		in := CreateInput{Name: Development, Namespace: ns}
		_, hasErr := in.normalize("x")["namespace"]
		if hasErr == want {
			t.Errorf("namespace %q: error=%v, want valid=%v", ns, hasErr, want)
		}
	}
	if _, ok := (&CreateInput{Name: "qa"}).normalize("x")["name"]; !ok {
		t.Error("unknown environment name accepted")
	}
}

func TestRequiredRole(t *testing.T) {
	if RequiredRole(Production) != auth.RoleAdmin || RequiredRole(Staging) != auth.RoleDeveloper || RequiredRole(Development) != auth.RoleDeveloper {
		t.Error("unexpected role requirements")
	}
}

func ptr(s string) *string { return &s }

func TestValidateVariable(t *testing.T) {
	ok := []struct {
		key string
		in  VariableInput
	}{
		{"LOG_LEVEL", VariableInput{Value: ptr("debug")}},
		{"DATABASE_PASSWORD", VariableInput{SecretRef: ptr("db-credentials/password")}},
		{"STRIPE_API_KEY", VariableInput{SecretRef: ptr("stripe/api.key")}},
		{"EMPTY", VariableInput{Value: ptr("")}},
	}
	for _, c := range ok {
		if errs := validateVariable(c.key, c.in); len(errs) != 0 {
			t.Errorf("%s rejected: %v", c.key, errs)
		}
	}

	bad := []struct {
		name, key string
		in        VariableInput
		field     string
	}{
		{"lowercase key", "log_level", VariableInput{Value: ptr("x")}, "key"},
		{"digit first", "1KEY", VariableInput{Value: ptr("x")}, "key"},
		{"reserved prefix", "KUBERNETES_SERVICE_HOST", VariableInput{Value: ptr("x")}, "key"},
		{"both set", "A", VariableInput{Value: ptr("x"), SecretRef: ptr("s/k")}, "body"},
		{"neither set", "A", VariableInput{}, "body"},
		{"plaintext password", "DB_PASSWORD", VariableInput{Value: ptr("hunter2")}, "value"},
		{"plaintext token", "GITHUB_TOKEN", VariableInput{Value: ptr("ghp_x")}, "value"},
		{"plaintext api key", "OPENAI_APIKEY", VariableInput{Value: ptr("sk-x")}, "value"},
		{"cross-namespace ref", "A", VariableInput{SecretRef: ptr("other-ns/secret/key")}, "secret_ref"},
		{"ref without key", "A", VariableInput{SecretRef: ptr("db-credentials")}, "secret_ref"},
		{"oversized value", "BIG", VariableInput{Value: ptr(strings.Repeat("x", MaxVariableValueLen+1))}, "value"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := validateVariable(c.key, c.in)[c.field]; !ok {
				t.Errorf("expected error on %s", c.field)
			}
		})
	}
}

func TestRuntimeValidation(t *testing.T) {
	in := CreateInput{Name: Development}
	if errs := in.normalize("api"); len(errs) != 0 || *in.Port != 8080 || in.HealthCheckPath != nil {
		t.Fatalf("defaults: %v %+v", errs, in)
	}
	for _, p := range []string{"healthz", "/a?b=c", "/has space", "/" + strings.Repeat("a", 200)} {
		in := CreateInput{Name: Development, HealthCheckPath: &p}
		if _, ok := in.normalize("api")["health_check_path"]; !ok {
			t.Errorf("health path %q accepted", p)
		}
	}
	for _, port := range []int{0, 65536} {
		in := CreateInput{Name: Development, Port: &port}
		if _, ok := in.normalize("api")["port"]; !ok {
			t.Errorf("port %d accepted", port)
		}
	}
	empty := ""
	if errs := (UpdateInput{HealthCheckPath: &empty}).validate(); len(errs) != 0 {
		t.Errorf("clearing the health path rejected: %v", errs)
	}
}
