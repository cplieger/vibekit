package buffer

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
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
						idx, _ := buf.AppendTextDelta("t", "")
						if idx < 0 {
							t.Errorf("AppendTextDelta returned negative index: %d", idx)
						}
					case 1:
						idx, _ := buf.AppendThinkingDelta("r", "")
						if idx < 0 {
							t.Errorf("AppendThinkingDelta returned negative index: %d", idx)
						}
					case 2:
						idx := buf.AppendToolUseBlock("tc-1", "")
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
			if blocks[i].Type == vibekit.BlockText && blocks[i-1].Type == vibekit.BlockText {
				t.Fatalf("adjacent BlockText at indices %d and %d", i-1, i)
			}
			if blocks[i].Type == vibekit.BlockThinking && blocks[i-1].Type == vibekit.BlockThinking {
				t.Fatalf("adjacent BlockThinking at indices %d and %d", i-1, i)
			}
		}
	})
}

// TestBuffer_EmittedNothingIsRaceFreeAgainstAppenders drives EmittedNothing from
// a reader goroutine while writers append text, thinking and tool-use blocks —
// the real shape, where the prompt's goroutine asks "did this turn produce
// anything" while the dispatch loop is still appending to the same buffer.
//
// The four accumulators it reads are EXPORTED and documented "guarded by mu", so
// the defect this pins is a caller reading them through the field instead of
// through a method: every read in this package takes the lock, and one caller in
// internal/agent did not. Only meaningful under -race, which CI runs.
//
// Red-checked: dropping the lock from EmittedNothing makes this report
// "WARNING: DATA RACE ... Write at ... by goroutine N / Previous read at ...",
// on strings.Builder.Len against AppendTextDelta.
func TestBuffer_EmittedNothingIsRaceFreeAgainstAppenders(t *testing.T) {
	buf := &Buffer{}

	const writers = 4
	const iterations = 200

	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range iterations {
				switch (w + i) % 3 {
				case 0:
					buf.AppendTextDelta("t", "")
				case 1:
					buf.AppendThinkingDelta("r", "")
				case 2:
					buf.AppendToolUseBlock("tc-1", "")
				}
			}
		})
	}

	// The reader races the writers deliberately; its ANSWER is not asserted,
	// because "empty" is only true until the first append lands and pinning a
	// value here would be asserting a scheduling order. What is asserted is that
	// asking the question concurrently is safe, and that it becomes false and
	// stays false once the writers are done.
	wg.Go(func() {
		for range writers * iterations {
			_ = buf.EmittedNothing()
		}
	})

	wg.Wait()

	if buf.EmittedNothing() {
		t.Error("EmittedNothing() = true after 800 appends, want false")
	}
}

// TestBuffer_EmittedNothingCountsEachAccumulator pins the four-field rule one
// field at a time, so a future edit that drops one from the predicate fails here
// rather than silently making an empty-turn retry spend a second model call to
// reproduce work the user already watched arrive.
func TestBuffer_EmittedNothingCountsEachAccumulator(t *testing.T) {
	cases := map[string]func(*Buffer){
		"content": func(b *Buffer) { b.AppendTextDelta("hello", "") },
		"reasoning": func(b *Buffer) {
			b.AppendThinkingDelta("thinking", "")
		},
		"tool call": func(b *Buffer) {
			b.AppendToolCall(&vibekit.ToolCall{ID: "tc-1"})
		},
		"block only": func(b *Buffer) { b.AppendToolUseBlock("tc-1", "") },
	}

	for name, emit := range cases {
		t.Run(name, func(t *testing.T) {
			buf := &Buffer{}
			if !buf.EmittedNothing() {
				t.Fatal("fresh buffer: EmittedNothing() = false, want true")
			}
			emit(buf)
			if buf.EmittedNothing() {
				t.Errorf("after emitting %s: EmittedNothing() = true, want false", name)
			}
		})
	}
}
