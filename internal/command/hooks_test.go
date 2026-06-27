package command

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// hookCmd builds an *api.ClientCommand carrying the marshaled hook payload p.
func hookCmd(t *testing.T, p hookCreatePayload) *api.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	return &api.ClientCommand{Payload: json.RawMessage(raw)}
}

// TestValidateHookPayload exercises the decode/name/event gate, the
// per-field length caps (a field of exactly MaxHookField bytes is
// accepted; one byte over is 413), and the action-specific empty checks.
func TestValidateHookPayload(t *testing.T) {
	const max = MaxHookField
	atMax := strings.Repeat("a", max)     // exactly MaxHookField bytes
	overMax := strings.Repeat("a", max+1) // one byte over
	const validName = "valid-hook"        // matches validHookNameRe
	const validEvent = "file:save"        // non-empty, short

	cases := []struct {
		name     string
		p        hookCreatePayload
		raw      []byte // when set, used verbatim instead of marshaling p
		wantCode int
		wantErr  bool
	}{
		// Happy paths.
		{name: "valid askAgent", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "do stuff"}, wantCode: 0, wantErr: false},
		{name: "valid runCommand", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: "make build"}, wantCode: 0, wantErr: false},
		// Decode / required-field gate.
		{name: "invalid json", raw: []byte(`{not json`), wantCode: http.StatusBadRequest, wantErr: true},
		{name: "empty name", p: hookCreatePayload{Name: "", EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "empty event_type", p: hookCreatePayload{Name: validName, EventType: "", ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},

		// Action-specific empty checks.
		{name: "askAgent blank prompt", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "   "}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "runCommand blank command", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: ""}, wantCode: http.StatusBadRequest, wantErr: true},

		// description: no other constraint -> at max passes, over max 413.
		{name: "description at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Description: atMax}, wantCode: 0, wantErr: false},
		{name: "description over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Description: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// event_type: non-empty, no regex -> at max passes.
		{name: "event_type at max", p: hookCreatePayload{Name: validName, EventType: atMax, ActionType: "askAgent", Prompt: "p"}, wantCode: 0, wantErr: false},
		{name: "event_type over max", p: hookCreatePayload{Name: validName, EventType: overMax, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// action_type: at max is not a valid action -> switch default 400; over max -> 413.
		{name: "action_type at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: atMax, Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "action_type over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: overMax, Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// prompt: non-empty for askAgent -> at max passes.
		{name: "prompt at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: atMax}, wantCode: 0, wantErr: false},
		{name: "prompt over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// command: non-empty for runCommand -> at max passes.
		{name: "command at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: atMax}, wantCode: 0, wantErr: false},
		{name: "command over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// patterns: no other constraint -> at max passes.
		{name: "patterns at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: atMax}, wantCode: 0, wantErr: false},
		{name: "patterns over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// name: the length check precedes the regex. At max the length
		// guard passes but the 1-64 char regex fails -> 400; over max -> 413.
		{name: "name at max", p: hookCreatePayload{Name: atMax, EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "name over max", p: hookCreatePayload{Name: overMax, EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmd *api.ClientCommand
			if tc.raw != nil {
				cmd = &api.ClientCommand{Payload: json.RawMessage(tc.raw)}
			} else {
				cmd = hookCmd(t, tc.p)
			}
			_, _, code, err := validateHookPayload(cmd)
			if code != tc.wantCode {
				t.Errorf("validateHookPayload(%s) code = %d, want %d", tc.name, code, tc.wantCode)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("validateHookPayload(%s) err = %v, wantErr = %v", tc.name, err, tc.wantErr)
			}
		})
	}
}

func FuzzValidateHookPayload(f *testing.F) {
	// Seed corpus: valid askAgent payload.
	f.Add([]byte(`{"name":"my-hook","event_type":"file:save","action_type":"askAgent","prompt":"do stuff"}`))
	// Valid runCommand payload.
	f.Add([]byte(`{"name":"build","event_type":"file:save","action_type":"runCommand","command":"make build"}`))
	// Oversized field.
	f.Add(make([]byte, 9000))
	// Empty name.
	f.Add([]byte(`{"name":"","event_type":"x","action_type":"askAgent","prompt":"p"}`))
	// Invalid action_type.
	f.Add([]byte(`{"name":"h","event_type":"x","action_type":"bad","prompt":"p"}`))
	// Name with path separators.
	f.Add([]byte(`{"name":"../evil","event_type":"x","action_type":"askAgent","prompt":"p"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cmd := &api.ClientCommand{Payload: json.RawMessage(data)}
		_, _, code, err := validateHookPayload(cmd)

		if err == nil && code != 0 {
			t.Errorf("err==nil but code=%d", code)
		}
		if err != nil && code == 0 {
			t.Errorf("err=%v but code=0", err)
		}
		if code == 0 && err == nil {
			var p hookCreatePayload
			if json.Unmarshal(data, &p) != nil {
				t.Error("validation passed but JSON unmarshal fails")
			}
		}
	})
}
