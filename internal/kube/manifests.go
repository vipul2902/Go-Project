// Package kube deploys applications to Kubernetes: it renders the objects
// for a deployment, applies them with server-side apply, and tracks the
// rollout until it succeeds or fails.
package kube

import (
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	acappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	accorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	acmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"

	"github.com/vipul2902/clouddeploy/internal/deployments"
)

// Label keys. app.kubernetes.io/* are the Kubernetes recommended labels.
const (
	LabelName         = "app.kubernetes.io/name"
	LabelInstance     = "app.kubernetes.io/instance"
	LabelManagedBy    = "app.kubernetes.io/managed-by"
	LabelProjectID    = "clouddeploy.dev/project-id"
	LabelEnvironment  = "clouddeploy.dev/environment"
	LabelDeploymentID = "clouddeploy.dev/deployment-id"
	ManagedBy         = "clouddeploy"
	containerName     = "app"
	portName          = "http"
)

// selector identifies an app's pods. It is immutable on a Deployment, so it
// holds only labels that never change across releases.
func selector(job deployments.Job) map[string]string {
	return map[string]string{
		LabelName:     job.ProjectName,
		LabelInstance: job.ProjectName + "-" + job.Deployment.Environment,
	}
}

func commonLabels(job deployments.Job) map[string]string {
	l := selector(job)
	l[LabelManagedBy] = ManagedBy
	l[LabelProjectID] = job.Deployment.ProjectID
	l[LabelEnvironment] = job.Deployment.Environment
	return l
}

// Namespace renders the environment's namespace. Pod Security Admission
// enforces the "baseline" profile (blocks privileged pods, host access) and
// warns/audits against "restricted" so teams see what to tighten.
func Namespace(job deployments.Job) *accorev1.NamespaceApplyConfiguration {
	labels := commonLabels(job)
	delete(labels, LabelName)
	delete(labels, LabelInstance)
	labels["pod-security.kubernetes.io/enforce"] = "baseline"
	labels["pod-security.kubernetes.io/warn"] = "restricted"
	labels["pod-security.kubernetes.io/audit"] = "restricted"
	return accorev1.Namespace(job.Deployment.Config.Namespace).WithLabels(labels)
}

// Deployment renders the app's Deployment from the snapshot taken when the
// deployment was created (not the environment's current config).
func Deployment(job deployments.Job, image string) (*acappsv1.DeploymentApplyConfiguration, error) {
	d, c := job.Deployment, job.Deployment.Config
	resources, err := resourceRequirements(c)
	if err != nil {
		return nil, err
	}
	podLabels := commonLabels(job)
	// Changes on every deployment, so even a redeploy of the same image rolls
	// the pods (and lets the rollout tracker find this release's pods).
	podLabels[LabelDeploymentID] = d.ID

	container := accorev1.Container().
		WithName(containerName).
		WithImage(image).
		// Tags are immutable commit SHAs, so a cached image is always right;
		// locally built images are never pulled from a registry.
		WithImagePullPolicy(corev1.PullIfNotPresent).
		WithPorts(accorev1.ContainerPort().WithName(portName).WithContainerPort(int32(c.Port)).WithProtocol(corev1.ProtocolTCP)).
		WithEnv(envVars(job)...).
		WithResources(resources).
		WithReadinessProbe(readinessProbe(c)).
		WithLivenessProbe(livenessProbe()).
		WithSecurityContext(accorev1.SecurityContext().
			WithAllowPrivilegeEscalation(false).
			WithCapabilities(accorev1.Capabilities().WithDrop("ALL")))

	return acappsv1.Deployment(job.ProjectName, c.Namespace).
		WithLabels(commonLabels(job)).
		WithAnnotations(map[string]string{
			"clouddeploy.dev/deployment-id": d.ID,
			"clouddeploy.dev/commit-sha":    d.CommitSHA,
		}).
		WithSpec(acappsv1.DeploymentSpec().
			WithReplicas(int32(c.Replicas)).
			WithRevisionHistoryLimit(5).
			// Kubernetes marks the rollout failed if it makes no progress for 5m.
			WithProgressDeadlineSeconds(300).
			WithSelector(acmetav1.LabelSelector().WithMatchLabels(selector(job))).
			// Zero-downtime: never remove an old pod before a new one is ready.
			WithStrategy(acappsv1.DeploymentStrategy().
				WithType(appsv1.RollingUpdateDeploymentStrategyType).
				WithRollingUpdate(acappsv1.RollingUpdateDeployment().
					WithMaxUnavailable(intstr.FromInt32(0)).
					WithMaxSurge(intstr.FromInt32(1)))).
			WithTemplate(accorev1.PodTemplateSpec().
				WithLabels(podLabels).
				WithSpec(accorev1.PodSpec().
					// Apps do not talk to the Kubernetes API: no token to steal.
					WithAutomountServiceAccountToken(false).
					// Do not inject docker-link-style env vars for every Service.
					WithEnableServiceLinks(false).
					WithTerminationGracePeriodSeconds(30).
					WithSecurityContext(accorev1.PodSecurityContext().
						WithSeccompProfile(accorev1.SeccompProfile().WithType(corev1.SeccompProfileTypeRuntimeDefault))).
					WithContainers(container)))), nil
}

