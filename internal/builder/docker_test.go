package builder

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/vipul2902/clouddeploy/internal/deployments"
)

func job(sha, repo string) deployments.Job {
	return deployments.Job{
		ProjectName:   "api",
		RepositoryURL: repo,
		Deployment:    deployments.Deployment{ID: "d1", ProjectID: "0123456789abcdef", CommitSHA: sha},
	}
}

func TestImageTagIsTenantScoped(t *testing.T) {
	a := job("abc1234", "")
	b := job("abc1234", "")
	b.Deployment.ProjectID = "fedcba9876543210"
	if ImageTag(a) == ImageTag(b) {
		t.Fatal("two projects named api produced the same image tag")
	}
	if got := ImageTag(a); got != "clouddeploy/api-01234567:abc1234" {
		t.Errorf("tag = %s", got)
	}
}

func TestValidateRejectsUnsafeInput(t *testing.T) {
	b := NewDocker(t.TempDir())
	for name, j := range map[string]deployments.Job{
		"credentials in url": job("abc1234", "https://u:tok@github.com/a/b"),
		"ssh url":            job("abc1234", "git@github.com:a/b.git"),
		"file url":           job("abc1234", "file:///etc"),
		"option injection":   job("--upload-pack=x", "https://github.com/a/b"),
		"uppercase sha":      job("ABC1234", "https://github.com/a/b"),
	} {
		if err := b.validate(j); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := b.validate(job("abc1234", "https://github.com/a/b")); err != nil {
		t.Errorf("valid job rejected: %v", err)
	}
}

func TestRunStreamsAndCapsOutput(t *testing.T) {
	b := &Docker{MaxLogLines: 5}
	var mu sync.Mutex
	var lines []string
	log := func(_, msg string) { mu.Lock(); lines = append(lines, msg); mu.Unlock() }

	err := b.run(context.Background(), t.TempDir(), log, nil, "sh", "-c", "for i in $(seq 1 200); do echo line $i; done")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 6 || lines[0] != "line 1" || !strings.Contains(lines[5], "truncated after 5 lines") {
		t.Errorf("lines = %v", lines)
	}

	err = b.run(context.Background(), t.TempDir(), log, nil, "sh", "-c", "exit 3")
	if err == nil || !strings.Contains(fmt.Sprint(err), "exit status 3") {
		t.Errorf("exit code not reported: %v", err)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	b := &Docker{MaxLogLines: 10}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.run(ctx, t.TempDir(), func(string, string) {}, nil, "sleep", "30") }()
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSanitizeStripsTerminalEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[33m1 warning found\x1b[0m": "1 warning found",
		"ok\x1b]0;evil title\x07 line":   "ok line",
		"progress\rOVERWRITE":            "progressOVERWRITE",
		"bell\x07 and \x1b[2K erase":     "bell and  erase",
		"tab\tkept":                      "tab\tkept",
		"unicode ✓ kept":                 "unicode ✓ kept",
	} {
		if got := sanitize(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
