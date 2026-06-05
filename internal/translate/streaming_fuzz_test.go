package translate

import (
	"encoding/json"
	"testing"
)

func FuzzACPPlanWireRoundTrip(f *testing.F) {
	f.Add([]byte(`{"entries":[{"title":"Step 1","status":"pending"}]}`))
	f.Add([]byte(`{"entries":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"entries":[{"title":"","status":"completed"},{"title":"y","status":""}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var p ACPPlanWire
		if json.Unmarshal(data, &p) != nil {
			return
		}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
		var p2 ACPPlanWire
		if json.Unmarshal(b, &p2) != nil {
			t.Fatal("round-trip decode failed")
		}
		if len(p.Entries) != len(p2.Entries) {
			t.Fatalf("entries length: %d vs %d", len(p.Entries), len(p2.Entries))
		}
	})
}

func FuzzACPSteeringWireDecode(f *testing.F) {
	f.Add([]byte(`{"documents":[{"name":"guide.md","path":"/docs/guide.md"}]}`))
	f.Add([]byte(`{"documents":[]}`))
	f.Add([]byte(`{"documents":[{"name":"","path":""}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var s ACPSteeringWire
		if json.Unmarshal(data, &s) != nil {
			return
		}
		for _, d := range s.Documents {
			name := d.Name
			if name == "" {
				name = d.Path
			}
			_ = name
		}
	})
}

func FuzzACPModeUpdateWireRoundTrip(f *testing.F) {
	f.Add([]byte(`{"modeId":"code"}`))
	f.Add([]byte(`{"modeId":""}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"modeId":"architect"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var m ACPModeUpdateWire
		if json.Unmarshal(data, &m) != nil {
			return
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("re-marshal: %v", err)
		}
		var m2 ACPModeUpdateWire
		if json.Unmarshal(b, &m2) != nil {
			t.Fatal("round-trip decode failed")
		}
		if m.ModeID != m2.ModeID {
			t.Fatalf("modeId mismatch: %q vs %q", m.ModeID, m2.ModeID)
		}
	})
}
