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
	var contract struct {
		Runs  []statusContract[RunStatus]     `json:"runs"`
		Nodes []statusContract[RunNodeStatus] `json:"nodes"`
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
	for _, row := range contract.Nodes {
		if got := row.Status.Active(); got != row.Active {
			t.Errorf("RunNodeStatus(%q).Active() = %v, want %v", row.Status, got, row.Active)
		}
		if got := row.Status.Terminal(); got != row.Terminal {
			t.Errorf("RunNodeStatus(%q).Terminal() = %v, want %v", row.Status, got, row.Terminal)
		}
	}
}
