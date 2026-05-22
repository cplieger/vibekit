package permissions

import "strings"

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
	// ShortPrefix matches a 2-char short option with trailing value (e.g. -oFILE).
	ShortPrefix
)

// SafeCommandRule is one entry in the safe-command allow-list. Pattern
// is matched according to Mode. Adding a new safe command is a single
// line; match semantics are explicit per-entry.
type SafeCommandRule struct {
	Pattern string
	Mode    SafeMatchMode
}

// safeCommandRules is the unified table of commands that auto-approve
// in "safe_commands" mode. Replaces the former safeCommands map and
// safeCommandPrefixes slice with a single inspectable data structure.
var safeCommandRules = []SafeCommandRule{
	// Single-word read-only commands (BaseExact).
	{"ls", BaseExact}, {"cat", BaseExact}, {"head", BaseExact},
	{"tail", BaseExact}, {"wc", BaseExact}, {"grep", BaseExact},
	{"rg", BaseExact}, {"echo", BaseExact}, {"pwd", BaseExact},
	{"which", BaseExact}, {"printenv", BaseExact}, {"date", BaseExact},
	{"whoami", BaseExact}, {"hostname", BaseExact}, {"uname", BaseExact},
	{"file", BaseExact}, {"stat", BaseExact}, {"du", BaseExact}, {"df", BaseExact},
	// Multi-word safe command prefixes (Prefix).
	{"git status", Prefix}, {"git log", Prefix}, {"git diff", Prefix},
	{"git show", Prefix}, {"go version", Prefix}, {"node --version", Prefix},
	{"python --version", Prefix}, {"npm list", Prefix}, {"cargo --version", Prefix},
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
// Uses SafeMatchMode for match semantics, unifying with the
// safe-command rule pattern.
type writeOption struct {
	token string
	mode  SafeMatchMode
}

// writeOptions is the unified table of argv tokens that turn a
// read-only command into a file-write primitive. Adding a new option
// is a single line; match semantics are explicit per-entry.
var writeOptions = []writeOption{
	{"--output", TokenExact}, {"--output=", TokenPrefix},
	{"--out-file", TokenExact}, {"--out-file=", TokenPrefix},
	{"--write", TokenExact}, {"--write=", TokenPrefix},
	{"--write-file", TokenExact}, {"--write-file=", TokenPrefix},
	{"-o", ShortPrefix}, {"-O", ShortPrefix},
}

// hasWriteOption reports whether command contains any argv token
// that grants the command a file-write side effect.
func hasWriteOption(command string) bool {
	for _, tok := range shellFields(command) {
		for _, wo := range writeOptions {
			switch wo.mode {
			case TokenExact:
				if tok == wo.token {
					return true
				}
			case TokenPrefix:
				if strings.HasPrefix(tok, wo.token) {
					return true
				}
			case ShortPrefix:
				if tok == wo.token || (len(tok) > 2 && strings.HasPrefix(tok, wo.token)) {
					return true
				}
			}
		}
	}
	return false
}

// hasSafePrefix reports whether command is either exactly the prefix
// or starts with the prefix followed by a space/tab.
func hasSafePrefix(command, prefix string) bool {
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

// extractBaseCommand returns the first token of a command string,
// respecting shell quoting so `"git" push` extracts `git` not `"git"`.
func extractBaseCommand(command string) string {
	fields := shellFields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// checkKind classifies a pipeline entry's role in the evaluation.
type checkKind int

const (
	// disqualifier entries route to the explicit-allow escape hatch on match.
	disqualifier checkKind = iota
	// allowlist entries return ShellAllow directly on match.
	allowlist
)

// commandCheck is one entry in the safe-command evaluation pipeline.
// Each check inspects the command and returns true if it matches.
// The pipeline's evaluation function uses kind to determine the
// outcome: disqualifiers route to the explicit-allow escape hatch,
// allowlist hits return directly.
type commandCheck struct {
	check func(command string) bool
	name  string
	kind  checkKind
}

// safeCommandChecks is the declarative pipeline for safe_commands mode.
// Evaluation order matters: disqualifiers first, then allow-lists.
// Each entry is inspectable (name + kind), testable per-check, and
// extensible without editing the orchestration function.
var safeCommandChecks = []commandCheck{
	{
		name:  "shell-metacharacter",
		kind:  disqualifier,
		check: metaGuard.CommandDisqualified,
	},
	{
		name:  "write-option",
		kind:  disqualifier,
		check: hasWriteOption,
	},
	{
		name: "safe-command-rule",
		kind: allowlist,
		check: func(command string) bool {
			// Fast path: exact base-command lookup.
			if safeCommandIndex[extractBaseCommand(command)] {
				return true
			}
			// Slow path: prefix rules.
			for _, r := range safeCommandRules {
				if r.Mode == Prefix && hasSafePrefix(command, r.Pattern) {
					return true
				}
			}
			return false
		},
	},
}

// evaluateShellCommand applies the shell policy to a command and
// returns the decision. Pure function — no I/O.
func evaluateShellCommand(policy shellPolicy, command string, rules *CommandRules) ShellDecision {
	if rules != nil && rules.MatchesDeny(command) {
		if policy == policyNone {
			return ShellDeny
		}
		return ShellAsk
	}
	switch policy {
	case policyNone:
		return ShellDeny
	case policyAll:
		return ShellAllow
	case policySafe:
		return evaluateSafeCommand(command, rules)
	default:
		return ShellAsk
	}
}

// evaluateSafeCommand applies the safe_commands policy by walking the
// declarative check table. Disqualifiers (metachar, write-option) route
// to the explicit-allow escape hatch; allow-list hits return directly;
// unmatched commands fall through to the explicit-allow escape hatch.
func evaluateSafeCommand(command string, rules *CommandRules) ShellDecision {
	for _, c := range safeCommandChecks {
		if !c.check(command) {
			continue
		}
		switch c.kind {
		case allowlist:
			return ShellAllow
		case disqualifier:
			return resolveExplicitAllow(command, rules)
		}
	}
	return resolveExplicitAllow(command, rules)
}

// resolveExplicitAllow handles the "disqualified by policy but the
// user has explicitly allowed this exact command" escape hatch.
func resolveExplicitAllow(command string, rules *CommandRules) ShellDecision {
	if rules != nil && rules.MatchesAllow(command) {
		return ShellAllow
	}
	return ShellAsk
}
