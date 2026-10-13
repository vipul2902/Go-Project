package slug

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Make must always produce a valid slug, and Valid must agree with
// Kubernetes' DNS-1123 label rules (project and namespace names).
func FuzzMakeAlwaysValid(f *testing.F) {
	for _, s := range []string{"Acme Inc", "", "---", "Ünïcödé", "a_b.c d"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if s := Make(name); !Valid(s) {
			t.Fatalf("Make(%q) = %q is not a valid slug", name, s)
		}
	})
}

func FuzzValidMatchesKubernetes(f *testing.F) {
	for _, s := range []string{"api", "-api", "a", "my-api-2", "A"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ours := Valid(s)
		theirs := len(validation.IsDNS1123Label(s)) == 0
		if ours != theirs {
			t.Fatalf("Valid(%q)=%v, Kubernetes says %v", s, ours, theirs)
		}
	})
}
