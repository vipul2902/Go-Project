package deployments

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/vipul2902/clouddeploy/internal/environments"
	"github.com/vipul2902/clouddeploy/internal/projects"
)

// Rollback errors.
var (
	ErrRollbackTarget    = errors.New("rollback target must be a deployment that completed successfully")
	ErrAlreadyCurrent    = errors.New("target is already the current release")
	ErrNoPreviousRelease = errors.New("no earlier successful release to roll back to")
)

// RollbackParams describes an authorized rollback request.
type RollbackParams struct {
	ActorID        string
	Project        projects.Project
	Environment    environments.Environment
	Target         Deployment // a RUNNING (successful) deployment of this environment
	IdempotencyKey string
}

// Rollback queues a NEW deployment that restores Target exactly: its commit
// (so its image is reused, not rebuilt) and its configuration snapshot. The
// environment's current settings are deliberately left untouched; the
// returned deployment's Warnings list how they differ, because the next
// regular deployment will apply them again.
func (s *Service) Rollback(ctx context.Context, p RollbackParams) (Deployment, bool, error) {
	if p.Target.Status != StatusRunning || p.Target.EnvironmentID != p.Environment.ID {
		return Deployment{}, false, ErrRollbackTarget
	}
	latest, err := s.LatestSucceeded(ctx, p.Environment.ID)
	if err != nil {
		return Deployment{}, false, err
	}
	if latest != nil && latest.ID == p.Target.ID {
		return Deployment{}, false, ErrAlreadyCurrent
	}
	cfg := p.Target.Config
	d, replayed, err := s.Create(ctx, CreateParams{
		ActorID: p.ActorID, Project: p.Project, Environment: p.Environment,
		CommitSHA: p.Target.CommitSHA, IdempotencyKey: p.IdempotencyKey,
		RollbackOf: p.Target.ID, Config: &cfg, AuditAction: "deployment.rollback",
	})
	if err != nil {
		return Deployment{}, false, err
	}
	d.Warnings = ConfigDifferences(p.Environment, cfg, s.currentVariables(ctx, p.Environment.ID))
	return d, replayed, nil
}

// PreviousSucceeded is the environment's most recent successful release
// with a different commit than the current one: "the release before this".
func (s *Service) PreviousSucceeded(ctx context.Context, environmentID string) (*Deployment, error) {
	current, err := s.LatestSucceeded(ctx, environmentID)
	if err != nil || current == nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, selectDeployment+`
		WHERE d.environment_id = $1 AND d.status = 'RUNNING' AND d.id <> $2 AND d.commit_sha <> $3
		ORDER BY d.completed_at DESC LIMIT 1`, environmentID, current.ID, current.CommitSHA)
	if err != nil {
		return nil, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, s.scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}

func (s *Service) currentVariables(ctx context.Context, envID string) []environments.Variable {
	env, err := snapshot(ctx, s.pool, environments.Environment{ID: envID})
	if err != nil {
		return nil
	}
	return env.Variables
}

