package permissions

import "github.com/cplieger/vibekit/internal/permissions/eval"

// Re-export types from the eval sub-package for backward compatibility.
type SafeMatchMode = eval.SafeMatchMode

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

const (
	ShellAllow = eval.ShellAllow
	ShellAsk   = eval.ShellAsk
	ShellDeny  = eval.ShellDeny
)
