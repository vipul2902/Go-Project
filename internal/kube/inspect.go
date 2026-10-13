package kube

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/vipul2902/clouddeploy/internal/logsafe"
)

// Inspector reads the live state of deployed apps. It needs only read access
// (deployments get; pods get/list; pods/log get) and never reads Secrets.
type Inspector struct {
	cs  kubernetes.Interface
	now func() time.Time
}

// NewInspector returns an Inspector for the given clientset.
func NewInspector(cs kubernetes.Interface) *Inspector {
	return &Inspector{cs: cs, now: time.Now}
}

// Target identifies an app instance: its namespace, Deployment name and the
// app.kubernetes.io/instance label value shared by its pods. It is always
// derived from database records the caller is authorized for, never from
// request input.
type Target struct {
	Namespace string
	Name      string // Deployment name (project name)
	Instance  string // <project>-<environment>
}

// Health status values.
const (
	StatusHealthy      = "healthy"        // every desired replica runs the current release and is available
	StatusProgressing  = "progressing"    // a rollout is under way
	StatusFailing      = "failing"        // the new release's pods are in a fatal state; the old one may still serve
	StatusDegraded     = "degraded"       // some, but not all, desired replicas are available
	StatusDown         = "down"           // no replica is available
	StatusScaledToZero = "scaled_to_zero" // 0 replicas desired
	StatusNotDeployed  = "not_deployed"   // no Deployment exists yet
)

// Health is the live state of one app instance.
type Health struct {
	Status           string      `json:"status"`
	Replicas         Replicas    `json:"replicas"`
	LiveDeploymentID string      `json:"live_deployment_id,omitempty"` // release in the pod template
	Live             LiveSpec    `json:"live_spec"`
	Pods             []PodStatus `json:"pods"`
}

// LiveSpec is what the cluster is configured to run right now, for drift
// detection against the spec CloudDeploy last applied.
type LiveSpec struct {
	Image         string `json:"image,omitempty"`
	CPULimit      string `json:"cpu_limit,omitempty"`
	MemoryLimit   string `json:"memory_limit,omitempty"`
	CPURequest    string `json:"cpu_request,omitempty"`
	MemoryRequest string `json:"memory_request,omitempty"`
}

// Replicas mirrors the Deployment's replica counters.
type Replicas struct {
	Desired   int32 `json:"desired"`
	Updated   int32 `json:"updated"`
	Ready     int32 `json:"ready"`
	Available int32 `json:"available"`
	Total     int32 `json:"total"`
}

