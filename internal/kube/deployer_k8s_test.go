//go:build k8s

// Tests against a real cluster (make test-k8s). They need KUBE_CONTEXT and
// a cluster that shares the local Docker image store (Colima k3s).
package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/environments"
)

const baseImage = "rancher/mirrored-library-busybox:1.37.0" // present in k3s already

var (
	imagesOnce sync.Once
	goodImage  = "cdtest/httpd:1"
	crashImage = "cdtest/crash:1"
)

// buildImages builds two tiny images into the shared Docker image store.
func buildImages(t *testing.T) {
	t.Helper()
	imagesOnce.Do(func() {
		build := func(tag, dockerfile string) {
			cmd := exec.Command("docker", "build", "-q", "-t", tag, "-")
			cmd.Stdin = strings.NewReader(dockerfile)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build %s: %v\n%s", tag, err, out)
			}
		}
		build(goodImage, "FROM "+baseImage+"\nRUN mkdir -p /www && echo ok > /www/healthz && echo hello > /www/index.html\nCMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/www\"]\n")
		build(crashImage, "FROM "+baseImage+"\nCMD [\"sh\",\"-c\",\"echo boom: config file missing; exit 3\"]\n")
	})
}

func clientset(t *testing.T) kubernetes.Interface {
	t.Helper()
	cs, err := NewClientset(os.Getenv("KUBE_CONTEXT"))
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newJob returns a job targeting a fresh namespace, deleted after the test.
func newJob(t *testing.T, cs kubernetes.Interface) deployments.Job {
	t.Helper()
	ns := "cdtest-" + randHex(4)
	t.Cleanup(func() {
		_ = cs.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	health := "/healthz"
	return deployments.Job{
		ProjectName: "web",
		Deployment: deployments.Deployment{
			ID: "dep-" + randHex(4), ProjectID: "proj-" + randHex(4), Environment: "development", CommitSHA: "abc1234",
			Config: deployments.Config{
				Namespace: ns, Replicas: 2, Port: 8080, HealthCheckPath: &health,
				Resources: environments.Resources{CPURequest: "10m", CPULimit: "100m", MemoryRequest: "16Mi", MemoryLimit: "32Mi"},
			},
		},
	}
}

func release(job deployments.Job) deployments.Job {
	job.Deployment.ID = "dep-" + randHex(4)
	return job
}

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) log(_, m string) { l.mu.Lock(); l.lines = append(l.lines, m); l.mu.Unlock() }
func (l *logs) text() string    { l.mu.Lock(); defer l.mu.Unlock(); return strings.Join(l.lines, "\n") }

func deploy(t *testing.T, d *Deployer, job deployments.Job, image string) (error, *logs) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l := &logs{}
	return d.Deploy(ctx, job, image, l.log), l
}

func readyPods(t *testing.T, cs kubernetes.Interface, ns, deploymentID string) int {
	t.Helper()
	pods, err := cs.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{LabelSelector: LabelDeploymentID + "=" + deploymentID})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range pods.Items {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && p.DeletionTimestamp == nil {
				n++
			}
		}
	}
	return n
}

func TestRolloutSucceedsAndRedeployReplacesPods(t *testing.T) {
	buildImages(t)
	cs := clientset(t)
	d := NewDeployer(cs)
	v1 := newJob(t, cs)

	err, l := deploy(t, d, v1, goodImage)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, l.text())
	}
	ns := v1.Deployment.Config.Namespace
	if n := readyPods(t, cs, ns, v1.Deployment.ID); n != 2 {
		t.Errorf("ready pods = %d, want 2", n)
	}
	nsObj, _ := cs.CoreV1().Namespaces().Get(context.Background(), ns, metav1.GetOptions{})
	if nsObj.Labels["pod-security.kubernetes.io/enforce"] != "baseline" {
		t.Errorf("namespace labels: %v", nsObj.Labels)
	}
	if _, err := cs.CoreV1().Services(ns).Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Errorf("service: %v", err)
	}
	if !strings.Contains(l.text(), "rollout complete: 2/2") {
		t.Errorf("log:\n%s", l.text())
	}

	// Redeploying (same image, new release) rolls every pod.
	v2 := release(v1)
	if err, l := deploy(t, d, v2, goodImage); err != nil {
		t.Fatalf("redeploy: %v\n%s", err, l.text())
	}
	if n := readyPods(t, cs, ns, v2.Deployment.ID); n != 2 {
		t.Errorf("new release ready pods = %d", n)
	}
}

