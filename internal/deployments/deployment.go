package deployments

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/vipul2902/clouddeploy/internal/environments"
)

// Domain errors.
var (
	ErrNotFound            = errors.New("deployment not found")
	ErrProjectGone         = errors.New("project was deleted")
	ErrIdempotencyMismatch = errors.New("idempotency key reused with a different request")
)

// InProgressError means the environment already has an active deployment.
type InProgressError struct {
	ActiveID string
}

func (e *InProgressError) Error() string {
	return fmt.Sprintf("deployment %s is already in progress for this environment", e.ActiveID)
}

// Deployment is one attempt to ship a commit to an environment.
type Deployment struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"project_id"`
	ProjectName     string     `json:"project_name"`
	EnvironmentID   string     `json:"environment_id"`
	Environment     string     `json:"environment"`
	CommitSHA       string     `json:"commit_sha"`
	Image           *string    `json:"image"`
	Status          Status     `json:"status"`
	TriggeredBy     *Trigger   `json:"triggered_by"`
	RollbackOf      *string    `json:"rollback_of"`
	ErrorMessage    *string    `json:"error_message"`
	Config          Config     `json:"config"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
	DurationSeconds *float64   `json:"duration_seconds"`
	// Warnings is set only on rollback responses (config differences).
	Warnings []string `json:"warnings,omitempty"`
}

// Trigger identifies who started a deployment (null if the user was deleted).
type Trigger struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Config is the environment configuration snapshotted at creation time.
// Secret variables appear only as references.
type Config struct {
	Namespace       string                  `json:"namespace"`
	Replicas        int                     `json:"replicas"`
	Resources       environments.Resources  `json:"resources"`
	Port            int                     `json:"port"`
	HealthCheckPath *string                 `json:"health_check_path"`
	Variables       []environments.Variable `json:"variables"`
}

// computeDuration fills DurationSeconds from start and end (or now, while
// the deployment is still running).
func (d *Deployment) computeDuration(now time.Time) {
	if d.StartedAt == nil {
		return
	}
	end := now
	if d.CompletedAt != nil {
		end = *d.CompletedAt
	}
	secs := end.Sub(*d.StartedAt).Seconds()
	d.DurationSeconds = &secs
}

// CreateInput is the body of POST /deployments.
type CreateInput struct {
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
	CommitSHA   string `json:"commit_sha"`
}

var (
	shaRE            = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	idempotencyKeyRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
)

func (in *CreateInput) validate(isUUID func(string) bool) map[string]string {
	errs := map[string]string{}
	in.CommitSHA = strings.ToLower(strings.TrimSpace(in.CommitSHA))
	if !isUUID(in.ProjectID) {
		errs["project_id"] = "must be a UUID"
	}
	if !environments.ValidName(in.Environment) {
		errs["environment"] = "must be development, staging or production"
	}
	if !shaRE.MatchString(in.CommitSHA) {
		errs["commit_sha"] = "must be a 7-40 character hex git commit SHA"
	}
	return errs
}

// ValidIdempotencyKey reports whether k is an acceptable Idempotency-Key.
// Clients typically send a UUID generated once per user action.
func ValidIdempotencyKey(k string) bool { return idempotencyKeyRE.MatchString(k) }
