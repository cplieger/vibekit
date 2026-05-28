package permissions

import (
	"context"
	"encoding/json"
	"log/slog"

	"vibekit/internal/permissions/eval"
)

// shellPolicy is the local alias for the eval sub-package's ShellPolicy.
type shellPolicy = eval.ShellPolicy

const (
	policyNone shellPolicy = "no_commands"
	policySafe shellPolicy = "safe_commands"
	policyAll  shellPolicy = "all_commands"
)

// ShellEvalResult pairs a ShellDecision with the reason that triggered
// it, making the decision inspectable without re-evaluation.
type ShellEvalResult struct {
	Decision ShellDecision
	Reason   string // e.g. "policy:all_commands", "policy:no_commands", "rule:allow", "builtin:safe", "default:ask"
}

// EvaluateShellCommand checks whether a shell command should be
// auto-approved, prompted, or rejected based on the shell policy
// and the command rule list. Returns a ShellEvalResult pairing the
// decision with the reason.
func EvaluateShellCommand(ctx context.Context, configDir, command string, rules *CommandRules) ShellEvalResult {
	policy := readShellPolicy(ctx, configDir)

	// Single rule evaluation up front — avoids redundant scans.
	var ruleMode string
	var ruleMatched bool
	if rules != nil {
		ruleMode, ruleMatched = rules.Evaluate(command)
	}

	var matcher eval.RuleMatcher
	if rules != nil {
		matcher = rules
	}
	decision := eval.EvaluateShellCommand(eval.ShellPolicy(policy), command, matcher)
	reason := shellEvalReason(policy, decision, ruleMode, ruleMatched)
	slog.Debug("permissions: shell policy decision",
		"command", command, "policy", policy, "decision", decision, "reason", reason)
	return ShellEvalResult{Decision: decision, Reason: reason}
}

// shellEvalReason derives a short reason tag for the decision using
// the pre-computed rule evaluation result, avoiding redundant scans.
func shellEvalReason(policy shellPolicy, decision ShellDecision, ruleMode string, ruleMatched bool) string {
	switch {
	case ruleMatched && ruleMode == "deny":
		return "rule:deny"
	case policy == policyNone:
		return "policy:no_commands"
	case policy == policyAll:
		return "policy:all_commands"
	case decision == ShellAllow && ruleMatched && ruleMode == "allow":
		return "rule:allow"
	case decision == ShellAllow:
		return "builtin:safe"
	default:
		return "default:ask"
	}
}

// Valid reports whether p is a recognised shell policy value.
func validShellPolicy(p shellPolicy) bool {
	switch p {
	case policyNone, policySafe, policyAll:
		return true
	}
	return false
}

// readShellPolicy reads shell_policy from config.json using the
// shared readPermissionSettings helper.
func readShellPolicy(ctx context.Context, configDir string) shellPolicy {
	ps, err := readPermissionSettings(ctx, configDir)
	if err != nil {
		slog.Warn("permissions: read config.json for shell_policy", "error", err)
		return policySafe
	}
	if !ps.hasShell {
		return policySafe
	}
	var policy shellPolicy
	if err := json.Unmarshal(ps.shellPolicy, &policy); err != nil {
		slog.Warn("permissions: parse shell_policy", "error", err)
		return policySafe
	}
	if policy == "" {
		return policySafe
	}
	if !validShellPolicy(policy) {
		slog.Warn("permissions: unknown shell_policy, defaulting to safe_commands", "value", string(policy))
		return policySafe
	}
	return policy
}
