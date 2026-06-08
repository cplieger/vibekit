// Supervised-mode staging helpers for fs/write_text_file.
//
// These functions implement the staging flow: when a chat is in
// Supervised mode and per-turn trust is not active, every agent write
// is staged in the pending store and the user must accept or reject
// before the write lands on disk. The staging flow is self-contained
// orchestration that changes independently from the fs dispatch
// protocol (bridge_fs.go) and the per-turn trust state machine
// (command_pending.go).

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/pending"
	"github.com/cplieger/vibekit/internal/workspace"
)

// fallbackSeq provides a monotonic collision-free fallback for
// extractToolCallID when both toolCallId and msg.ID are absent.
var fallbackSeq atomic.Int64

// extractToolCallID pulls the tool_call_id out of an fs/write_text_file
// params object when kiro-cli sends one. kiro-cli doesn't document
// whether this field is stable across versions, so we fail open: when
// absent or unparseable, fall back to the request's JSON-RPC id so every
// staged op still has a unique key for the store.
//
// The returned id is always non-empty; uniqueness is guaranteed by the
// fallback because msg.ID is monotonic per bridge.
func extractToolCallID(msg *api.RPCResponse) string {
	var p struct {
		ToolCallID string `json:"toolCallId"`
	}
	if err := json.Unmarshal(msg.Params, &p); err == nil && p.ToolCallID != "" {
		return p.ToolCallID
	}
	if msg.ID != nil {
		return fmt.Sprintf("fs-%d", *msg.ID)
	}
	return fmt.Sprintf("fs-fallback-%d", fallbackSeq.Add(1))
}

// stageFSWrite stages a write in the pending store and blocks until
// the user resolves it. Returns (accepted, override, nil) on resolution;
// returns (false, "", err) when the pending store refused the op
// (e.g. path busy) or when staging infrastructure failed. `override`
// is non-empty only when the user resolved the op via
// resolve_pending_change_partial: the fs handler MUST write that
// content instead of the agent's proposal so per-hunk merges take
// effect.
//
// Broadcasts pending_change_added on stage, pending_change_resolved
// on user decision. The fs handler turns accepted=false into an error
// returned to kiro-cli (so the agent sees the rejection) and
// accepted=true into a fall-through to the normal write path.
func (h *Hub) stageFSWrite(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse, abs, relArg, newText string) (accepted bool, override string, err error) {
	// Pre-read the existing content to compute the diff the UI will
	// render. Missing file = create; a read error other than ENOENT
	// is reported verbatim so the agent sees the real failure reason.
	oldText, kind, rErr := readStagedOld(abs)
	if rErr != nil {
		return false, "", rErr
	}

	// Normalise the path to workspace-relative + forward slashes so
	// the UI renders consistent breadcrumbs.
	rel, relErr := workspace.RelPath(h.lifecycle.workDir, abs)
	if relErr != nil {
		rel = relArg
	}

	// Truncate oversized text so the SSE payload stays bounded. The
	// editor's pending-diff tab refetches full content when the flag
	// is set.
	stagedOld, stagedNew, truncated := truncateForStaging(oldText, newText)

	// Compute the tool-call id once. Used as the pending-store key
	// AND as the lookup we use to fetch the broadcast snapshot; if
	// we recomputed it at each use a future change to
	// extractToolCallID's fallback (e.g. time-based ids) would
	// silently de-sync.
	id := extractToolCallID(msg)
	wait, acceptedFn, addErr := h.perm.pending.Add(ctx, &pending.AddParams{
		ToolCallID: id,
		ChatID:     chatID,
		Path:       rel,
		Kind:       kind,
		OldText:    stagedOld,
		NewText:    stagedNew,
		Truncated:  truncated,
	})
	if addErr != nil {
		return false, "", addErr
	}

	// Close the trust-race window. If cmdTrustPendingChanges ran
	// between the supervised.HasTrust check at the top of the write
	// handler (bridge_fs_write.go) and this Add call, its accept-all
	// loop already finished walking a list that didn't contain our
	// op — we'd block on <-wait forever. Re-read the flag now
	// and self-resolve with accept semantics so the handler
	// falls through to the write path with the same result the
	// trust command would have produced.
	if h.perm.supervised.HasTrust(chatID) {
		if _, err := h.perm.pending.Resolve(ctx, id, pending.ActionAccept); err != nil && !errors.Is(err, pending.ErrUnknown) {
			slog.Warn("stage fs write: race-close resolve failed",
				"chat_id", chatID, "tool_call_id", id, "error", err)
		}
	}

	// Build the wire-form snapshot for the broadcast. We re-read
	// from the store rather than rebuilding by hand so the event
	// matches exactly what ListForChat will replay on reconnect.
	snap, ok := h.perm.pending.Get(id)
	if !ok {
		// Should be impossible immediately after Add; log and
		// continue with an open wait.
		slog.Error("pending snapshot missing immediately after Add",
			"chat_id", chatID, "tool_call_id", id)
	} else {
		h.Broadcast(ctx, api.NewEvent(api.EventPendingChangeAdded, chatID, api.PendingChangeAddedPayload{Change: snap}))
		h.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID, api.WorkingLabelPayload{Label: api.WorkingLabelApproval}))
		h.coord.NotifyPush(ctx, "Pending change: "+rel, api.PushKindPermission)
	}

	// Block until the user resolves or RejectAllForChat flushes.
	<-wait
	res := acceptedFn()
	action := pending.ActionAccept
	if !res.Accepted {
		action = pending.ActionReject
	}
	// Read back the merged-text override (if any). Only set when
	// resolve_pending_change_partial was used; plain accept leaves
	// it empty. No separate cleanup needed — the Resolution carries
	// the text atomically.
	var mergedText string
	if res.Accepted {
		mergedText = res.MergedText
	}
	// Broadcast the resolved event so clients that bypassed the
	// resolve command (e.g. on another tab) update their pending
	// list. The resolve command itself also emits this; it's a
	// no-op dedup on the client.
	h.Broadcast(ctx, api.NewEvent(api.EventPendingChangeResolved, chatID, api.PendingChangeResolvedPayload{
		ToolCallID: id,
		Action:     action,
		Path:       rel,
	}))
	return res.Accepted, mergedText, nil
}

