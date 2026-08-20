package translate

// v3 (KAS) native-policy notification handlers.
//
// KAS watches the permissions.{yaml,json} files (chokidar) and, on any
// change, rebuilds the policy engine and emits _kiro/policy/changed
// ({sessionId, status, errors?}); a parse/validation/persist failure emits
// _kiro/policy/error ({sessionId, errors[]}). vibekit writes the user/
// workspace files itself, so this is how the write becomes visible: the
// notification → an SSE the client keys off to refetch GET /api/permissions.
//
// Both are broadcast with an EMPTY chatID: the policy is workspace/user
// global and every live bridge's KAS emits its own changed notification for
// the same file, so a global fan-out (every client refetches) is correct and
// idempotent. Verified live against the KAS 2.12 acp-server bundle.

import (
	"context"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// v3PolicyChanged mirrors _kiro/policy/changed.
type v3PolicyChanged struct {
	SessionID string                    `json:"sessionId"`
	Status    string                    `json:"status"`
	Errors    []vibekit.PolicyErrorItem `json:"errors"`
}

// HandlePolicyChanged translates _kiro/policy/changed → the
// permissions_changed SSE. Clients refetch the native policy view.
func (t *Translator) HandlePolicyChanged(ctx context.Context, _ vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[v3PolicyChanged](msg, "policy/changed")
	if !ok {
		return
	}
	t.streaming.Broadcast(ctx, vibekit.NewEvent(vibekit.EventPermissionsChanged, "", vibekit.PermissionsChangedPayload{
		Status: p.Status,
		Errors: p.Errors,
	}))
}

// v3PolicyError mirrors _kiro/policy/error.
type v3PolicyError struct {
	SessionID string                    `json:"sessionId"`
	Errors    []vibekit.PolicyErrorItem `json:"errors"`
}

// HandlePolicyError translates _kiro/policy/error → the policy_error SSE
// (rendered as a banner so a bad hand-edit or rejected rule is visible).
func (t *Translator) HandlePolicyError(ctx context.Context, _ vibekit.ChatID, msg *vibekit.RPCResponse) {
	p, ok := unmarshalParams[v3PolicyError](msg, "policy/error")
	if !ok {
		return
	}
	t.streaming.Broadcast(ctx, vibekit.NewEvent(vibekit.EventPolicyError, "", vibekit.PolicyErrorPayload{Errors: p.Errors}))
}
