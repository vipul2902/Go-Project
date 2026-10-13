package deployments

import (
	"slices"
	"testing"
)

var all = []Status{StatusQueued, StatusBuilding, StatusDeploying, StatusRunning, StatusFailed, StatusCancelled}

// TestTransitionMatrix checks every one of the 36 (from, to) pairs against
// the intended state machine, so any accidental change to the table fails.
func TestTransitionMatrix(t *testing.T) {
	allowed := map[[2]Status]bool{
		{StatusQueued, StatusBuilding}:     true,
		{StatusQueued, StatusFailed}:       true,
		{StatusQueued, StatusCancelled}:    true,
		{StatusBuilding, StatusDeploying}:  true,
		{StatusBuilding, StatusFailed}:     true,
		{StatusBuilding, StatusCancelled}:  true,
		{StatusDeploying, StatusRunning}:   true,
		{StatusDeploying, StatusFailed}:    true,
		{StatusDeploying, StatusCancelled}: true,
	}
	for _, from := range all {
		for _, to := range all {
			want := allowed[[2]Status{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestNamedInvalidTransitions(t *testing.T) {
	for _, c := range [][2]Status{
		{StatusQueued, StatusRunning},     // must build and deploy first
		{StatusQueued, StatusDeploying},   // cannot skip the build
		{StatusRunning, StatusQueued},     // terminal states never change
		{StatusFailed, StatusBuilding},    // no resurrecting failures
		{StatusCancelled, StatusRunning},  // cancelled stays cancelled
		{StatusRunning, StatusFailed},     // a finished deploy cannot fail later
		{StatusBuilding, StatusBuilding},  // no self-loops
		{Status("BOGUS"), StatusBuilding}, // unknown states have no edges
	} {
		if CanTransition(c[0], c[1]) {
			t.Errorf("%s -> %s should be invalid", c[0], c[1])
		}
	}
}

func TestTerminalAndActive(t *testing.T) {
	for _, s := range all {
		terminal := s == StatusRunning || s == StatusFailed || s == StatusCancelled
		if s.Terminal() != terminal || s.Active() == terminal {
			t.Errorf("%s: Terminal=%v Active=%v", s, s.Terminal(), s.Active())
		}
	}
	if Status("BOGUS").Valid() || Status("BOGUS").Active() || Status("BOGUS").Terminal() {
		t.Error("unknown status treated as known")
	}
}

func TestPredecessors(t *testing.T) {
	got := predecessors(StatusCancelled)
	slices.Sort(got)
	if want := []string{"BUILDING", "DEPLOYING", "QUEUED"}; !slices.Equal(got, want) {
		t.Errorf("predecessors(CANCELLED) = %v, want %v", got, want)
	}
	if got := predecessors(StatusRunning); !slices.Equal(got, []string{"DEPLOYING"}) {
		t.Errorf("predecessors(RUNNING) = %v", got)
	}
	if got := predecessors(StatusQueued); len(got) != 0 {
		t.Errorf("nothing may transition back to QUEUED, got %v", got)
	}
}

// The database CHECK constraint (terminal <=> completed_at set) mirrors
// Terminal(); keep the two definitions in sync.
func TestTerminalMatchesSchemaConstraint(t *testing.T) {
	schemaTerminal := []Status{StatusRunning, StatusFailed, StatusCancelled}
	for _, s := range all {
		if s.Terminal() != slices.Contains(schemaTerminal, s) {
			t.Errorf("%s terminal mismatch with migrations/00001 CHECK", s)
		}
	}
}