// PodStatus summarizes one pod for humans.
type PodStatus struct {
	Name         string     `json:"name"`
	Phase        string     `json:"phase"`
	Ready        bool       `json:"ready"`
	Restarts     int32      `json:"restarts"`
	DeploymentID string     `json:"deployment_id"`
	Reason       string     `json:"reason,omitempty"` // e.g. CrashLoopBackOff
	Message      string     `json:"message,omitempty"`
	LastExitCode *int32     `json:"last_exit_code,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	AgeSeconds   int64      `json:"age_seconds"`
	Terminating  bool       `json:"terminating"`
}

// Health returns the instance's live state.
func (in *Inspector) Health(ctx context.Context, t Target) (Health, error) {
	dep, err := in.cs.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Health{Status: StatusNotDeployed, Pods: []PodStatus{}}, nil
	}
	if err != nil {
		return Health{}, fmt.Errorf("get deployment: %w", err)
	}
	pods, err := in.cs.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{LabelSelector: LabelInstance + "=" + t.Instance})
	if err != nil {
		return Health{}, fmt.Errorf("list pods: %w", err)
	}
	h := Health{Replicas: replicasOf(dep), LiveDeploymentID: dep.Spec.Template.Labels[LabelDeploymentID], Pods: []PodStatus{}}
	for _, c := range dep.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			h.Live = LiveSpec{Image: c.Image,
				CPURequest: c.Resources.Requests.Cpu().String(), MemoryRequest: c.Resources.Requests.Memory().String(),
				CPULimit: c.Resources.Limits.Cpu().String(), MemoryLimit: c.Resources.Limits.Memory().String()}
		}
	}
	h.Status = statusOf(dep, h.Replicas)
	for _, p := range pods.Items {
		ps := in.podStatus(p)
		h.Pods = append(h.Pods, ps)
		// A rollout whose new pods are crash-looping (etc.) will never finish
		// on its own: say so now instead of "progressing" for 5 minutes.
		if _, fatal := fatalReasons[ps.Reason]; fatal && ps.DeploymentID == h.LiveDeploymentID && h.Status == StatusProgressing {
			h.Status = StatusFailing
		}
	}
	sort.Slice(h.Pods, func(i, j int) bool { return h.Pods[i].Name < h.Pods[j].Name })
	return h, nil
}

func replicasOf(dep *appsv1.Deployment) Replicas {
	desired := int32(1)
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	s := dep.Status
	return Replicas{Desired: desired, Updated: s.UpdatedReplicas, Ready: s.ReadyReplicas, Available: s.AvailableReplicas, Total: s.Replicas}
}

// statusOf derives a single human-facing status from the Deployment.
func statusOf(dep *appsv1.Deployment, r Replicas) string {
	switch {
	case r.Desired == 0:
		return StatusScaledToZero
	case dep.Status.ObservedGeneration < dep.Generation,
		r.Updated < r.Desired, r.Total > r.Desired: // new spec not rolled out / old pods remain
		if r.Available == 0 {
			return StatusDown
		}
		return StatusProgressing
	case r.Available >= r.Desired:
		return StatusHealthy
	case r.Available == 0:
		return StatusDown
	default:
		return StatusDegraded
	}
}

func (in *Inspector) podStatus(p corev1.Pod) PodStatus {
	ps := PodStatus{
		Name: p.Name, Phase: string(p.Status.Phase), DeploymentID: p.Labels[LabelDeploymentID],
		AgeSeconds: int64(in.now().Sub(p.CreationTimestamp.Time).Seconds()), Terminating: p.DeletionTimestamp != nil,
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			ps.Ready = c.Status == corev1.ConditionTrue
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Name != containerName {
			continue
		}
		ps.Restarts = cs.RestartCount
		if w := cs.State.Waiting; w != nil {
			ps.Reason, ps.Message = w.Reason, logsafe.Line(w.Message)
		}
		if r := cs.State.Running; r != nil {
			t := r.StartedAt.Time
			ps.StartedAt = &t
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			code := t.ExitCode
			ps.LastExitCode = &code
		}
	}
	return ps
}

// Log limits keep a single request from pulling unbounded data through the API.
const (
	MaxTailLines   = 1000
	MaxLogPods     = 10
	maxBytesPerPod = 256 << 10
	MaxSince       = 24 * time.Hour
)

// LogOptions select which log lines to return.
type LogOptions struct {
	TailLines int64         // per pod, 1..MaxTailLines
	Since     time.Duration // 0 = no limit
	Pod       string        // optional: a single pod of the instance
	Previous  bool          // the previous (crashed) container instance
}

// LogLine is one line of application output.
type LogLine struct {
	Pod  string    `json:"pod"`
	Time time.Time `json:"time"`
	Line string    `json:"line"`
}

// ErrPodNotFound means the requested pod is not part of the instance.
var ErrPodNotFound = errors.New("pod not found in this environment")

// pods lists the instance's pods, or just the named one if it belongs to it.
func (in *Inspector) pods(ctx context.Context, t Target, only string) ([]corev1.Pod, error) {
	list, err := in.cs.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{LabelSelector: LabelInstance + "=" + t.Instance})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	pods := list.Items
	if only != "" {
		for _, p := range pods {
			if p.Name == only {
				return []corev1.Pod{p}, nil
			}
		}
		return nil, ErrPodNotFound
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].CreationTimestamp.After(pods[j].CreationTimestamp.Time) })
	if len(pods) > MaxLogPods {
		pods = pods[:MaxLogPods] // newest first
	}
	return pods, nil
}

func (in *Inspector) podLogOptions(o LogOptions, follow bool) *corev1.PodLogOptions {
	opts := &corev1.PodLogOptions{Container: containerName, Timestamps: true, Previous: o.Previous, Follow: follow}
	if o.TailLines > 0 {
		opts.TailLines = ptr(o.TailLines)
	}
	if o.Since > 0 {
		opts.SinceSeconds = ptr(int64(o.Since.Seconds()))
	}
	if !follow {
		opts.LimitBytes = ptr(int64(maxBytesPerPod))
	}
	return opts
}

// Logs returns recent lines from the instance's pods, merged in time order.
func (in *Inspector) Logs(ctx context.Context, t Target, o LogOptions) ([]LogLine, error) {
	pods, err := in.pods(ctx, t, o.Pod)
	if err != nil {
		return nil, err
	}
	var out []LogLine
	for _, p := range pods {
		raw, err := in.cs.CoreV1().Pods(t.Namespace).GetLogs(p.Name, in.podLogOptions(o, false)).Do(ctx).Raw()
		if err != nil {
			// A pod that has not started yet has no logs; skip, do not fail.
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			if l, ok := parseLogLine(p.Name, line); ok {
				out = append(out, l)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if out == nil {
		out = []LogLine{}
	}
	return out, nil
}

// Follow streams new lines from the instance's current pods until ctx ends
// or every stream closes. Pods created later (a new rollout) are not picked
// up; clients reconnect, as SSE clients do automatically.
func (in *Inspector) Follow(ctx context.Context, t Target, o LogOptions, emit func(LogLine)) error {
	pods, err := in.pods(ctx, t, o.Pod)
	if err != nil {
		return err
	}
	lines := make(chan LogLine)
	var wg sync.WaitGroup
	for _, p := range pods {
		stream, err := in.cs.CoreV1().Pods(t.Namespace).GetLogs(p.Name, in.podLogOptions(o, true)).Stream(ctx)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(name string, r io.ReadCloser) {
			defer wg.Done()
			defer r.Close()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				if l, ok := parseLogLine(name, sc.Text()); ok {
					select {
					case lines <- l:
					case <-ctx.Done():
						return
					}
				}
			}
		}(p.Name, stream)
	}
	go func() { wg.Wait(); close(lines) }()
	for l := range lines {
		emit(l) // single consumer: emit never runs concurrently
	}
	return ctx.Err()
}

// parseLogLine splits a "<RFC3339Nano timestamp> <text>" line produced with
// Timestamps=true and sanitizes the untrusted text.
func parseLogLine(pod, raw string) (LogLine, bool) {
	ts, text, ok := strings.Cut(raw, " ")
	if !ok {
		return LogLine{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return LogLine{}, false
	}
	return LogLine{Pod: pod, Time: t, Line: logsafe.Line(text)}, true
}
