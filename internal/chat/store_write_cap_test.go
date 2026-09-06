package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// chatFileDigest is the on-disk chat file's content digest, so a test can say
// the previous file survived BYTE FOR BYTE rather than merely still existing.
func chatFileDigest(t *testing.T, s *Store, id string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.dir, id+chatFileSuffix))
	if err != nil {
		t.Fatalf("read chat %s: %v", id, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestWriteChat_OverCapRefusalLeavesThePreviousFileIntact is the data-loss half,
// never pinned before: atomicfile pre-checks the size BEFORE staging its temp, so
// the old file survives and the turn is gone. The refusal must reach the caller.
//
// The fixture must CONTAIN a chat that already persisted plus a second mutation
// over the cap; without the first write there is no previous file to survive.
func TestWriteChat_OverCapRefusalLeavesThePreviousFileIntact(t *testing.T) {
	const capBytes = 4 << 10
	s := newCappedTestStore(t, capBytes)

	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "first"
		c.Messages = []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Content: "hello", Ts: 1}}
		return true
	}); err != nil {
		t.Fatalf("Setup: first Mutate: %v", err)
	}
	before := chatFileDigest(t, s, "c1")

	// One long assistant message, well past the cap. Content, not tool calls, so
	// the persist bound cannot shrink it into fitting.
	err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Messages = append(c.Messages, vibekit.Message{
			ID: "m2", Role: vibekit.RoleAssistant, Ts: 2,
			Content: strings.Repeat("x", capBytes*2),
		})
		return true
	})
	if err == nil {
		t.Fatal("Mutate past the cap = nil error; the caller cannot tell the turn was discarded")
	}
	if after := chatFileDigest(t, s, "c1"); after != before {
		t.Errorf("chat file digest = %s, want %s: the refused write must leave the previous file byte-identical",
			after, before)
	}
	got, ok := s.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("Get after a refused write = not found, want the previous chat")
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != "m1" {
		t.Errorf("messages = %+v, want only the message that fit", got.Messages)
	}
	if got.Name != "first" {
		t.Errorf("name = %q, want %q", got.Name, "first")
	}
}

// TestWriteChat_UnlimitedMeansUnlimited pins the DEFAULT on this deployment: a
// container with no memory limit gets no cap, so a write that any derived cap
// would refuse succeeds.
//
// The fixture must CONTAIN a chat larger than the smallest cap the derivation can
// produce — the floor — or "unlimited" would be indistinguishable from a cap
// nothing reached.
func TestWriteChat_UnlimitedMeansUnlimited(t *testing.T) {
	s := newCappedTestStore(t, 0)
	if !s.fileCap.unlimited() {
		t.Fatalf("Setup: fileCap = %d, want unlimited", s.fileCap)
	}

	body := strings.Repeat("u", minChatFileCap+1)
	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "big"
		c.Messages = []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Content: body, Ts: 1}}
		return true
	}); err != nil {
		t.Fatalf("Mutate under an unlimited cap = %v, want nil: the operator declined to bound this container", err)
	}

	path := filepath.Join(s.dir, "c1"+chatFileSuffix)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() <= minChatFileCap {
		t.Fatalf("chat file is %d bytes, want more than the %d-byte floor or this test asserts nothing",
			st.Size(), minChatFileCap)
	}
	// The read side must agree, or an uncapped write would produce a file the
	// store cannot load back.
	got, ok := s.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("Get on an over-floor chat = not found; the read path is still capped")
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != body {
		t.Error("the message did not round-trip through an uncapped write")
	}
	headers := s.List(t.Context())
	if len(headers) != 1 || headers[0].MessageCount != 1 {
		t.Errorf("List() = %+v, want one header with one message", headers)
	}
}
