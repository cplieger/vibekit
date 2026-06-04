package checkpoint

import (
	"encoding/json"
	"testing"
)

func FuzzStateApply(f *testing.F) {
	f.Add([]byte(`{"type":"snapshot","tag":"1","path":"a.go","before_sha":"abc","after_sha":"def","ts":1}`))
	f.Add([]byte(`{"type":"turn_start","turn":1,"ts":2}`))
	f.Add([]byte(`{"type":"restore","tag":"1","ts":3}`))
	f.Add([]byte(`{"type":"conflict_detected","path":"x","other_chat":"c2","ts":4}`))
	f.Add([]byte(`{"type":"unknown","ts":5}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var ev event
		if err := json.Unmarshal(data, &ev); err != nil {
			return
		}
		s := newState()
		s.apply(&ev)
	})
}
