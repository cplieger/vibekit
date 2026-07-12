package permissions

import "github.com/cplieger/vibekit/internal/permissions/eval"

// ShellDecision is re-exported from the eval sub-package.
type ShellDecision = eval.ShellDecision

// ShellAllow and the following constants re-export the ShellDecision values from the eval sub-package for backward compatibility.
const (
	ShellAllow = eval.ShellAllow
	ShellAsk   = eval.ShellAsk
	ShellDeny  = eval.ShellDeny
)
