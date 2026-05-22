package pending

import (
	"context"
	"errors"
	"time"

	"vibekit/internal/api"
)

// Add stages a new pending op and returns the blocking channel the
// fs handler must read to learn the decision. The channel is closed
// when the user resolves (accept or reject), when the op is flushed
// via RejectAllForChat, or when ctx is cancelled. The returned
// readResolution func reports the final decision (including any merged
// text) after the channel has closed; reads take the store mutex so
// the mutex provides the happens-before barrier.
//
// If ctx is cancelled before the op is resolved, the store
// auto-rejects the op (accepted=false) and closes the resume channel.
// Callers that don't need cancellation should pass context.Background().
//
// Returns ErrPathBusy when the chat already has a pending op for the
// same path; callers should return an error to the agent in that case
// (no staging, no block).
//
// Returns ErrTooManyPending when either the per-chat or total
// pending-op caps are exhausted.
func (s *Store) Add(ctx context.Context, p *AddParams) (waitCh <-chan struct{}, readResolution func() Resolution, err error) {
	if p.ToolCallID == "" {
		return nil, nil, ErrEmptyID
	}
	if !api.ValidChatID(string(p.ChatID)) {
		return nil, nil, ErrEmptyChatID
	}
	if p.Path == "" {
		return nil, nil, ErrEmptyPath
	}
	switch p.Kind {
	case KindCreate, KindEdit, KindDelete:
	default:
		return nil, nil, ErrBadKind
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Resource caps: fail loud before the heap balloons. Per-chat
	// is the important axis (a single misbehaving agent); total is
	// defense-in-depth for cross-chat bursts.
	if len(s.byChat[p.ChatID]) >= MaxPendingPerChat {
		return nil, nil, ErrTooManyPending
	}
	if len(s.ops) >= MaxPendingTotal {
		return nil, nil, ErrTooManyPending
	}

	// Path-busy check via per-chat path index. O(1) map lookup
	// replaces the previous O(pending) linear scan.
	if paths, ok := s.pathIndex[p.ChatID]; ok {
		if _, busy := paths[p.Path]; busy {
			return nil, nil, ErrPathBusy
		}
	}

	op := &op{
		ToolCallID: p.ToolCallID,
		ChatID:     p.ChatID,
		Path:       p.Path,
		Kind:       p.Kind,
		OldText:    p.OldText,
		NewText:    p.NewText,
		CreatedAt:  time.Now(),
		Truncated:  p.Truncated,
		resume:     make(chan struct{}),
	}
	s.ops[p.ToolCallID] = op
	s.byChat[p.ChatID] = append(s.byChat[p.ChatID], p.ToolCallID)
	if s.pathIndex[p.ChatID] == nil {
		s.pathIndex[p.ChatID] = make(map[string]struct{})
	}
	s.pathIndex[p.ChatID][p.Path] = struct{}{}

	ch := op.resume

	// Context cancellation: register a callback via context.AfterFunc
	// that auto-rejects the op when the context fires. Unlike the
	// previous goroutine-per-op pattern, AfterFunc only spawns a
	// goroutine when cancellation actually occurs, eliminating up to
	// MaxPendingPerChat blocked goroutines in the common case.
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, func() {
			s.cancelOp(p.ToolCallID, op)
		})
		op.cancelStop = stop
	}

	return ch, func() Resolution {
		// Reading after close is safe: sync.Mutex provides
		// happens-before from the Resolve write to this read.
		s.mu.Lock()
		defer s.mu.Unlock()
		return Resolution{Accepted: op.accepted, MergedText: op.mergedText}
	}, nil
}

// ResolveWithText settles an op with accept semantics BUT overrides
// the agent's proposed NewText with caller-supplied merged content.
// Used by per-hunk Accept/Reject flows where the client computes a
// partial merge from OldText + only the accepted hunks' NewText.
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
// via the Resolution closure from Add. No separate cleanup step is
// needed — the Resolution read is the single retrieval point.
//
// Returns ErrUnknown if the id isn't pending. Merged text must be
// under Cap bytes; callers validate before invoking. Accept semantics
// are implicit — to reject everything, use plain Resolve(…, "reject").
func (s *Store) ResolveWithText(ctx context.Context, toolCallID, mergedText string) (api.PendingChange, error) {
	if len(mergedText) > Cap {
		return api.PendingChange{}, errors.New("pending: merged_text exceeds cap")
	}
	s.mu.Lock()
	op, ok := s.ops[toolCallID]
	if !ok {
		s.mu.Unlock()
		return api.PendingChange{}, ErrUnknown
	}
	if op.Kind == KindDelete {
		s.mu.Unlock()
		return api.PendingChange{}, ErrMergeNotApplicable
	}
	op.accepted = true
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

// Resolve settles a single op. Returns ErrUnknown if the id isn't
// pending. Idempotent: a second Resolve for a closed op returns
// ErrUnknown (the op is gone by then). On success, returns the op's
// snapshot so the hub can broadcast the resolved event with the
// original path/kind (the caller may not have them handy).
func (s *Store) Resolve(ctx context.Context, toolCallID string, action api.PendingAction) (api.PendingChange, error) {
	s.mu.Lock()
	op, ok := s.ops[toolCallID]
	if !ok {
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
	// Close outside the lock: readers don't acquire s.mu; closing
	// here keeps the lock hold-time minimal.
	close(op.resume)
	return snap, nil
}

// RejectAllForChat flushes every pending op for the chat, closing
// each resume channel with accepted=false. Returns the snapshots of
// flushed ops (for broadcasting resolved events) and the count.
// Used by cancel, mode-disable, and chat-delete paths.
func (s *Store) RejectAllForChat(chatID api.ChatID) []api.PendingChange {
	s.mu.Lock()
	ids := s.byChat[chatID]
	if len(ids) == 0 {
		s.mu.Unlock()
		return nil
	}
	ops := make([]*op, 0, len(ids))
	snaps := make([]api.PendingChange, 0, len(ids))
	for _, id := range ids {
		op := s.ops[id]
		op.accepted = false
		ops = append(ops, op)
		snaps = append(snaps, op.Snapshot())
		delete(s.ops, id)
	}
	delete(s.byChat, chatID)
	delete(s.pathIndex, chatID)
	s.mu.Unlock()

	for _, op := range ops {
		if op.cancelStop != nil {
			op.cancelStop()
		}
		close(op.resume)
	}
	return snaps
}

// unlinkOp removes an op from all store indexes (ops, byChat, pathIndex).
// Caller must hold s.mu.
func (s *Store) unlinkOp(toolCallID string, op *op) {
	delete(s.ops, toolCallID)
	s.byChat[op.ChatID] = removeID(s.byChat[op.ChatID], toolCallID)
	if len(s.byChat[op.ChatID]) == 0 {
		delete(s.byChat, op.ChatID)
	}
	if paths, ok := s.pathIndex[op.ChatID]; ok {
		delete(paths, op.Path)
		if len(paths) == 0 {
			delete(s.pathIndex, op.ChatID)
		}
	}
}

// cancelOp auto-rejects a pending op when its context is cancelled.
// Acquires the store mutex, verifies the op is still pending, marks it
// rejected, removes it from both maps and the path index, and closes
// the resume channel. No-op if the op was already resolved by another path.
func (s *Store) cancelOp(toolCallID string, op *op) {
	s.mu.Lock()
	if _, ok := s.ops[toolCallID]; !ok {
		s.mu.Unlock()
		return // already resolved by another path
	}
	op.accepted = false
	s.unlinkOp(toolCallID, op)
	s.mu.Unlock()
	close(op.resume)
}
