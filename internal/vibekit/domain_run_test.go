package vibekit

import (
	"encoding/json"
	"os"
	"testing"
)

type statusContract[T ~string] struct {
	Status   T    `json:"status"`
	Active   bool `json:"active"`
	Terminal bool `json:"terminal"`
}

func TestRunStatusContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/run_statuses.json")
	if err != nil {
		t.Fatalf("read run-status contract: %v", err)
	}
	// Runs only: the node half of the fixture declares a VOCABULARY, checked against
	// KAS's enum by internal/kascap's census, and RunNodeStatus carries no predicate
	// to exercise here.
	var contract struct {
		Runs []statusContract[RunStatus] `json:"runs"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("decode run-status contract: %v", err)
	}

	for _, row := range contract.Runs {
		if got := row.Status.Active(); got != row.Active {
			t.Errorf("RunStatus(%q).Active() = %v, want %v", row.Status, got, row.Active)
		}
		if got := row.Status.Terminal(); got != row.Terminal {
			t.Errorf("RunStatus(%q).Terminal() = %v, want %v", row.Status, got, row.Terminal)
		}
	}
}

// TestRunStatusCancelledIsTerminal pins the one status the shared fixture cannot
// carry: `cancelled` is a DECLARED EXTRA (internal/kascap's declaredExtras records
// why), and that census asserts the fixture equals KAS's enum exactly, so a row for
// a status the bundle does not name would fail it. Terminal is the safe reading —
// every consumer of this predicate errs toward "the run is over", so one value too
// wide releases a lease early while one too narrow strands the lease AND wedges the
// recipe under the single-run rule with no clearing path.
func TestRunStatusCancelledIsTerminal(t *testing.T) {
	if !RunStatusCancelled.Terminal() {
		t.Error("RunStatus(cancelled).Terminal() = false, want true")
	}
	if RunStatusCancelled.Active() {
		t.Error("RunStatus(cancelled).Active() = true, want false")
	}
}
