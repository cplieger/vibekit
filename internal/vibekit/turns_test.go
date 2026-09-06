package vibekit

import (
	"encoding/json"
	"os"
	"testing"
)

// severityFixture mirrors testdata/turn_severity.json. See that file's _comment
// for why the table is shared with the TypeScript side rather than duplicated.
type severityFixture struct {
	Cases []struct {
		Outcome  string `json:"outcome"`
		Severity string `json:"severity"`
		Reason   string `json:"default_reason"`
	} `json:"cases"`
}

// everyTurnOutcome is every declared TurnOutcome constant. Hand-listed rather
// than derived, because there is nothing to derive it from — a Go const block is
// not enumerable at runtime — and that is exactly why the coverage assertion below
// matters: this list and the fixture must agree, so an outcome added to the type
// and forgotten in one of the two fails here.
var everyTurnOutcome = []TurnOutcome{
	TurnOutcomeRunning,
	TurnOutcomeCompleted,
	TurnOutcomeCancelled,
	TurnOutcomeInterrupted,
	TurnOutcomeFailed,
	TurnOutcomeRefused,
	TurnOutcomeUnknown,
}

func loadSeverityFixture(t *testing.T) severityFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/turn_severity.json")
	if err != nil {
		t.Fatalf("read testdata/turn_severity.json: %v", err)
	}
	var fx severityFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode testdata/turn_severity.json: %v", err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("testdata/turn_severity.json has no cases")
	}
	return fx
}

// TestTurnSeverityContract is one half of a cross-language pin:
// turn-severity.node.test.ts runs the same table against the TypeScript
// implementation. A rule changed in only one language fails in the other.
func TestTurnSeverityContract(t *testing.T) {
	for _, c := range loadSeverityFixture(t).Cases {
		t.Run(c.Outcome, func(t *testing.T) {
			o := TurnOutcome(c.Outcome)
			if got := SeverityOf(o); string(got) != c.Severity {
				t.Errorf("SeverityOf(%q) = %q, want %q", c.Outcome, got, c.Severity)
			}
			if got := DefaultFailureReason(o); got != c.Reason {
				t.Errorf("DefaultFailureReason(%q) = %q, want %q", c.Outcome, got, c.Reason)
			}
		})
	}
}

// TestTurnSeverityContract_CoversEveryOutcome is the other direction, and it is
// what makes the table a contract rather than a sample: an eighth TurnOutcome
// added without a fixture row fails here instead of reaching five client surfaces
// with no case for it.
func TestTurnSeverityContract_CoversEveryOutcome(t *testing.T) {
	inFixture := make(map[string]bool)
	for _, c := range loadSeverityFixture(t).Cases {
		if inFixture[c.Outcome] {
			t.Errorf("outcome %q appears twice in the fixture", c.Outcome)
		}
		inFixture[c.Outcome] = true
	}
	for _, o := range everyTurnOutcome {
		if !inFixture[string(o)] {
			t.Errorf("TurnOutcome %q has no row in testdata/turn_severity.json", o)
		}
		delete(inFixture, string(o))
	}
	for extra := range inFixture {
		t.Errorf("fixture row %q names no declared TurnOutcome", extra)
	}
}

// TestSeverityOf_GradesEveryOutcomeAndNeverClean pins the two properties the
// table's rows cannot state between them.
//
// Totality first: every declared outcome must grade to one of the four declared
// severities, so a missing case arm cannot answer the zero value — which is not
// "clean", but is not a severity any surface has a branch for either.
//
// Then the direction that matters: NO outcome but `completed` may grade `clean`.
// That is the one direction a status mark must not fail in, and it is the defect
// this whole type exists to remove — `interrupted` graded as nothing at all, so a
// broken turn painted the hollow ring that means "nothing is happening here".
func TestSeverityOf_GradesEveryOutcomeAndNeverClean(t *testing.T) {
	declared := map[TurnSeverity]bool{
		TurnSeverityRunning: true,
		TurnSeverityClean:   true,
		TurnSeverityStopped: true,
		TurnSeverityBroken:  true,
	}
	for _, o := range everyTurnOutcome {
		sev := SeverityOf(o)
		if !declared[sev] {
			t.Errorf("SeverityOf(%q) = %q, which is not a declared TurnSeverity", o, sev)
		}
		if sev == TurnSeverityClean && o != TurnOutcomeCompleted {
			t.Errorf("SeverityOf(%q) = clean; only `completed` may read as a turn that worked", o)
		}
	}
}

// TestDefaultFailureReason_SpeaksForEveryOutcomeThatEndedBadly pins the property
// symptom 1 turned on: a turn whose severity is `broken` must have something to
// say, because a card with a red mark and an empty body is what a reader of chat
// c-c4b791… actually got — 26 blocks, a settled `failed` outcome, and no sentence
// anywhere in the record.
//
// The `stopped` half is asserted too, one rung weaker: `cancelled` and `unknown`
// also render a footer, and "Interrupted" with no account beside it is the same
// silence one severity down.
func TestDefaultFailureReason_SpeaksForEveryOutcomeThatEndedBadly(t *testing.T) {
	for _, o := range everyTurnOutcome {
		reason := DefaultFailureReason(o)
		switch SeverityOf(o) {
		case TurnSeverityBroken, TurnSeverityStopped:
			if reason == "" {
				t.Errorf("DefaultFailureReason(%q) is empty; a turn that ended badly must say something", o)
			}
		case TurnSeverityClean, TurnSeverityRunning:
			if reason != "" {
				t.Errorf("DefaultFailureReason(%q) = %q, want empty: nothing went wrong", o, reason)
			}
		}
	}
}
