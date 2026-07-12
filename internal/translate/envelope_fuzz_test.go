package translate

import (
	"encoding/json"
	"testing"
)

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
