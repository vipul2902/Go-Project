// Package builder builds container images from a project's repository.
package builder

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/worker"
)

// Docker builds images with the local git and docker CLIs. The image stays
// in the local Docker daemon (no registry), which the local Kubernetes
// cluster can use directly (Phase 10).
//
// SECURITY: `docker build` executes the repository's RUN instructions on the
// Docker host. That is acceptable for local development; production builds
// must run in an isolated, rootless builder (BuildKit rootless, kaniko or
// ACR Tasks), never on a shared Docker socket. See ADR-008.
type Docker struct {
	// WorkDir holds temporary checkouts; each build gets its own directory,
	// always removed afterwards.
	WorkDir string
	// MaxSourceBytes bounds the checked-out tree (excluding .git).
	MaxSourceBytes int64
	// MaxLogLines bounds build output copied into the pipeline log.
	MaxLogLines int

	allowFileRepos bool // tests only: build from local file:// repositories
}

// NewDocker returns a builder with production defaults.
func NewDocker(workDir string) *Docker {
	return &Docker{WorkDir: workDir, MaxSourceBytes: 500 << 20, MaxLogLines: 1000}
}

var (
	shaRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	// ansiRE matches terminal escape sequences (CSI and OSC). Build output is
	// untrusted: escapes could re-color, hide or rewrite lines when the log is
	// later shown in a terminal or the dashboard, so they are stripped.
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
)

// ImageTag names the image for a deployment's project and commit. The
// project ID prefix keeps two organizations' projects named "api" apart.
func ImageTag(job deployments.Job) string {
	return fmt.Sprintf("clouddeploy/%s-%s:%s", job.ProjectName, job.Deployment.ProjectID[:8], job.Deployment.CommitSHA)
}

// Build implements worker.Builder.
func (b *Docker) Build(ctx context.Context, job deployments.Job, log worker.LogFunc) (string, error) {
	d := job.Deployment
	if err := b.validate(job); err != nil {
		return "", err
	}
	tag := ImageTag(job)

	// Same commit already built: reuse it (makes redeploys and rollbacks fast,
	// and makes a retried build step idempotent).
	if rev, err := output(ctx, "", "docker", "image", "inspect", "--format",
		`{{index .Config.Labels "org.opencontainers.image.revision"}}`, tag); err == nil && rev != "" {
		log("info", fmt.Sprintf("image %s already exists (revision %s); reusing it", tag, rev))
		return tag, nil
	}

	dir, err := os.MkdirTemp(b.WorkDir, "build-")
	if err != nil {
		return "", fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, "src")

	log("info", fmt.Sprintf("fetching %s", job.RepositoryURL))
	// Blobless partial clone: all commits/trees (so short SHAs resolve) but
	// file contents only for the commit we check out.
	if err := b.git(ctx, dir, log, "clone", "--filter=blob:none", "--no-checkout", "--quiet", "--", job.RepositoryURL, src); err != nil {
		return "", fmt.Errorf("clone repository: %w", err)
	}
	full, err := output(ctx, src, "git", append(b.gitSafety(), "rev-parse", "--verify", "--quiet", d.CommitSHA+"^{commit}")...)
	if err != nil || full == "" {
		return "", fmt.Errorf("commit %s not found in %s", d.CommitSHA, job.RepositoryURL)
	}
	if err := b.git(ctx, src, log, "checkout", "--quiet", "--detach", full); err != nil {
		return "", fmt.Errorf("checkout %s: %w", full, err)
	}
	log("info", "checked out commit "+full)

	size, err := treeSize(src)
	if err != nil {
		return "", err
	}
	if size > b.MaxSourceBytes {
		return "", fmt.Errorf("repository is %d MB; limit is %d MB", size>>20, b.MaxSourceBytes>>20)
	}
	if _, err := os.Stat(filepath.Join(src, "Dockerfile")); err != nil {
		return "", errors.New("no Dockerfile at the repository root")
	}

	log("info", "building image "+tag)
	start := time.Now()
	err = b.run(ctx, src, log, nil, "docker", "build", "--progress=plain",
		"--tag", tag,
		"--label", "org.opencontainers.image.revision="+full,
		"--label", "org.opencontainers.image.source="+job.RepositoryURL,
		"--label", "dev.clouddeploy.project-id="+d.ProjectID,
		"--label", "dev.clouddeploy.deployment-id="+d.ID,
		".")
	if err != nil {
		return "", fmt.Errorf("docker build: %w (see deployment logs)", err)
	}
	log("info", fmt.Sprintf("built %s in %s", tag, time.Since(start).Round(100*time.Millisecond)))
	return tag, nil
}

func (b *Docker) validate(job deployments.Job) error {
	// Defense in depth: the API already validates these, but the builder
	// shells out with them, so it re-checks rather than trusting the row.
	if !shaRE.MatchString(job.Deployment.CommitSHA) {
		return fmt.Errorf("invalid commit SHA %q", job.Deployment.CommitSHA)
	}
	u, err := url.Parse(job.RepositoryURL)
	if err != nil || u.User != nil || !(u.Scheme == "https" || (b.allowFileRepos && u.Scheme == "file")) {
		return errors.New("repository URL must be https without credentials")
	}
	return nil
}

// gitSafety returns options that keep an untrusted repository from running
// code or prompting: no hooks, no system/global config, no local-file
// transport (unless testing), no terminal prompts.
func (b *Docker) gitSafety() []string {
	fileProto := "never"
	if b.allowFileRepos {
		fileProto = "always"
	}
	return []string{"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=" + fileProto,
		"-c", "advice.detachedHead=false"}
}

func (b *Docker) git(ctx context.Context, dir string, log worker.LogFunc, args ...string) error {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	return b.run(ctx, dir, log, env, "git", append(b.gitSafety(), args...)...)
}

// run executes a command, streaming combined output into the pipeline log
// (capped at MaxLogLines). Cancelling ctx interrupts the process (SIGINT,
// so docker can cancel the build cleanly), then kills it after 10s.
func (b *Docker) run(ctx context.Context, dir string, log worker.LogFunc, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(env, "NO_COLOR=1", "BUILDKIT_COLORS=")...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second

	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); copyLines(pr, log, b.MaxLogLines) }()

	err := cmd.Run()
	pw.Close()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// copyLines forwards up to max lines to log, then drains the rest so the
// process never blocks on a full pipe.
func copyLines(r io.Reader, log worker.LogFunc, max int) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	n := 0
	for sc.Scan() {
		line := sanitize(sc.Text())
		if line == "" {
			continue
		}
		n++
		switch {
		case n <= max:
			log("info", line)
		case n == max+1:
			log("warn", fmt.Sprintf("[output truncated after %d lines]", max))
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

// sanitize removes terminal escape sequences and other control characters
// (keeping tabs) from one line of untrusted output.
func sanitize(line string) string {
	line = ansiRE.ReplaceAllString(line, "")
	return strings.Map(func(r rune) rune {
		if r == '\t' || r >= 0x20 && r != 0x7f {
			return r
		}
		return -1
	}, line)
}

// output runs a command and returns its trimmed stdout.
func output(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func treeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
