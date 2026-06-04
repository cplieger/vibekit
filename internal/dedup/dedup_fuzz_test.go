package dedup

import (
	"encoding/binary"
	"testing"
	"time"
)

func FuzzDedupCacheInvariant(f *testing.F) {
	f.Add([]byte("op1\x00key1\x00val1"))
	f.Add([]byte("op2\x00key2\x00val2val2val2"))

	f.Fuzz(func(t *testing.T, data []byte) {
		c := New(5*time.Minute, 10, 64)
		for len(data) >= 3 {
			op := data[0]
			keyLen := int(data[1])
			data = data[2:]
			if keyLen > len(data) {
				break
			}
			key := string(data[:keyLen])
			data = data[keyLen:]
			switch op % 2 {
			case 0:
				c.Check(key)
			case 1:
				valLen := 0
				if len(data) >= 2 {
					valLen = int(binary.LittleEndian.Uint16(data[:2]))
					data = data[2:]
					if valLen > len(data) {
						valLen = len(data)
					}
				}
				val := data[:valLen]
				data = data[valLen:]
				c.Record(key, val)
			}
		}
		c.Check("")
	})
}
