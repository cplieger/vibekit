package permissions

// AutoDecision represents the outcome of an automatic permission
// policy evaluation. The translate handler acts on the decision
// (respond to kiro-cli, broadcast events) without embedding policy
// logic.
type AutoDecision int

const (
	// DecisionNone means no automatic decision was made; the
	// permission should be surfaced to the user.
	DecisionNone AutoDecision = iota
	// DecisionAllow means the permission should be auto-approved.
	DecisionAllow
	// DecisionDeny means the permission should be auto-rejected.
	DecisionDeny
)

// AutoDecideCrew evaluates whether a crew (subagent) permission
// request should be auto-approved based on the chat's
// AutoApproveCrew flag. Returns DecisionAllow if the flag is set
// and an allow_once option exists; DecisionNone otherwise.
func AutoDecideCrew(autoApproveCrew bool, hasAllowOnce bool) AutoDecision {
	if !autoApproveCrew {
		return DecisionNone
	}
	if !hasAllowOnce {
		return DecisionNone
	}
	return DecisionAllow
}

// AutoDecideShell evaluates whether a shell command permission
// request should be auto-approved or auto-denied based on the
// shell safety policy. Returns DecisionAllow, DecisionDeny, or
// DecisionNone (prompt the user).
func AutoDecideShell(decision ShellDecision, hasAllowOnce bool) AutoDecision {
	switch decision {
	case ShellAllow:
		if !hasAllowOnce {
			return DecisionNone
		}
		return DecisionAllow
	case ShellDeny:
		return DecisionDeny
	}
	return DecisionNone
}
