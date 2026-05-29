package permissions

import "vibekit/internal/permissions/eval"

// matchPattern delegates to the eval sub-package.
func matchPattern(pattern, command string) bool {
	return eval.MatchPattern(pattern, command)
}
