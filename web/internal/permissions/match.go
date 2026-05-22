package permissions

import "strings"

// shellMetacharacters disqualify a command from the safe-commands
// auto-approve path. Co-located with metaPolicy which is its sole consumer.
const shellMetacharacters = ";&|`$><\n\r\\\"'"

// metaPolicy encapsulates the metacharacter safety-net invariant shared
// by both the pattern-matching guard (matchPattern) and the pipeline
// disqualifier (safeCommandChecks). The invariant:
//
//	"A metachar-free context (pattern or policy) must not silently
//	 approve a metachar-bearing command."
//
// Two enforcement points reference this policy:
//   - matchPattern: a metachar-free pattern rejects metachar-bearing
//     commands (prevents wildcard allow rules from swallowing shell
//     operators).
//   - safeCommandChecks disqualifier: a metachar-bearing command is
//     routed to the explicit-allow escape hatch (prevents the built-in
//     safe list from auto-approving piped/chained commands).
//
// Both use shellMetacharacters as the character set; metaPolicy makes
// the shared invariant inspectable and testable as a unit.
type metaPolicy struct{}

// CommandDisqualified reports whether command contains shell
// metacharacters and should be routed away from auto-approve paths.
// Used by the pipeline disqualifier in safeCommandChecks.
func (metaPolicy) CommandDisqualified(command string) bool {
	return strings.ContainsAny(command, shellMetacharacters)
}

// PatternRejectsCommand reports whether a metachar-free pattern must
// reject a metachar-bearing command. Used by matchPattern's safety net.
func (metaPolicy) PatternRejectsCommand(pattern, command string) bool {
	return !strings.ContainsAny(pattern, shellMetacharacters) &&
		strings.ContainsAny(command, shellMetacharacters)
}

// metaGuard is the package-level instance of metaPolicy. Both
// matchPattern and safeCommandChecks reference this single policy
// so the metacharacter invariant has one source of truth.
var metaGuard metaPolicy

// matchPattern reports whether command matches pattern. Two behaviours:
//   - Pattern without '*': exact string equality (e.g. "ls" matches
//     only "ls").
//   - Pattern with one or more '*': each '*' matches any sequence;
//     non-star segments must appear in order, the first non-star must
//     anchor the start of command, and the last non-star must anchor
//     the end. So "npm *" matches "npm install" (prefix-style),
//     "* --version" matches "node --version" (suffix-style),
//     "docker * build" matches "docker compose build" (infix).
//
// Metacharacter safety net: when the pattern contains no shell
// metacharacters, the command must not contain any either — a user
// who writes "git *" expects "any git subcommand", not "git prefix
// followed by arbitrary bytes including `;` / `|` / $()". Without
// this guard, a wildcard allow rule silently re-enables every
// metacharacter the safe_commands policy was supposed to reject.
// Patterns that deliberately include a metacharacter (e.g.
// "ls -la | grep foo" as an explicit allow) still match because
// both sides carry the pipe.
//
// Escaping (e.g. "\*" to match a literal star) is not supported.
func matchPattern(pattern, command string) bool {
	if metaGuard.PatternRejectsCommand(pattern, command) {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == command
	}
	return matchWildcard(pattern, command)
}

// matchWildcard implements the wildcard-matching algorithm for
// patterns containing one or more '*'. Each '*' matches any sequence;
// non-star segments must appear in order, the first non-star anchors
// the start of command, and the last non-star anchors the end.
func matchWildcard(pattern, command string) bool {
	parts := strings.Split(pattern, "*")
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		idx := strings.Index(command[pos:], part)
		if idx < 0 {
			return false
		}
		// First part must match at the start.
		if i == 0 && idx != 0 {
			return false
		}
		pos += idx + len(part)
	}
	// Last part must match at the end (unless pattern ends with *).
	if last := parts[len(parts)-1]; last != "" {
		return strings.HasSuffix(command, last)
	}
	return true
}
