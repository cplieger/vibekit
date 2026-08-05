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

		// event_type: non-empty, no regex -> at max passes.
		// event_type has no at-max accept row: a field-length probe needs a value
		// of exactly MaxHookField, and no real trigger name is that long, so the
		// row could only ever assert that an over-long UNKNOWN trigger is
		// rejected for the wrong reason. The over-max row below still pins the
		// size gate, and it must be rejected for SIZE rather than for being an
		// unknown trigger, which is why it stays first in validateHookPayload.
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

// TestNormalizeTrigger pins the event-type -> PascalCase v1 trigger map:
// canonical names pass through, v2/IDE camelCase aliases are rewritten
// (case-insensitively), and unknown values pass through trimmed.
func TestNormalizeTrigger(t *testing.T) {
	cases := []struct{ in, want string }{
		// Canonical PascalCase passes through.
		{"SessionStart", "SessionStart"},
		{"PostFileSave", "PostFileSave"},
		{"Manual", "Manual"},
		// v2 / Kiro-IDE camelCase aliases map to PascalCase.
		{"fileEdited", "PostFileSave"},
		{"fileCreated", "PostFileCreate"},
		{"fileDeleted", "PostFileDelete"},
		{"userTriggered", "Manual"},
		{"agentStop", "Stop"},
		{"userPromptSubmit", "UserPromptSubmit"},
		// Case-insensitive + trimmed.
		{"POSTFILESAVE", "PostFileSave"},
		{"  fileEdited  ", "PostFileSave"},
		// The three aliases KAS accepts that this map used to be missing.
		{"agentSpawn", "SessionStart"},
		{"SessionEnd", "Stop"},
		{"AfterFileEdit", "PostFileSave"},
	}
	for _, tc := range cases {
		got, ok := normalizeTrigger(tc.in)
		if !ok {
			t.Errorf("normalizeTrigger(%q) reported unknown, want %q", tc.in, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeTrigger(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestNormalizeTrigger_RejectsUnknown is the inverted case, and the inversion is
// the point. This used to pass an unknown trigger through trimmed, which read as
// leniency and behaved as silence: KAS's parseHookDocument DROPS a hook whose
// trigger it does not recognise, so create_hook answered 200 with a file path for
// a hook that loads nowhere, never fires and never appears in /api/hooks.
func TestNormalizeTrigger_RejectsUnknown(t *testing.T) {
	for _, in := range []string{"someFutureTrigger", "  x  ", "", "PostFileSaved!"} {
		if got, ok := normalizeTrigger(in); ok {
			t.Errorf("normalizeTrigger(%q) = (%q, true), want it reported unknown so the "+
				"caller can refuse instead of writing a hook KAS will discard", in, got)
		}
	}
}

// TestKnownHookTriggers_NamesTheAcceptedSet guards the error message rather than
// the refusal, because a rejection that does not say what IS accepted just moves
// the guessing from the server to the user.
func TestKnownHookTriggers_NamesTheAcceptedSet(t *testing.T) {
	got := knownHookTriggers()
	for _, want := range []string{"SessionStart", "Stop", "PreToolUse", "PostToolUse", "Manual"} {
		if !strings.Contains(got, want) {
			t.Errorf("knownHookTriggers() = %q, missing %q", got, want)
		}
	}
	// Deduped: every trigger has several aliases mapping onto it.
	if strings.Count(got, "PostFileSave") != 1 {
		t.Errorf("knownHookTriggers() = %q, want PostFileSave listed once", got)
	}
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
