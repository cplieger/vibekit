package hub

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/dedup"
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
	big := bytes.Repeat([]byte("x"), dedup.DefaultMaxResult+1)
	h.recordDedup("big", big)
	if _, ok := h.sse.idempotency.Check("big"); ok {
		t.Error("oversize result was cached, should be skipped to bound cache RSS")
	}
}

func TestRecordDedup_evictsOldestAtCap(t *testing.T) {
	// Use a small cache to test eviction without filling 10k entries.
	c := dedup.New(dedup.DefaultTTL, 100, dedup.DefaultMaxResult)
	for i := range 100 {
		c.Record("req-"+strconv.Itoa(i), []byte("x"))
	}
	// Insert one more — should evict the oldest.
	c.Record("fresh", []byte("y"))
	if _, ok := c.Check("fresh"); !ok {
		t.Error("fresh entry was not recorded after eviction")
	}
}

func TestPruneExpired(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	ttl := 5 * time.Minute
	entries := map[string]dedup.Entry{
		"fresh":   {Ts: now.Add(-1 * time.Minute)},
		"edge":    {Ts: now.Add(-ttl)}, // exactly at cutoff -> NOT pruned (strict before)
		"expired": {Ts: now.Add(-ttl - time.Second)},
		"ancient": {Ts: now.Add(-24 * time.Hour)},
	}
	removed := dedup.PruneExpired(now, ttl, entries)
	if removed != 2 {
		t.Errorf("PruneExpired removed %d, want 2", removed)
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
	got := dedup.PruneExpired(time.Now(), time.Minute, map[string]dedup.Entry{})
	if got != 0 {
		t.Errorf("PruneExpired(empty) removed %d, want 0", got)
	}
}

func BenchmarkIdempotencyCache_Record(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 128)

	cases := []struct {
		name string
		fill float64
	}{
		{"empty", 0},
		{"half", 0.5},
		{"at_capacity", 1.0},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			c := dedup.New(dedup.DefaultTTL, dedup.DefaultMaxEntries, dedup.DefaultMaxResult)
			prefill := int(tc.fill * float64(dedup.DefaultMaxEntries))
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
