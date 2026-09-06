package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzDecodeChatHeaderMessageCount pins the streaming header decoder against
// encoding/json over the same bytes: the streaming message count must equal
// stdlib's element count, and what stdlib refuses must be refused here too.
func FuzzDecodeChatHeaderMessageCount(f *testing.F) {
	f.Add([]byte(`[1,2,3]`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`null`))
	f.Add([]byte(`"not an array"`))
	f.Add([]byte(`[{"id":"m1"},{"id":"m2"}]`))
	f.Add([]byte(`[[1,2],[3,4]]`))
	f.Add([]byte(`[1`))          // truncated
	f.Add([]byte(`{}`))          // object not array
	f.Add([]byte(``))            // empty
	f.Add([]byte(`[null,null]`)) // null elements

	f.Fuzz(func(t *testing.T, messages []byte) {
		body := `{"id":"c1","name":"n","messages":` + string(messages) + `}`

		h, err := decodeChatHeader(strings.NewReader(body))

		var probe struct {
			Messages []json.RawMessage `json:"messages"`
		}
		stdlibOK := json.Unmarshal([]byte(body), &probe) == nil

		if err != nil {
			// Refusing what stdlib reads would drop the chat out of the sidebar
			// while the transcript still opens it.
			if stdlibOK {
				t.Fatalf("decodeChatHeader(%q) = error %v, want it read like encoding/json", messages, err)
			}
			return
		}
		if !stdlibOK {
			t.Fatalf("decodeChatHeader(%q) accepted a body encoding/json rejects", messages)
		}
		if h.MessageCount != len(probe.Messages) {
			t.Fatalf("decodeChatHeader(%q).MessageCount = %d, want %d",
				messages, h.MessageCount, len(probe.Messages))
		}
		if h.ID != "c1" || h.Name != "n" {
			t.Fatalf("decodeChatHeader(%q) header = {ID:%q Name:%q}, want {ID:\"c1\" Name:\"n\"}",
				messages, h.ID, h.Name)
		}
	})
}
