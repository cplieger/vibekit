package permissions

import "github.com/cplieger/vibekit/internal/permissions/eval"

// SafeMatchMode re-exports the eval.SafeMatchMode type for backward compatibility.
type SafeMatchMode = eval.SafeMatchMode

// BaseExact and the following constants re-export the SafeMatchMode values from the eval sub-package for backward compatibility.
const (
	BaseExact   = eval.BaseExact
	Prefix      = eval.Prefix
	TokenExact  = eval.TokenExact
	TokenPrefix = eval.TokenPrefix
	ShortPrefix = eval.ShortPrefix
)

// SafeCommandRule is re-exported from the eval sub-package.
type SafeCommandRule = eval.SafeCommandRule

// ShellDecision is re-exported from the eval sub-package.
type ShellDecision = eval.ShellDecision

// ShellAllow and the following constants re-export the ShellDecision values from the eval sub-package for backward compatibility.
const (
	ShellAllow = eval.ShellAllow
	ShellAsk   = eval.ShellAsk
	ShellDeny  = eval.ShellDeny
)
