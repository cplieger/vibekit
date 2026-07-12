package pending

import (
	"context"
	"time"

	"github.com/cplieger/vibekit/internal/api"
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
	if !p.Kind.Valid() {
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
		return Resolution{Accepted: op.accepted, MergedText: op.mergedText, Merged: op.merged}
	}, nil
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
