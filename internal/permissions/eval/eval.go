// Package eval implements the pure shell-command evaluation pipeline.
// It classifies shell commands as allow/ask/deny based on a policy and
// a set of command rules. No I/O, no config reads — pure logic.
package eval

import "strings"

// ShellPolicy controls how shell commands are handled.
type ShellPolicy string

const (
	PolicyNone ShellPolicy = "no_commands"   // all shell commands rejected
	PolicySafe ShellPolicy = "safe_commands" // read-only auto-approve, destructive prompt
	PolicyAll  ShellPolicy = "all_commands"  // all commands auto-approve
)

// ShellDecision is the result of EvaluateShellCommand.
type ShellDecision string

// Shell command evaluation results.
const (
	ShellAllow ShellDecision = "allow"
	ShellAsk   ShellDecision = "ask"
	ShellDeny  ShellDecision = "deny"
)

// SafeMatchMode describes how a rule pattern matches against the
// input command or argv token.
type SafeMatchMode int

const (
	// BaseExact matches the first whitespace-separated token exactly.
	BaseExact SafeMatchMode = iota
	// Prefix matches the command start with a hard word boundary.
	Prefix
	// TokenExact matches an argv token by full string equality.
	TokenExact
	// TokenPrefix matches an argv token by strings.HasPrefix.
	TokenPrefix
	// ShortPrefix matches a 2-char short option with trailing value.
	ShortPrefix
)

// SafeCommandRule is one entry in the safe-command allow-list.
type SafeCommandRule struct {
	Pattern string
	Mode    SafeMatchMode
}

// RuleMatcher is the interface that the evaluation pipeline uses to
// check whether a command matches allow/deny rules. This decouples
// the pure evaluation from the CommandRules storage type.
type RuleMatcher interface {
	// Evaluate returns the highest-priority matching rule's mode and
	// whether any rule matched. A single call answers both "is it
	// deny?" and "is it allow?" without scanning the rule set twice.
	Evaluate(command string) (mode string, matched bool)
	MatchesAllow(command string) bool
	MatchesDeny(command string) bool
}

// safeCommandRules is the unified table of commands that auto-approve
// in "safe_commands" mode.
var safeCommandRules = []SafeCommandRule{
	// Single-word read-only commands (BaseExact).
	{"ls", BaseExact},
	{"cat", BaseExact},
	{"head", BaseExact},
	{"tail", BaseExact},
	{"wc", BaseExact},
	{"grep", BaseExact},
	{"rg", BaseExact},
	{"echo", BaseExact},
	{"pwd", BaseExact},
	{"which", BaseExact},
	{"printenv", BaseExact},
	{"date", BaseExact},
	{"whoami", BaseExact},
	{"hostname", BaseExact},
	{"uname", BaseExact},
	{"file", BaseExact},
	{"stat", BaseExact},
	{"du", BaseExact},
	{"df", BaseExact},
	// Multi-word safe command prefixes (Prefix).
	{"git status", Prefix},
	{"git log", Prefix},
	{"git diff", Prefix},
	{"git show", Prefix},
	{"go version", Prefix},
	{"node --version", Prefix},
	{"python --version", Prefix},
	{"npm list", Prefix},
	{"cargo --version", Prefix},
}

// safeCommandIndex is the lookup map for BaseExact rules, built at init.
var safeCommandIndex = func() map[string]bool {
	m := make(map[string]bool)
	for _, r := range safeCommandRules {
		if r.Mode == BaseExact {
			m[r.Pattern] = true
		}
	}
	return m
}()

// writeOption is one entry in the write-option detection table.
type writeOption struct {
	token string
	mode  SafeMatchMode
}

// Matches reports whether tok matches this write-option rule.
func (wo writeOption) Matches(tok string) bool {
	switch wo.mode {
	case TokenExact:
		return tok == wo.token
	case TokenPrefix:
		return strings.HasPrefix(tok, wo.token)
	case ShortPrefix:
		return tok == wo.token || (len(tok) > 2 && strings.HasPrefix(tok, wo.token))
	default:
		return false
	}
}

// writeOptions is the unified table of argv tokens that turn a
// read-only command into a file-write primitive.
var writeOptions = []writeOption{
	{"--output", TokenExact},
	{"--output=", TokenPrefix},
	{"--out-file", TokenExact},
	{"--out-file=", TokenPrefix},
	{"--write", TokenExact},
	{"--write=", TokenPrefix},
	{"--write-file", TokenExact},
	{"--write-file=", TokenPrefix},
	{"-o", ShortPrefix},
	{"-O", ShortPrefix},
}

