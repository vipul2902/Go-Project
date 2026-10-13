// Package deployments implements the deployment domain: the deployment
// state machine, creation with idempotency, and history.
package deployments

import "fmt"

// Status is a deployment's lifecycle state.
type Status string

// Deployment statuses.
const (
	StatusQueued    Status = "QUEUED"
	StatusBuilding  Status = "BUILDING"
	StatusDeploying Status = "DEPLOYING"
	StatusRunning   Status = "RUNNING"
	StatusFailed    Status = "FAILED"
	StatusCancelled Status = "CANCELLED"
)

// transitions is the complete state machine. Anything not listed is invalid.
//
//	QUEUED ──► BUILDING ──► DEPLOYING ──► RUNNING
//	   │           │            │
//	   ├───────────┴────────────┴──► FAILED
//	   └───────────┴────────────┴──► CANCELLED
//
// RUNNING, FAILED and CANCELLED are terminal: a deployment never changes once
// it has finished. A new deployment (or rollback) is a new record.
var transitions = map[Status][]Status{
	StatusQueued:    {StatusBuilding, StatusFailed, StatusCancelled},
	StatusBuilding:  {StatusDeploying, StatusFailed, StatusCancelled},
	StatusDeploying: {StatusRunning, StatusFailed, StatusCancelled},
	StatusRunning:   nil,
	StatusFailed:    nil,
	StatusCancelled: nil,
}

// Valid reports whether s is a known status.
func (s Status) Valid() bool { _, ok := transitions[s]; return ok }

// Terminal reports whether s is a final state.
func (s Status) Terminal() bool { return s.Valid() && len(transitions[s]) == 0 }

// Active reports whether a deployment in s is still in progress. At most one
// active deployment may exist per environment (enforced by a unique index).
func (s Status) Active() bool { return s.Valid() && !s.Terminal() }

// CanTransition reports whether from -> to is allowed.
func CanTransition(from, to Status) bool {
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// predecessors returns every status that may transition to `to`. Used to
// build the compare-and-swap UPDATE (... WHERE status = ANY(predecessors)).
func predecessors(to Status) []string {
	var out []string
	for from, nexts := range transitions {
		for _, n := range nexts {
			if n == to {
				out = append(out, string(from))
			}
		}
	}
	return out
}

// TransitionError reports a rejected status change.
type TransitionError struct {
	From, To Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid deployment transition %s -> %s", e.From, e.To)
}
