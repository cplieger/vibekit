package translate

import (
	"encoding/json"
	"testing"

	"vibekit/internal/api"
)

func FuzzUnmarshalParams(f *testing.F) {
	f.Add([]byte(`{"commands":[{"name":"x"}]}`))
	f.Add([]byte(`{"subagents":[{"group":"g","status":{"type":"running"}}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"string"`))
	f.Add([]byte{0xff, 0xfe}) // invalid UTF-8

	f.Fuzz(func(t *testing.T, data []byte) {
		msg := &api.RPCResponse{
			JSONRPC: "2.0",
			Method:  "test",
			Params:  json.RawMessage(data),
		}
		// Exercise multiple concrete type instantiations.
		unmarshalParams[CrewNotifPayload](msg, "crew")

		type commandsParams struct {
			Commands []map[string]any `json:"commands"`
		}
		unmarshalParams[commandsParams](msg, "commands")

		type metadataParams struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		unmarshalParams[metadataParams](msg, "metadata")
	})
}
