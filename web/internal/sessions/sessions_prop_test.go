package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

func TestCleanupStale_RapidSafety(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir, err := os.MkdirTemp("", "sessions-prop-*")
		if err != nil {
			rt.Fatalf("MkdirTemp: %v", err)
		}
		defer os.RemoveAll(dir)
		mgr := New(dir)

		// Generate N lock files with random PIDs.
		n := rapid.IntRange(1, 20).Draw(rt, "n")
		type entry struct {
			id   string
			pid  int
			live bool
		}
		entries := make([]entry, n)
		livePIDs := make(map[int]bool)

		for i := range n {
			pid := rapid.IntRange(100, 99999).Draw(rt, "pid")
			live := rapid.Bool().Draw(rt, "live")
			// Use a valid session ID format (hex chars, 8+ chars).
			id := fmt.Sprintf("%08x%04x", rapid.IntRange(0, 0xFFFFFFFF).Draw(rt, "id_hi"), i)
			entries[i] = entry{id: id, pid: pid, live: live}
			if live {
				livePIDs[pid] = true
			}

			lockPath := filepath.Join(dir, id+".lock")
			data, _ := json.Marshal(lockFile{PID: pid})
			if err := os.WriteFile(lockPath, data, 0o644); err != nil {
				rt.Fatalf("write lock: %v", err)
			}
		}

		// Stub IsKiroCLI to return true for "live" PIDs.
		orig := IsKiroCLI
		IsKiroCLI = func(pid int) bool { return livePIDs[pid] }
		defer func() { IsKiroCLI = orig }()

		mgr.CleanupStale(context.Background())

		// The key invariant: no panic on any input shape.
		// CleanupStale completed without panic (the rapid framework catches panics).
	})
}
