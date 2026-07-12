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

// extractToolCallID derives the pending-store key for an
// fs/write_text_file request. The key is CHAT-UNIQUE: the store is a
// single process-wide map shared by every chat, and on v3 the natural
// per-request value (msg.ID) is only monotonic PER BRIDGE, so two
// concurrent supervised chats would otherwise collide (both "fs-7") —
// leaking one chat's staged diff into the other and cross-resolving to
// disk. Prefixing with chatID makes the key globally unique.
//
// v3's fs/write_text_file carries no toolCallId (the params branch is
// dead on the v3-only wire), but it is prefixed too so the key is
// chat-unique regardless of source. The id is opaque to the client
// (the tool card correlates the pending row by path), so it is echoed
// back unchanged on resolve. The returned id is always non-empty.
func extractToolCallID(chatID api.ChatID, msg *api.RPCResponse) string {
	var p struct {
		ToolCallID string `json:"toolCallId"`
	}
	if err := json.Unmarshal(msg.Params, &p); err == nil && p.ToolCallID != "" {
		return fmt.Sprintf("fs-%s-%s", chatID, p.ToolCallID)
	}
	if msg.ID != nil {
		return fmt.Sprintf("fs-%s-%d", chatID, *msg.ID)
	}
	return fmt.Sprintf("fs-%s-fallback-%d", chatID, fallbackSeq.Add(1))
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
func (h *Hub) stageFSWrite(ctx context.Context, chatID api.ChatID, msg *api.RPCResponse, abs, relArg, newText string) (accepted bool, override string, merged bool, err error) {
	// Pre-read the existing content to compute the diff the UI will
	// render. Missing file = create; an oversize existing file stages
	// blind (empty OldText + Truncated); a read error other than ENOENT
	// is reported verbatim so the agent sees the real failure reason.
	oldText, kind, oldTruncated, rErr := readStagedOld(abs)
	if rErr != nil {
		return false, "", false, rErr
	}

	// Normalise the path to workspace-relative + forward slashes so
	// the UI renders consistent breadcrumbs.
	rel, relErr := workspace.RelPath(h.lifecycle.workDir, abs)
	if relErr != nil {
		rel = relArg
	}

	// Truncate oversized text so the SSE payload stays bounded. The
	// editor's pending-diff tab refetches full content when the flag
	// is set. Fold in oldTruncated so an oversize-old stage-blind is
	// also flagged truncated.
	stagedOld, stagedNew, truncated := truncateForStaging(oldText, newText)
	truncated = truncated || oldTruncated

	// Compute the tool-call id once. Used as the pending-store key
	// AND as the lookup we use to fetch the broadcast snapshot; if
	// we recomputed it at each use a future change to
	// extractToolCallID's fallback (e.g. time-based ids) would
	// silently de-sync.
	id := extractToolCallID(chatID, msg)
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
		return false, "", false, addErr
	}

	// Close the trust-race window. If cmdTrustPendingChanges ran
	// between the supervised.HasTrust check at the top of
	// respondFSWrite (bridge_fs_write.go) and this Add call, its
	// accept-all loop already finished walking a list that didn't
	// contain our op — we'd block on <-wait forever. Re-read the flag now
	// and self-resolve with accept semantics so the handler
	// falls through to the write path with the same result the
	// trust command would have produced. raceClosed tracks the
	// intentional self-resolve so the snapshot-missing branch below
	// doesn't log it as an error (it's the documented normal race).
	raceClosed := false
	if h.perm.supervised.HasTrust(chatID) {
		_, resolveErr := h.perm.pending.Resolve(ctx, chatID, id, pending.ActionAccept)
		switch {
		case resolveErr == nil:
			raceClosed = true
		case !errors.Is(resolveErr, pending.ErrUnknown):
			slog.Warn("stage fs write: race-close resolve failed",
				"chat_id", chatID, "tool_call_id", id, "error", resolveErr)
		}
	}

	// Build the wire-form snapshot for the broadcast. We re-read
	// from the store rather than rebuilding by hand so the event
	// matches exactly what ListForChat will replay on reconnect.
	snap, ok := h.perm.pending.Get(id)
	switch {
	case ok:
		h.Broadcast(ctx, api.NewEvent(api.EventPendingChangeAdded, chatID, api.PendingChangeAddedPayload{Change: snap}))
		h.Broadcast(ctx, api.NewEvent(api.EventWorkingLabel, chatID, api.WorkingLabelPayload{Label: api.WorkingLabelApproval}))
		h.coord.NotifyPush(ctx, "Pending change: "+rel, api.PushKindPermission)
	case !raceClosed:
		// The op vanished immediately after Add WITHOUT the documented
		// trust-race self-resolve — a genuine fault worth an error log.
		// When raceClosed, the op is intentionally gone (already accepted),
		// so skip both the broadcast and the error log.
		slog.Error("pending snapshot missing immediately after Add",
			"chat_id", chatID, "tool_call_id", id)
	}

	// Block until the user resolves or RejectAllForChat flushes.
	<-wait
	res := acceptedFn()
	action := pending.ActionAccept
	if !res.Accepted {
		action = pending.ActionReject
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
	// merged is true only for an accepted partial merge; the caller
	// gates its content override on this flag (not on non-empty text)
	// so an empty merge still overrides the agent's proposal.
	return res.Accepted, res.MergedText, res.Accepted && res.Merged, nil
}

// readStagedOld reads the pre-write content at abs. Returns
// (oldText, kind, truncated, err) where kind is "edit" for an existing
// file and "create" for ENOENT. An existing file over the fsReadCap
// diff cap stages BLIND — empty OldText + truncated=true — rather than
// erroring: the unstaged write path would overwrite such a file fine,
// so a supervised gate must not turn a legal large-file overwrite into
// a tool failure; the UI surfaces "too large to diff" and the user can
// still accept/reject. Other read errors are returned verbatim so the
// agent sees the real failure reason.
//
// The pre-stat guard mirrors respondFSRead so supervised-mode staging
// doesn't spike RSS by loading a multi-gigabyte existing file into
// memory. The non-supervised write path bypasses this function entirely
// (it writes directly without reading the old content), so this cap is
// specific to the diff-staging branch.
func readStagedOld(abs string) (oldText string, kind pending.Kind, truncated bool, err error) {
	info, statErr := os.Stat(abs) // #nosec G304 -- abs already sandboxed
	if statErr == nil {
		if info.Size() > fsReadCap {
			return "", pending.KindEdit, true, nil
		}
		data, readErr := os.ReadFile(abs) // #nosec G304 -- abs sandboxed
		if readErr != nil {
			return "", "", false, readErr
		}
		return string(data), pending.KindEdit, false, nil
	}
	if os.IsNotExist(statErr) {
		return "", pending.KindCreate, false, nil
	}
	return "", "", false, statErr
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
