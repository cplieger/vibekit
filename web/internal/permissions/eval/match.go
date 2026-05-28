package eval

import "strings"

// ShellMetacharacters disqualify a command from the safe-commands
// auto-approve path.
const ShellMetacharacters = ";&|`$><\n\r\\\"'"

// MetaPolicyType encapsulates the metacharacter safety-net invariant.
type MetaPolicyType struct{}

// CommandDisqualified reports whether command contains shell
// metacharacters and should be routed away from auto-approve paths.
func (MetaPolicyType) CommandDisqualified(command string) bool {
	return strings.ContainsAny(command, ShellMetacharacters)
}

// PatternRejectsCommand reports whether a metachar-free pattern must
// reject a metachar-bearing command.
func (MetaPolicyType) PatternRejectsCommand(pattern, command string) bool {
	return !strings.ContainsAny(pattern, ShellMetacharacters) &&
		strings.ContainsAny(command, ShellMetacharacters)
}

// MetaGuard is the package-level instance of MetaPolicyType.
var MetaGuard MetaPolicyType

// MatchPattern reports whether command matches pattern.
func MatchPattern(pattern, command string) bool {
	if MetaGuard.PatternRejectsCommand(pattern, command) {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == command
	}
	return MatchWildcard(pattern, command)
}

// MatchWildcard implements the wildcard-matching algorithm for
// patterns containing one or more '*'.
func MatchWildcard(pattern, command string) bool {
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
		if i == 0 && idx != 0 {
			return false
		}
		pos += idx + len(part)
	}
	if last := parts[len(parts)-1]; last != "" {
		return strings.HasSuffix(command, last)
	}
	return true
}
