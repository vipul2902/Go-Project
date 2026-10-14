package builder

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"

	"github.com/vipul2902/clouddeploy/internal/deployments"
	"github.com/vipul2902/clouddeploy/internal/worker"
)

// ACR builds images with ACR Tasks: Azure clones the repository at the
// commit and runs `docker build` on its own isolated, ephemeral build
// agents, then pushes to the registry. The repository's untrusted RUN
// instructions never execute on the cluster, and the worker holds no Docker
// socket and no registry password. It authenticates with workload identity
// (ADR-008).
type ACR struct {
	api          acrAPI
	loginServer  string
	PollInterval time.Duration
	Timeout      time.Duration // ACR Tasks run timeout
	MaxLogLines  int
	httpClient   *http.Client
}

// acrAPI is the subset of ACR Tasks the builder uses (faked in tests).
type acrAPI interface {
	Schedule(ctx context.Context, req *armcontainerregistry.DockerBuildRequest) (runID string, err error)
	Status(ctx context.Context, runID string) (status armcontainerregistry.RunStatus, message string, err error)
	LogURL(ctx context.Context, runID string) (string, error)
	Cancel(ctx context.Context, runID string) error
}

// NewACR connects to the registry identified by its Azure resource ID. It
// reads the registry once, so a wrong ID or missing role fails at startup
// rather than on the first deployment.
func NewACR(ctx context.Context, registryID string, cred azcore.TokenCredential) (*ACR, error) {
	id, err := arm.ParseResourceID(registryID)
	if err != nil || !strings.EqualFold(id.ResourceType.String(), "Microsoft.ContainerRegistry/registries") {
		return nil, fmt.Errorf("ACR_REGISTRY_ID: not a container registry resource ID")
	}
	f, err := armcontainerregistry.NewClientFactory(id.SubscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}
	reg, err := f.NewRegistriesClient().Get(ctx, id.ResourceGroupName, id.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("read registry %s: %w", id.Name, err)
	}
	if reg.Properties == nil || reg.Properties.LoginServer == nil {
		return nil, errors.New("registry has no login server")
	}
	api := &armACR{rg: id.ResourceGroupName, name: id.Name, registries: f.NewRegistriesClient(), runs: f.NewRunsClient()}
	return newACR(api, *reg.Properties.LoginServer), nil
}

func newACR(api acrAPI, loginServer string) *ACR {
	return &ACR{api: api, loginServer: loginServer, PollInterval: 5 * time.Second,
		Timeout: 15 * time.Minute, MaxLogLines: 1000, httpClient: &http.Client{Timeout: time.Minute}}
}

// Build implements worker.Builder.
func (b *ACR) Build(ctx context.Context, job deployments.Job, log worker.LogFunc) (string, error) {
	if err := validateJob(job, false); err != nil {
		return "", err
	}
	tag := ImageTag(job)
	d := job.Deployment
	// Git context: ACR clones the repository at the given ref itself, so
	// no source ever passes through the worker.
	source := job.RepositoryURL + "#" + d.CommitSHA
	req := &armcontainerregistry.DockerBuildRequest{
		Type:           to("DockerBuildRequest"),
		SourceLocation: &source,
		DockerFilePath: to("Dockerfile"),
		ImageNames:     []*string{&tag},
		IsPushEnabled:  to(true),
		Timeout:        to(int32(b.Timeout / time.Second)), //nolint:gosec // G115: minutes, far below int32
		Platform: &armcontainerregistry.PlatformProperties{
			OS: to(armcontainerregistry.OSLinux), Architecture: to(armcontainerregistry.ArchitectureAmd64),
		},
		// Labels as build arguments are not used: the Dockerfile is the
		// tenant's, and no values are passed into it (no secrets, no args).
	}

	log("info", fmt.Sprintf("scheduling ACR build of %s at %s", job.RepositoryURL, d.CommitSHA))
	runID, err := b.api.Schedule(ctx, req)
	if err != nil {
		return "", fmt.Errorf("schedule ACR build: %w", err)
	}
	log("info", "ACR run "+runID+" queued")
	start := time.Now()

	status, msg, err := b.wait(ctx, runID)
	if ctx.Err() != nil {
		// Cancelled (user cancel, lease lost, shutdown): stop the remote
		// run too, so it doesn't keep building and pushing.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cerr := b.api.Cancel(cctx, runID); cerr != nil {
			log("warn", "could not cancel ACR run "+runID)
		}
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("ACR run %s: %w", runID, err)
	}
	b.copyLog(ctx, runID, log)
	if status != armcontainerregistry.RunStatusSucceeded {
		if msg != "" {
			return "", fmt.Errorf("ACR build %s: %s (see deployment logs)", strings.ToLower(string(status)), msg)
		}
		return "", fmt.Errorf("ACR build %s (see deployment logs)", strings.ToLower(string(status)))
	}
	image := b.loginServer + "/" + tag
	log("info", fmt.Sprintf("built and pushed %s in %s", image, time.Since(start).Round(time.Second)))
	return image, nil
}

