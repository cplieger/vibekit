package vibekit

import (
	"encoding/json"
	"testing"
)

func FuzzRPCResponseUnmarshal(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"fail"}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","method":"update","params":{"kind":"agent_message_chunk"}}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"id":9999999999999999999}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var resp RPCResponse
		if json.Unmarshal(data, &resp) != nil {
			return
		}
		if resp.Error != nil {
			_ = resp.Error.Error()
		}
		enc, err := json.Marshal(&resp)
		if err != nil {
			t.Fatalf("Marshal after Unmarshal: %v", err)
		}
		var resp2 RPCResponse
		if json.Unmarshal(enc, &resp2) != nil {
			t.Fatal("round-trip Unmarshal failed")
		}
		if resp2.JSONRPC != resp.JSONRPC {
			t.Errorf("JSONRPC mismatch")
		}
		if resp2.Method != resp.Method {
			t.Errorf("Method mismatch")
		}
	})
}
