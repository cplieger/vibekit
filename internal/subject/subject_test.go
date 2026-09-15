package subject

import (
	"strconv"
	"sync"
	"testing"
)

func TestBumpCounter_StrictlyIncreasingDecimal(t *testing.T) {
	var v Versions
	prev := uint64(0)
	for i := range 50 {
		got := v.BumpCounter(KindChat, "c-1")
		n, err := strconv.ParseUint(got, 10, 64)
		if err != nil {
			t.Fatalf("BumpCounter #%d = %q, want a decimal uint64: %v", i, got, err)
		}
		if n <= prev {
			t.Fatalf("BumpCounter #%d = %d, want > %d", i, n, prev)
		}
		prev = n
	}
	if got, ok := v.Current(KindChat, "c-1"); !ok || got != "50" {
		t.Fatalf("Current(chat, c-1) = (%q, %v), want (\"50\", true)", got, ok)
	}
}

func TestBumpCounter_KeysAreIndependent(t *testing.T) {
	var v Versions
	v.BumpCounter(KindChat, "a")
	v.BumpCounter(KindChat, "a")
	v.BumpCounter(KindChat, "b")
	v.BumpCounter(KindChats, "")
	if got, _ := v.Current(KindChat, "a"); got != "2" {
		t.Errorf("Current(chat, a) = %q, want \"2\"", got)
	}
	if got, _ := v.Current(KindChat, "b"); got != "1" {
		t.Errorf("Current(chat, b) = %q, want \"1\"", got)
	}
	if got, _ := v.Current(KindChats, ""); got != "1" {
		t.Errorf("Current(chats, \"\") = %q, want \"1\"", got)
	}
}

func TestCurrent_UnmintedAnswersZeroFalse(t *testing.T) {
	var v Versions
	got, ok := v.Current(KindPending, "")
	if ok || got != Unminted {
		t.Fatalf("Current(pending, \"\") = (%q, %v), want (%q, false)", got, ok, Unminted)
	}
	v.BumpCounter(KindStatus, "")
	got, ok = v.Current(KindPending, "")
	if ok || got != Unminted {
		t.Fatalf("Current(pending, \"\") after an unrelated bump = (%q, %v), want (%q, false)", got, ok, Unminted)
	}
}

func TestSet_RoundTrips(t *testing.T) {
	var v Versions
	v.Set(KindLiveTurn, "c-1", "7:12")
	got, ok := v.Current(KindLiveTurn, "c-1")
	if !ok || got != "7:12" {
		t.Fatalf("Current(live_turn, c-1) = (%q, %v), want (\"7:12\", true)", got, ok)
	}
	v.Set(KindLiveTurn, "c-1", "7:13")
	if got, _ = v.Current(KindLiveTurn, "c-1"); got != "7:13" {
		t.Fatalf("Current(live_turn, c-1) after second Set = %q, want \"7:13\"", got)
	}
}

func TestBumpCounter_ConcurrentBumpsAreAllCounted(t *testing.T) {
	var v Versions
	const workers, perWorker = 16, 200
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perWorker {
				v.BumpCounter(KindRuns, "")
			}
		})
	}
	wg.Wait()
	want := strconv.Itoa(workers * perWorker)
	if got, ok := v.Current(KindRuns, ""); !ok || got != want {
		t.Fatalf("Current(runs, \"\") = (%q, %v), want (%q, true)", got, ok, want)
	}
}
