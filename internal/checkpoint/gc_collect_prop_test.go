package checkpoint

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pgregory.net/rapid"
)

// TestGCCollect_RapidNoFalseNegatives verifies that every SHA written to
// event logs is present in the collected reference set.
func TestGCCollect_RapidNoFalseNegatives(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()
		chatsDir := filepath.Join(dir, "chats")
		if err := os.MkdirAll(chatsDir, 0o755); err != nil {
			rt.Fatal(err)
		}

		numChats := rapid.IntRange(1, 5).Draw(rt, "numChats")
		allSHAs := make(map[string]bool)

		for range numChats {
			chatID := rapid.StringMatching(`chat-[a-z0-9]{4}`).Draw(rt, "chatID")
			chatDir := filepath.Join(chatsDir, chatID)
			if err := os.MkdirAll(chatDir, 0o755); err != nil {
				rt.Fatal(err)
			}

			numEvents := rapid.IntRange(1, 10).Draw(rt, "numEvents")
			f, err := os.Create(filepath.Join(chatDir, "events.jsonl"))
			if err != nil {
				rt.Fatal(err)
			}
			enc := json.NewEncoder(f)
			for range numEvents {
				ev := struct {
					BeforeSHA string `json:"before_sha,omitempty"`
					AfterSHA  string `json:"after_sha,omitempty"`
				}{}
				if rapid.Bool().Draw(rt, "hasBefore") {
					ev.BeforeSHA = rapid.StringMatching(`[0-9a-f]{8}`).Draw(rt, "beforeSHA")
					allSHAs[ev.BeforeSHA] = true
				}
				if rapid.Bool().Draw(rt, "hasAfter") {
					ev.AfterSHA = rapid.StringMatching(`[0-9a-f]{8}`).Draw(rt, "afterSHA")
					allSHAs[ev.AfterSHA] = true
				}
				_ = enc.Encode(ev)
			}
			f.Close()
		}

		// Simulate GC reference collection: read all event logs.
		collected := make(map[string]bool)
		entries, err := os.ReadDir(chatsDir)
		if err != nil {
			rt.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			evPath := filepath.Join(chatsDir, entry.Name(), "events.jsonl")
			data, err := os.ReadFile(evPath)
			if err != nil {
				continue
			}
			dec := json.NewDecoder(bytes.NewReader(data))
			for dec.More() {
				var ev struct {
					BeforeSHA string `json:"before_sha,omitempty"`
					AfterSHA  string `json:"after_sha,omitempty"`
				}
				if err := dec.Decode(&ev); err != nil {
					continue
				}
				if ev.BeforeSHA != "" {
					collected[ev.BeforeSHA] = true
				}
				if ev.AfterSHA != "" {
					collected[ev.AfterSHA] = true
				}
			}
		}

		// Invariant: no false negatives.
		for sha := range allSHAs {
			if !collected[sha] {
				rt.Fatalf("SHA %q written but not collected — would cause premature blob deletion", sha)
			}
		}
	})
}