// ConfigDifferences describes how the environment's current configuration
// differs from a snapshot. Variable values are never included (they may be
// sensitive even when not marked secret); only keys are named.
func ConfigDifferences(env environments.Environment, snap Config, current []environments.Variable) []string {
	var out []string
	if env.Replicas != snap.Replicas {
		out = append(out, fmt.Sprintf("replicas: environment has %d, this release uses %d", env.Replicas, snap.Replicas))
	}
	if env.Resources != snap.Resources {
		out = append(out, "resources: environment requests/limits differ from this release")
	}
	if env.Port != snap.Port {
		out = append(out, fmt.Sprintf("port: environment has %d, this release uses %d", env.Port, snap.Port))
	}
	if deref(env.HealthCheckPath) != deref(snap.HealthCheckPath) {
		out = append(out, "health_check_path differs from this release")
	}
	want := map[string]environments.Variable{}
	for _, v := range snap.Variables {
		want[v.Key] = v
	}
	var added, removed, changed []string
	for _, v := range current {
		old, ok := want[v.Key]
		switch {
		case !ok:
			added = append(added, v.Key)
		case deref(old.Value) != deref(v.Value) || deref(old.SecretRef) != deref(v.SecretRef) || old.Secret != v.Secret:
			changed = append(changed, v.Key)
		}
		delete(want, v.Key)
	}
	for k := range want {
		removed = append(removed, k)
	}
	for label, keys := range map[string][]string{"added since this release": added, "removed since this release": removed, "changed since this release": changed} {
		if len(keys) > 0 {
			sort.Strings(keys)
			out = append(out, fmt.Sprintf("variables %s: %v", label, keys))
		}
	}
	sort.Strings(out)
	if len(out) > 0 {
		out = append(out, "the next regular deployment will apply the environment's current configuration")
	}
	return out
}

// AutoRollback queues a rollback after a deployment failed during rollout,
// if the environment allows it. It returns the rollback (nil when skipped)
// and a human-readable reason when skipped. Safe to call repeatedly: the
// idempotency key "auto-rollback:<failed id>" yields at most one rollback.
func (s *Service) AutoRollback(ctx context.Context, failedID string) (*Deployment, string, error) {
	failed, err := s.Get(ctx, failedID)
	if err != nil {
		return nil, "", err
	}
	if failed.Status != StatusFailed {
		return nil, "the deployment did not fail", nil
	}
	if failed.RollbackOf != nil {
		// Never roll back a failed rollback: that is how loops start.
		return nil, "a failed rollback is not rolled back automatically; investigate manually", nil
	}
	// Idempotency: if this failure was already handled, return that result
	// BEFORE recomputing the target. After the first rollback succeeds, the
	// "latest good release" is the rollback itself, so recomputing would ask
	// for a different target under the same key.
	key := "auto-rollback:" + failed.ID
	if prev, err := s.byIdempotencyKey(ctx, failed.EnvironmentID, key); err != nil {
		return nil, "", err
	} else if prev != nil {
		return prev, "", nil
	}
	var enabled bool
	var p projects.Project
	err = s.pool.QueryRow(ctx, `
		SELECT e.auto_rollback, p.id, p.organization_id, p.name
		FROM environments e JOIN projects p ON p.id = e.project_id
		WHERE e.id = $1 AND p.deleted_at IS NULL`, failed.EnvironmentID).Scan(&enabled, &p.ID, &p.OrganizationID, &p.Name)
	if err != nil {
		return nil, "", err
	}
	if !enabled {
		return nil, "auto-rollback is disabled for this environment", nil
	}
	target, err := s.LatestSucceeded(ctx, failed.EnvironmentID)
	if err != nil {
		return nil, "", err
	}
	if target == nil {
		return nil, "there is no earlier successful release to restore", nil
	}
	cfg := target.Config
	d, _, err := s.Create(ctx, CreateParams{
		Project: p, Environment: environments.Environment{ID: failed.EnvironmentID, Name: failed.Environment},
		CommitSHA: target.CommitSHA, IdempotencyKey: key,
		RollbackOf: target.ID, Config: &cfg,
		AuditAction: "deployment.auto_rollback", AuditMetadata: map[string]any{"failed_deployment": failed.ID},
	})
	var inProgress *InProgressError
	if errors.As(err, &inProgress) {
		return nil, "another deployment started meanwhile (" + inProgress.ActiveID + ")", nil
	}
	if err != nil {
		return nil, "", err
	}
	return &d, "", nil
}

func (s *Service) byIdempotencyKey(ctx context.Context, envID, key string) (*Deployment, error) {
	rows, err := s.pool.Query(ctx, selectDeployment+` WHERE d.environment_id = $1 AND d.idempotency_key = $2`, envID, key)
	if err != nil {
		return nil, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, s.scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}
