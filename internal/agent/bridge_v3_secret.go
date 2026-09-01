// v3 (KAS) credential storage: the `_kiro/secret/{get,store,delete}` A→C
// requests, answered from the process-global internal/secretstore.
//
// KAS owns the entire MCP OAuth flow but keeps only an in-process memory
// copy of the results — no KAS-side file — and asks the client to hold
// them, gated on `_meta.kiro.secretStorage` in initialize. That declaration
// is CONDITIONAL on a store existing (vibekit.StartOpts.SecretStorage):
// declaring it without one is worse than declining, because KAS rethrows a
// store failure into the MCP connect path.
//
// Keys and values are opaque here and never logged; see internal/secretstore
// for the shape KAS derives. One store serves every bridge, because KAS's
// key namespace is global.

package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cplieger/vibekit/internal/secretstore"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// secretKeyParams is the shape of a get/delete request: `{key}`.
type secretKeyParams struct {
	Key string `json:"key"`
}

// secretStoreParams is the shape of a store request: `{key, value}`.
type secretStoreParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// secretGetBody is the reply to a get: `{value}`.
//
// A POINTER, so a miss marshals to an explicit JSON `null` rather than an
// omitted key — KAS reads result.value and treats null as "no credential yet".
// A typed body rather than a map also keeps the wire key in one place.
type secretGetBody struct {
	Value *string `json:"value"`
}

// handleKiroSecretRequest answers the three `_kiro/secret/*` A→C requests.
// Returns true when msg was one of them (so translateACPEvent stops).
//
// Answered SYNCHRONOUSLY on the forward goroutine, unlike getAccessToken:
// every operation here is a map lookup plus at most one bounded atomic write,
// with no network call and no blocking refresh, and KAS issues these inside its
// MCP connect path — dispatching them async would reorder a store against the
// get that follows it.
func (in *inbound) handleKiroSecretRequest(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse) bool {
	switch msg.Method {
	case methodKiroSecretGet:
		in.respondBridge(ctx, chatID, msg, secretGetResult(in.secrets, msg.Params), nil)
		return true
	case methodKiroSecretStore:
		result, err := secretStoreResult(ctx, in.secrets, msg.Params)
		in.respondBridge(ctx, chatID, msg, result, err)
		return true
	case methodKiroSecretDelete:
		result, err := secretDeleteResult(ctx, in.secrets, msg.Params)
		in.respondBridge(ctx, chatID, msg, result, err)
		return true
	default:
		return false
	}
}

// secretGetResult answers `_kiro/secret/get` with `{value}`.
//
// A miss returns an explicit JSON null rather than an error or an omitted key:
// KAS reads `result.value` and treats null as "no credential yet", which is the
// correct first-run answer. Erroring instead would land in its catch-and-warn
// path and mean the same thing less clearly.
//
// A nil store also returns null. That is the not-configured case (no configDir),
// and reporting "absent" degrades to the pre-capability behaviour — one DCR per
// spawn — instead of failing an MCP connect.
func secretGetResult(store *secretstore.Store, params json.RawMessage) secretGetBody {
	p := decodeSecretKey(params)
	if store == nil || p.Key == "" {
		return secretGetBody{}
	}
	v, ok := store.Get(p.Key)
	if !ok {
		return secretGetBody{}
	}
	return secretGetBody{Value: &v}
}

// secretStoreResult answers `_kiro/secret/store` with `{}`, or an error.
//
// Errors are returned rather than swallowed: KAS rethrows a store failure into
// the MCP connect path, so a failure the user can act on (a full disk, a
// read-only volume) surfaces as a failed connect instead of a credential that
// silently reads back empty on the next spawn.
func secretStoreResult(ctx context.Context, store *secretstore.Store, params json.RawMessage) (map[string]any, error) {
	var p secretStoreParams
	if params != nil {
		if err := json.Unmarshal(params, &p); err != nil {
			slog.Warn("v3 secret store: undecodable params", "error", err)
			return nil, &vibekit.RPCError{Code: -32602, Message: "secret/store: params must be {key, value}"}
		}
	}
	if p.Key == "" {
		return nil, &vibekit.RPCError{Code: -32602, Message: "secret/store: key is required"}
	}
	if store == nil {
		// Unreachable in normal operation: a runtime with no store does not declare
		// the capability, so KAS never asks. Reaching it means the peer called a
		// method it was not offered, which is a protocol error and answered as
		// one rather than reported as a successful write that never happened.
		slog.Warn("v3 secret store: no store configured, credential not persisted", "key", p.Key)
		return nil, &vibekit.RPCError{Code: -32603, Message: "secret/store: no credential store configured"}
	}
	if err := store.Set(ctx, p.Key, p.Value); err != nil {
		// Key only — the value is a token or a client secret.
		slog.Error("v3 secret store: persist failed", "key", p.Key, "error", err)
		return nil, &vibekit.RPCError{Code: -32603, Message: "secret/store: " + err.Error()}
	}
	slog.Debug("v3 secret store: persisted", "key", p.Key)
	return map[string]any{}, nil
}

// secretDeleteResult answers `_kiro/secret/delete` with `{}`, or an error.
// KAS rethrows a delete failure too, so the same rule as store applies.
// Deleting an absent key succeeds — the requested post-state already holds.
func secretDeleteResult(ctx context.Context, store *secretstore.Store, params json.RawMessage) (map[string]any, error) {
	p := decodeSecretKey(params)
	if p.Key == "" {
		return nil, &vibekit.RPCError{Code: -32602, Message: "secret/delete: key is required"}
	}
	if store == nil {
		// Nothing was ever stored, so the key is already absent.
		return map[string]any{}, nil
	}
	if err := store.Delete(ctx, p.Key); err != nil {
		slog.Error("v3 secret delete: persist failed", "key", p.Key, "error", err)
		return nil, &vibekit.RPCError{Code: -32603, Message: "secret/delete: " + err.Error()}
	}
	return map[string]any{}, nil
}

// decodeSecretKey pulls `{key}` out of a request's params, yielding an empty
// key on absent or undecodable params so callers take their key-required path.
func decodeSecretKey(params json.RawMessage) secretKeyParams {
	var p secretKeyParams
	if params != nil {
		if err := json.Unmarshal(params, &p); err != nil {
			slog.Warn("v3 secret: undecodable params", "error", err)
		}
	}
	return p
}
