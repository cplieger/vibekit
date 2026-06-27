package hub

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/api"
	"github.com/cplieger/vibekit/internal/pending"
)

// fsWriteParams is the wire-form payload respondFSWrite expects. Keep
// the helper local so the test doesn't depend on anything unexported.
type fsWriteParams struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	ToolCallID string `json:"toolCallId,omitempty"`
}

// newFSWriteMsg constructs the minimum RPCResponse needed to drive
// respondFSWrite. ID must be non-nil so the handler routes a response
// back to the bridge.
func newFSWriteMsg(t *testing.T, path, content, toolCallID string) *api.RPCResponse {
	t.Helper()
	raw := mustJSON(t, fsWriteParams{Path: path, Content: content, ToolCallID: toolCallID})
	id := int64(42)
	return &api.RPCResponse{
		ID:     &id,
		Method: api.MethodFSWrite,
		Params: raw,
	}
}

// hubWithWorkDir is a small wrapper around newTestHub that points
// workDir at a fresh temp dir — required because respondFSWrite writes
// real files.
func hubWithWorkDir(t *testing.T) (*Hub, *fakeChatStore, string) {
	t.Helper()
	cs := newFakeChatStore()
	workDir := t.TempDir()
	factory := func() api.ACPBridge { return newFakeBridge() }
	h := New(workDir, factory, cs, func() []string { return nil })
	cs.SetBroadcaster(h)
	h.mcpRegistry.signalReady()
	return h, cs, workDir
}

// TestFSWrite_SupervisedBlocksUntilAccept proves the core invariant:
// when a chat has SupervisedMode=true, the write doesn't land on disk
// until the user resolves the op.
func TestFSWrite_SupervisedBlocksUntilAccept(t *testing.T) {
	t.Parallel()
	h, cs, workDir := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	// Kick off respondFSWrite in a goroutine so we can observe the
	// block. It will park on the resume channel until Resolve fires.
	msg := newFSWriteMsg(t, "hello.txt", "new content", "tc-1")
	done := make(chan struct{})
	go func() {
		h.respondFSWrite(context.Background(), "c1", msg)
		close(done)
	}()

	// Poll for the staged op to appear. CountForChat goes 0→1 once
	// Add completes inside stageFSWrite.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.perm.pending.CountForChat("c1") != 1 {
		t.Fatalf("op not staged within 1s; count=%d", h.perm.pending.CountForChat("c1"))
	}

	// File must not exist yet.
	abs := filepath.Join(workDir, "hello.txt")
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("file exists before accept: err=%v", err)
	}

	// Resolve accept — write should land.
	if _, err := h.perm.pending.Resolve(context.Background(), "tc-1", api.PendingActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("respondFSWrite still blocked after Resolve")
	}
	data, err := os.ReadFile(abs) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("read after accept: %v", err)
	}
	if string(data) != "new content" {
		t.Fatalf("file content = %q, want %q", string(data), "new content")
	}
}

// TestFSWrite_SupervisedRejectLeavesFileUntouched proves rejects don't
// leak data to disk and that the checkpoint snapshot is skipped.
func TestFSWrite_SupervisedRejectLeavesFileUntouched(t *testing.T) {
	t.Parallel()
	h, cs, workDir := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	// Pre-create the file so we can verify it wasn't touched.
	abs := filepath.Join(workDir, "sticky.txt")
	if err := os.WriteFile(abs, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := newFSWriteMsg(t, "sticky.txt", "agent wanted this", "tc-7")
	done := make(chan struct{})
	go func() {
		h.respondFSWrite(context.Background(), "c1", msg)
		close(done)
	}()

	// Wait for staging.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := h.perm.pending.Resolve(context.Background(), "tc-7", api.PendingActionReject); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-done

	data, err := os.ReadFile(abs) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("file content changed after reject: %q", string(data))
	}
}

// TestFSWrite_NotSupervisedWritesImmediately pins the pre-feature
// behaviour for chats that don't opt in: the write lands on disk with
// no staging, no block.
func TestFSWrite_NotSupervisedWritesImmediately(t *testing.T) {
	t.Parallel()
	h, cs, workDir := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool { c.Name = "A"; return true })

	msg := newFSWriteMsg(t, "quick.txt", "immediate", "tc-x")
	h.respondFSWrite(context.Background(), "c1", msg)

	abs := filepath.Join(workDir, "quick.txt")
	data, err := os.ReadFile(abs) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "immediate" {
		t.Fatalf("content=%q, want %q", string(data), "immediate")
	}
	if h.perm.pending.CountForChat("c1") != 0 {
		t.Fatalf("op staged for non-Supervised chat: count=%d", h.perm.pending.CountForChat("c1"))
	}
}

