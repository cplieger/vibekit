package mcp

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// serverByName returns the stored *Server with the given name (or nil).
// Reads s.servers directly without the store lock; safe because these
// callers construct the store with a nil onChange, so SetKnownTools
// spawns no notifier goroutine that could race this read.
func serverByName(s *Store, name string) *Server {
	for _, sv := range s.servers {
		if sv.Name == name {
			return sv
		}
	}
	return nil
}

// TestSetKnownTools_updatesMatchingServerOnly verifies SetKnownTools
// updates the tool list of the server whose name matches and leaves
// every other server untouched.
func TestSetKnownTools_updatesMatchingServerOnly(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"}); err != nil {
		t.Fatalf("create a: %v", err)
	}
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "b", Command: "bash"}); err != nil {
		t.Fatalf("create b: %v", err)
	}

	s.SetKnownTools(context.Background(), "b", []string{"tool_x"})

	a := serverByName(s, "a")
	b := serverByName(s, "b")
	if a == nil || b == nil {
		t.Fatal("servers missing after create")
	}
	if len(b.KnownTools) != 1 || b.KnownTools[0] != "tool_x" {
		t.Errorf("named server b.KnownTools = %v, want [tool_x]", b.KnownTools)
	}
	if len(a.KnownTools) != 0 {
		t.Errorf("unnamed server a.KnownTools = %v, want empty", a.KnownTools)
	}
}

// TestSetKnownTools_persistsToolList verifies the updated tool list is
// written to mcp.json so the UI can show suggestions while the server
// is disconnected.
func TestSetKnownTools_persistsToolList(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	s.SetKnownTools(context.Background(), "a", []string{"persist_tool"})

	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatalf("read mcp.json: %v", err)
	}
	if !strings.Contains(string(data), "persist_tool") {
		t.Errorf("SetKnownTools did not persist the tool list; file=%s", data)
	}
}

// TestSetKnownTools_noWarnOnPersistSuccess verifies the success path
// emits no persist-failure warning.
func TestSetKnownTools_noWarnOnPersistSuccess(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "a", Command: "bash"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	buf := captureSlog(t)
	s.SetKnownTools(context.Background(), "a", []string{"tool_y"})

	if strings.Contains(buf.String(), "persist after SetKnownTools failed") {
		t.Errorf("persist succeeded but failure warn logged; log=%q", buf.String())
	}
}

// TestSetKnownTools_changeDetection verifies SetKnownTools skips BOTH the
// disk write and the onChange broadcast when the incoming tool set is
// identical to the stored one, and resumes both on a real change. Without
// the guard every bridge spawn re-reports the same tools, firing redundant
// mcp.json writes + mcp_config_changed broadcasts + npx prewarm passes.
func TestSetKnownTools_changeDetection(t *testing.T) {
	dir := t.TempDir()
	var broadcasts atomic.Int32
	s, err := New(context.Background(), dir, func(context.Context) { broadcasts.Add(1) })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Create(context.Background(), &Server{Transport: TransportStdio, Name: "srv", Command: "bash"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Create fires onChange; wait for it so the later counts start clean.
	waitForCounter(t, &broadcasts, 1)

	// First set is a real change (nil -> [a b]): persists + broadcasts.
	s.SetKnownTools(context.Background(), "srv", []string{"a", "b"})
	waitForCounter(t, &broadcasts, 2)
	if got := serverByName(s, "srv").KnownTools; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("KnownTools after change = %v, want [a b]", got)
	}

	// Remove mcp.json so a subsequent persist is detectable by re-creation.
	if err := os.Remove(s.path); err != nil {
		t.Fatalf("remove mcp.json: %v", err)
	}

	// Identical set: must skip the write and the broadcast entirely.
	s.SetKnownTools(context.Background(), "srv", []string{"a", "b"})
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Errorf("unchanged SetKnownTools rewrote mcp.json (stat err=%v), want skip", err)
	}
	// The skip path returns before notifyChange, so no notifier goroutine
	// is spawned and the count is stably 2 (no timing race to guard).
	if got := broadcasts.Load(); got != 2 {
		t.Errorf("unchanged SetKnownTools broadcast count = %d, want 2 (no new broadcast)", got)
	}

	// A different set (adds "c") resumes both persist and broadcast.
	s.SetKnownTools(context.Background(), "srv", []string{"a", "b", "c"})
	waitForCounter(t, &broadcasts, 3)
	if _, err := os.Stat(s.path); err != nil {
		t.Errorf("changed SetKnownTools did not rewrite mcp.json: %v", err)
	}
	if got := serverByName(s, "srv").KnownTools; len(got) != 3 {
		t.Fatalf("KnownTools after second change = %v, want len 3", got)
	}
}
