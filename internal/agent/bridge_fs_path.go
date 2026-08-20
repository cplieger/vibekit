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
// It answers a LEXICAL question — "does this name lie inside the workspace" —
// and answering it is not the same as performing the operation inside the
// workspace. Every handler that goes on to touch the filesystem uses
// confineInWorkDir instead, which pairs the answer with the handle that makes it
// enforceable. This entry point survives for the callers that need only the
// verdict and the absolute path.
func (lt *lifetime) resolveInsideWorkDir(p string) (string, error) {
	return workspace.ResolveInsideAbs(lt.workDir, p)
}

// confineInWorkDir resolves p inside the workspace and returns the workspace
// root together with the root-relative name that addresses p through it.
//
// This is the whole fix for the check-then-act the resolver used to leave open.
// ResolveInsideAbs is one lexical evaluation: it calls EvalSymlinks, checks
// containment, and hands back an absolute path. Every operation the handlers
// then performed was an AMBIENT os call on that path, so the components it had
// verified were re-resolved by the kernel with no boundary attached — and the
// requester of these handlers is the agent, which has write access to the
// workspace and can rename an intermediate directory into a symlink between the
// two. syscall.O_NOFOLLOW did not close it either: it guards only the FINAL
// component, so a swapped ANCESTOR redirected the write regardless.
//
// Naming the operation through lt.workRoot closes it, because the kernel
// re-resolves every component of the name on each operation and refuses any
// component that leaves the root. The residual, stated because os.Root does not
// remove it: a symlink that stays INSIDE the workspace is still followed, so a
// lost race can land the operation on a different in-workspace file. The delete
// path — the only one where that is unrecoverable — descends component by
// component instead (atomicfile.OpenParentInRoot), which refuses a symlink
// component outright. os.Root cannot be asked for that refusal: it ORs
// O_NOFOLLOW in itself and then re-resolves the link on the resulting ELOOP, so
// a caller-supplied O_NOFOLLOW is silently ignored (measured on go1.27.0,
// src/os/root_unix.go:85-101).
//
// The returned rel is also what the agent-ignore filter matches on, and that is
// deliberate: it makes the gate's input the same string the operation is named
// by. It used to be derived separately, with a fail-OPEN branch if the
// derivation failed — the read went ahead unfiltered. There is no such branch
// now, because a name the operation cannot be expressed in is a name the
// operation does not run on.
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
