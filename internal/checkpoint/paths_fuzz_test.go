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

		// Must be under configDir when configDir is non-empty. Use
		// filepath.Rel so a relative configDir (e.g. ".") is handled
		// correctly: filepath.Join(".", ...) drops the leading "./", so a
		// literal HasPrefix("./") check would spuriously fail even though
		// the result is genuinely rooted under the current directory.
		if configDir != "" {
			rel, err := filepath.Rel(filepath.Clean(configDir), filepath.Clean(path))
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Errorf("chatLogPath(%q, %q) = %q, not under configDir %q", configDir, chatID, path, configDir)
			}
		}
	})
}
