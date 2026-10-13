package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/vipul2902/clouddeploy/internal/deployments"
)

// LogFunc appends a line to the deployment's pipeline log.
type LogFunc func(level, msg string)

// Executor performs the external side effects of a deployment. Steps must
// honour ctx: it is cancelled when the user cancels the deployment, when the
// worker loses its lease, or when a shutdown grace period runs out.
//
// The interface exists because there are genuinely several implementations
// (dry-run now; the Docker builder in Phase 9; the Kubernetes deployer in
// Phase 10) and tests need to drive failures, hangs and panics.
type Executor interface {
	// Build produces a container image for the job's commit and returns its
	// reference.
	Build(ctx context.Context, job deployments.Job, log LogFunc) (image string, err error)
	// Deploy rolls the image out to the job's environment and returns once
	// the rollout has succeeded.
	Deploy(ctx context.Context, job deployments.Job, image string, log LogFunc) error
}

// DryRun describes what a real deployment would do without doing it. It is
// the executor until the real builder (Phase 9) and Kubernetes deployer
// (Phase 10) exist, so the queue, leases and state machine can run end to
// end. Every line it writes says [dry-run]: nothing is built or deployed.
type DryRun struct{}

// Build reports the image that would be built.
func (DryRun) Build(ctx context.Context, job deployments.Job, log LogFunc) (string, error) {
	d := job.Deployment
	image := fmt.Sprintf("%s:%s", job.ProjectName, d.CommitSHA)
	log("info", fmt.Sprintf("[dry-run] would clone %s at %s", job.RepositoryURL, d.CommitSHA))
	log("info", fmt.Sprintf("[dry-run] would build image %s", image))
	return image, ctx.Err()
}

// Deploy reports the rollout that would be applied.
func (DryRun) Deploy(ctx context.Context, job deployments.Job, image string, log LogFunc) error {
	c := job.Deployment.Config
	log("info", fmt.Sprintf("[dry-run] would apply Deployment %q in namespace %q: image=%s replicas=%d cpu=%s/%s memory=%s/%s",
		job.ProjectName, c.Namespace, image, c.Replicas,
		c.Resources.CPURequest, c.Resources.CPULimit, c.Resources.MemoryRequest, c.Resources.MemoryLimit))
	var vars []string
	for _, v := range c.Variables {
		if v.Secret && v.SecretRef != nil {
			vars = append(vars, v.Key+"=<secret "+*v.SecretRef+">")
		} else {
			vars = append(vars, v.Key)
		}
	}
	log("info", "[dry-run] environment variables: "+strings.Join(vars, ", "))
	log("warn", "[dry-run] no image was built and no cluster changes were made")
	return ctx.Err()
}