// wait polls until the run reaches a terminal state or ctx ends.
func (b *ACR) wait(ctx context.Context, runID string) (armcontainerregistry.RunStatus, string, error) {
	t := time.NewTicker(b.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-t.C:
		}
		status, msg, err := b.api.Status(ctx, runID)
		if err != nil {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			return "", "", err
		}
		switch status {
		case armcontainerregistry.RunStatusSucceeded, armcontainerregistry.RunStatusFailed,
			armcontainerregistry.RunStatusError, armcontainerregistry.RunStatusTimeout,
			armcontainerregistry.RunStatusCanceled:
			return status, msg, nil
		}
	}
}

// copyLog forwards the run's build log into the pipeline log. A log that
// can't be fetched is reported but never fails the build on its own. The
// log URL carries a SAS token, so it is never logged.
func (b *ACR) copyLog(ctx context.Context, runID string, log worker.LogFunc) {
	u, err := b.api.LogURL(ctx, runID)
	if err != nil || u == "" {
		log("warn", "build log unavailable")
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		log("warn", "build log unavailable")
		return
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		log("warn", "build log unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log("warn", fmt.Sprintf("build log unavailable (HTTP %d)", resp.StatusCode))
		return
	}
	copyLines(resp.Body, log, b.MaxLogLines)
}

func to[T any](v T) *T { return &v }

var azruntimePollOpts = runtime.PollUntilDoneOptions{Frequency: 2 * time.Second}

// armACR implements acrAPI with the Azure SDK.
type armACR struct {
	rg, name   string
	registries *armcontainerregistry.RegistriesClient
	runs       *armcontainerregistry.RunsClient
}

func (a *armACR) Schedule(ctx context.Context, req *armcontainerregistry.DockerBuildRequest) (string, error) {
	// ScheduleRun is a long-running operation that completes when the run is
	// QUEUED, not when it finishes; the run itself is polled separately.
	p, err := a.registries.BeginScheduleRun(ctx, a.rg, a.name, req, nil)
	if err != nil {
		return "", err
	}
	res, err := p.PollUntilDone(ctx, &azruntimePollOpts)
	if err != nil {
		return "", err
	}
	if res.Properties == nil || res.Properties.RunID == nil {
		return "", errors.New("ACR returned no run ID")
	}
	return *res.Properties.RunID, nil
}

func (a *armACR) Status(ctx context.Context, runID string) (armcontainerregistry.RunStatus, string, error) {
	r, err := a.runs.Get(ctx, a.rg, a.name, runID, nil)
	if err != nil {
		return "", "", err
	}
	if r.Properties == nil || r.Properties.Status == nil {
		return armcontainerregistry.RunStatusQueued, "", nil
	}
	msg := ""
	if r.Properties.RunErrorMessage != nil {
		msg = *r.Properties.RunErrorMessage
	}
	return *r.Properties.Status, msg, nil
}

func (a *armACR) LogURL(ctx context.Context, runID string) (string, error) {
	r, err := a.runs.GetLogSasURL(ctx, a.rg, a.name, runID, nil)
	if err != nil || r.LogLink == nil {
		return "", err
	}
	return *r.LogLink, nil
}

func (a *armACR) Cancel(ctx context.Context, runID string) error {
	p, err := a.runs.BeginCancel(ctx, a.rg, a.name, runID, nil)
	if err != nil {
		return err
	}
	_, err = p.PollUntilDone(ctx, &azruntimePollOpts)
	return err
}
