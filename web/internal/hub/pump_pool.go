package hub

import "sync"

// pumpBufPool is a pool of 4 KB byte slices reused by PTY/terminal
// output pump goroutines to avoid per-goroutine allocations.
var pumpBufPool = sync.Pool{
	New: func() any { return make([]byte, 4096) },
}

// getPumpBuf fetches a pooled 4 KB buffer and performs the type
// assertion once. On the impossible path where the pool yields an
// unexpected type (only possible if pumpBufPool.New is ever changed
// incorrectly), it falls back to a fresh slice so callers never get nil.
func getPumpBuf() []byte {
	buf, ok := pumpBufPool.Get().([]byte)
	if !ok {
		return make([]byte, 4096)
	}
	return buf
}
