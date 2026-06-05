package translate

import (
	"encoding/json"
	"testing"
)

func FuzzACPToolCallContentBlockDecode(f *testing.F) {
	f.Add([]byte(`{"type":"content","content":{"text":"hello"}}`))
	f.Add([]byte(`{"type":"diff","path":"/src/main.go","oldText":"a","newText":"b"}`))
	f.Add([]byte(`{"type":"","path":"","content":{"text":""}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var block ACPToolCallContentBlock
		if json.Unmarshal(data, &block) != nil {
			return
		}
		b, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
		var block2 ACPToolCallContentBlock
		if json.Unmarshal(b, &block2) != nil {
			t.Fatal("round-trip decode failed")
		}
		if block.Type != block2.Type {
			t.Fatalf("type mismatch: %q vs %q", block.Type, block2.Type)
		}
	})
}

func FuzzCrewNotifSubagentDecode(f *testing.F) {
	f.Add([]byte(`{"sessionId":"s1","sessionName":"w","agentName":"a","initialQuery":"q","group":"g","role":"r","status":{"type":"running","message":"ok"},"dependsOn":["x"]}`))
	f.Add([]byte(`{"status":{}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var s CrewNotifSubagent
		if json.Unmarshal(data, &s) != nil {
			return
		}
		if _, err := json.Marshal(s); err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
	})
}

func FuzzCrewNotifPendingStageDecode(f *testing.F) {
	f.Add([]byte(`{"name":"stage1","agentName":"a","role":"review","dependsOn":["s1"]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"name":"","dependsOn":[]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var ps CrewNotifPendingStage
		if json.Unmarshal(data, &ps) != nil {
			return
		}
		if _, err := json.Marshal(ps); err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
	})
}
