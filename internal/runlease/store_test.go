package runlease

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStore_RoundTripsALeaseAcrossARestart is the durability the whole change
// rests on: before it, a run's safety envelope died with the process while the
// run itself survived, so a restart silently removed the only bound on a
// non-scheduled run and the deny-fast budget that keeps an unattended run from
// parking on a permission prompt at 03:00.
func TestStore_RoundTripsALeaseAcrossARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	slot := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	want := Lease{
		StartedAt:  time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC),
		Deadline:   slot,
		SlotAt:     slot,
		WorkflowID: "wf_1",
		Recipe:     "publish",
		Origin:     OriginScheduled,
		ScheduleID: "sched-1",
		Unattended: true,
	}
	if err := s.Put(t.Context(), &want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The restart.
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get("wf_1")
	if !ok {
		t.Fatal("the lease did not survive the restart, so the run is unbounded and unattributed")
	}
	if got.Recipe != want.Recipe || got.Origin != want.Origin || got.ScheduleID != want.ScheduleID {
		t.Errorf("lease = %+v, want recipe/origin/schedule from %+v", got, want)
	}
	if !got.Unattended {
		t.Error("the unattended mark did not survive; the permission floor would not arm at 03:00")
	}
	if !got.SlotAt.Equal(slot) {
		t.Errorf("SlotAt = %v, want %v; a re-arm could not re-apply the slot bound", got.SlotAt, slot)
	}
	if !got.StartedAt.Equal(want.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
	// The DEADLINE is the one field that must not survive: it was set by a
	// process that no longer exists, and the bound is on executing time.
	if got.Bounded() {
		t.Errorf("the reloaded lease carries deadline %v; a stale deadline would cancel a run "+
			"the moment it resumed", got.Deadline)
	}
}

// TestStore_FileShapeIsAVersionedObject pins the format decision. schedules.json
// is a bare array with nowhere to put a version; this file is the one genuinely
// irreversible part of the lease work, so it carries one from the first write.
func TestStore_FileShapeIsAVersionedObject(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Put(t.Context(), &Lease{
		StartedAt: time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC),
		Recipe:    "publish", WorkflowID: "wf_1", Origin: OriginManual,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("read %s: %v", FileName, err)
	}
	body := string(raw)
	if !strings.HasPrefix(strings.TrimSpace(body), "{") {
		t.Errorf("the file is not a JSON object, so it has nowhere to carry a version:\n%s", body)
	}
	if !strings.Contains(body, `"version": 1`) {
		t.Errorf("the file carries no version field:\n%s", body)
	}
	if !strings.Contains(body, `"leases"`) {
		t.Errorf("the file carries no leases array:\n%s", body)
	}
	// A parked lease must not write a zero timestamp the reader would have to
	// interpret; omitzero is what keeps the file readable by a person.
	if strings.Contains(body, "0001-01-01") {
		t.Errorf("a zero time reached the file:\n%s", body)
	}
}

// TestStore_RoundTripsTheLaunchingChat: the chat id is the live-runs
// projection's whole payload, so a restart that dropped it would leave a
// still-live agent run unable to exempt its chat from client eviction.
func TestStore_RoundTripsTheLaunchingChat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Put(t.Context(), &Lease{
		StartedAt: time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC),
		Recipe:    "publish", WorkflowID: "wf_1", ChatID: "c-live", Origin: OriginAgent,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := reopened.Get("wf_1")
	if !ok {
		t.Fatal("the lease did not survive the restart")
	}
	if got.ChatID != "c-live" {
		t.Errorf("ChatID = %q after a restart, want %q", got.ChatID, "c-live")
	}
}

// TestNewStore_APreUpgradeRowDecodesWithAnEmptyChatID pins why ChatID is
// ADDITIVE at Version 1: a lease written before the field existed must load as
// a usable lease whose empty chat id means "no chat to exempt" — the same
// designed value the parentless launch verbs mint — rather than costing a
// version bump that would discard the whole file and strip every live lease of
// its deadline at boot.
func TestNewStore_APreUpgradeRowDecodesWithAnEmptyChatID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Version-1 bytes exactly as a pre-upgrade build wrote them: no chat_id key.
	body := `{"version":1,"leases":[{"started_at":"2026-08-01T03:00:00Z",` +
		`"workflow_id":"wf_old","recipe":"nightly","origin":"scheduled",` +
		`"schedule_id":"sched-1","unattended":true}]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore rejected a version-1 file a pre-upgrade build wrote: %v", err)
	}
	got, ok := s.Get("wf_old")
	if !ok {
		t.Fatal("the pre-upgrade lease was dropped; its run is unbounded and its recipe reads idle")
	}
	if got.ChatID != "" {
		t.Errorf("ChatID = %q for a row written before the field existed, want empty", got.ChatID)
	}
	if got.Recipe != "nightly" || got.Origin != OriginScheduled || !got.Unattended {
		t.Errorf("the pre-upgrade row lost fields it did carry: %+v", got)
	}
}

// TestStore_RejectsAVersionItDoesNotKnow is the forward-compatibility rule, and
// the reason it DISCARDS rather than refuses.
//
// A record written by another build may carry semantics this one cannot honour,
// and acting on half-understood leases is how a live run gets cancelled by a
// sweep. But refusing to open the store would leave the runtime with no lease
// registry at all, so no run would get a wall clock — strictly worse. So: a
// usable empty store, plus an error the caller logs.
func TestStore_RejectsAVersionItDoesNotKnow(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"a future version":                    `{"version":99,"leases":[{"workflow_id":"wf_1","origin":"manual"}]}`,
		"no version at all":                   `{"leases":[{"workflow_id":"wf_1","origin":"manual"}]}`,
		"the array shape schedules.json uses": `[{"workflow_id":"wf_1","origin":"manual"}]`,
		"malformed JSON":                      `{"version":1,"leases":`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
				t.Fatalf("seed: %v", err)
			}
			s, err := NewStore(dir)
			if err == nil {
				t.Error("the store accepted a file it cannot reason about, and its leases are now live")
			}
			if s == nil {
				t.Fatal("NewStore returned no store; the runtime would have no lease registry at all")
			}
			if got := len(s.List()); got != 0 {
				t.Errorf("the store kept %d leases from an unreadable file", got)
			}
			// Still writable, so the next launch is bounded.
			if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_2", Origin: OriginManual}); err != nil {
				t.Errorf("the store refused a fresh lease after a bad read: %v", err)
			}
			if _, ok := s.Get("wf_2"); !ok {
				t.Error("the fresh lease did not land")
			}
		})
	}
}

// TestStore_RefusesALeaseItCouldNotActOn: a lease with no id has no key, and one
// with an unknown origin says neither whether the run is sweepable nor whether
// it is unattended. Both are refused on the way IN as well as dropped on the way
// back off disk.
func TestStore_RefusesALeaseItCouldNotActOn(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{Origin: OriginManual}); err == nil {
		t.Error("a lease with no workflow id was accepted")
	}
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1"}); err == nil {
		t.Error("a lease with no origin was accepted")
	}
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: "tui"}); err == nil {
		t.Error("a lease with an unknown origin was accepted")
	}
	if got := len(s.List()); got != 0 {
		t.Errorf("a refused lease landed anyway: %d held", got)
	}
}

// TestStore_DropsUnusableLeasesOnLoad is the same rule on the read side, and it
// is separate because the file is not written only by this build's Put: a
// hand-edited or partially-written record must not become a sweep candidate.
func TestStore_DropsUnusableLeasesOnLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `{"version":1,"leases":[` +
		`{"workflow_id":"wf_good","origin":"manual","recipe":"publish"},` +
		`{"workflow_id":"","origin":"manual"},` +
		`{"workflow_id":"wf_unknown_origin","origin":"tui"}` +
		`]}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	held := s.List()
	if len(held) != 1 || held[0].WorkflowID != "wf_good" {
		t.Fatalf("held %+v, want only wf_good", held)
	}
	if _, ok := s.Get("wf_unknown_origin"); ok {
		t.Error("a lease with an unknown origin loaded, and the sweep can now reach it")
	}
}

// TestStore_SetDeadlineIsTheReArm pins the mutable half: every start re-stamps,
// every pause parks, and a lease that is gone cannot be re-armed.
func TestStore_SetDeadlineIsTheReArm(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	const id = "wf_1"
	if err := s.Put(t.Context(), &Lease{WorkflowID: id, Origin: OriginManual}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	first := time.Now().Add(time.Hour)
	if err := s.SetDeadline(t.Context(), id, first); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	if got, _ := s.Get(id); !got.Deadline.Equal(first) {
		t.Errorf("deadline = %v, want %v", got.Deadline, first)
	}

	// The pause.
	if err := s.SetDeadline(t.Context(), id, time.Time{}); err != nil {
		t.Fatalf("park: %v", err)
	}
	if got, _ := s.Get(id); got.Bounded() {
		t.Error("a parked lease still reports bounded, so its timer could still cancel it")
	}

	// The resume, with a FRESH budget rather than the first one's remainder.
	second := time.Now().Add(time.Hour)
	if err := s.SetDeadline(t.Context(), id, second); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	if got, _ := s.Get(id); !got.Deadline.Equal(second) {
		t.Errorf("the resume did not take the fresh deadline: %v", got.Deadline)
	}

	if err := s.SetDeadline(t.Context(), "wf_gone", first); err == nil {
		t.Error("a released lease was re-armed, so a timer would outlive its run")
	}
}

// TestStore_ReleaseIsIdempotent: both the terminal frame and the cancel path
// release, and neither knows which arrived first.
func TestStore_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Release(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := s.Release(t.Context(), "wf_1"); err != nil {
		t.Errorf("a second release failed: %v", err)
	}
	if err := s.Release(t.Context(), ""); err != nil {
		t.Errorf("releasing the empty id failed: %v", err)
	}
	if _, ok := s.Get("wf_1"); ok {
		t.Error("the lease survived its release")
	}
}

// TestStore_ListIsOrdered keeps the file's own bytes stable across rewrites, so a
// diff of runs.json shows a change rather than a reshuffle.
func TestStore_ListIsOrdered(t *testing.T) {
	t.Parallel()
	s := NewMemory()
	for _, id := range []string{"wf_c", "wf_a", "wf_b"} {
		if err := s.Put(t.Context(), &Lease{WorkflowID: id, Origin: OriginManual}); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}
	got := s.List()
	for i, want := range []string{"wf_a", "wf_b", "wf_c"} {
		if got[i].WorkflowID != want {
			t.Errorf("List()[%d] = %q, want %q", i, got[i].WorkflowID, want)
		}
	}
}

// TestStore_MemoryPersistsNothing pins the zero-path branch a test agent relies on:
// no file is created, and nothing fails for the want of one.
func TestStore_MemoryPersistsNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewMemory()
	if err := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the memory store wrote %d files", len(entries))
	}
}

