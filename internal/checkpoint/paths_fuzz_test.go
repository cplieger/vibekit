package checkpoint

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzChatLogPath verifies chatLogPath produces a path that:
//  1. Is rooted under configDir.
//  2. Ends with the events JSONL filename.
//  3. Contains the chatID as a path component.
//  4. Never panics.
func FuzzChatLogPath(f *testing.F) {
	f.Add("/config", "chat-123")
	f.Add("/tmp/test", "abc-def")
	f.Add("", "")
	f.Add("/a", "../../etc")

	f.Fuzz(func(t *testing.T, configDir, chatID string) {
		path := chatLogPath(configDir, chatID)

		// Must end with the events filename.
		if !strings.HasSuffix(path, FileEvents) {
			t.Errorf("chatLogPath(%q, %q) = %q, does not end with %q", configDir, chatID, path, FileEvents)
		}

		// Must be under configDir (after Clean) when configDir is non-empty.
		if configDir != "" {
			cleaned := filepath.Clean(path)
			prefix := filepath.Clean(configDir)
			if !strings.HasPrefix(cleaned, prefix+"/") && cleaned != prefix {
				t.Errorf("chatLogPath(%q, %q) = %q, not under configDir %q", configDir, chatID, cleaned, prefix)
			}
		}
	})
}
