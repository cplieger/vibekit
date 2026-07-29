package translate

import (
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzDeriveSubSession exercises the session-routing helper with
// arbitrary session IDs. Invariants:
//   - Returns "" when sessionID equals the parent.
//   - Returns "" when either sessionID or parent is empty.
//   - Returns sessionID when it differs from a non-empty parent.
func FuzzDeriveSubSession(f *testing.F) {
	f.Add("sess-1", "parent-1")
	f.Add("", "parent-1")
	f.Add("sess-1", "")
	f.Add("", "")
	f.Add("same", "same")
	f.Add("child\x00", "parent\x00")
	f.Add("a", "b")

	f.Fuzz(func(t *testing.T, sessionID, parentSession string) {
		deps := &stubDeriveSubDeps{parent: parentSession}
		tr := New(deps, withIDGenerator(func() string { return "id" }))
		chatID := api.ChatID("fuzz-chat")

		got := tr.deriveSubSession(chatID, sessionID)

		// Apply the same logic to derive expected result.
		var want string
		if sessionID != "" && parentSession != "" && sessionID != parentSession {
			want = sessionID
		}
		if got != want {
			t.Fatalf("deriveSubSession(%q, %q) with parent=%q: got %q, want %q",
				chatID, sessionID, parentSession, got, want)
		}
	})
}

// stubDeriveSubDeps satisfies Deps with minimal stubs for deriveSubSession tests.
type stubDeriveSubDeps struct {
	baseDeps
	parent string
}

func (d *stubDeriveSubDeps) ParentACPSession(_ api.ChatID) string {
	return d.parent
}
