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

// shellDecisionMap maps each ShellDecision to its corresponding
// AutoDecision outcome. New ShellDecision values must be added here;
// unlisted values fall through to DecisionNone.
var shellDecisionMap = map[ShellDecision]AutoDecision{
	ShellAllow: DecisionAllow,
	ShellDeny:  DecisionDeny,
}

// AutoDecideShell evaluates whether a shell command permission
// request should be auto-approved or auto-denied based on the
// shell safety policy. Returns DecisionAllow, DecisionDeny, or
// DecisionNone (prompt the user).
func AutoDecideShell(decision ShellDecision, hasAllowOnce bool) AutoDecision {
	outcome, ok := shellDecisionMap[decision]
	if !ok {
		return DecisionNone
	}
	// ShellAllow requires an allow_once option to be present.
	if outcome == DecisionAllow && !hasAllowOnce {
		return DecisionNone
	}
	return outcome
}
