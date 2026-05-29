// File-system path resolution and error helpers for kiro-cli ACP bridges.

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"vibekit/internal/api"
	"vibekit/internal/pending"
	"vibekit/internal/workspace"
)

// fsReadCap caps text reads at 8 MiB. kiro-cli's scratch reads are tiny
// (a README, a steering file); anything bigger is either a mistake or a
// binary file that shouldn't go through readTextFile.
const fsReadCap = 8 << 20

// fsWriteCap is the canonical per-write byte cap (4 MiB). Derived from
// pending.Cap so the supervised staging path and the unstaged fs handler
// share a single authoritative constant.
const fsWriteCap = pending.Cap

// Sentinel errors for routine fs handler rejections. These are expected
// outcomes (not bugs) and are logged at Debug rather than Warn.
var (
	errIgnored        = errors.New("path is in agent ignore list")
	errCapExceeded    = errors.New("file exceeds byte cap")
	errRejectedByUser = errors.New("change rejected by user")
)

// resolveInsideWorkDir turns a client-supplied path into an absolute
// one guaranteed to live inside h.lifecycle.workDir. Rejects empty input, paths
// that escape via `..`, and symlink-based escape — both the parent
// directory and the final target are evaluated. Delegates to
// workspace.ResolveInsideAbs (skips redundant filepath.Abs since h.lifecycle.workDir is already absolute).
func (h *Hub) resolveInsideWorkDir(p string) (string, error) {
	return workspace.ResolveInsideAbs(h.lifecycle.workDir, p)
}

// respondFSError writes a JSON-RPC error response for an fs request and
// logs the failure. The log level is classified by error type so routine
// policy rejections (ignore-list denial, cap-exceeded) stay at Debug and
// don't trip operator alert dashboards that key off Warn+. Real OS /
// parse failures remain at Warn for triage.
func (h *Hub) respondFSError(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse, err error) {
	if fsErrorIsRoutine(err) {
		slog.Debug("fs request denied", "chat_id", chatID, "method", msg.Method, "error", err)
	} else {
		slog.Warn("fs request failed", "chat_id", chatID, "method", msg.Method, "error", err)
	}
	h.respondBridge(ctx, chatID, msg, nil, err)
}

// fsErrorIsRoutine reports whether err is an expected policy denial or
// input validation failure rather than an actionable OS / parse error.
// Matched by substring; the error surfaces are small and stable.
func fsErrorIsRoutine(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, errIgnored) ||
		errors.Is(err, errCapExceeded) ||
		errors.Is(err, errRejectedByUser)
}

// respondBridge routes a response back to the bridge that issued the
// request. msg.ID is required; if the bridge is gone, we drop silently
// (the agent's Call will time out on its side).
func (h *Hub) respondBridge(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse, result any, err error) {
	if msg.ID == nil {
		slog.Warn("fs request missing id", "chat_id", chatID, "method", msg.Method)
		return
	}
	sb := h.coord.GetBridge(chatID)
	if sb == nil {
		slog.Warn("fs response dropped: no bridge", "chat_id", chatID, "method", msg.Method)
		return
	}
	if wErr := sb.bridge.Respond(ctx, *msg.ID, result, err); wErr != nil {
		slog.Error("fs response write failed", "chat_id", chatID, "method", msg.Method, "error", wErr)
	}
}

// --- ACP request/response helpers (consolidated from bridge_respond.go) ---

func parseRequest(msg *api.RPCResponse, v any) error {
	if msg.Params == nil {
		return io.ErrUnexpectedEOF
	}
	return json.Unmarshal(msg.Params, v)
}

func respondOK(ctx context.Context, h *Hub, chatID api.ChatID, msg *api.RPCResponse, result any) {
	if msg.ID == nil {
		return
	}
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	if err := sb.bridge.Respond(ctx, *msg.ID, result, nil); err != nil {
		slog.Warn("respondOK: bridge respond failed", "error", err)
	}
}

func respondErr(ctx context.Context, h *Hub, chatID api.ChatID, msg *api.RPCResponse, errMsg string) {
	if msg.ID == nil {
		return
	}
	sb := h.bridge.mgr.get(chatID)
	if sb == nil {
		return
	}
	if err := sb.bridge.Respond(ctx, *msg.ID, nil, &api.RPCError{Code: -1, Message: errMsg}); err != nil {
		slog.Warn("respondErr: bridge respond failed", "error", err)
	}
}
