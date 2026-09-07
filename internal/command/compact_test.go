package command

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// captureSlog swaps the process-global default logger for the duration of one
// test. Released through t.Cleanup rather than defer: a defer does not run on a
// subtest's failure path and would leak the test handler into the rest of the
// package. These tests therefore may not call t.Parallel.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func compactReq(chatID vibekit.ChatID) *vibekit.ClientCommand {
	return &vibekit.ClientCommand{Type: vibekit.CmdCompact, ChatID: chatID}
}

// TestCmdCompact_ReportsAcceptanceNotCompaction pins the one thing this handler
// can honestly claim.
//
// `{success: true}` covers five outcomes and nothing on the wire separates them:
// committed and notified, committed with the frame withheld because the session
// advanced during the durable commit, committed with the notify swallowed, and
// three skips that compacted nothing. An operator diagnosing "/compact did
// nothing" reads this line first, so a line saying the chat WAS compacted tells
// them the opposite of what they need.
func TestCmdCompact_ReportsAcceptanceNotCompaction(t *testing.T) {
	buf := captureSlog(t)
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}

	if _, err := CmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), compactReq("c1")); err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}

	if got := buf.String(); !strings.Contains(got, `msg="compact accepted"`) {
		t.Errorf("log did not report acceptance; got %q", got)
	}
}

// There is deliberately NO test asserting this handler records and broadcasts
// nothing. Its `bridges BridgeAccess` parameter exposes only bridge operations —
// no chat store, no broadcaster — so a synthesized boundary, watermark or event
// is not expressible here, and the narrow parameter type is a better guard than
// a test: it fails at compile time and it cannot be satisfied by accident. Such
// a test could only go red if someone WIDENED that parameter, which is a design
// change reviewed on its own merits, so it would be a change detector rather
// than a bug catcher. Keep the parameter narrow; that IS the assertion.

// TestCmdCompact_SendsTheSessionsWire pins the verb and its params. The handler
// had no test at all before this file, so the wire contract was unpinned.
func TestCmdCompact_SendsTheSessionsWire(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": true}, sessionID: "sess-1"}

	if _, err := CmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), compactReq("c1")); err != nil {
		t.Fatalf("CmdCompact: %v", err)
	}

	if b.gotMethod != vibekit.MethodSessionCompact {
		t.Errorf("method = %q, want %q", b.gotMethod, vibekit.MethodSessionCompact)
	}
	if got := b.gotParams["sessionId"]; got != b.sessionID {
		t.Errorf("sessionId = %v, want %v", got, b.sessionID)
	}
}

// TestCmdCompact_RefusalIs409 pins the branch whose message the release notes
// record as misdirecting across seven refusal causes, so a reword lands against
// a test rather than into a vacuum.
func TestCmdCompact_RefusalIs409(t *testing.T) {
	b := &recordingBridge{result: map[string]any{"success": false}, sessionID: "sess-1"}

	_, err := CmdCompact(t.Context(), newBridgeHost(testsupport.NewInMemoryChatStore(), b), compactReq("c1"))
	if err == nil {
		t.Fatal("a refused compaction reported success")
	}
	if got := statusOf(err); got != 409 {
		t.Errorf("status = %d, want 409", got)
	}
}
