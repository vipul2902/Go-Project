package kube

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func dep(generation, observed int64) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: generation},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: observed},
	}
}

func TestStatusOf(t *testing.T) {
	cases := []struct {
		name string
		d    *appsv1.Deployment
		r    Replicas
		want string
	}{
		{"all good", dep(2, 2), Replicas{Desired: 2, Updated: 2, Available: 2, Total: 2}, StatusHealthy},
		{"scaled to zero", dep(2, 2), Replicas{Desired: 0}, StatusScaledToZero},
		{"new spec not yet observed", dep(3, 2), Replicas{Desired: 2, Updated: 2, Available: 2, Total: 2}, StatusProgressing},
		{"rolling: old pods still around", dep(2, 2), Replicas{Desired: 2, Updated: 2, Available: 2, Total: 3}, StatusProgressing},
		{"rolling: not all updated", dep(2, 2), Replicas{Desired: 2, Updated: 1, Available: 2, Total: 3}, StatusProgressing},
		{"failed rollout, old version still serving", dep(2, 2), Replicas{Desired: 2, Updated: 1, Available: 2, Total: 3}, StatusProgressing},
		{"one replica lost", dep(2, 2), Replicas{Desired: 3, Updated: 3, Available: 2, Total: 3}, StatusDegraded},
		{"nothing available", dep(2, 2), Replicas{Desired: 2, Updated: 2, Available: 0, Total: 2}, StatusDown},
		{"rolling with nothing available", dep(2, 2), Replicas{Desired: 2, Updated: 1, Available: 0, Total: 2}, StatusDown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statusOf(c.d, c.r); got != c.want {
				t.Errorf("statusOf = %s, want %s", got, c.want)
			}
		})
	}
}

func TestParseLogLine(t *testing.T) {
	l, ok := parseLogLine("web-1", "2026-10-13T10:00:00.123456789Z \x1b[31mERROR\x1b[0m db timeout")
	if !ok || l.Pod != "web-1" || l.Line != "ERROR db timeout" || l.Time.Nanosecond() != 123456789 {
		t.Errorf("parsed %+v %v", l, ok)
	}
	for _, bad := range []string{"", "no-timestamp-here", "notatime text"} {
		if _, ok := parseLogLine("p", bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}