// TestStore_WritesA0600File makes the file's mode a pinned FACT rather than a
// claim in a comment, which is what the store's doc asserts.
//
// The verification lives in the write, not in a pass afterwards:
// atomicfile.WriteFile runs EnforceMode on the open temp descriptor (fchmod then
// fstat, one handle) and fails the write rather than publishing a wider file, and
// the rename then publishes that verified inode. So the two shapes below are the
// ones that would catch a regression — a permissive umask, which is what a bare
// O_CREATE mode loses to, and a mode widened between two writes.
//
// Not parallel: it sets the process umask, which is per-process state.
func TestStore_WritesA0600File(t *testing.T) {
	dir := t.TempDir()
	// The umask a request has to survive. Restored before any assertion runs, so a
	// failure cannot leak it into the rest of the package.
	prev := syscall.Umask(0)
	s, err := NewStore(dir)
	if err != nil {
		syscall.Umask(prev)
		t.Fatalf("NewStore: %v", err)
	}
	putErr := s.Put(t.Context(), &Lease{WorkflowID: "wf_1", Origin: OriginManual})
	syscall.Umask(prev)
	if putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	path := filepath.Join(dir, FileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("%s is %v under umask 000, want 0600: the mode a caller asks for is a REQUEST, "+
			"and this file's is meant to be enforced by the write", FileName, got)
	}

	// A mode widened between two writes is corrected by the next one, because the
	// published inode is the temp's rather than the old file's.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := s.Release(t.Context(), "wf_1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("%s stayed %v after a rewrite, so a widened mode survives every later write", FileName, got)
	}
}
