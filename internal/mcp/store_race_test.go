package mcp

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestStore_CreateDeleteConcurrent exercises concurrent Create and
// Delete operations. The store uses append/Delete on the servers
// slice under a single mutex — this verifies no panics or races.
func TestStore_CreateDeleteConcurrent(t *testing.T) {
	dir := t.TempDir()
	ctx := t.Context()
	s, err := New(ctx, dir, nil, WithKASConfigPath(filepath.Join(dir, "kas-mcp.json")))
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu      sync.Mutex
		created []ServerID
	)

	var wg sync.WaitGroup

	// Creator.
	wg.Go(func() {
		for i := range 20 {
			srv := &Server{
				Transport: TransportStdio,
				Name:      "c-" + string(rune('A'+i)),
				Command:   "true",
				Enabled:   true,
			}
			out, err := s.Create(ctx, srv)
			if err != nil {
				continue // name conflict on retry is fine
			}
			mu.Lock()
			created = append(created, out.ID)
			mu.Unlock()
		}
	})

	// Deleter.
	wg.Go(func() {
		for range 30 {
			mu.Lock()
			if len(created) == 0 {
				mu.Unlock()
				continue
			}
			id := created[0]
			created = created[1:]
			mu.Unlock()
			_ = s.Delete(ctx, id)
		}
	})

	wg.Wait()
}
