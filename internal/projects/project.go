// Package projects manages projects: the applications CloudDeploy deploys.
package projects

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vipul2902/clouddeploy/internal/slug"
)

// Domain errors.
var (
	ErrNotFound           = errors.New("project not found")
	ErrForbidden          = errors.New("forbidden")
	ErrNameTaken          = errors.New("project name already used in this organization")
	ErrPreconditionFailed = errors.New("project was modified by someone else")
	ErrActiveDeployment   = errors.New("project has a deployment in progress")
)

// Project is an application registered with CloudDeploy.
type Project struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	RepositoryURL  string    `json:"repository_url"`
	DefaultBranch  string    `json:"default_branch"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ETag is a version tag derived from updated_at (microsecond precision,
// matching Postgres). Clients send it back in If-Match to detect conflicts.
func (p Project) ETag() string {
	return `"` + strconv.FormatInt(p.UpdatedAt.UnixMicro(), 10) + `"`
}

// ParseETag converts an If-Match value back to a version timestamp.
func ParseETag(v string) (time.Time, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "W/")
	n, err := strconv.ParseInt(strings.Trim(v, `"`), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMicro(n), true
}

// CreateInput is the create-project request.
type CreateInput struct {
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	RepositoryURL  string `json:"repository_url"`
	DefaultBranch  string `json:"default_branch"`
}

// UpdateInput is a partial update; nil fields are left unchanged.
type UpdateInput struct {
	Name          *string `json:"name"`
	RepositoryURL *string `json:"repository_url"`
	DefaultBranch *string `json:"default_branch"`
}

func (in *CreateInput) validate() map[string]string {
	errs := map[string]string{}
	in.Name = strings.TrimSpace(in.Name)
	in.RepositoryURL = strings.TrimSpace(in.RepositoryURL)
	in.DefaultBranch = strings.TrimSpace(in.DefaultBranch)
	if in.DefaultBranch == "" {
		in.DefaultBranch = "main"
	}
	if in.OrganizationID == "" {
		errs["organization_id"] = "is required"
	}
	validateName(in.Name, errs)
	validateRepoURL(in.RepositoryURL, errs)
	validateBranch(in.DefaultBranch, errs)
	return errs
}

func (in *UpdateInput) validate() map[string]string {
	errs := map[string]string{}
	if in.Name == nil && in.RepositoryURL == nil && in.DefaultBranch == nil {
		errs["body"] = "at least one of name, repository_url, default_branch is required"
		return errs
	}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		validateName(*in.Name, errs)
	}
	if in.RepositoryURL != nil {
		*in.RepositoryURL = strings.TrimSpace(*in.RepositoryURL)
		validateRepoURL(*in.RepositoryURL, errs)
	}
	if in.DefaultBranch != nil {
		*in.DefaultBranch = strings.TrimSpace(*in.DefaultBranch)
		validateBranch(*in.DefaultBranch, errs)
	}
	return errs
}

func validateName(name string, errs map[string]string) {
	// Project names become Kubernetes resource names, hence DNS-1123 rules.
	if !slug.Valid(name) {
		errs["name"] = "must be lowercase letters, digits and hyphens (1-63 chars, no leading/trailing hyphen)"
	}
}

func validateRepoURL(raw string, errs map[string]string) {
	u, err := url.Parse(raw)
	switch {
	case err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host == "":
		errs["repository_url"] = "must be an https URL"
	case u.User != nil:
		// Never accept credentials embedded in URLs: they would be stored
		// in plaintext and shown to every viewer.
		errs["repository_url"] = "must not contain credentials"
	case u.RawQuery != "" || u.Fragment != "":
		errs["repository_url"] = "must not contain a query or fragment"
	}
}

// branchRE approximates git check-ref-format for branch names.
var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)

func validateBranch(b string, errs map[string]string) {
	if !branchRE.MatchString(b) || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") ||
		strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "..") || strings.Contains(b, "//") {
		errs["default_branch"] = "must be a valid git branch name"
	}
}

// sortSpec is a whitelisted sort option. Column names come only from this
// table, never from user input, so building ORDER BY is injection-safe.
type sortSpec struct {
	column string // SQL column
	desc   bool
	cast   string // SQL type for the cursor value
}

var sorts = map[string]sortSpec{
	"name":        {column: "p.name", cast: "text"},
	"-name":       {column: "p.name", desc: true, cast: "text"},
	"created_at":  {column: "p.created_at", cast: "timestamptz"},
	"-created_at": {column: "p.created_at", desc: true, cast: "timestamptz"},
}

// DefaultSort lists newest projects first.
const DefaultSort = "-created_at"

// cursor is the keyset position: the sort value and ID of the last row seen.
type cursor struct {
	Sort  string `json:"s"`
	Value string `json:"v"`
	ID    string `json:"id"`
}

func sortValue(p Project, sort string) string {
	if sorts[sort].column == "p.name" {
		return p.Name
	}
	return p.CreatedAt.Format(time.RFC3339Nano)
}
