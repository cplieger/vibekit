package dedup

import (
	"encoding/binary"
	"testing"
	"time"
)

// FuzzPruneExpired exercises the TTL pruning path with arbitrary
// entry timestamps to verify it never panics and always removes
// exactly the entries older than TTL.
func FuzzPruneExpired(f *testing.F) {
	f.Add([]byte("\x03k1\x01\x00\x00\x00\x00\x00\x00\x00\x03k2\xff\xff\xff\xff\xff\xff\xff\xff"))

	f.Fuzz(func(t *testing.T, data []byte) {
		entries := make(map[string]Entry)
		for len(data) >= 9 {
			keyLen := int(data[0])
			data = data[1:]
			if keyLen > len(data)-8 {
				break
			}
			key := string(data[:keyLen])
			data = data[keyLen:]
			ts := int64(binary.LittleEndian.Uint64(data[:8]))
			data = data[8:]
			entries[key] = Entry{Ts: time.Unix(0, ts)}
		}
		if len(entries) == 0 {
			return
		}
		before := len(entries)
		now := time.Now()
		ttl := 5 * time.Minute
		removed := PruneExpired(now, ttl, entries)
		after := len(entries)
		if before-removed != after {
			t.Fatalf("count mismatch: before=%d removed=%d after=%d", before, removed, after)
		}
		cutoff := now.Add(-ttl)
		for k, e := range entries {
			if e.Ts.Before(cutoff) {
				t.Fatalf("entry %q with ts %v should have been pruned (cutoff %v)", k, e.Ts, cutoff)
			}
		}
	})
}
