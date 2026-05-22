package checkpoint

import "context"

// This file exposes internal helpers that are only meant to be used
// from checkpoint tests. It compiles only when running `go test`
// (via the _test.go suffix), so the production binary never sees
// these methods and external packages cannot depend on them.

// tags returns all snapshot tags for this chat in (turn, tool)
// ascending order. Does not include restore markers. Used by tests
// to assert allocation + resume properties without going through
// Store, which never surfaces the tag list.
func (m *Manager) tags() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureLoaded(context.Background()); err != nil {
		return nil
	}
	return m.state.tagList()
}

// tagList returns every tag in (turn, tool) ascending order. O(N)
// copy of the precomputed orderedTags; callers always want a copy
// so they can mutate without corrupting state.
func (s *state) tagList() []string {
	out := make([]string, len(s.orderedTags))
	copy(out, s.orderedTags)
	return out
}