// The property that makes maxUnavailable=0 worth it: a bad release fails
// but the previous version keeps serving at full capacity.
func TestFailedReleaseKeepsPreviousVersionServing(t *testing.T) {
	buildImages(t)
	cs := clientset(t)
	d := NewDeployer(cs)
	v1 := newJob(t, cs)
	if err, l := deploy(t, d, v1, goodImage); err != nil {
		t.Fatalf("v1: %v\n%s", err, l.text())
	}

	v2 := release(v1)
	err, l := deploy(t, d, v2, crashImage)
	var re *RolloutError
	if !errors.As(err, &re) || re.Reason != "CrashLoopBackOff" {
		t.Fatalf("v2 err = %v\n%s", err, l.text())
	}
	// The crash output is in the pipeline log for whoever debugs it.
	if !strings.Contains(l.text(), "boom: config file missing") || !strings.Contains(re.Detail, "exit code 3") {
		t.Errorf("crash diagnostics missing: %v\n%s", re, l.text())
	}
	if n := readyPods(t, cs, v1.Deployment.Config.Namespace, v1.Deployment.ID); n != 2 {
		t.Fatalf("previous version has %d ready pods during failed rollout, want 2", n)
	}
}

func TestFailFastReasons(t *testing.T) {
	buildImages(t)
	cs := clientset(t)
	d := NewDeployer(cs)

	t.Run("image cannot be pulled", func(t *testing.T) {
		begin := time.Now()
		err, l := deploy(t, d, newJob(t, cs), "cdtest/does-not-exist:"+randHex(3))
		var re *RolloutError
		if !errors.As(err, &re) || (re.Reason != "ImagePullBackOff" && re.Reason != "ErrImagePull") {
			t.Fatalf("err = %v\n%s", err, l.text())
		}
		t.Logf("failed in %v (progress deadline is 5m)", time.Since(begin).Round(time.Second))
	})

	t.Run("referenced secret is missing", func(t *testing.T) {
		job := newJob(t, cs)
		job.Deployment.Config.Variables = []environments.Variable{{Key: "DB_PASSWORD", Secret: true, SecretRef: strp("db-credentials/password")}}
		err, l := deploy(t, d, job, goodImage)
		var re *RolloutError
		if !errors.As(err, &re) || re.Reason != "CreateContainerConfigError" || !strings.Contains(re.Detail, "db-credentials") {
			t.Fatalf("err = %v\n%s", err, l.text())
		}
	})
}

func TestSecretReferenceResolvedByKubelet(t *testing.T) {
	buildImages(t)
	cs := clientset(t)
	job := newJob(t, cs)
	ns := job.Deployment.Config.Namespace
	job.Deployment.Config.Variables = []environments.Variable{{Key: "DB_PASSWORD", Secret: true, SecretRef: strp("db-credentials/password")}}
	// The namespace must exist (managed) before the Secret can be created in it.
	d := NewDeployer(cs)
	if err := d.ensureNamespace(context.Background(), job, metav1.ApplyOptions{FieldManager: FieldManager, Force: true}); err != nil {
		t.Fatal(err)
	}
	_, err := cs.CoreV1().Secrets(ns).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-credentials"}, StringData: map[string]string{"password": "s3cr3t"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err, l := deploy(t, d, job, goodImage); err != nil {
		t.Fatalf("deploy: %v\n%s", err, l.text())
	}
	pods, _ := cs.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{LabelSelector: LabelDeploymentID + "=" + job.Deployment.ID})
	out, err := exec.Command("kubectl", "--context", os.Getenv("KUBE_CONTEXT"), "-n", ns, "exec", pods.Items[0].Name, "--", "printenv", "DB_PASSWORD").Output()
	if err != nil || strings.TrimSpace(string(out)) != "s3cr3t" {
		t.Errorf("secret not injected: %q %v", out, err)
	}
}

func TestRefusesUnmanagedNamespace(t *testing.T) {
	cs := clientset(t)
	job := newJob(t, cs)
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: job.Deployment.Config.Namespace},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err, _ := deploy(t, NewDeployer(cs), job, goodImage); !errors.Is(err, ErrNamespaceNotManaged) {
		t.Fatalf("err = %v, want ErrNamespaceNotManaged", err)
	}
	// Nothing was created in it.
	if deps, _ := cs.AppsV1().Deployments(job.Deployment.Config.Namespace).List(context.Background(), metav1.ListOptions{}); len(deps.Items) != 0 {
		t.Errorf("created %d deployments in an unmanaged namespace", len(deps.Items))
	}
}