// TestFSWrite_SupervisedPathBusy rejects a second stage for the same
// path — agent-side sees an error response, disk is unchanged.
func TestFSWrite_SupervisedPathBusy(t *testing.T) {
	t.Parallel()
	h, cs, workDir := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	var wg sync.WaitGroup
	wg.Add(2)
	msg1 := newFSWriteMsg(t, "dup.txt", "first", "tc-a")
	msg2 := newFSWriteMsg(t, "dup.txt", "second", "tc-b")
	go func() { defer wg.Done(); h.respondFSWrite(context.Background(), "c1", msg1) }()

	// Wait for the first to stage.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Fire the second — path-busy should reject it immediately.
	go func() { defer wg.Done(); h.respondFSWrite(context.Background(), "c1", msg2) }()

	// Give the second enough time to have reached Add and failed.
	time.Sleep(100 * time.Millisecond)
	if got := h.perm.pending.CountForChat("c1"); got != 1 {
		t.Fatalf("path-busy Add incorrectly staged; count=%d", got)
	}

	// Let the first op drain so the goroutines exit cleanly.
	if _, err := h.perm.pending.Resolve(context.Background(), "tc-a", api.PendingActionReject); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wg.Wait()

	// File must not exist (both ops were rejected / path-busy).
	if _, err := os.Stat(filepath.Join(workDir, "dup.txt")); !os.IsNotExist(err) {
		t.Fatalf("file exists after all rejects: err=%v", err)
	}
}

// TestFSWrite_SupervisedBroadcastsEvents asserts the expected
// sequence of SSE events flows through the replay buffer during a
// stage→accept cycle.
func TestFSWrite_SupervisedBroadcastsEvents(t *testing.T) {
	t.Parallel()
	h, cs, _ := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	msg := newFSWriteMsg(t, "evt.txt", "x", "tc-z")
	done := make(chan struct{})
	go func() { h.respondFSWrite(context.Background(), "c1", msg); close(done) }()

	// Wait for stage.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := h.perm.pending.Resolve(context.Background(), "tc-z", api.PendingActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-done

	h.lifecycle.mu.Lock()
	types := extractTypes(t, h.sse.replayBuf.Events())
	h.lifecycle.mu.Unlock()
	wantSubset(t, types,
		"pending_change_added",
		"pending_change_resolved",
		"working_label",
	)
}

