package mcp

import (
	"context"
	"sync"
	"testing"
)

// TestStore_SetKnownToolsConcurrent exercises the race between
// SetKnownTools (which modifies a server's KnownTools field under lock
// then calls persist outside the lock) and other CRUD operations that
// also read/write s.servers. Under -race this catches data races on
// the server slice or individual Server struct fields.
func TestStore_SetKnownToolsConcurrent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := New(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Seed a few servers.
	for i := range 5 {
		name := "srv-" + string(rune('A'+i))
		srv := &Server{
			Transport: TransportStdio,
			Name:      name,
			Command:   "echo",
			Enabled:   true,
		}
		if _, err := s.Create(ctx, srv); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup

	// Concurrent SetKnownTools.
	wg.Go(func() {
		for range 50 {
			s.SetKnownTools(ctx, "srv-A", []string{"tool1", "tool2"})
			s.SetKnownTools(ctx, "srv-B", []string{"tool3"})
		}
	})

	// Concurrent List (reader).
	wg.Go(func() {
		for range 50 {
			_ = s.List(ctx)
		}
	})

	// Concurrent EnabledRaw (reader).
	wg.Go(func() {
		for range 50 {
			_ = s.EnabledRaw(ctx)
		}
	})

	wg.Wait()
}

// TestStore_CreateDeleteConcurrent exercises concurrent Create and
// Delete operations. The store uses append/Delete on the servers
// slice under a single mutex — this verifies no panics or races.
func TestStore_CreateDeleteConcurrent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := New(ctx, dir, nil)
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
