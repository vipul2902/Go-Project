package kube

import "testing"

// Rendering the Deployment for a release (per deployment, not per request).
func BenchmarkRenderDeployment(b *testing.B) {
	job := testJob()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Deployment(job, "img:abc1234"); err != nil {
			b.Fatal(err)
		}
	}
}
