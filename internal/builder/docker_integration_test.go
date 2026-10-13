//go:build integration

package builder

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vipul2902/clouddeploy/internal/deployments"
)

// repo is a throwaway local git repository used as a build source.
type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "--quiet", "--initial-branch=main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files and commits them, returning the full SHA.
func (r *repo) commit(files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "--quiet", "-m", "change")
	return r.git("rev-parse", "HEAD")
}

func (r *repo) url() string { return "file://" + r.dir }

type capture struct {
	mu    sync.Mutex
	lines []string
}

func (c *capture) log(_, msg string) { c.mu.Lock(); c.lines = append(c.lines, msg); c.mu.Unlock() }
func (c *capture) text() string      { c.mu.Lock(); defer c.mu.Unlock(); return strings.Join(c.lines, "\n") }

func testBuilder(t *testing.T) *Docker {
	b := NewDocker(t.TempDir())
	b.allowFileRepos = true
	return b
}

func testJob(t *testing.T, r *repo, sha string) deployments.Job {
	// A unique project ID per test keeps image tags (and the reuse cache) apart.
	pid := strings.ReplaceAll(strings.ToLower(t.Name()), "_", "")
	pid = (pid + "0000000000000000")[:16]
	j := deployments.Job{ProjectName: "app", RepositoryURL: r.url(),
		Deployment: deployments.Deployment{ID: "dep-" + t.Name(), ProjectID: pid, CommitSHA: sha}}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", ImageTag(j)).Run() })
	return j
}

func label(t *testing.T, image, key string) string {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", "--format", `{{index .Config.Labels "`+key+`"}}`, image).Output()
	if err != nil {
		t.Fatalf("inspect %s: %v", image, err)
	}
	return strings.TrimSpace(string(out))
}

// Builds the OLDER of two commits, addressed by a 7-char SHA: proves short
// SHA resolution and that the build is pinned to the exact commit.
func TestBuildPinsExactCommitFromShortSHA(t *testing.T) {
	r := newRepo(t)
	old := r.commit(map[string]string{"Dockerfile": "FROM scratch\nCOPY version.txt /version.txt\n", "version.txt": "v1"})
	r.commit(map[string]string{"version.txt": "v2"})

	b, c := testBuilder(t), &capture{}
	job := testJob(t, r, old[:7])
	image, err := b.Build(context.Background(), job, c.log)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, c.text())
	}
	if image != ImageTag(job) {
		t.Errorf("image = %s", image)
	}
	if got := label(t, image, "org.opencontainers.image.revision"); got != old {
		t.Errorf("revision label = %s, want the older commit %s", got, old)
	}
	if got := label(t, image, "dev.clouddeploy.deployment-id"); got != job.Deployment.ID {
		t.Errorf("deployment label = %s", got)
	}
	for _, want := range []string{"checked out commit " + old, "building image", "built "} {
		if !strings.Contains(c.text(), want) {
			t.Errorf("log missing %q", want)
		}
	}

	// Second build of the same commit reuses the image instead of rebuilding.
	c2 := &capture{}
	if _, err := b.Build(context.Background(), job, c2.log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c2.text(), "already exists") || strings.Contains(c2.text(), "fetching") {
		t.Errorf("expected reuse without fetching:\n%s", c2.text())
	}
}

func TestBuildFailures(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		sha   func(head string) string
		want  string
	}{
		"no dockerfile":  {map[string]string{"README.md": "hi"}, func(h string) string { return h }, "no Dockerfile"},
		"unknown commit": {map[string]string{"Dockerfile": "FROM scratch\n"}, func(string) string { return "deadbee" }, "not found"},
		"failing build":  {map[string]string{"Dockerfile": "FROM scratch\nCOPY missing.txt /\n"}, func(h string) string { return h }, "docker build"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRepo(t)
			head := r.commit(tc.files)
			_, err := testBuilder(t).Build(context.Background(), testJob(t, r, tc.sha(head)), (&capture{}).log)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSourceSizeLimit(t *testing.T) {
	r := newRepo(t)
	head := r.commit(map[string]string{"Dockerfile": "FROM scratch\n", "big.bin": strings.Repeat("x", 2<<20)})
	b := testBuilder(t)
	b.MaxSourceBytes = 1 << 20
	if _, err := b.Build(context.Background(), testJob(t, r, head), (&capture{}).log); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("err = %v", err)
	}
}

// Cancelling mid-build (user cancel / lease lost) stops docker promptly and
// leaves no image behind. Uses golang:1.27-alpine, already present locally.
func TestCancelStopsDockerBuild(t *testing.T) {
	r := newRepo(t)
	// `echo st""arted` prints "started" only when the shell actually runs it;
	// BuildKit's echo of the command line shows st""arted instead.
	head := r.commit(map[string]string{"Dockerfile": "FROM golang:1.27-alpine\nRUN echo st\"\"arted && sleep 60\n"})
	job := testJob(t, r, head)
	c := &capture{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := testBuilder(t).Build(ctx, job, c.log); done <- err }()

	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(c.text(), " started") {
		if time.Now().After(deadline) {
			t.Fatalf("build never reached RUN step:\n%s", c.text())
		}
		time.Sleep(100 * time.Millisecond)
	}
	begin := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		t.Logf("build stopped %v after cancel", time.Since(begin).Round(100*time.Millisecond))
	case <-time.After(20 * time.Second):
		t.Fatal("build did not stop within 20s of cancel")
	}
	if err := exec.Command("docker", "image", "inspect", ImageTag(job)).Run(); err == nil {
		t.Error("cancelled build left an image behind")
	}
}