// readStagedOld reads the pre-write content at abs. Returns
// (oldText, kind, err) where kind is "edit" for an existing file and
// "create" for ENOENT. Other errors are returned verbatim so the
// agent sees the real failure reason.
//
// Mirrors respondFSRead's pre-stat guard so supervised-mode staging
// doesn't spike RSS by loading a multi-gigabyte existing file into
// memory before truncateForStaging clips it to pending.Cap. The
// non-supervised write path bypasses this function entirely (it
// writes directly without reading the old content), so this cap is
// specific to the diff-staging branch.
func readStagedOld(abs string) (oldText string, kind pending.Kind, err error) {
	info, statErr := os.Stat(abs) // #nosec G304 -- abs already sandboxed
	if statErr == nil {
		if info.Size() > fsReadCap {
			return "", "", fmt.Errorf(
				"existing file exceeds %d byte cap", fsReadCap)
		}
		data, err := os.ReadFile(abs) // #nosec G304 -- abs sandboxed
		if err != nil {
			return "", "", err
		}
		return string(data), pending.KindEdit, nil
	}
	if os.IsNotExist(statErr) {
		return "", pending.KindCreate, nil
	}
	return "", "", statErr
}

// truncateForStaging caps oldText and newText at pending.Cap each.
// The returned truncated flag is true if either side was shortened,
// so the client can render a "content truncated — open file to see
// full diff" banner.
func truncateForStaging(oldText, newText string) (trimmedOld, trimmedNew string, truncated bool) {
	if len(oldText) > pending.Cap {
		oldText = oldText[:pending.Cap]
		truncated = true
	}
	if len(newText) > pending.Cap {
		newText = newText[:pending.Cap]
		truncated = true
	}
	return oldText, newText, truncated
}
