package translate

import (
	"encoding/json"
	"testing"
)

// TestACPKiroBlock_SourceIsNestedUnderMetaKiro pins the ONE trap the steer
// discriminator has: KAS's replay builder writes
// `{kiro:{…, messageId, timestamp, ...t.source==="steer"?{source:"steer"}:{}}}`,
// so `source` is a member of `_meta.kiro` and NOT of the update object. Re-nesting
// it decodes to "" for every frame, which is indistinguishable from a wire that
// never sent one — so both directions are asserted.
func TestACPKiroBlock_SourceIsNestedUnderMetaKiro(t *testing.T) {
	t.Run("_meta.kiro.source lands on the field", func(t *testing.T) {
		var u struct {
			Meta ACPKiroMeta `json:"_meta"`
		}
		raw := []byte(`{"sessionUpdate":"user_message_chunk",
			"content":{"type":"text","text":"use tabs"},
			"_meta":{"kiro":{"replay":true,"messageId":"steer-m-1","source":"steer"}}}`)
		if err := json.Unmarshal(raw, &u); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if u.Meta.Kiro.Source != "steer" {
			t.Errorf("Source = %q, want %q", u.Meta.Kiro.Source, "steer")
		}
	})

	t.Run("source on the UPDATE object does not", func(t *testing.T) {
		var u struct {
			Meta ACPKiroMeta `json:"_meta"`
		}
		raw := []byte(`{"sessionUpdate":"user_message_chunk","source":"steer",
			"content":{"type":"text","text":"use tabs"},
			"_meta":{"kiro":{"replay":true,"messageId":"steer-m-1"}}}`)
		if err := json.Unmarshal(raw, &u); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if u.Meta.Kiro.Source != "" {
			t.Errorf("Source = %q, want empty — a `source` on the update object is NOT the discriminator", u.Meta.Kiro.Source)
		}
	})
}
