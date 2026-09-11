package vibekit

import "testing"

// TestTurnSourcePredicates decides all five predicates for every member of the
// enum. The count is DERIVED from turnSourceCount rather than written twice, so a
// member added to the const block fails here instead of silently answering false
// for a predicate nobody decided about it — the mutation that measured this gap
// was widening PromptClass to a wire-opened source, which left the whole tree
// green.
func TestTurnSourcePredicates(t *testing.T) {
	rows := []struct {
		name                                                                        string
		src                                                                         TurnOpenSource
		promptClass, userAnswered, acknowledgeable, engineOpened, clientVisibleTurn bool
	}{
		{"prompt", TurnSourcePrompt, true, true, true, false, true},
		{"localShell", TurnSourceLocalShell, false, false, false, false, true},
		{"wireTurnStart", TurnSourceWireTurnStart, false, false, false, true, false},
		{"emptyRetry", TurnSourceEmptyRetry, true, true, true, false, true},
		{"workflowStep", TurnSourceWorkflowStep, false, false, false, true, false},
	}
	if len(rows) != int(turnSourceCount) {
		t.Fatalf("the table covers %d sources, the enum has %d: decide every predicate for the new member",
			len(rows), turnSourceCount)
	}
	// A duplicated src would let a member go unasserted while the count still passes.
	seen := make(map[TurnOpenSource]string, len(rows))
	for _, row := range rows {
		if prev, dup := seen[row.src]; dup {
			t.Fatalf("row %q repeats the source row %q already covers", row.name, prev)
		}
		seen[row.src] = row.name
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			if got := row.src.PromptClass(); got != row.promptClass {
				t.Errorf("PromptClass() = %v, want %v", got, row.promptClass)
			}
			if got := row.src.UserAnswered(); got != row.userAnswered {
				t.Errorf("UserAnswered() = %v, want %v", got, row.userAnswered)
			}
			if got := row.src.Acknowledgeable(); got != row.acknowledgeable {
				t.Errorf("Acknowledgeable() = %v, want %v", got, row.acknowledgeable)
			}
			if got := row.src.EngineOpened(); got != row.engineOpened {
				t.Errorf("EngineOpened() = %v, want %v", got, row.engineOpened)
			}
			if got := row.src.ClientVisibleTurn(); got != row.clientVisibleTurn {
				t.Errorf("ClientVisibleTurn() = %v, want %v", got, row.clientVisibleTurn)
			}
		})
	}
}
