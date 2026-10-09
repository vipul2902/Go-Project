package environments

import (
	"regexp"
	"strings"
	"time"
)

// Variable is one environment variable. Exactly one of Value (plain config)
// or SecretRef (a pointer to a Kubernetes Secret) is set. Secret values are
// never stored or returned by CloudDeploy.
type Variable struct {
	Key       string    `json:"key"`
	Value     *string   `json:"value"`
	Secret    bool      `json:"secret"`
	SecretRef *string   `json:"secret_ref"`
	UpdatedAt time.Time `json:"updated_at"`
}

// VariableInput is the body of PUT .../variables/{key}.
type VariableInput struct {
	Value     *string `json:"value"`
	SecretRef *string `json:"secret_ref"`
}

var (
	keyRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	// secretRefRE is "<secret-name>/<key>": a Kubernetes Secret in the
	// environment's own namespace (so one tenant cannot reference another's).
	secretRefRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?/[A-Za-z0-9._-]{1,253}$`)
	// sensitiveKeyRE flags names that almost always hold credentials.
	sensitiveKeyRE = regexp.MustCompile(`(PASSWORD|PASSWD|SECRET|TOKEN|API_?KEY|PRIVATE_?KEY|CREDENTIAL|ACCESS_?KEY)`)
)

// reservedPrefixes are injected by the platform or the kubelet.
var reservedPrefixes = []string{"CLOUDDEPLOY_", "KUBERNETES_"}

func validateVariable(key string, in VariableInput) map[string]string {
	errs := map[string]string{}
	if !keyRE.MatchString(key) {
		errs["key"] = "must be UPPER_SNAKE_CASE (A-Z, 0-9, _), max 128 chars, not starting with a digit"
	}
	for _, p := range reservedPrefixes {
		if strings.HasPrefix(key, p) {
			errs["key"] = "prefix " + p + " is reserved"
		}
	}

	switch {
	case (in.Value == nil) == (in.SecretRef == nil):
		errs["body"] = "exactly one of value or secret_ref is required"
	case in.SecretRef != nil && !secretRefRE.MatchString(*in.SecretRef):
		errs["secret_ref"] = `must be "<secret-name>/<key>", e.g. "db-credentials/password"`
	case in.Value != nil && len(*in.Value) > MaxVariableValueLen:
		errs["value"] = "must be at most 32 KiB"
	case in.Value != nil && sensitiveKeyRE.MatchString(key):
		// Guardrail: refuse to store likely credentials as plain config.
		errs["value"] = "this key looks like a credential; store it in a Kubernetes Secret and set secret_ref instead"
	}
	return errs
}
