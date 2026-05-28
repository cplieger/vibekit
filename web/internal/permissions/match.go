package permissions

import "vibekit/internal/permissions/eval"

// shellMetacharacters is re-exported from the eval sub-package.
const shellMetacharacters = eval.ShellMetacharacters

// metaPolicy delegates to the eval sub-package's MetaPolicyType.
type metaPolicy = eval.MetaPolicyType

// metaGuard is the package-level instance.
var metaGuard = eval.MetaGuard

// matchPattern delegates to the eval sub-package.
func matchPattern(pattern, command string) bool {
	return eval.MatchPattern(pattern, command)
}

// matchWildcard delegates to the eval sub-package.
func matchWildcard(pattern, command string) bool {
	return eval.MatchWildcard(pattern, command)
}
