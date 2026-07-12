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
