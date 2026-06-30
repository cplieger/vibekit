package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// sha256Hex is the expected fallback mapping safeChatID applies to an
// unsafe id: a hex-encoded SHA-256 of the raw bytes.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestSafeChatID_PassesThroughValidID verifies a legitimate
// server-generated id (alphanumerics + '-') is returned unchanged.
// Each of the leading "not empty / not . / not .." guards must hold for
// the pass-through to happen; inverting any one of them would instead
// hash a perfectly valid id and silently relocate its event log.
func TestSafeChatID_PassesThroughValidID(t *testing.T) {
	for _, id := range []string{"chat-abc123", "a", "ABCdef-0123456789"} {
		if got := safeChatID(id); got != id {
			t.Errorf("safeChatID(%q) = %q, want it returned unchanged (valid ids must not be hashed)", id, got)
		}
	}
}

// TestSafeChatID_HashesUnsafeID verifies ids that could traverse out of
// the chats root — empty, ".", "..", and ids containing a separator —
// are replaced by a deterministic SHA-256 hex rather than used verbatim.
func TestSafeChatID_HashesUnsafeID(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"dot", "."},
		{"dotdot", ".."},
		{"slash", "a/b"},
		{"leading-traversal", "../etc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := safeChatID(tc.id)
			if got == tc.id {
				t.Fatalf("safeChatID(%q) returned the raw id; an unsafe id must be hashed to stay inside the chats root", tc.id)
			}
			if want := sha256Hex(tc.id); got != want {
				t.Errorf("safeChatID(%q) = %q, want %q (SHA-256 hex of the raw bytes)", tc.id, got, want)
			}
		})
	}
}
