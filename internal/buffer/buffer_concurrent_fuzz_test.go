package buffer

import (
	"sync"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// FuzzBufferConcurrentBlockAppend exercises the mutex-protected block
// append paths (AppendTextDelta, AppendThinkingDelta, AppendToolUseBlock)
// under concurrent callers, each op carrying one of two subtask ids so the
// interleaved case — two agents streaming into ONE array — is in the population.
//
// The invariant: block indices returned are always valid (0 <= idx <
// len(Blocks)), and within ONE subtask's subsequence no two consecutive blocks
// share a coalescing type.
//
// Filtering to the subtask BEFORE testing adjacency is the whole rule, and array
// adjacency cannot state it. The defect this guards against cut a parent's
// paragraph at every delegate delta, and the two halves are separated BY that
// delegate's block — so an array-adjacent test never has the pair in hand and
// passes on the broken buffer. Two adjacent text blocks in the array are legal
// when they belong to different streams; two text blocks of the SAME stream with
// nothing of that stream between them never are.
//
// The concatenation property (a subtask's blocks in array order reproduce its
// deltas in call order) is deliberately NOT asserted: call order across
// goroutines is not observable from here, so it would be asserting a schedule.
// The per-subtask adjacency invariant is its order-free half.
func FuzzBufferConcurrentBlockAppend(f *testing.F) {
	f.Add([]byte{0, 1, 2, 0, 0, 1, 1, 2, 0, 1})
	f.Add([]byte{2, 2, 2, 2, 2})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	// Both subtasks in one run: op/3 selects the id, so 0/1/2 are the parent's
	// and 3/4/5 a delegate's.
	f.Add([]byte{0, 0, 3, 0, 3, 1, 4, 2, 5, 0, 3})

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
					sub := ""
					if op/3%2 == 1 {
						sub = "agent-1"
					}
					switch op % 3 {
					case 0:
						idx, _ := buf.AppendTextDelta("t", sub)
						if idx < 0 {
							t.Errorf("AppendTextDelta returned negative index: %d", idx)
						}
					case 1:
						idx, _ := buf.AppendThinkingDelta("r", sub)
						if idx < 0 {
							t.Errorf("AppendThinkingDelta returned negative index: %d", idx)
						}
					case 2:
						idx := buf.AppendToolUseBlock("tc-1", sub)
						if idx < 0 {
							t.Errorf("AppendToolUseBlock returned negative index: %d", idx)
						}
					}
				}
			}()
		}
		wg.Wait()

		// Post-condition: per subtask, no two CONSECUTIVE blocks of that subtask
		// share a coalescing type. `last` tracks each subtask's own most recent
		// block, so blocks of other subtasks are skipped rather than read as
		// separators — that skip is what lets this fail on a buffer that cut a
		// stream's run at a foreign delta. tool_use is excluded because
		// back-to-back tool calls legitimately each get their own block.
		buf.mu.Lock()
		blocks := buf.Blocks
		buf.mu.Unlock()
		last := make(map[string]int, 2)
		for i := range blocks {
			sub := blocks[i].AgentSubtaskID
			typ := blocks[i].Type
			if typ == vibekit.BlockText || typ == vibekit.BlockThinking {
				if j, seen := last[sub]; seen && blocks[j].Type == typ {
					t.Fatalf("consecutive %s of subtask %q at indices %d and %d with no block of that subtask between them",
						typ, sub, j, i)
				}
			}
			last[sub] = i
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
