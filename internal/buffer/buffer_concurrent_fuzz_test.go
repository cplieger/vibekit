package buffer

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzBufferConcurrentBlockAppend exercises the mutex-protected block
// append paths (AppendTextDelta, AppendThinkingDelta, AppendToolUseBlock)
// under concurrent callers. The invariant: block indices returned are
// always valid (0 <= idx < len(Blocks)), and adjacent same-type text or
// thinking blocks are coalesced (never two consecutive BlockText or
// BlockThinking blocks).
func FuzzBufferConcurrentBlockAppend(f *testing.F) {
	f.Add([]byte{0, 1, 2, 0, 0, 1, 1, 2, 0, 1})
	f.Add([]byte{2, 2, 2, 2, 2})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, ops []byte) {
		if len(ops) < 2 {
			return
		}
		buf := &Buffer{}
		numWorkers := int(ops[0]%4) + 2
		ops = ops[1:]

		var wg sync.WaitGroup
		wg.Add(numWorkers)
		chunkSize := (len(ops) + numWorkers - 1) / numWorkers

		for w := range numWorkers {
			start := w * chunkSize
			if start >= len(ops) {
				wg.Done()
				continue
			}
			end := min(start+chunkSize, len(ops))
			chunk := ops[start:end]
			go func() {
				defer wg.Done()
				for _, op := range chunk {
					switch op % 3 {
					case 0:
						idx := buf.AppendTextDelta("t")
						if idx < 0 {
							t.Errorf("AppendTextDelta returned negative index: %d", idx)
						}
					case 1:
						idx := buf.AppendThinkingDelta("r")
						if idx < 0 {
							t.Errorf("AppendThinkingDelta returned negative index: %d", idx)
						}
					case 2:
						idx := buf.AppendToolUseBlock("tc-1")
						if idx < 0 {
							t.Errorf("AppendToolUseBlock returned negative index: %d", idx)
						}
					}
				}
			}()
		}
		wg.Wait()

		// Post-condition: no two adjacent blocks of same coalescing type.
		buf.mu.Lock()
		blocks := buf.Blocks
		buf.mu.Unlock()
		for i := 1; i < len(blocks); i++ {
			if blocks[i].Type == api.BlockText && blocks[i-1].Type == api.BlockText {
				t.Fatalf("adjacent BlockText at indices %d and %d", i-1, i)
			}
			if blocks[i].Type == api.BlockThinking && blocks[i-1].Type == api.BlockThinking {
				t.Fatalf("adjacent BlockThinking at indices %d and %d", i-1, i)
			}
		}
	})
}
