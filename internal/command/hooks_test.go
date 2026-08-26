package command

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// hookCmd builds an *vibekit.ClientCommand carrying the marshaled hook payload p.
func hookCmd(t *testing.T, p hookCreatePayload) *vibekit.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	return &vibekit.ClientCommand{Payload: json.RawMessage(raw)}
}

// TestValidateHookPayload exercises the decode/name/event gate, the
// per-field length caps (a field of exactly MaxHookField bytes is
// accepted; one byte over is 413), and the action-specific empty checks.
func TestValidateHookPayload(t *testing.T) {
	const max = MaxHookField
	atMax := strings.Repeat("a", max)     // exactly MaxHookField bytes
	overMax := strings.Repeat("a", max+1) // one byte over
	const validName = "valid-hook"        // matches validHookNameRe
	// A real trigger, not merely a non-empty string. The old fixture was
	// "file:save", chosen for being short, which is not a value KAS loads -- so
	// every "valid" row here was asserting that a hook destined to be silently
	// discarded validates fine.
	const validEvent = "PostFileSave"

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

		// event_type: the size gate runs before the trigger check, so at
		// exactly MaxHookField the field must survive the size gate and be
		// refused as an unknown trigger instead -> 400, not 413. No real
		// trigger name is that long, so an accept row is impossible; the code
		// it does NOT return is what pins the boundary.
		{name: "event_type at max", p: hookCreatePayload{Name: validName, EventType: atMax, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
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

		// patterns: no other constraint -> at max passes. validEvent is a
		// filePath-subject trigger, so a matcher is effective and the pairing
		// check below leaves these rows alone.
		{name: "patterns at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: atMax}, wantCode: 0, wantErr: false},
		{name: "patterns over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// The trigger-and-matcher pairing. A matcher on a trigger with nothing to
		// match on is refused, because KAS ignores it and says so only in its own
		// log; the SIBLING condition (a tool-name trigger with no matcher) is
		// accepted, because "run on every tool call" is a legitimate choice that
		// earns a badge on the read surface instead. Both directions are here, or
		// the check could be a blanket refusal and still pass.
		{name: "a matcher on a none-subject trigger is refused", p: hookCreatePayload{Name: validName, EventType: "SessionStart", ActionType: "askAgent", Prompt: "p", Patterns: `\.go$`}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "an alias spelling is refused too", p: hookCreatePayload{Name: validName, EventType: "userTriggered", ActionType: "askAgent", Prompt: "p", Patterns: "x"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "a none-subject trigger with no matcher is accepted", p: hookCreatePayload{Name: validName, EventType: "SessionStart", ActionType: "askAgent", Prompt: "p"}, wantCode: 0, wantErr: false},
		{name: "whitespace is not a matcher", p: hookCreatePayload{Name: validName, EventType: "SessionStart", ActionType: "askAgent", Prompt: "p", Patterns: "   "}, wantCode: 0, wantErr: false},
		{name: "a tool trigger with NO matcher is accepted", p: hookCreatePayload{Name: validName, EventType: "PreToolUse", ActionType: "askAgent", Prompt: "p"}, wantCode: 0, wantErr: false},
		{name: "a tool trigger with a matcher is accepted", p: hookCreatePayload{Name: validName, EventType: "PreToolUse", ActionType: "askAgent", Prompt: "p", Patterns: "fsWrite"}, wantCode: 0, wantErr: false},

		// name: the length check precedes the regex. At max the length
		// guard passes but the 1-64 char regex fails -> 400; over max -> 413.
		{name: "name at max", p: hookCreatePayload{Name: atMax, EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "name over max", p: hookCreatePayload{Name: overMax, EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cmd *vibekit.ClientCommand
			if tc.raw != nil {
				cmd = &vibekit.ClientCommand{Payload: json.RawMessage(tc.raw)}
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
	f.Add([]byte(`{"name":"my-hook","event_type":"PostFileSave","action_type":"askAgent","prompt":"do stuff"}`))
	// An unknown trigger must be refused, not panic.
	f.Add([]byte(`{"name":"my-hook","event_type":"file:save","action_type":"askAgent","prompt":"do stuff"}`))
	// Valid runCommand payload.
	f.Add([]byte(`{"name":"build","event_type":"PostFileSave","action_type":"runCommand","command":"make build"}`))
	// Oversized field.
	f.Add(make([]byte, 9000))
	// Empty name.
	f.Add([]byte(`{"name":"","event_type":"x","action_type":"askAgent","prompt":"p"}`))
	// Invalid action_type.
	f.Add([]byte(`{"name":"h","event_type":"x","action_type":"bad","prompt":"p"}`))
	// Name with path separators.
	f.Add([]byte(`{"name":"../evil","event_type":"x","action_type":"askAgent","prompt":"p"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		cmd := &vibekit.ClientCommand{Payload: json.RawMessage(data)}
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

// TestBuildHookDoc pins the v1 envelope shape and the action/matcher/
// timeout mapping for both action branches.
func TestBuildHookDoc(t *testing.T) {
	t.Run("askAgent maps to an agent action", func(t *testing.T) {
		doc := buildHookDoc(&hookCreatePayload{
			Name: "Review", EventType: "fileEdited", ActionType: "askAgent",
			Prompt: "review", Patterns: `\.go$`, Description: "desc",
		})
		if doc.Version != "v1" {
			t.Errorf("version = %q, want v1", doc.Version)
		}
		if len(doc.Hooks) != 1 {
			t.Fatalf("hooks = %d, want 1", len(doc.Hooks))
		}
		h := doc.Hooks[0]
		if h.Name != "Review" || h.Trigger != "PostFileSave" ||
			h.Matcher != `\.go$` || h.Description != "desc" {
			t.Errorf("hook = %+v", h)
		}
		if h.Action.Type != "agent" || h.Action.Prompt != "review" || h.Action.Command != "" {
			t.Errorf("action = %+v", h.Action)
		}
		if h.Timeout != 0 {
			t.Errorf("timeout = %d, want 0 (omitted)", h.Timeout)
		}
	})

	t.Run("runCommand maps to a command action with timeout", func(t *testing.T) {
		doc := buildHookDoc(&hookCreatePayload{
			Name: "Lint", EventType: "PostFileSave", ActionType: "runCommand",
			Command: "make lint", Timeout: 30,
		})
		h := doc.Hooks[0]
		if h.Action.Type != "command" || h.Action.Command != "make lint" || h.Action.Prompt != "" {
			t.Errorf("action = %+v", h.Action)
		}
		if h.Timeout != 30 {
			t.Errorf("timeout = %d, want 30", h.Timeout)
		}
		if h.Matcher != "" {
			t.Errorf("matcher = %q, want empty", h.Matcher)
		}
	})
}

// TestValidateHookPayload_IneffectiveMatcherNamesTheTrigger guards the MESSAGE
// rather than the refusal, which the table above already covers.
//
// A 400 that says only "invalid payload" on a form with seven fields moves the
// guessing to the user, and this refusal is one the user can fix in one keystroke
// once they know which field and which trigger. It also has to name the value, so
// a matcher arriving from a paste is recognisable in the message.
func TestValidateHookPayload_IneffectiveMatcherNamesTheTrigger(t *testing.T) {
	_, _, code, err := validateHookPayload(hookCmd(t, hookCreatePayload{
		Name: "valid-hook", EventType: "sessionStart", ActionType: "askAgent",
		Prompt: "p", Patterns: `  \.go$  `,
	}))
	if code != http.StatusBadRequest || err == nil {
		t.Fatalf("validateHookPayload = (%d, %v), want 400 with an error", code, err)
	}
	// The CANONICAL name, not the payload's spelling: the user has to be able to
	// look the trigger up, and "sessionStart" is one of several aliases.
	for _, want := range []string{"SessionStart", `\.go$`, "patterns"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err.Error(), want)
		}
	}
	// TrimSpace'd, because that is what buildHookDoc writes into the file. Quoting
	// the raw padded value would describe a matcher the hook would never have had.
	if strings.Contains(err.Error(), `  \.go$  `) {
		t.Errorf("refusal %q quotes the untrimmed value; buildHookDoc stores the trimmed one", err.Error())
	}
}
