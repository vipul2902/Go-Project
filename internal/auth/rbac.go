package auth

import "errors"

// Role is a user's role within one organization.
type Role string

// Roles, from most to least privileged.
const (
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
	RoleViewer    Role = "viewer"
)

// ErrForbidden means the user is authenticated but lacks permission.
var ErrForbidden = errors.New("forbidden")

var roleRank = map[Role]int{RoleViewer: 1, RoleDeveloper: 2, RoleAdmin: 3}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { _, ok := roleRank[r]; return ok }

// Allows reports whether a user holding r may perform an action that
// requires need. Roles are hierarchical: admin ⊃ developer ⊃ viewer.
// Unknown roles are never allowed anything (fail closed).
func (r Role) Allows(need Role) bool {
	have, ok1 := roleRank[r]
	want, ok2 := roleRank[need]
	return ok1 && ok2 && have >= want
}

// Permission matrix (enforced by handlers via Service.Authorize):
//
//	action                          viewer  developer  admin
//	read projects/deployments/logs    ✓        ✓         ✓
//	create/update projects, deploy             ✓         ✓
//	rollback, cancel deployment                ✓         ✓
//	manage members, delete projects                      ✓
//	manage infrastructure                                ✓
