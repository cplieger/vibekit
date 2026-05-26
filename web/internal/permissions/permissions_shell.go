package permissions

import (
	"context"
	"encoding/json"
	"log/slog"
)

// shellPolicy controls how shell commands are handled.
type shellPolicy string

const (
	policyNone shellPolicy = "no_commands"   // all shell commands rejected
	policySafe shellPolicy = "safe_commands" // read-only auto-approve, destructive prompt
	policyAll  shellPolicy = "all_commands"  // all commands auto-approve
)

// ShellDecision is the result of EvaluateShellCommand.
type ShellDecision string

// Shell command evaluation results.
const (
	ShellAllow ShellDecision = "allow"
	ShellAsk   ShellDecision = "ask"
	ShellDeny  ShellDecision = "deny"
)

// EvaluateShellCommand checks whether a shell command should be
// auto-approved, prompted, or rejected based on the shell policy
// and the command rule list.
func EvaluateShellCommand(ctx context.Context, configDir, command string, rules *CommandRules) ShellDecision {
	policy := readShellPolicy(ctx, configDir)
	decision := evaluateShellCommand(policy, command, rules)
	slog.Debug("permissions: shell policy decision",
		"command", command, "policy", policy, "decision", decision)
	return decision
}

// Valid reports whether p is a recognised shell policy value.
func (p shellPolicy) Valid() bool {
	switch p {
	case policyNone, policySafe, policyAll:
		return true
	}
	return false
}

// readShellPolicy reads shell_policy from config.json using the
// shared readSettingsRaw helper.
func readShellPolicy(ctx context.Context, configDir string) shellPolicy {
	raw, err := readSettingsRaw(ctx, configDir)
	if err != nil {
		slog.Warn("permissions: read config.json for shell_policy", "error", err)
		return policySafe
	}
	if raw == nil {
		return policySafe
	}
	v, ok := raw["shell_policy"]
	if !ok {
		return policySafe
	}
	var policy shellPolicy
	if err := json.Unmarshal(v, &policy); err != nil {
		slog.Warn("permissions: parse shell_policy", "error", err)
		return policySafe
	}
	if policy == "" {
		return policySafe
	}
	if !policy.Valid() {
		slog.Warn("permissions: unknown shell_policy, defaulting to safe_commands", "value", string(policy))
		return policySafe
	}
	return policy
}
