package mcp

import (
	"context"
	"os"
	"strings"
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