func TestCancelStopsWaiting(t *testing.T) {
	buildImages(t)
	cs := clientset(t)
	job := newJob(t, cs)
	never := "/never-ready" // httpd returns 404: pods never become ready
	job.Deployment.Config.HealthCheckPath = &never

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewDeployer(cs).Deploy(ctx, job, goodImage, (&logs{}).log) }()
	time.Sleep(3 * time.Second)
	begin := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
		if took := time.Since(begin); took > 3*time.Second {
			t.Errorf("took %v to stop", took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deploy did not return after cancel")
	}
}

var tickerImage = "cdtest/ticker:1"

// buildTicker: an app that serves HTTP (so it becomes ready) and prints a
// numbered line every second (so it has logs to read and follow).
func buildTicker(t *testing.T) {
	t.Helper()
	buildImages(t)
	cmd := exec.Command("docker", "build", "-q", "-t", tickerImage, "-")
	cmd.Stdin = strings.NewReader("FROM " + baseImage + "\nRUN mkdir -p /www && echo ok > /www/healthz\n" +
		"CMD [\"sh\",\"-c\",\"httpd -p 8080 -h /www; i=0; while true; do echo tick $i; i=$((i+1)); sleep 1; done\"]\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build ticker: %v\n%s", err, out)
	}
}

func target(job deployments.Job) Target {
	return Target{Namespace: job.Deployment.Config.Namespace, Name: job.ProjectName, Instance: job.ProjectName + "-" + job.Deployment.Environment}
}

func TestInspectorHealthAndLogs(t *testing.T) {
	buildTicker(t)
	cs := clientset(t)
	in := NewInspector(cs)
	job := newJob(t, cs)
	ctx := context.Background()

	if h, err := in.Health(ctx, target(job)); err != nil || h.Status != StatusNotDeployed {
		t.Fatalf("before deploy: %+v %v", h, err)
	}
	if err, l := deploy(t, NewDeployer(cs), job, tickerImage); err != nil {
		t.Fatalf("deploy: %v\n%s", err, l.text())
	}

	h, err := in.Health(ctx, target(job))
	if err != nil || h.Status != StatusHealthy || h.Replicas.Available != 2 || len(h.Pods) != 2 || h.LiveDeploymentID != job.Deployment.ID {
		t.Fatalf("healthy: %+v %v", h, err)
	}
	for _, p := range h.Pods {
		if !p.Ready || p.DeploymentID != job.Deployment.ID {
			t.Errorf("pod %+v", p)
		}
	}

	time.Sleep(3 * time.Second) // let each pod print a few ticks
	lines, err := in.Logs(ctx, target(job), LogOptions{TailLines: 5})
	if err != nil || len(lines) < 4 {
		t.Fatalf("logs: %d lines, %v", len(lines), err)
	}
	pods := map[string]bool{}
	for i, l := range lines {
		pods[l.Pod] = true
		if !strings.HasPrefix(l.Line, "tick ") {
			t.Errorf("unexpected line %q", l.Line)
		}
		if i > 0 && l.Time.Before(lines[i-1].Time) {
			t.Error("lines from several pods are not merged in time order")
		}
	}
	if len(pods) != 2 {
		t.Errorf("lines came from %d pods, want 2", len(pods))
	}
	if _, err := in.Logs(ctx, target(job), LogOptions{TailLines: 5, Pod: "someone-elses-pod"}); !errors.Is(err, ErrPodNotFound) {
		t.Errorf("foreign pod: %v", err)
	}

	// Follow receives lines printed after it started.
	fctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var mu sync.Mutex
	var followed []LogLine
	_ = in.Follow(fctx, target(job), LogOptions{TailLines: 1}, func(l LogLine) { mu.Lock(); followed = append(followed, l); mu.Unlock() })
	if len(followed) < 4 { // ~2 pods x 3-4 seconds
		t.Errorf("followed only %d lines", len(followed))
	}

	// A crash-looping release is reported as failing (not "progressing")
	// while the previous release keeps serving.
	if err, _ := deploy(t, NewDeployer(cs), release(job), crashImage); err == nil {
		t.Fatal("crash release succeeded")
	}
	h, _ = in.Health(ctx, target(job))
	reasons := map[string]bool{}
	for _, p := range h.Pods {
		reasons[p.Reason] = true
	}
	if h.Status != StatusFailing || !reasons["CrashLoopBackOff"] || h.Replicas.Available != 2 {
		t.Errorf("after crash release: status=%s available=%d reasons=%v", h.Status, h.Replicas.Available, reasons)
	}
}
