package permissions

import "testing"

// FuzzAutoDecideShell verifies the decision mapping function returns only
// valid AutoDecision values and enforces the allow_once gate.
//
// Bug class: exhaustive enum bypass via unknown ShellDecision values.
func FuzzAutoDecideShell(f *testing.F) {
	f.Add(string(ShellAllow), true)
	f.Add(string(ShellAllow), false)
	f.Add(string(ShellDeny), true)
	f.Add(string(ShellDeny), false)
	f.Add("", true)
	f.Add("unknown", false)
	f.Add("ask", true)

	f.Fuzz(func(t *testing.T, decision string, hasAllowOnce bool) {
		result := AutoDecideShell(ShellDecision(decision), hasAllowOnce)

		// Invariant 1: result is always a known AutoDecision.
		switch result {
		case DecisionNone, DecisionAllow, DecisionDeny:
			// ok
		default:
			t.Fatalf("AutoDecideShell(%q, %v) = %v; not a valid AutoDecision", decision, hasAllowOnce, result)
		}

		// Invariant 2: ShellAllow without hasAllowOnce → never DecisionAllow.
		if ShellDecision(decision) == ShellAllow && !hasAllowOnce && result == DecisionAllow {
			t.Fatalf("AutoDecideShell(ShellAllow, false) = DecisionAllow; should be None")
		}

		// Invariant 3: ShellDeny → always DecisionDeny regardless of hasAllowOnce.
		if ShellDecision(decision) == ShellDeny && result != DecisionDeny {
			t.Fatalf("AutoDecideShell(ShellDeny, %v) = %v; should be Deny", hasAllowOnce, result)
		}
	})
}
