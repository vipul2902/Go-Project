// Package environments manages a project's deployment environments and
// their configuration (replicas, resources, environment variables).
package environments

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vipul2902/clouddeploy/internal/auth"
	"github.com/vipul2902/clouddeploy/internal/slug"
)

// Environment names. The set is fixed: promotion flows dev -> staging -> prod.
const (
	Development = "development"
	Staging     = "staging"
	Production  = "production"
)

var shortName = map[string]string{Development: "dev", Staging: "staging", Production: "prod"}

// Domain errors.
var (
	ErrNotFound         = errors.New("environment not found")
	ErrExists           = errors.New("environment already exists")
	ErrNamespaceTaken   = errors.New("namespace already used")
	ErrHasDeployments   = errors.New("environment has deployments")
	ErrVariableNotFound = errors.New("variable not found")
	ErrTooManyVariables = errors.New("too many variables")
)

// Limits protecting the cluster from a single tenant.
const (
	MaxReplicas         = 20
	MaxCPUMillis        = 4000
	MinCPUMillis        = 10
	MaxMemoryBytes      = 8 << 30
	MinMemoryBytes      = 16 << 20
	MaxVariablesPerEnv  = 100
	MaxVariableValueLen = 32 << 10
)

// Environment is one deployment target of a project.
type Environment struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Replicas  int       `json:"replicas"`
	Resources Resources `json:"resources"`
	// Port the app listens on; HealthCheckPath (optional) answers 2xx when
	// the app is ready. Together they drive the Service and the probes.
	Port            int     `json:"port"`
	HealthCheckPath *string `json:"health_check_path"`
	// AutoRollback queues a rollback to the last good release when a rollout fails.
	AutoRollback bool      `json:"auto_rollback"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Resources are Kubernetes container requests/limits in quantity syntax.
type Resources struct {
	CPURequest    string `json:"cpu_request"`
	CPULimit      string `json:"cpu_limit"`
	MemoryRequest string `json:"memory_request"`
	MemoryLimit   string `json:"memory_limit"`
}

// DefaultResources is a small footprint suitable for a laptop cluster.
var DefaultResources = Resources{CPURequest: "100m", CPULimit: "500m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}

// RequiredRole returns the role needed to change an environment's
// configuration: production changes are restricted to admins.
func RequiredRole(envName string) auth.Role {
	if envName == Production {
		return auth.RoleAdmin
	}
	return auth.RoleDeveloper
}

// ValidName reports whether name is a supported environment name.
func ValidName(name string) bool { _, ok := shortName[name]; return ok }

// DefaultNamespace derives "<project>-<env>" (e.g. "billing-api-prod"),
// trimmed to the 63-char DNS label limit.
func DefaultNamespace(projectName, envName string) string {
	suffix := "-" + shortName[envName]
	base := projectName
	if len(base)+len(suffix) > 63 {
		base = strings.TrimRight(base[:63-len(suffix)], "-")
	}
	return base + suffix
}

// reservedNamespaces must never be deployment targets: they hold cluster
// components (and CloudDeploy itself).
var reservedNamespaces = map[string]bool{
	"default": true, "kube-system": true, "kube-public": true, "kube-node-lease": true, "clouddeploy": true,
}

func validateNamespace(ns string, errs map[string]string) {
	switch {
	case !slug.Valid(ns):
		errs["namespace"] = "must be a DNS-1123 label (lowercase letters, digits, hyphens; max 63)"
	case reservedNamespaces[ns] || strings.HasPrefix(ns, "kube-"):
		errs["namespace"] = "is reserved"
	}
}

var healthPathRE = regexp.MustCompile(`^/[A-Za-z0-9._~/%-]{0,199}$`)

func validatePort(p int, errs map[string]string) {
	if p < 1 || p > 65535 {
		errs["port"] = "must be between 1 and 65535"
	}
}

func validateHealthPath(p string, errs map[string]string) {
	if !healthPathRE.MatchString(p) {
		errs["health_check_path"] = `must be an absolute URL path like "/healthz" (no query string)`
	}
}

func validateReplicas(n int, errs map[string]string) {
	if n < 0 || n > MaxReplicas {
		errs["replicas"] = fmt.Sprintf("must be between 0 and %d", MaxReplicas)
	}
}

// validate checks quantity syntax, bounds, and request <= limit.
func (r Resources) validate(errs map[string]string) {
	cpuReq, err1 := ParseCPU(r.CPURequest)
	cpuLim, err2 := ParseCPU(r.CPULimit)
	memReq, err3 := ParseMemory(r.MemoryRequest)
	memLim, err4 := ParseMemory(r.MemoryLimit)
	for field, err := range map[string]error{
		"resources.cpu_request": err1, "resources.cpu_limit": err2,
		"resources.memory_request": err3, "resources.memory_limit": err4,
	} {
		if err != nil {
			errs[field] = err.Error()
		}
	}
	if err1 == nil && err2 == nil && cpuReq > cpuLim {
		errs["resources.cpu_request"] = "must not exceed cpu_limit"
	}
	if err3 == nil && err4 == nil && memReq > memLim {
		errs["resources.memory_request"] = "must not exceed memory_limit"
	}
}

var (
	cpuMilliRE = regexp.MustCompile(`^([0-9]{1,6})m$`)
	cpuCoreRE  = regexp.MustCompile(`^([0-9]{1,3})(\.[0-9]{1,3})?$`)
	memRE      = regexp.MustCompile(`^([0-9]{1,12})(Ki|Mi|Gi|K|M|G)?$`)
	memUnits   = map[string]int64{"": 1, "K": 1e3, "M": 1e6, "G": 1e9, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30}
)

// ParseCPU converts a Kubernetes CPU quantity ("250m", "0.5", "2") to
// millicores, enforcing platform bounds.
func ParseCPU(q string) (int64, error) {
	var millis int64
	if m := cpuMilliRE.FindStringSubmatch(q); m != nil {
		millis, _ = strconv.ParseInt(m[1], 10, 64)
	} else if cpuCoreRE.MatchString(q) {
		f, _ := strconv.ParseFloat(q, 64)
		millis = int64(f*1000 + 0.5)
	} else {
		return 0, errors.New(`must be a CPU quantity like "250m" or "0.5"`)
	}
	if millis < MinCPUMillis || millis > MaxCPUMillis {
		return 0, fmt.Errorf("must be between %dm and %dm", MinCPUMillis, MaxCPUMillis)
	}
	return millis, nil
}

// ParseMemory converts a Kubernetes memory quantity ("256Mi", "1Gi") to
// bytes, enforcing platform bounds.
func ParseMemory(q string) (int64, error) {
	m := memRE.FindStringSubmatch(q)
	if m == nil {
		return 0, errors.New(`must be a memory quantity like "256Mi" or "1Gi"`)
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	bytes := n * memUnits[m[2]]
	if bytes < MinMemoryBytes || bytes > MaxMemoryBytes {
		return 0, errors.New("must be between 16Mi and 8Gi")
	}
	return bytes, nil
}

// CreateInput is the create-environment request. Zero values take defaults.
type CreateInput struct {
	Name            string     `json:"name"`
	Namespace       string     `json:"namespace"`
	Replicas        *int       `json:"replicas"`
	Resources       *Resources `json:"resources"`
	Port            *int       `json:"port"`
	HealthCheckPath *string    `json:"health_check_path"`
	AutoRollback    *bool      `json:"auto_rollback"`
}

func (in *CreateInput) normalize(projectName string) map[string]string {
	errs := map[string]string{}
	if !ValidName(in.Name) {
		errs["name"] = "must be development, staging or production"
		return errs
	}
	if in.Namespace == "" {
		in.Namespace = DefaultNamespace(projectName, in.Name)
	}
	if in.Replicas == nil {
		one := 1
		in.Replicas = &one
	}
	if in.Resources == nil {
		r := DefaultResources
		in.Resources = &r
	}
	validateNamespace(in.Namespace, errs)
	if in.Port == nil {
		p := 8080
		in.Port = &p
	}
	if in.AutoRollback == nil {
		on := true
		in.AutoRollback = &on
	}
	validateReplicas(*in.Replicas, errs)
	in.Resources.validate(errs)
	validatePort(*in.Port, errs)
	if in.HealthCheckPath != nil {
		validateHealthPath(*in.HealthCheckPath, errs)
	}
	return errs
}

// UpdateInput is a partial update. Resources, if given, replaces all four
// values so request/limit pairs are always validated together.
type UpdateInput struct {
	Replicas  *int       `json:"replicas"`
	Resources *Resources `json:"resources"`
	Port      *int       `json:"port"`
	// HealthCheckPath: nil leaves it unchanged; "" clears it (TCP probes).
	HealthCheckPath *string `json:"health_check_path"`
	AutoRollback    *bool   `json:"auto_rollback"`
}

func (in UpdateInput) validate() map[string]string {
	errs := map[string]string{}
	if in.Replicas == nil && in.Resources == nil && in.Port == nil && in.HealthCheckPath == nil && in.AutoRollback == nil {
		errs["body"] = "at least one of replicas, resources, port, health_check_path, auto_rollback is required"
		return errs
	}
	if in.Port != nil {
		validatePort(*in.Port, errs)
	}
	if in.HealthCheckPath != nil && *in.HealthCheckPath != "" {
		validateHealthPath(*in.HealthCheckPath, errs)
	}
	if in.Replicas != nil {
		validateReplicas(*in.Replicas, errs)
	}
	if in.Resources != nil {
		in.Resources.validate(errs)
	}
	return errs
}