// HasWriteOption reports whether command contains any argv token
// that grants the command a file-write side effect.
func HasWriteOption(command string) bool {
	for _, tok := range ShellFields(command) {
		for _, wo := range writeOptions {
			if wo.Matches(tok) {
				return true
			}
		}
	}
	return false
}

// HasSafePrefix reports whether command is either exactly the prefix
// or starts with the prefix followed by a space/tab.
func HasSafePrefix(command, prefix string) bool {
	if command == prefix {
		return true
	}
	if !strings.HasPrefix(command, prefix) {
		return false
	}
	if len(command) <= len(prefix) {
		return false
	}
	next := command[len(prefix)]
	return next == ' ' || next == '\t'
}

// ExtractBaseCommand returns the first token of a command string,
// respecting shell quoting.
func ExtractBaseCommand(command string) string {
	fields := ShellFields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// checkKind classifies a pipeline entry's role in the evaluation.
type checkKind int

const (
	disqualifier checkKind = iota
	allowlist
)

// commandCheck is one entry in the safe-command evaluation pipeline.
type commandCheck struct {
	check func(command string) bool
	name  string
	kind  checkKind
}

// safeCommandChecks is the declarative pipeline for safe_commands mode.
var safeCommandChecks = []commandCheck{
	{
		name:  "shell-metacharacter",
		kind:  disqualifier,
		check: MetaGuard.CommandDisqualified,
	},
	{
		name:  "write-option",
		kind:  disqualifier,
		check: HasWriteOption,
	},
	{
		name: "safe-command-rule",
		kind: allowlist,
		check: func(command string) bool {
			if safeCommandIndex[ExtractBaseCommand(command)] {
				return true
			}
			for _, r := range safeCommandRules {
				if r.Mode == Prefix && HasSafePrefix(command, r.Pattern) {
					return true
				}
			}
			return false
		},
	},
}

// EvaluateShellCommand applies the shell policy to a command and
// returns the decision. Pure function — no I/O.
func EvaluateShellCommand(policy ShellPolicy, command string, rules RuleMatcher) ShellDecision {
	// Single rule evaluation: answers both deny and allow in one pass.
	var ruleMode string
	var ruleMatched bool
	if rules != nil {
		ruleMode, ruleMatched = rules.Evaluate(command)
	}

	if ruleMatched && ruleMode == "deny" {
		if policy == PolicyNone {
			return ShellDeny
		}
		return ShellAsk
	}
	switch policy {
	case PolicyNone:
		return ShellDeny
	case PolicyAll:
		return ShellAllow
	case PolicySafe:
		return evaluateSafeCommandWithRule(command, ruleMode, ruleMatched)
	default:
		return ShellAsk
	}
}

// EvaluateSafeCommand applies the safe_commands policy by walking the
// declarative check table.
func EvaluateSafeCommand(command string, rules RuleMatcher) ShellDecision {
	var ruleMode string
	var ruleMatched bool
	if rules != nil {
		ruleMode, ruleMatched = rules.Evaluate(command)
	}
	return evaluateSafeCommandWithRule(command, ruleMode, ruleMatched)
}

// evaluateSafeCommandWithRule is the inner safe-command evaluator that
// uses a pre-computed rule result to avoid rescanning.
func evaluateSafeCommandWithRule(command, ruleMode string, ruleMatched bool) ShellDecision {
	for _, c := range safeCommandChecks {
		if !c.check(command) {
			continue
		}
		switch c.kind {
		case allowlist:
			return ShellAllow
		case disqualifier:
			return resolveExplicitAllowWithRule(ruleMode, ruleMatched)
		}
	}
	return resolveExplicitAllowWithRule(ruleMode, ruleMatched)
}

// ResolveExplicitAllow handles the "disqualified by policy but the
// user has explicitly allowed this exact command" escape hatch.
func ResolveExplicitAllow(command string, rules RuleMatcher) ShellDecision {
	if rules != nil {
		mode, matched := rules.Evaluate(command)
		return resolveExplicitAllowWithRule(mode, matched)
	}
	return ShellAsk
}

// resolveExplicitAllowWithRule uses a pre-computed rule result.
func resolveExplicitAllowWithRule(ruleMode string, ruleMatched bool) ShellDecision {
	if ruleMatched && ruleMode == "allow" {
		return ShellAllow
	}
	return ShellAsk
}
