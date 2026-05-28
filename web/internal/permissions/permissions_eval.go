package permissions

import "vibekit/internal/permissions/eval"

// Re-export types from the eval sub-package for backward compatibility.
type SafeMatchMode = eval.SafeMatchMode

const (
	BaseExact   = eval.BaseExact
	Prefix      = eval.Prefix
	TokenExact  = eval.TokenExact
	TokenPrefix = eval.TokenPrefix
	ShortPrefix = eval.ShortPrefix
)

// SafeCommandRule is re-exported from the eval sub-package.
type SafeCommandRule = eval.SafeCommandRule

// ShellDecision is re-exported from the eval sub-package.
type ShellDecision = eval.ShellDecision

const (
	ShellAllow = eval.ShellAllow
	ShellAsk   = eval.ShellAsk
	ShellDeny  = eval.ShellDeny
)

// evaluateShellCommand delegates to the eval sub-package.
func evaluateShellCommand(policy shellPolicy, command string, rules *CommandRules) ShellDecision {
	var matcher eval.RuleMatcher
	if rules != nil {
		matcher = rules
	}
	return eval.EvaluateShellCommand(eval.ShellPolicy(policy), command, matcher)
}

// evaluateSafeCommand delegates to the eval sub-package.
func evaluateSafeCommand(command string, rules *CommandRules) ShellDecision {
	var matcher eval.RuleMatcher
	if rules != nil {
		matcher = rules
	}
	return eval.EvaluateSafeCommand(command, matcher)
}

// resolveExplicitAllow delegates to the eval sub-package.
func resolveExplicitAllow(command string, rules *CommandRules) ShellDecision {
	var matcher eval.RuleMatcher
	if rules != nil {
		matcher = rules
	}
	return eval.ResolveExplicitAllow(command, matcher)
}

// hasWriteOption delegates to the eval sub-package.
func hasWriteOption(command string) bool {
	return eval.HasWriteOption(command)
}

// hasSafePrefix delegates to the eval sub-package.
func hasSafePrefix(command, prefix string) bool {
	return eval.HasSafePrefix(command, prefix)
}

// extractBaseCommand delegates to the eval sub-package.
func extractBaseCommand(command string) string {
	return eval.ExtractBaseCommand(command)
}
