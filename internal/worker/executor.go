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
type Executor interface {
	Builder
	Deployer
}

// Builder produces a container image for the job's commit and returns its
// reference.
type Builder interface {
	Build(ctx context.Context, job deployments.Job, log LogFunc) (image string, err error)
}

// Deployer rolls the image out to the job's environment and returns once
// the rollout has succeeded.
type Deployer interface {
	Deploy(ctx context.Context, job deployments.Job, image string, log LogFunc) error
}

// Pipeline combines independently chosen build and deploy implementations
// (e.g. the Docker builder with the dry-run deployer until Phase 10).
type Pipeline struct {
	Builder
	Deployer
}

// DryRun describes what a real deployment would do without doing it. It can
// stand in for the builder, the deployer, or both, so the queue, leases and
// state machine run end to end before the real implementations exist. Every
// line it writes says [dry-run].
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
	log("warn", "[dry-run] no cluster changes were made")
	return ctx.Err()
}
