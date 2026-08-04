package composition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/steering"
)

// newCacheForTest builds a forgeSnapshotCache around an injected build
// function and a real Generator writing into an isolated kiro home.
// Returns the cache and the environment.md path.
func newCacheForTest(t *testing.T, build func(context.Context) steering.ForgeSnapshot) (*forgeSnapshotCache, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	steer := steering.New(t.TempDir(), t.TempDir())
	steer.SetForgeSnapshot(func() steering.ForgeSnapshot { return steering.ForgeSnapshot{} })
	c := newForgeSnapshotCache(context.Background(), steer, build)
	steer.SetForgeSnapshot(c.snapshot)
	return c, filepath.Join(home, ".kiro", "steering", "environment.md")
}

// TestForgeSnapshotCache_SnapshotNeverBlocksOnBuild pins the core
// contract: snapshot() returns immediately even while the build
// function (a network-bound CLI call in production) is stuck. This is
// what keeps steering.Generate safe on the pre-bridge-spawn path.
func TestForgeSnapshotCache_SnapshotNeverBlocksOnBuild(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	c, _ := newCacheForTest(t, func(context.Context) steering.ForgeSnapshot {
		started <- struct{}{}
		<-release // simulate a slow gh repo list
		return steering.ForgeSnapshot{}
	})

	done := make(chan steering.ForgeSnapshot, 1)
	go func() { done <- c.snapshot() }() // stale cache → kicks async rebuild

	select {
	case snap := <-done:
		if len(snap.Providers) != 0 {
			t.Errorf("cold snapshot = %+v, want zero value", snap)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot() blocked on the build function")
	}
	<-started
	close(release)
}

// TestForgeSnapshotCache_RefreshRegeneratesOnChange verifies a refresh
// with new data lands in the cache AND regenerates environment.md,
// while a no-change refresh (after cache invalidation via dirty flag
// path) leaves the file untouched.
func TestForgeSnapshotCache_RefreshRegeneratesOnChange(t *testing.T) {
	var current atomic.Pointer[steering.ForgeSnapshot]
	current.Store(&steering.ForgeSnapshot{Providers: []steering.ForgeProvider{
		{Kind: "github", Host: "github.com", User: "alice"},
	}})
	c, envPath := newCacheForTest(t, func(context.Context) steering.ForgeSnapshot {
		return *current.Load()
	})

	c.refresh()
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("refresh with changed data did not regenerate: %v", err)
	}
	if !strings.Contains(string(data), "alice") {
		t.Errorf("regenerated file missing forge user:\n%s", data)
	}
	if got := c.snapshot(); len(got.Providers) != 1 || got.Providers[0].User != "alice" {
		t.Errorf("cached snapshot = %+v, want alice provider", got)
	}

	// Second refresh with identical data: file must not be rewritten.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(envPath, old, old); err != nil {
		t.Fatal(err)
	}
	c.refresh()
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Before(time.Now().Add(-30 * time.Minute)) {
		t.Error("no-change refresh regenerated the steering file")
	}
}

// TestForgeSnapshotCache_CoalescesConcurrentRefresh verifies a refresh
// arriving while a rebuild is in flight is not dropped: the in-flight
// rebuild may have read pre-change data, so the dirty flag must force
// one more rebuild pass that observes the post-change data.
func TestForgeSnapshotCache_CoalescesConcurrentRefresh(t *testing.T) {
	firstBuild := make(chan struct{})
	proceed := make(chan struct{})
	var calls atomic.Int32
	var current atomic.Pointer[steering.ForgeSnapshot]
	current.Store(&steering.ForgeSnapshot{})

	c, _ := newCacheForTest(t, func(context.Context) steering.ForgeSnapshot {
		n := calls.Add(1)
		if n == 1 {
			close(firstBuild)
			<-proceed // hold the first rebuild until the second refresh queues
		}
		return *current.Load()
	})

	var wg sync.WaitGroup
	wg.Go(func() {
		c.refresh() // first rebuild: reads pre-change data
	})
	<-firstBuild

	// A login lands mid-rebuild: data changes, refresh is requested.
	current.Store(&steering.ForgeSnapshot{Providers: []steering.ForgeProvider{
		{Kind: "github", Host: "github.com", User: "bob"},
	}})
	c.refresh() // busy → coalesced into the in-flight rebuild
	close(proceed)
	wg.Wait()

	if got := calls.Load(); got != 2 {
		t.Errorf("build calls = %d, want 2 (coalesced re-run)", got)
	}
	if got := c.snapshot(); len(got.Providers) != 1 || got.Providers[0].User != "bob" {
		t.Errorf("cached snapshot = %+v, want post-change bob provider", got)
	}
}
