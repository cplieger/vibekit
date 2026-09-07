package chat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
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

// captureChatLogs swaps the slog default to a buffer for the test's duration.
// The handler is global, so no test using it runs in parallel.
func captureChatLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestWriteChat_RefusalIsLoud pins the ONLY signal a discarded turn produces.
// Nothing else reports it: the caller gets an error it may log as a generic
// failure, and the file on disk looks healthy.
//
// The fixture must CONTAIN a write over the cap AND a successful one under it, so
// the test can fail both ways — a log line emitted unconditionally would pass an
// assertion that only looked for its presence.
func TestWriteChat_RefusalIsLoud(t *testing.T) {
	const capBytes = 4 << 10
	logs := captureChatLogs(t)
	s := newCappedTestStore(t, capBytes)

	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "small"
		return true
	}); err != nil {
		t.Fatalf("Setup: under-cap Mutate: %v", err)
	}
	if strings.Contains(logs.String(), "refused over the chat file cap") {
		t.Fatal("a write that fitted logged the refusal")
	}

	_ = s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Messages = []vibekit.Message{{
			ID: "m1", Role: vibekit.RoleUser, Ts: 1, Content: strings.Repeat("x", capBytes*2),
		}}
		return true
	})

	got := logs.String()
	if !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "refused over the chat file cap") {
		t.Fatalf("logs = %q, want an ERROR naming the refusal", got)
	}
	for _, want := range []string{`chat_id=c1`, `cap_bytes=4096`, `size_bytes=`} {
		if !strings.Contains(got, want) {
			t.Errorf("logs = %q, want it to carry %s", got, want)
		}
	}
}

// TestWriteChat_NearTheCapWarnsWhileThereIsRoom pins the early signal: a chat
// inside the last tenth of its budget is reported on the write that got it there,
// so the wall is visible before a turn is lost to it.
//
// The fixture must CONTAIN a write with plenty of headroom and one without, or an
// unconditional warning would satisfy the assertion.
func TestWriteChat_NearTheCapWarnsWhileThereIsRoom(t *testing.T) {
	const capBytes = 8 << 10
	logs := captureChatLogs(t)
	s := newCappedTestStore(t, capBytes)

	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Name = "roomy"
		return true
	}); err != nil {
		t.Fatalf("Setup: roomy Mutate: %v", err)
	}
	if strings.Contains(logs.String(), "near the chat file cap") {
		t.Fatal("a write with the whole budget free warned about the cap")
	}

	// Inside the last tenth: over 90% of the cap and under it.
	body := strings.Repeat("y", capBytes-(capBytes/20))
	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Messages = []vibekit.Message{{ID: "m1", Role: vibekit.RoleUser, Ts: 1, Content: body}}
		return true
	}); err != nil {
		t.Fatalf("near-cap Mutate = %v, want it to succeed: this is a warning, not a refusal", err)
	}

	got := logs.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, "near the chat file cap") {
		t.Fatalf("logs = %q, want a WARN naming the headroom", got)
	}
	if !strings.Contains(got, "headroom_bytes=") {
		t.Errorf("logs = %q, want the headroom stated so an operator knows how much room is left", got)
	}
}

// TestWriteChat_UnlimitedLogsNeither pins that both surfaces are no-ops under the
// deployment's own default: with no cap there is no refusal to report and no
// headroom to be near, so a chat of any size must produce neither line.
func TestWriteChat_UnlimitedLogsNeither(t *testing.T) {
	logs := captureChatLogs(t)
	s := newCappedTestStore(t, 0)
	if err := s.Mutate(t.Context(), "c1", func(c *vibekit.Chat, _ bool) bool {
		c.Messages = []vibekit.Message{{
			ID: "m1", Role: vibekit.RoleUser, Ts: 1, Content: strings.Repeat("z", minChatFileCap+1),
		}}
		return true
	}); err != nil {
		t.Fatalf("Mutate under an unlimited cap = %v, want nil", err)
	}
	for _, unwanted := range []string{"refused over the chat file cap", "near the chat file cap"} {
		if strings.Contains(logs.String(), unwanted) {
			t.Errorf("logs carry %q under an unlimited cap", unwanted)
		}
	}
}
