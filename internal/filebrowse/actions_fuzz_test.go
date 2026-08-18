package filebrowse

import (
	"encoding/json"
	"testing"
)

func FuzzFileActionParsing(f *testing.F) {
	f.Add([]byte(`{"action":"mkdir","path":"/workspace/test"}`))
	f.Add([]byte(`{"action":"delete","path":"/../etc"}`))
	f.Add([]byte(`{"action":"rename","path":"/workspace/a","name":"b"}`))
	f.Add([]byte(`{"action":"unknown","path":"x"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var body fileAction
		if err := json.Unmarshal(data, &body); err != nil {
			return
		}
		_ = fileActions[body.Action]
	})
}
