package pending

import (
	"context"
	"errors"

	"github.com/cplieger/vibekit/internal/api"
)

// ResolveWithText settles an op with accept semantics BUT overrides
// the agent's proposed NewText with caller-supplied merged content.
// Used by per-hunk Accept/Reject flows where the client computes a
// partial merge from OldText + only the accepted hunks' NewText.
//
// chatID scopes the resolution: if the op's ChatID differs from chatID
// it is treated as unknown (ErrUnknown), so a resolve carrying a
// mismatched chat_id can never settle another chat's op. This is
// defense-in-depth alongside the chat-unique op keys.
//
// KindDelete ops refuse merged text: there is no file content to
// override — the user-intended action is either "accept the delete"
// or "reject the delete". Accepting a delete with merged content
// would turn a deletion into a caller-controlled write, which is a
// data-integrity bug. KindCreate and KindEdit are both allowed;
// KindCreate means "the agent wanted to create this file with X;
// the user says write Y instead."
//
// The merged text is stored on the op struct and returned atomically
// via the Resolution closure from Add, with Merged=true so the caller
// gates the write-override on the flag rather than on non-empty text
// (an empty merge must still override the agent's proposal). No
// separate cleanup step is needed — the Resolution read is the single
// retrieval point.
//
// Returns ErrUnknown if the id isn't pending. Merged text must be
// under Cap bytes; callers validate before invoking. Accept semantics
// are implicit — to reject everything, use plain Resolve(…, "reject").
func (s *Store) ResolveWithText(_ context.Context, chatID api.ChatID, toolCallID, mergedText string) (api.PendingChange, error) {
	if len(mergedText) > Cap {
		return api.PendingChange{}, errors.New("pending: merged_text exceeds cap")
	}
	s.mu.Lock()
	op, ok := s.ops[toolCallID]
	if !ok || op.ChatID != chatID {
		s.mu.Unlock()
		return api.PendingChange{}, ErrUnknown
	}
	if op.Kind == KindDelete {
		s.mu.Unlock()
		return api.PendingChange{}, ErrMergeNotApplicable
	}
	op.accepted = true
	op.merged = true
	op.mergedText = mergedText
	s.unlinkOp(toolCallID, op)
	cancelStop := op.cancelStop
	snap := op.Snapshot()
	s.mu.Unlock()

	if cancelStop != nil {
		cancelStop()
	}
	close(op.resume)
	return snap, nil
}

// Resolve settles a single op. chatID scopes the resolution: an op
// whose ChatID differs from chatID is treated as unknown, so a resolve
// command carrying a mismatched chat_id can never settle another chat's
// op. Returns ErrUnknown if the id isn't pending. Idempotent: a second
// Resolve for a closed op returns ErrUnknown (the op is gone by then).
// On success, returns the op's snapshot so the hub can broadcast the
// resolved event with the original path/kind (the caller may not have
// them handy).
func (s *Store) Resolve(_ context.Context, chatID api.ChatID, toolCallID string, action api.PendingAction) (api.PendingChange, error) {
	s.mu.Lock()
	op, ok := s.ops[toolCallID]
	if !ok || op.ChatID != chatID {
		s.mu.Unlock()
		return api.PendingChange{}, ErrUnknown
	}
	op.accepted = action == api.PendingActionAccept
	s.unlinkOp(toolCallID, op)
	cancelStop := op.cancelStop
	snap := op.Snapshot()
	s.mu.Unlock()

	if cancelStop != nil {
		cancelStop()
	}
	close(op.resume)
	return snap, nil
}

// resolveAllForChat is the shared implementation for AcceptAllForChat
// and RejectAllForChat. The accepted parameter determines whether ops
// are marked as accepted or rejected. The ops are detached under the
// store mutex (via detachChatOps, which defers the unlock so a
// byChat/ops skew can never leak the lock), and their resume channels
// are closed AFTER the lock is released.
func (s *Store) resolveAllForChat(chatID api.ChatID, accepted bool) []api.PendingChange {
	ops, snaps := s.detachChatOps(chatID, accepted)
	for _, op := range ops {
		if op.cancelStop != nil {
			op.cancelStop()
		}
		close(op.resume)
	}
	return snaps
}

// detachChatOps marks (accepted/rejected) and removes every op for
// chatID from all store indexes under the mutex, returning the detached
// ops (for the caller to close their resume channels) and their wire
// snapshots. The unlock is deferred so a nil op from a byChat/ops skew
// (an id listed in byChat but absent from ops — should be impossible,
// but a panic here while holding s.mu would brick staging hub-wide)
// cannot leak the lock: the skew is skipped, and even a genuine panic
// releases the mutex.
func (s *Store) detachChatOps(chatID api.ChatID, accepted bool) ([]*op, []api.PendingChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.byChat[chatID]
	if len(ids) == 0 {
		return nil, nil
	}
	ops := make([]*op, 0, len(ids))
	snaps := make([]api.PendingChange, 0, len(ids))
	for _, id := range ids {
		op := s.ops[id]
		if op == nil {
			continue
		}
		op.accepted = accepted
		ops = append(ops, op)
		snaps = append(snaps, op.Snapshot())
		delete(s.ops, id)
	}
	delete(s.byChat, chatID)
	delete(s.pathIndex, chatID)
	return ops, snaps
}

// AcceptAllForChat accepts every pending op for the chat in a single
// lock acquisition.
func (s *Store) AcceptAllForChat(chatID api.ChatID) []api.PendingChange {
	return s.resolveAllForChat(chatID, true)
}

// RejectAllForChat flushes every pending op for the chat, closing
// each resume channel with accepted=false.
func (s *Store) RejectAllForChat(chatID api.ChatID) []api.PendingChange {
	return s.resolveAllForChat(chatID, false)
}
