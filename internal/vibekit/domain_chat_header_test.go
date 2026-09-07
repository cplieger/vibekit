package vibekit

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The header must not carry the workspace catalog. Measured per field on a 1.25 MiB
// /api/chats response: available_modes 93.1% of it, available_models 5.5%, the same 59 modes
// repeated in every chat. Re-adding either field restores the whole cost silently, and it is
// a size claim rather than a behaviour one, hence the assertion on the SERIALIZED header.
func TestChatHeader_CarriesNoWorkspaceCatalog(t *testing.T) {
	c := &Chat{
		ID:            "c1",
		Name:          "Hello",
		Model:         "claude",
		CurrentModeID: "plan",
		EffortLevels:  []SessionEffortLevel{{ID: "high", Name: "High"}},
	}

	b, err := json.Marshal(c.Header())
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}

	for _, key := range []string{`"available_modes"`, `"available_models"`} {
		if bytes.Contains(b, []byte(key)) {
			t.Errorf("header carries %s — the workspace catalog belongs to /api/config-template, once: %s",
				key, b)
		}
	}
	// The chat's own CHOICE from that vocabulary still rides the header; dropping it would
	// break the mode pill.
	if !bytes.Contains(b, []byte(`"current_mode_id":"plan"`)) {
		t.Errorf("header lost current_mode_id, which is this chat's own choice: %s", b)
	}
	// And so does effort, which is the vocabulary of THIS chat's model, not the workspace's.
	if !bytes.Contains(b, []byte(`"effort_levels"`)) {
		t.Errorf("header lost effort_levels, which is per-model rather than per-workspace: %s", b)
	}
}

// The same claim one layer down: the record on disk must not hold a second copy either, or
// the holder and the chat files can disagree about what the workspace can run.
func TestChat_PersistsNoWorkspaceCatalog(t *testing.T) {
	b, err := json.Marshal(&Chat{ID: "c1", Name: "Hello", Messages: []Message{}})
	if err != nil {
		t.Fatalf("marshal chat: %v", err)
	}
	for _, key := range []string{`"available_modes"`, `"available_models"`} {
		if bytes.Contains(b, []byte(key)) {
			t.Errorf("the persisted chat carries %s — one copy, in agent.Catalog: %s", key, b)
		}
	}
}
