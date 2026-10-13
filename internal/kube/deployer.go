package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/logsafe"
	"github.com/vipul2902/clouddeploy/internal/worker"
)

// FieldManager identifies CloudDeploy as the owner of the fields it applies
// (server-side apply). Fields set by others (e.g. an HPA scaling replicas
// later) are tracked separately, so neither side silently clobbers the other.
const FieldManager = "clouddeploy"

// Deployer implements worker.Deployer against a Kubernetes cluster.
type Deployer struct {
	cs           kubernetes.Interface
	pollInterval time.Duration
}

// NewDeployer returns a Deployer using the given clientset.
func NewDeployer(cs kubernetes.Interface) *Deployer {
	return &Deployer{cs: cs, pollInterval: 2 * time.Second}
}

// ErrNamespaceNotManaged is returned rather than deploying into a namespace
// CloudDeploy did not create (or that belongs to another project).
var ErrNamespaceNotManaged = errors.New("namespace exists and is not managed by this project; refusing to deploy into it")

// Deploy applies the namespace, Deployment and Service, then waits for the
// rollout. Old pods keep serving until new ones are ready (maxUnavailable 0),
// so a failed rollout does not take the running version down.
func (d *Deployer) Deploy(ctx context.Context, job deployments.Job, image string, log worker.LogFunc) error {
	ns := job.Deployment.Config.Namespace
	apply := metav1.ApplyOptions{FieldManager: FieldManager, Force: true}

	if err := d.ensureNamespace(ctx, job, apply); err != nil {
		return err
	}
	dep, err := Deployment(job, image)
	if err != nil {
		return err
	}
	applied, err := d.cs.AppsV1().Deployments(ns).Apply(ctx, dep, apply)
	if err != nil {
		return fmt.Errorf("apply deployment: %w", err)
	}
	if _, err := d.cs.CoreV1().Services(ns).Apply(ctx, Service(job), apply); err != nil {
		return fmt.Errorf("apply service: %w", err)
	}
	log("info", fmt.Sprintf("applied Deployment %s/%s (generation %d) and Service %s:80; waiting for rollout",
		ns, job.ProjectName, applied.Generation, job.ProjectName))
	return d.waitForRollout(ctx, job, log)
}

func (d *Deployer) ensureNamespace(ctx context.Context, job deployments.Job, apply metav1.ApplyOptions) error {
	ns := job.Deployment.Config.Namespace
	existing, err := d.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("get namespace %s: %w", ns, err)
	case existing.Labels[LabelManagedBy] != ManagedBy || existing.Labels[LabelProjectID] != job.Deployment.ProjectID:
		return fmt.Errorf("%s: %w", ns, ErrNamespaceNotManaged)
	}
	if _, err := d.cs.CoreV1().Namespaces().Apply(ctx, Namespace(job), apply); err != nil {
		return fmt.Errorf("apply namespace %s: %w", ns, err)
	}
	return nil
}

// fatalReasons are container states that will not fix themselves; seeing
// one fails the deployment immediately instead of waiting out the 5-minute
// progress deadline.
var fatalReasons = map[string]string{
	"ImagePullBackOff":           "the image could not be pulled",
	"InvalidImageName":           "the image name is invalid",
	"CrashLoopBackOff":           "the container keeps crashing",
	"CreateContainerConfigError": "the container configuration is invalid (often a missing Secret)",
	"CreateContainerError":       "the container could not be created",
}

// RolloutError describes why a rollout failed.
type RolloutError struct{ Reason, Detail string }

func (e *RolloutError) Error() string { return "rollout failed: " + e.Reason + ": " + e.Detail }

// waitForRollout polls until every replica runs this release and is
// available, the rollout exceeds its progress deadline, a pod hits a fatal
// state, or ctx ends (cancel, lease loss, shutdown, deploy timeout).
func (d *Deployer) waitForRollout(ctx context.Context, job deployments.Job, log worker.LogFunc) error {
	ns, name := job.Deployment.Config.Namespace, job.ProjectName
	last := ""
	for {
		dep, err := d.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get deployment: %w", err)
		}
		want := int32(1)
		if dep.Spec.Replicas != nil {
			want = *dep.Spec.Replicas
		}
		st := dep.Status
		// Status describes the spec we applied only once the controller has
		// observed our generation; before that, it is the previous rollout's.
		if st.ObservedGeneration >= dep.Generation {
			for _, c := range st.Conditions {
				if c.Type == "Progressing" && c.Reason == "ProgressDeadlineExceeded" {
					return &RolloutError{Reason: "ProgressDeadlineExceeded", Detail: c.Message}
				}
			}
			if st.UpdatedReplicas == want && st.Replicas == want && st.AvailableReplicas == want {
				log("info", fmt.Sprintf("rollout complete: %d/%d replicas available", want, want))
				return nil
			}
		}
		if err := d.checkPods(ctx, job, log); err != nil {
			return err
		}
		if msg := fmt.Sprintf("rollout in progress: %d/%d updated, %d available, %d total",
			st.UpdatedReplicas, want, st.AvailableReplicas, st.Replicas); msg != last {
			log("info", msg)
			last = msg
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.pollInterval):
		}
	}
}

// checkPods inspects this release's pods for fatal states.
func (d *Deployer) checkPods(ctx context.Context, job deployments.Job, log worker.LogFunc) error {
	ns := job.Deployment.Config.Namespace
	pods, err := d.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: LabelDeploymentID + "=" + job.Deployment.ID,
	})
	if err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting == nil {
				continue
			}
			reason := cs.State.Waiting.Reason
			explanation, fatal := fatalReasons[reason]
			if !fatal {
				continue
			}
			detail := explanation
			if m := strings.TrimSpace(cs.State.Waiting.Message); m != "" {
				detail += ": " + logsafe.Line(m)
			}
			if reason == "CrashLoopBackOff" {
				if t := cs.LastTerminationState.Terminated; t != nil {
					detail += fmt.Sprintf(" (last exit code %d)", t.ExitCode)
				}
				d.logCrashOutput(ctx, ns, pod.Name, log)
			}
			return &RolloutError{Reason: reason, Detail: fmt.Sprintf("pod %s: %s", pod.Name, detail)}
		}
	}
	return nil
}

// logCrashOutput copies the crashed container's last output into the
// pipeline log: the first thing anyone debugging a crash loop needs.
func (d *Deployer) logCrashOutput(ctx context.Context, ns, pod string, log worker.LogFunc) {
	tail := int64(20)
	raw, err := d.cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: containerName, Previous: true, TailLines: &tail, LimitBytes: ptr(int64(16 << 10)),
	}).Do(ctx).Raw()
	if err != nil {
		log("warn", "could not read the crashed container's output: "+err.Error())
		return
	}
	log("error", "last output of the crashed container:")
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line = logsafe.Line(line); line != "" {
			log("error", "  | "+line)
		}
	}
}

func ptr[T any](v T) *T { return &v }