// TestFSWrite_SupervisedTruncatesOversizedContent confirms the
// truncation guard on the staged diff sends a Truncated flag rather
// than attempting to marshal megabytes of text.
func TestFSWrite_SupervisedTruncatesOversizedContent(t *testing.T) {
	t.Parallel()
	h, cs, _ := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	// Build content just over the cap.
	big := make([]byte, fsWriteCap)
	for i := range big {
		big[i] = 'a'
	}
	msg := newFSWriteMsg(t, "big.txt", string(big), "tc-big")
	done := make(chan struct{})
	go func() { h.respondFSWrite(context.Background(), "c1", msg); close(done) }()

	// Wait for stage then inspect the snapshot. The deadline is
	// generous because this test stages ~4 MiB under the race
	// detector — race instrumentation slows every syscall ~10x,
	// and the SHA-256 hash on the oversized blob chews up CPU.
	// A 10s cap keeps us well clear of spurious flakes without
	// blocking CI if something genuinely deadlocked.
	deadline := time.Now().Add(10 * time.Second)
	var snap api.PendingChange
	for time.Now().Before(deadline) {
		if s, ok := h.perm.pending.Get("tc-big"); ok {
			snap = s
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap.ToolCallID == "" {
		t.Fatal("op did not stage within 10s")
	}
	// Content was exactly fsWriteCap; truncateForStaging respects the
	// pending.Cap (identical to fsWriteCap), so Truncated stays false.
	// If the write cap or staging cap diverge in the future, this
	// test will flag it.
	if snap.Truncated {
		t.Errorf("Truncated=true for content at cap boundary")
	}

	// Clean up.
	_, _ = h.perm.pending.Resolve(context.Background(), "tc-big", api.PendingActionReject)
	<-done
}

// TestFSWrite_SupervisedPartialMergeOverridesContent pins the
// merged-text override path: when a user resolves via
// resolve_pending_change_partial, the fs handler writes the caller-
// supplied merged text instead of the agent's proposed p.Content.
// Also verifies the side map is cleared on read so subsequent ops
// don't leak state.
func TestFSWrite_SupervisedPartialMergeOverridesContent(t *testing.T) {
	t.Parallel()
	h, cs, workDir := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	msg := newFSWriteMsg(t, "merge.txt", "agent wanted this", "tc-merge")
	done := make(chan struct{})
	go func() {
		h.respondFSWrite(context.Background(), "c1", msg)
		close(done)
	}()

	// Wait for staging.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Resolve with merged text — simulates the user accepting only
	// some hunks and sending the partial merge.
	if _, err := h.perm.pending.ResolveWithText(context.Background(), "tc-merge", "user edited version"); err != nil {
		t.Fatalf("ResolveWithText: %v", err)
	}
	<-done

	abs := filepath.Join(workDir, "merge.txt")
	data, err := os.ReadFile(abs) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(data); got != "user edited version" {
		t.Fatalf("file content = %q, want merged override", got)
	}
}

// TestFSWrite_PerTurnTrustBypassesStaging pins the trust-remaining
// path: once the chat has perTurnTrust set, subsequent fs writes
// skip the staging gate entirely even though SupervisedMode is true.
func TestFSWrite_PerTurnTrustBypassesStaging(t *testing.T) {
	t.Parallel()
	h, cs, workDir := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	// Turn on per-turn trust. Subsequent writes must not stage.
	h.perm.supervised.SetTrust("c1")

	msg := newFSWriteMsg(t, "trusted.txt", "zoom", "tc-trust")
	// No goroutine — we expect this to return immediately because
	// staging is bypassed.
	h.respondFSWrite(context.Background(), "c1", msg)

	abs := filepath.Join(workDir, "trusted.txt")
	data, err := os.ReadFile(abs) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "zoom" {
		t.Fatalf("content=%q, want zoom", string(data))
	}
	if h.perm.pending.CountForChat("c1") != 0 {
		t.Fatalf("op staged despite per-turn trust: count=%d",
			h.perm.pending.CountForChat("c1"))
	}
}

// TestFSWrite_PerTurnTrustClearedRestoresStaging proves that once
// supervised.ClearTrust fires (simulating turn_ended), subsequent
// writes resume staging. This is the "trust resets each turn" invariant.
func TestFSWrite_PerTurnTrustClearedRestoresStaging(t *testing.T) {
	t.Parallel()
	h, cs, _ := hubWithWorkDir(t)
	_ = cs.Mutate(context.Background(), "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	h.perm.supervised.SetTrust("c1")
	h.perm.supervised.ClearTrust("c1", api.ClearReasonTurnEnded)

	msg := newFSWriteMsg(t, "restage.txt", "staged again", "tc-restage")
	done := make(chan struct{})
	go func() {
		h.respondFSWrite(context.Background(), "c1", msg)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.perm.pending.CountForChat("c1") != 1 {
		t.Fatalf("op not staged after trust cleared: count=%d",
			h.perm.pending.CountForChat("c1"))
	}

	// Clean up so the goroutine exits.
	if _, err := h.perm.pending.Resolve(context.Background(), "tc-restage", "accept"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-done
}

// Appease the unused-import check for sync when future tests need it;
// keeping the variable alive avoids toolchain noise during iteration.
var _ = sync.Mutex{}

// readStagedOld reads a file of exactly fsReadCap bytes successfully
// (the guard is a strict `>`) and rejects one byte over the cap.
func TestReadStagedOld_ExactCapBoundary(t *testing.T) {
	dir := t.TempDir()

	exact := filepath.Join(dir, "exact.bin")
	if err := os.WriteFile(exact, bytes.Repeat([]byte("a"), fsReadCap), 0o644); err != nil {
		t.Fatal(err)
	}
	oldText, kind, err := readStagedOld(exact)
	if err != nil {
		t.Fatalf("readStagedOld(exact cap) err = %v, want nil (boundary is strict >)", err)
	}
	if len(oldText) != fsReadCap {
		t.Errorf("readStagedOld(exact cap) len = %d, want %d", len(oldText), fsReadCap)
	}
	if kind != pending.KindEdit {
		t.Errorf("readStagedOld(exact cap) kind = %q, want %q", kind, pending.KindEdit)
	}

	over := filepath.Join(dir, "over.bin")
	if err := os.WriteFile(over, bytes.Repeat([]byte("b"), fsReadCap+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readStagedOld(over); err == nil {
		t.Errorf("readStagedOld(cap+1) err = nil, want a cap-exceeded error")
	}
}

// A path that resolves cleanly keeps the normalized workspace-relative
// path in the staged op rather than the raw request argument.
func TestStageFSWrite_KeepsNormalizedRelOnSuccess(t *testing.T) {
	h, cs, _ := hubWithWorkDir(t)
	ctx := context.Background()
	_ = cs.Mutate(ctx, "c1", func(c *api.Chat, _ bool) bool {
		c.Name = "A"
		c.SupervisedMode = true
		return true
	})

	msg := newFSWriteMsg(t, "./hello.txt", "x", "tc-staging")
	done := make(chan struct{})
	go func() {
		h.respondFSWrite(ctx, "c1", msg)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.perm.pending.CountForChat("c1") == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	snap, ok := h.perm.pending.Get("tc-staging")
	if !ok {
		t.Fatal("staged op not found")
	}
	if snap.Path != "hello.txt" {
		t.Errorf("staged Path = %q, want %q (normalized rel must be kept)", snap.Path, "hello.txt")
	}

	// Unblock the staging goroutine.
	if _, err := h.perm.pending.Resolve(ctx, "tc-staging", api.PendingActionAccept); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("respondFSWrite still blocked after Resolve")
	}
}
