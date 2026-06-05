package translate

import (
	"encoding/json"
	"testing"
)

func FuzzContextRecoveryMetaRoundTrip(f *testing.F) {
	f.Add(0)

	f.Fuzz(func(t *testing.T, _ int) {
		m := newContextRecoveryMeta()
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded contextRecoveryMeta
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !decoded.Kiro.ContextRecovery {
			t.Fatal("contextRecovery lost in round-trip")
		}
	})
}

func FuzzACPSessionUpdateEnvelopeLayered(f *testing.F) {
	f.Add([]byte(`{"sessionId":"sess-1","update":{"sessionUpdate":"session_started"}}`))
	f.Add([]byte(`{"sessionId":"","update":null}`))
	f.Add([]byte(`{"sessionId":"s","update":{}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"sessionId":"x","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var env ACPSessionUpdateEnvelope
		if json.Unmarshal(data, &env) != nil {
			return
		}
		if env.Update != nil {
			var base ACPSessionUpdateBase
			_ = json.Unmarshal(env.Update, &base)
		}
	})
}
