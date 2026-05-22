package buffer

import (
	"strings"
	"sync"

	"vibekit/internal/api"
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

// LineTracker tracks per-file line changes across all chats.
type LineTracker struct {
	data map[api.ChatID]map[string][]LineRange
	mu   sync.RWMutex
}

// NewLineTracker creates a new LineTracker.
func NewLineTracker() *LineTracker {
	return &LineTracker{data: make(map[api.ChatID]map[string][]LineRange)}
}

// Record adds a line range for a file change.
func (lt *LineTracker) Record(chatID api.ChatID, filePath string, startLine, endLine, turn int, kind string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if lt.data[chatID] == nil {
		lt.data[chatID] = make(map[string][]LineRange)
	}
	chatFiles := lt.data[chatID]
	if _, exists := chatFiles[filePath]; !exists && len(chatFiles) >= maxFilesPerChat {
		var oldestPath string
		oldestTurn := int(^uint(0) >> 1)
		for p, ranges := range chatFiles {
			if len(ranges) > 0 {
				lastTurn := ranges[len(ranges)-1].Turn
				if lastTurn < oldestTurn {
					oldestTurn = lastTurn
					oldestPath = p
				}
			}
		}
		if oldestPath != "" {
			delete(chatFiles, oldestPath)
		}
	}
	existing := chatFiles[filePath]
	if len(existing) >= maxLineRangesPerFile {
		existing = existing[1:]
	}
	chatFiles[filePath] = append(existing, LineRange{
		StartLine: startLine,
		EndLine:   endLine,
		Turn:      turn,
		Kind:      kind,
	})
}

// RecordFromDiffs extracts line ranges from tool call diffs.
func (lt *LineTracker) RecordFromDiffs(chatID api.ChatID, diffs []api.ToolDiff, turn int, kind string) {
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
		lt.Record(chatID, d.Path, 1, lines, turn, kind)
	}
}

// Get returns the line ranges for a file in a chat.
func (lt *LineTracker) Get(chatID api.ChatID, filePath string) []LineRange {
	lt.mu.RLock()
	defer lt.mu.RUnlock()
	if lt.data[chatID] == nil {
		return nil
	}
	return lt.data[chatID][filePath]
}

// Clear removes all tracking data for a chat.
func (lt *LineTracker) Clear(chatID api.ChatID) {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	delete(lt.data, chatID)
}
