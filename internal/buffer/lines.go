package buffer

import (
	"container/heap"
	"strings"
	"sync"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// LineRange is a range of lines modified by the agent.
type LineRange struct {
	Kind      string `json:"kind"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Turn      int    `json:"turn"`
}

// maxLineRangesPerFile caps per-file ranges.
const maxLineRangesPerFile = 200

// maxFilesPerChat caps the number of distinct file paths tracked per chat.
const maxFilesPerChat = 500

// fileHeapEntry tracks a file's last turn for heap-based eviction.
type fileHeapEntry struct {
	path     string
	lastTurn int
	index    int // heap index
}

// fileHeap implements heap.Interface for O(log n) eviction of the oldest file.
type fileHeap []*fileHeapEntry

func (h fileHeap) Len() int           { return len(h) }
func (h fileHeap) Less(i, j int) bool { return h[i].lastTurn < h[j].lastTurn }
func (h fileHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *fileHeap) Push(x any)        { e, _ := x.(*fileHeapEntry); e.index = len(*h); *h = append(*h, e) }

func (h *fileHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	e.index = -1
	*h = old[:n-1]
	return e
}

// chatLineState holds per-chat line tracking data with a heap for eviction.
type chatLineState struct {
	ranges  map[string][]LineRange
	entries map[string]*fileHeapEntry
	h       fileHeap
}

// LineTracker tracks per-file line changes across all chats.
type LineTracker struct {
	data map[vibekit.ChatID]*chatLineState
	mu   sync.RWMutex
}

// NewLineTracker creates a new LineTracker.
func NewLineTracker() *LineTracker {
	return &LineTracker{data: make(map[vibekit.ChatID]*chatLineState)}
}

// Record adds a line range for a file change.
//
// The range travels as a LineRange rather than as `startLine, endLine, turn int,
// kind string`: three adjacent ints with no relationship the compiler can see,
// where a transposition is silent. Swapping start and end stores an inverted
// range the editor's changed-line gutter then paints backwards or not at all;
// swapping either with turn corrupts the eviction key, because the heap orders
// files by lastTurn and a line number used as a turn makes the wrong file the
// oldest. The struct is the one the tracker stores anyway, so this also deletes
// a field-by-field copy that could drift from it.
func (lt *LineTracker) Record(chatID vibekit.ChatID, filePath string, r LineRange) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	state := lt.data[chatID]
	if state == nil {
		state = &chatLineState{
			ranges:  make(map[string][]LineRange),
			entries: make(map[string]*fileHeapEntry),
		}
		lt.data[chatID] = state
	}
	if _, exists := state.ranges[filePath]; !exists && len(state.ranges) >= maxFilesPerChat {
		// Evict oldest file via heap pop — O(log n).
		e, _ := heap.Pop(&state.h).(*fileHeapEntry)
		delete(state.ranges, e.path)
		delete(state.entries, e.path)
	}
	existing := state.ranges[filePath]
	if len(existing) >= maxLineRangesPerFile {
		existing = existing[1:]
	}
	state.ranges[filePath] = append(existing, r)
	// Update or insert heap entry.
	if e, ok := state.entries[filePath]; ok {
		e.lastTurn = r.Turn
		heap.Fix(&state.h, e.index)
	} else {
		e = &fileHeapEntry{path: filePath, lastTurn: r.Turn}
		state.entries[filePath] = e
		heap.Push(&state.h, e)
	}
}

// RecordFromDiffs extracts line ranges from tool call diffs.
func (lt *LineTracker) RecordFromDiffs(chatID vibekit.ChatID, diffs []vibekit.ToolDiff, turn int, kind string) {
	for _, d := range diffs {
		if d.Path == "" || d.NewText == "" {
			continue
		}
		lines := strings.Count(d.NewText, "\n")
		if !strings.HasSuffix(d.NewText, "\n") {
			lines++
		}
		if lines == 0 {
			lines = 1
		}
		lt.Record(chatID, d.Path, LineRange{StartLine: 1, EndLine: lines, Turn: turn, Kind: kind})
	}
}

// Get returns the line ranges for a file in a chat.
func (lt *LineTracker) Get(chatID vibekit.ChatID, filePath string) []LineRange {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	state := lt.data[chatID]
	if state == nil {
		return nil
	}
	return state.ranges[filePath]
}

// Clear removes all tracking data for a chat.
func (lt *LineTracker) Clear(chatID vibekit.ChatID) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	delete(lt.data, chatID)
}
