package translate

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// FuzzUnmarshalParams pins the cross-function consistency of the generic
// decode helper: for any input bytes, unmarshalParams reports success
// exactly when a direct json.Unmarshal into the same type succeeds, and
// returns the same decoded value on success. This is the security-
// relevant ACP decode boundary, so the ok flag must never disagree with
// the underlying decoder.
func FuzzUnmarshalParams(f *testing.F) {
	f.Add([]byte(`{"commands":[{"name":"x"}]}`))
	f.Add([]byte(`{"subagents":[{"group":"g","status":{"type":"running"}}]}`))
	f.Add([]byte(`{"contextUsagePercentage":42.5,"meteringUsage":[{"unitPlural":"credits","value":3}]}`))
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
		// Exercise several concrete decode targets the handlers use.
		assertUnmarshalParamsConsistent[CrewNotifPayload](t, msg, data)
		assertUnmarshalParamsConsistent[metadataParams](t, msg, data)
		assertUnmarshalParamsConsistent[ACPToolCallWire](t, msg, data)
	})
}

// assertUnmarshalParamsConsistent checks unmarshalParams[T] against a
// direct json.Unmarshal[T] oracle: the ok flag must match the decoder's
// error, and on success the decoded values must be deeply equal.
func assertUnmarshalParamsConsistent[T any](t *testing.T, msg *api.RPCResponse, data []byte) {
	t.Helper()
	got, ok := unmarshalParams[T](msg, "test")
	var want T
	wantErr := json.Unmarshal(data, &want)
	if (wantErr == nil) != ok {
		t.Fatalf("%T: unmarshalParams ok=%v but json.Unmarshal err=%v (must agree)", want, ok, wantErr)
	}
	if ok && !reflect.DeepEqual(got, want) {
		t.Fatalf("%T: unmarshalParams=%+v, direct json.Unmarshal=%+v (must match on success)", want, got, want)
	}
}