// Service renders a stable ClusterIP address for the app on port 80.
func Service(job deployments.Job) *accorev1.ServiceApplyConfiguration {
	return accorev1.Service(job.ProjectName, job.Deployment.Config.Namespace).
		WithLabels(commonLabels(job)).
		WithSpec(accorev1.ServiceSpec().
			WithType(corev1.ServiceTypeClusterIP).
			WithSelector(selector(job)).
			WithPorts(accorev1.ServicePort().
				WithName(portName).WithPort(80).WithTargetPort(intstr.FromString(portName)).WithProtocol(corev1.ProtocolTCP)))
}

// envVars builds the container environment. Secret variables become
// secretKeyRef references, resolved by the kubelet from a Secret in the same
// namespace: their values never pass through CloudDeploy.
func envVars(job deployments.Job) []*accorev1.EnvVarApplyConfiguration {
	d, c := job.Deployment, job.Deployment.Config
	vars := []*accorev1.EnvVarApplyConfiguration{
		accorev1.EnvVar().WithName("CLOUDDEPLOY_DEPLOYMENT_ID").WithValue(d.ID),
		accorev1.EnvVar().WithName("CLOUDDEPLOY_COMMIT_SHA").WithValue(d.CommitSHA),
		accorev1.EnvVar().WithName("CLOUDDEPLOY_ENVIRONMENT").WithValue(d.Environment),
	}
	hasPort := false
	for _, v := range c.Variables {
		hasPort = hasPort || v.Key == "PORT"
		switch {
		case v.Secret && v.SecretRef != nil:
			name, key, _ := strings.Cut(*v.SecretRef, "/")
			vars = append(vars, accorev1.EnvVar().WithName(v.Key).WithValueFrom(
				accorev1.EnvVarSource().WithSecretKeyRef(accorev1.SecretKeySelector().WithName(name).WithKey(key))))
		case v.Value != nil:
			vars = append(vars, accorev1.EnvVar().WithName(v.Key).WithValue(*v.Value))
		}
	}
	if !hasPort { // 12-factor: tell the app which port to bind
		vars = append(vars, accorev1.EnvVar().WithName("PORT").WithValue(strconv.Itoa(c.Port)))
	}
	return vars
}

func resourceRequirements(c deployments.Config) (*accorev1.ResourceRequirementsApplyConfiguration, error) {
	var q [4]resource.Quantity
	for i, v := range []string{c.Resources.CPURequest, c.Resources.CPULimit, c.Resources.MemoryRequest, c.Resources.MemoryLimit} {
		parsed, err := resource.ParseQuantity(v)
		if err != nil {
			return nil, fmt.Errorf("invalid resource quantity %q: %w", v, err)
		}
		q[i] = parsed
	}
	return accorev1.ResourceRequirements().
		WithRequests(corev1.ResourceList{corev1.ResourceCPU: q[0], corev1.ResourceMemory: q[2]}).
		WithLimits(corev1.ResourceList{corev1.ResourceCPU: q[1], corev1.ResourceMemory: q[3]}), nil
}

// readinessProbe gates traffic: HTTP on the health path if configured,
// otherwise "is the port accepting connections".
func readinessProbe(c deployments.Config) *accorev1.ProbeApplyConfiguration {
	p := accorev1.Probe().WithInitialDelaySeconds(1).WithPeriodSeconds(3).WithTimeoutSeconds(2).WithFailureThreshold(3)
	if c.HealthCheckPath != nil {
		return p.WithHTTPGet(accorev1.HTTPGetAction().WithPath(*c.HealthCheckPath).WithPort(intstr.FromString(portName)))
	}
	return p.WithTCPSocket(accorev1.TCPSocketAction().WithPort(intstr.FromString(portName)))
}

// livenessProbe restarts a wedged process. It deliberately checks only the
// TCP port, never app health: an app that is unhealthy because its database
// is down must not be restarted in a loop (that would not fix anything).
func livenessProbe() *accorev1.ProbeApplyConfiguration {
	return accorev1.Probe().
		WithTCPSocket(accorev1.TCPSocketAction().WithPort(intstr.FromString(portName))).
		WithInitialDelaySeconds(10).WithPeriodSeconds(10).WithTimeoutSeconds(2).WithFailureThreshold(3)
}
