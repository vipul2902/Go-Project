package environments

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Differential fuzzing: whatever our validator accepts, Kubernetes itself
// must accept with the same value. Otherwise users get a confusing failure
// at deploy time instead of a clear validation error at the API.
func FuzzParseCPUMatchesKubernetes(f *testing.F) {
	for _, s := range []string{"100m", "0.5", "1", "2.25", "4000m", "1e3", "0.0001", "010m"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		millis, err := ParseCPU(s)
		if err != nil {
			return
		}
		q, kerr := resource.ParseQuantity(s)
		if kerr != nil {
			t.Fatalf("we accept %q, Kubernetes rejects it: %v", s, kerr)
		}
		if q.MilliValue() != millis {
			t.Fatalf("%q: we compute %dm, Kubernetes %dm", s, millis, q.MilliValue())
		}
	})
}

func FuzzParseMemoryMatchesKubernetes(f *testing.F) {
	for _, s := range []string{"64Mi", "1Gi", "100M", "16777216", "0Mi", "8Gi"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		bytes, err := ParseMemory(s)
		if err != nil {
			return
		}
		q, kerr := resource.ParseQuantity(s)
		if kerr != nil {
			t.Fatalf("we accept %q, Kubernetes rejects it: %v", s, kerr)
		}
		if q.Value() != bytes {
			t.Fatalf("%q: we compute %d bytes, Kubernetes %d", s, bytes, q.Value())
		}
	})
}

// A secret reference we accept must name a valid Kubernetes Secret and key,
// or the deployment fails later with CreateContainerConfigError.
func FuzzSecretRefMatchesKubernetes(f *testing.F) {
	for _, s := range []string{"db-credentials/password", "a.b/c", "a..b/key", "x/y.z", "-bad/key", "ok/..", "ok/."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		if errs := validateVariable("DB_PASSWORD", VariableInput{SecretRef: &ref}); len(errs) > 0 {
			return
		}
		name, key, _ := strings.Cut(ref, "/")
		if msgs := validation.IsDNS1123Subdomain(name); len(msgs) > 0 {
			t.Fatalf("accepted secret name %q that Kubernetes rejects: %v", name, msgs)
		}
		if msgs := validation.IsConfigMapKey(key); len(msgs) > 0 {
			t.Fatalf("accepted secret key %q that Kubernetes rejects: %v", key, msgs)
		}
	})
}

// An environment variable key we accept must be a valid container env name.
func FuzzVariableKeyMatchesKubernetes(f *testing.F) {
	for _, s := range []string{"LOG_LEVEL", "_X", "A1", "1A", "a-b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, key string) {
		v := "x"
		if errs := validateVariable(key, VariableInput{Value: &v}); errs["key"] != "" {
			return
		}
		if msgs := validation.IsEnvVarName(key); len(msgs) > 0 {
			t.Fatalf("accepted env var name %q that Kubernetes rejects: %v", key, msgs)
		}
	})
}
