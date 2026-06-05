package checkpoint

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// FuzzCrossChatIndexCheckInvariant applies a sequence of random
// snapshot events to the cross-chat index, then verifies check()
// never panics and returns consistent observations.
func FuzzCrossChatIndexCheckInvariant(f *testing.F) {
	f.Add([]byte("\x01\x01a\x03f.go\x03abc\x03def"))

	f.Fuzz(func(t *testing.T, data []byte) {
		idx := newCrossChatIndex()
		// Apply random events
		for len(data) >= 4 {
			chatLen := int(data[0] % 4)
			pathLen := int(data[1] % 8)
			data = data[2:]
			if chatLen+pathLen > len(data) {
				break
			}
			chatID := fmt.Sprintf("chat-%d", chatLen)
			path := fmt.Sprintf("file-%d.go", pathLen)
			// Consume 2 bytes for tag
			if len(data) < 2 {
				break
			}
			tag := fmt.Sprintf("%d.%d", data[0], data[1])
			data = data[2:]
			// Consume bytes for SHAs
			if len(data) < 4 {
				break
			}
			beforeSHA := fmt.Sprintf("%x", binary.LittleEndian.Uint16(data[:2]))
			afterSHA := fmt.Sprintf("%x", binary.LittleEndian.Uint16(data[2:4]))
			data = data[4:]

			ev := &event{
				Kind:      kindSnapshot,
				Tag:       tag,
				Path:      path,
				BeforeSHA: beforeSHA,
				AfterSHA:  afterSHA,
			}
			idx.apply(chatID, ev)
		}
		// Verify check never panics
		idx.check("chat-0", "file-0.go", "abc")
		idx.check("chat-1", "file-1.go", "")
		idx.check("nonexistent", "nonexistent.go", "xyz")
	})
}
