package hub

import (
	"bytes"
	"strconv"
	"testing"
	"time"
)

func TestCheckDedup_emptyReqIDIsMiss(t *testing.T) {
	h, _, _ := newTestHub()
	if got, ok := h.sse.idempotency.Check(""); ok || got != nil {
		t.Errorf("Check(\"\") = (%v, %v), want (nil, false)", got, ok)
	}
}

func TestRecordDedup_emptyReqIDIsNoop(t *testing.T) {
	h, _, _ := newTestHub()
	h.recordDedup("", []byte(`{"ok":true}`))
	if got, ok := h.sse.idempotency.Check(""); ok || got != nil {
		t.Errorf("empty key stored: (%v, %v)", got, ok)
	}
	h.sse.idempotency.mu.Lock()
	defer h.sse.idempotency.mu.Unlock()
	if len(h.sse.idempotency.entries) != 0 {
		t.Errorf("idempotency map = %d entries, want 0", len(h.sse.idempotency.entries))
	}
}

func TestRecordDedup_roundTrip(t *testing.T) {
	h, _, _ := newTestHub()
	want := []byte(`{"result":"ok"}`)
	h.recordDedup("req-1", want)
	got, ok := h.sse.idempotency.Check("req-1")
	if !ok {
		t.Fatal("Check(req-1) = (_, false), want true")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Check(req-1) = %q, want %q", got, want)
	}
}

func TestRecordDedup_overwrites(t *testing.T) {
	h, _, _ := newTestHub()
	h.recordDedup("r", []byte("first"))
	h.recordDedup("r", []byte("second"))
	got, ok := h.sse.idempotency.Check("r")
	if !ok || string(got) != "second" {
		t.Errorf("overwrite: got (%q, %v), want (\"second\", true)", got, ok)
	}
}

func TestRecordDedup_skipsOversizeResults(t *testing.T) {
	h, _, _ := newTestHub()
	big := bytes.Repeat([]byte("x"), idempotencyMaxResult+1)
	h.recordDedup("big", big)
	if _, ok := h.sse.idempotency.Check("big"); ok {
		t.Error("oversize result was cached, should be skipped to bound cache RSS")
	}
}

func TestRecordDedup_evictsOldestAtCap(t *testing.T) {
	h, _, _ := newTestHub()
	// Fill to cap with ascending timestamps so the oldest entry is
	// unambiguous. Keys must be unique; use a formatted integer.
	base := time.Now().Add(-time.Hour)
	h.sse.idempotency.mu.Lock()
	for i := range idempotencyMaxEntries {
		key := "req-" + strconv.Itoa(i)
		h.sse.idempotency.entries[key] = idempotencyEntry{ts: base.Add(time.Duration(i) * time.Second), result: []byte("x")}
	}
	// Pin "req-0" to a clearly-oldest timestamp below every other
	// entry so the eviction scan must pick it.
	h.sse.idempotency.entries["req-0"] = idempotencyEntry{ts: base.Add(-time.Hour), result: []byte("x")}
	h.sse.idempotency.mu.Unlock()

	// Inserting a new key must evict "req-0" to stay at cap.
	h.recordDedup("fresh", []byte("y"))

	h.sse.idempotency.mu.Lock()
	defer h.sse.idempotency.mu.Unlock()
	if _, ok := h.sse.idempotency.entries["req-0"]; ok {
		t.Error("oldest entry was not evicted on cap overflow")
	}
	if _, ok := h.sse.idempotency.entries["fresh"]; !ok {
		t.Error("fresh entry was not recorded after eviction")
	}
	if len(h.sse.idempotency.entries) > idempotencyMaxEntries {
		t.Errorf("map grew past cap: got %d, want <= %d", len(h.sse.idempotency.entries), idempotencyMaxEntries)
	}
}

func TestPruneExpired(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	ttl := 5 * time.Minute
	entries := map[string]idempotencyEntry{
		"fresh":   {ts: now.Add(-1 * time.Minute)},
		"edge":    {ts: now.Add(-ttl)}, // exactly at cutoff -> NOT pruned (strict before)
		"expired": {ts: now.Add(-ttl - time.Second)},
		"ancient": {ts: now.Add(-24 * time.Hour)},
	}
	removed := pruneExpired(now, ttl, entries)
	if removed != 2 {
		t.Errorf("pruneExpired removed %d, want 2", removed)
	}
	if _, ok := entries["fresh"]; !ok {
		t.Error("fresh pruned")
	}
	if _, ok := entries["edge"]; !ok {
		t.Error("edge (exactly ttl) pruned — strict-before semantics broken")
	}
	if _, ok := entries["expired"]; ok {
		t.Error("expired retained")
	}
	if _, ok := entries["ancient"]; ok {
		t.Error("ancient retained")
	}
}

func TestPruneExpired_empty(t *testing.T) {
	got := pruneExpired(time.Now(), time.Minute, map[string]idempotencyEntry{})
	if got != 0 {
		t.Errorf("pruneExpired(empty) removed %d, want 0", got)
	}
}

func BenchmarkIdempotencyCache_Record(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 128)

	cases := []struct {
		name string
		fill float64 // fraction of maxEntries to pre-fill
	}{
		{"empty", 0},
		{"half", 0.5},
		{"at_capacity", 1.0},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			c := newIdempotencyCache()
			prefill := int(tc.fill * float64(c.maxEntries))
			for i := range prefill {
				c.Record("prefill-"+strconv.Itoa(i), payload)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				c.Record("bench-"+strconv.Itoa(i), payload)
			}
		})
	}
}
