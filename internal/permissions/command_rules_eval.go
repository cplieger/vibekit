package permissions

import "strings"

// EvaluateCommand checks all matching rules and returns the decision
// from the highest-priority match. At equal priority, deny wins.
// Returns ("", false) if no rule matches.
func (r *CommandRules) EvaluateCommand(command string) (RuleMode, bool) {
	entries := *r.entriesPtr.Load()
	command = strings.TrimSpace(command)

	bestPriority := -1
	var bestMode RuleMode
	matched := false

	for _, e := range entries {
		if !matchPattern(e.Pattern, command) {
			continue
		}
		if !matched || e.Priority > bestPriority ||
			(e.Priority == bestPriority && e.Mode == RuleDeny) {
			bestPriority = e.Priority
			bestMode = e.Mode
			matched = true
		}
	}
	return bestMode, matched
}

// Evaluate satisfies the eval.RuleMatcher interface. It calls
// EvaluateCommand once and returns the mode as a string.
func (r *CommandRules) Evaluate(command string) (string, bool) {
	mode, matched := r.EvaluateCommand(command)
	return string(mode), matched
}

// MatchesAllow returns true if the command's highest-priority matching
// rule is an allow rule. Uses priority-based evaluation: higher priority
// wins; at equal priority, deny wins over allow.
func (r *CommandRules) MatchesAllow(command string) bool {
	mode, matched := r.EvaluateCommand(command)
	return matched && mode == RuleAllow
}

// MatchesDeny returns true if the command's highest-priority matching
// rule is a deny rule. Uses priority-based evaluation.
func (r *CommandRules) MatchesDeny(command string) bool {
	mode, matched := r.EvaluateCommand(command)
	return matched && mode == RuleDeny
}
