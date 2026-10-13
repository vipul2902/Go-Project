package builder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
)

type fakeACR struct {
	mu        sync.Mutex
	req       *armcontainerregistry.DockerBuildRequest
	statuses  []armcontainerregistry.RunStatus // returned in order; last one repeats
	message   string
	logURL    string
	cancelled string
}

func (f *fakeACR) Schedule(_ context.Context, req *armcontainerregistry.DockerBuildRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.req = req
	return "ca1", nil
}

func (f *fakeACR) Status(context.Context, string) (armcontainerregistry.RunStatus, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return s, f.message, nil
}

func (f *fakeACR) LogURL(context.Context, string) (string, error) { return f.logURL, nil }

func (f *fakeACR) Cancel(_ context.Context, runID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = runID
	return nil
}

func testACR(f *fakeACR) *ACR {
	b := newACR(f, "cddevx1acr.azurecr.io")
	b.PollInterval = time.Millisecond
	return b
}

type logRec struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRec) log(_, msg string) { l.mu.Lock(); l.lines = append(l.lines, msg); l.mu.Unlock() }
func (l *logRec) text() string      { l.mu.Lock(); defer l.mu.Unlock(); return strings.Join(l.lines, "\n") }

func TestACRBuildSucceeds(t *testing.T) {
	logs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "Step 1/2 : FROM alpine\nSuccessfully built\n")
	}))
	defer logs.Close()
	f := &fakeACR{statuses: []armcontainerregistry.RunStatus{"Queued", "Running", "Succeeded"}, logURL: logs.URL + "/log?sig=secret"}
	var l logRec
	j := job("abc1234", "https://github.com/a/b")

	image, err := testACR(f).Build(context.Background(), j, l.log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "cddevx1acr.azurecr.io/" + ImageTag(j); image != want {
		t.Errorf("image = %q, want %q", image, want)
	}
	r := f.req
	if *r.SourceLocation != "https://github.com/a/b#abc1234" {
		t.Errorf("source = %q: must pin the commit", *r.SourceLocation)
	}
	if !*r.IsPushEnabled || *r.ImageNames[0] != ImageTag(j) {
		t.Errorf("must push exactly the deployment's tag: %+v", r.ImageNames)
	}
	if *r.Platform.OS != armcontainerregistry.OSLinux || *r.Platform.Architecture != armcontainerregistry.ArchitectureAmd64 {
		t.Error("AKS nodes are linux/amd64")
	}
	if len(r.Arguments) != 0 || r.Credentials != nil {
		t.Error("no build arguments or credentials may be passed to tenant builds")
	}
	out := l.text()
	if !strings.Contains(out, "Successfully built") {
		t.Errorf("build log not copied:\n%s", out)
	}
	if strings.Contains(out, "sig=") {
		t.Error("the SAS log URL must never be logged")
	}
}

func TestACRBuildFailureReportsReason(t *testing.T) {
	f := &fakeACR{statuses: []armcontainerregistry.RunStatus{"Running", "Failed"}, message: "failed to fetch commit"}
	var l logRec
	_, err := testACR(f).Build(context.Background(), job("abc1234", "https://github.com/a/b"), l.log)
	if err == nil || !strings.Contains(err.Error(), "failed to fetch commit") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(l.text(), "build log unavailable") {
		t.Error("a missing log should be reported, not silently skipped")
	}
}

func TestACRCancelStopsRemoteRun(t *testing.T) {
	f := &fakeACR{statuses: []armcontainerregistry.RunStatus{"Running"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var l logRec
	_, err := testACR(f).Build(ctx, job("abc1234", "https://github.com/a/b"), l.log)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if f.cancelled != "ca1" {
		t.Error("cancelling the deployment must cancel the ACR run")
	}
}

func TestACRRejectsUnsafeJobBeforeScheduling(t *testing.T) {
	f := &fakeACR{statuses: []armcontainerregistry.RunStatus{"Succeeded"}}
	var l logRec
	if _, err := testACR(f).Build(context.Background(), job("abc1234", "https://github.com/a/b#main"), l.log); err == nil {
		t.Fatal("accepted a URL with a ref fragment")
	}
	if f.req != nil {
		t.Error("nothing may be scheduled for an invalid job")
	}
}

func TestNewACRRejectsBadRegistryID(t *testing.T) {
	if _, err := NewACR(context.Background(), "/subscriptions/x/resourceGroups/rg/providers/Microsoft.KeyVault/vaults/v", nil); err == nil {
		t.Fatal("accepted a non-registry resource ID")
	}
}
