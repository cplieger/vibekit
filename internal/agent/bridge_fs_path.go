// File-system path resolution and error helpers for kiro-cli ACP bridges.

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/vibekit/internal/workspace"
)

// fsReadCap caps text reads at 8 MiB. kiro-cli's scratch reads are tiny
// (a README, a steering file); anything bigger is either a mistake or a
// binary file that shouldn't go through readTextFile.
const fsReadCap = 8 << 20

// fsWriteCap is the per-write byte cap (4 MiB). It used to be an alias of
// pending.Cap so the staging path and the plain fs handler shared one constant;
// there is only one write path now, so the number lives here.
const fsWriteCap = 4 << 20

// Sentinel errors for routine fs handler rejections. These are expected
// outcomes (not bugs) and are logged at Debug rather than Warn.
var (
	errIgnored        = errors.New("path is in agent ignore list")
	errCapExceeded    = errors.New("file exceeds byte cap")
	errRejectedByUser = errors.New("change rejected by user")
)

// errNoWorkRoot is the refusal when the workspace could not be opened as a
// confined root. Not routine: an operator has to fix it.
var errNoWorkRoot = errors.New("workspace is not open for confined access")

// resolveInsideWorkDir confines p to the workspace. On the type that holds
// workDir, so a collaborator needing it does not need a *Runtime.
//
// Answers a LEXICAL question — "does this name lie inside the workspace" —
// which is not the same as performing the operation inside the workspace.
// Every handler that touches the filesystem uses confineInWorkDir instead,
// which pairs the answer with the handle that makes it enforceable. This entry
// point survives for callers that need only the verdict and the absolute path.
func (lt *lifetime) resolveInsideWorkDir(p string) (string, error) {
	return workspace.ResolveInsideAbs(lt.workDir, p)
}

// confineInWorkDir resolves p inside the workspace and returns the workspace
// root together with the root-relative name that addresses p through it.
//
// This closes the check-then-act window a lexical-only resolve leaves open:
// the agent has write access to the workspace and can swap an intermediate
// directory for a symlink between the verdict and the operation, so naming
// the operation through lt.workRoot is what re-resolves every path component
// on each op. Residual: a symlink that stays INSIDE the workspace is still
// followed, so a lost race can land on a different in-workspace file — the
// delete path descends component by component instead
// (atomicfile.OpenParentInRoot), because that is the one place the race is
// unrecoverable.
//
// rel is also what the agent-ignore filter matches on, so the gate's input
// is the same string the operation is named by.
func (lt *lifetime) confineInWorkDir(p string) (*os.Root, string, error) {
	if lt.workRoot == nil {
		return nil, "", errNoWorkRoot
	}
	abs, err := lt.resolveInsideWorkDir(p)
	if err != nil {
		return nil, "", err
	}
	rel, err := workspace.RelPath(lt.workDir, abs)
	if err != nil {
		return nil, "", fmt.Errorf("workspace-relative path for %q: %w", p, err)
	}
	return lt.workRoot, rel, nil
}

// respondFSError writes a JSON-RPC error response for an fs request and
// logs the failure. The log level is classified by error type so routine
// policy rejections (ignore-list denial, cap-exceeded) stay at Debug and
// don't trip operator alert dashboards that key off Warn+. Real OS /
// parse failures remain at Warn for triage.
func (in *inbound) respondFSError(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse, err error) {
	if fsErrorIsRoutine(err) {
		slog.Debug("fs request denied", "chat_id", chatID, "method", msg.Method, "error", err)
	} else {
		slog.Warn("fs request failed", "chat_id", chatID, "method", msg.Method, "error", err)
	}
	in.respondBridge(ctx, chatID, msg, nil, err)
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
func (in *inbound) respondBridge(ctx context.Context, chatID vibekit.ChatID, msg *vibekit.RPCResponse, result any, err error) {
	if msg.ID == nil {
		slog.Warn("fs request missing id", "chat_id", chatID, "method", msg.Method)
		return
	}
	sb := in.coord.Bridge(chatID)
	if sb == nil {
		slog.Warn("fs response dropped: no bridge", "chat_id", chatID, "method", msg.Method)
		return
	}
	if wErr := sb.bridge.Respond(ctx, *msg.ID, result, err); wErr != nil {
		slog.Error("fs response write failed", "chat_id", chatID, "method", msg.Method, "error", wErr)
	}
}

// --- ACP request/response helpers (consolidated from bridge_respond.go) ---

func parseRequest(msg *vibekit.RPCResponse, v any) error {
	if msg.Params == nil {
		return io.ErrUnexpectedEOF
	}
	return json.Unmarshal(msg.Params, v)
}

func respondOK(ctx context.Context, bridges *bridgeManager, chatID vibekit.ChatID, msg *vibekit.RPCResponse, result any) {
	if msg.ID == nil {
		return
	}
	sb := bridges.get(chatID)
	if sb == nil {
		return
	}
	if err := sb.bridge.Respond(ctx, *msg.ID, result, nil); err != nil {
		slog.Warn("respondOK: bridge respond failed", "error", err)
	}
}

func respondErr(ctx context.Context, bridges *bridgeManager, chatID vibekit.ChatID, msg *vibekit.RPCResponse, errMsg string) {
	if msg.ID == nil {
		return
	}
	sb := bridges.get(chatID)
	if sb == nil {
		return
	}
	if err := sb.bridge.Respond(ctx, *msg.ID, nil, &vibekit.RPCError{Code: -1, Message: errMsg}); err != nil {
		slog.Warn("respondErr: bridge respond failed", "error", err)
	}
}
