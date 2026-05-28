package permissions

import "vibekit/internal/permissions/eval"

// shellFields delegates to the eval sub-package.
func shellFields(s string) []string {
	return eval.ShellFields(s)
}
