package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/environments"
)

func strp(s string) *string { return &s }

func testJob() deployments.Job {
	return deployments.Job{
		ProjectName: "billing-api",
		Deployment: deployments.Deployment{
			ID: "dep-1", ProjectID: "proj-1", Environment: "staging", CommitSHA: "abc1234",
			Config: deployments.Config{
				Namespace: "billing-api-staging", Replicas: 3, Port: 9000,
				Resources: environments.Resources{CPURequest: "100m", CPULimit: "500m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"},
				Variables: []environments.Variable{
					{Key: "DB_PASSWORD", Secret: true, SecretRef: strp("db-credentials/password")},
					{Key: "LOG_LEVEL", Value: strp("debug")},
				},
			},
		},
	}
}

func TestDeploymentSpec(t *testing.T) {
	d, err := Deployment(testJob(), "clouddeploy/billing-api-proj:abc1234")
	if err != nil {
		t.Fatal(err)
	}
	spec := d.Spec
	if *d.Name != "billing-api" || *d.Namespace != "billing-api-staging" || *spec.Replicas != 3 {
		t.Errorf("identity: %s/%s replicas=%d", *d.Namespace, *d.Name, *spec.Replicas)
	}

	// Zero-downtime rolling update.
	ru := spec.Strategy.RollingUpdate
	if ru.MaxUnavailable.IntValue() != 0 || ru.MaxSurge.IntValue() != 1 || *spec.ProgressDeadlineSeconds != 300 {
		t.Errorf("strategy: maxUnavailable=%v maxSurge=%v", ru.MaxUnavailable, ru.MaxSurge)
	}

	// The selector is immutable, so it must not contain per-release labels;
	// the pod template must carry the release label for rollout tracking.
	if _, ok := spec.Selector.MatchLabels[LabelDeploymentID]; ok {
		t.Error("selector contains the deployment ID (selector is immutable)")
	}
	if spec.Template.Labels[LabelDeploymentID] != "dep-1" {
		t.Error("pod template missing deployment ID label")
	}
	for k, v := range spec.Selector.MatchLabels {
		if spec.Template.Labels[k] != v {
			t.Errorf("pod template does not match selector on %s", k)
		}
	}

	pod := spec.Template.Spec
	if *pod.AutomountServiceAccountToken || *pod.EnableServiceLinks {
		t.Error("service account token or service links enabled")
	}
	if pod.SecurityContext.SeccompProfile.Type == nil || *pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Error("seccomp RuntimeDefault not set")
	}
	c := pod.Containers[0]
	if *c.SecurityContext.AllowPrivilegeEscalation || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Error("container security context too permissive")
	}
	if *c.ImagePullPolicy != corev1.PullIfNotPresent || *c.Ports[0].ContainerPort != 9000 {
		t.Errorf("image pull policy / port: %v %d", *c.ImagePullPolicy, *c.Ports[0].ContainerPort)
	}
	if got := (*c.Resources.Limits)[corev1.ResourceMemory]; got.String() != "256Mi" {
		t.Errorf("memory limit = %s", got.String())
	}
}

func TestEnvVars(t *testing.T) {
	env := map[string]*string{}
	refs := map[string]string{}
	for _, e := range envVars(testJob()) {
		env[*e.Name] = e.Value
		if e.ValueFrom != nil {
			refs[*e.Name] = *e.ValueFrom.SecretKeyRef.Name + "/" + *e.ValueFrom.SecretKeyRef.Key
		}
	}
	// Secrets are references for the kubelet, never values.
	if env["DB_PASSWORD"] != nil || refs["DB_PASSWORD"] != "db-credentials/password" {
		t.Errorf("secret env: value=%v ref=%q", env["DB_PASSWORD"], refs["DB_PASSWORD"])
	}
	if *env["LOG_LEVEL"] != "debug" || *env["PORT"] != "9000" || *env["CLOUDDEPLOY_COMMIT_SHA"] != "abc1234" {
		t.Errorf("plain env: %v", env)
	}

	// A user-defined PORT wins over the injected one (no duplicate keys).
	job := testJob()
	job.Deployment.Config.Variables = append(job.Deployment.Config.Variables, environments.Variable{Key: "PORT", Value: strp("1234")})
	ports := 0
	for _, e := range envVars(job) {
		if *e.Name == "PORT" {
			ports++
			if *e.Value != "1234" {
				t.Errorf("PORT = %s", *e.Value)
			}
		}
	}
	if ports != 1 {
		t.Errorf("PORT defined %d times", ports)
	}
}

func TestProbes(t *testing.T) {
	cfg := testJob().Deployment.Config
	if p := readinessProbe(cfg); p.TCPSocket == nil || p.HTTPGet != nil {
		t.Error("without a health path, readiness must be a TCP check")
	}
	cfg.HealthCheckPath = strp("/healthz")
	if p := readinessProbe(cfg); p.HTTPGet == nil || *p.HTTPGet.Path != "/healthz" {
		t.Error("readiness must use the health path")
	}
	// Liveness never depends on app health (no restart loops on DB outages).
	if p := livenessProbe(); p.TCPSocket == nil || p.HTTPGet != nil {
		t.Error("liveness must be a TCP check")
	}
}

func TestNamespaceAndService(t *testing.T) {
	ns := Namespace(testJob())
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "baseline" || ns.Labels[LabelManagedBy] != ManagedBy || ns.Labels[LabelProjectID] != "proj-1" {
		t.Errorf("namespace labels: %v", ns.Labels)
	}
	svc := Service(testJob())
	if *svc.Spec.Ports[0].Port != 80 || svc.Spec.Ports[0].TargetPort.StrVal != portName || svc.Spec.Selector[LabelInstance] != "billing-api-staging" {
		t.Errorf("service: %+v", svc.Spec)
	}
}

func TestInvalidQuantityRejected(t *testing.T) {
	job := testJob()
	job.Deployment.Config.Resources.CPULimit = "lots"
	if _, err := Deployment(job, "img"); err == nil {
		t.Error("invalid quantity accepted")
	}
}
