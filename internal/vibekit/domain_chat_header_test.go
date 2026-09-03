package vibekit

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestChatHeader_CarriesNoWorkspaceCatalog is the payload contract B4 exists to
// hold, and it is a size claim rather than a behaviour one — which is why it is
// asserted on the SERIALIZED header rather than on the struct.
//
// Per-field measurement of the 1.25 MiB /api/chats response: available_modes
// 1,236,118 B (93.1%), available_models 73,090 B (5.5%), everything else under
// 1%. 59 modes across 29 chats, identical in all of them, and the boot fetched
// the list twice. Re-adding either field would restore the whole cost silently:
// nothing else in the tree would fail.
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
	// The chat's own CHOICE from that vocabulary still rides the header: this is
	// the line between the two, and dropping it would break the mode pill.
	if !bytes.Contains(b, []byte(`"current_mode_id":"plan"`)) {
		t.Errorf("header lost current_mode_id, which is this chat's own choice: %s", b)
	}
	// And so does the effort vocabulary, because it is the vocabulary of THIS
	// chat's model rather than of the workspace.
	if !bytes.Contains(b, []byte(`"effort_levels"`)) {
		t.Errorf("header lost effort_levels, which is per-model rather than per-workspace: %s", b)
	}
}

// TestChat_PersistsNoWorkspaceCatalog is the same claim one layer down: the
// record on disk must not hold a second copy either, or the holder and the 29
// chat files can disagree about what the workspace can run.
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
