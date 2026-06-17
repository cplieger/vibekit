// Tests added by the gremlins mutant-killing protocol (unit vibekit-u18).
// Tests only; no production code is modified. Every new identifier is
// prefixed gk_vibekit_u18_ to avoid colliding with sibling units.
package command

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/api"
)

// gk_vibekit_u18_serve pushes a raw POST /api/command body through a
// Dispatcher backed by the package bench stub and returns the recorder.
func gk_vibekit_u18_serve(body string) *httptest.ResponseRecorder {
	d := New(newBenchDeps())
	req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	return w
}

// Kills command.go:151 CONDITIONALS_NEGATION on the JSON-decode guard
// `err != nil`. Original: a decode failure enters the error branch and
// returns 400 "invalid json". Mutant (`err == nil`): the failure skips the
// branch, a zero-value command falls through, and dispatch ends in the
// "unknown command" path. The status is 400 either way, so the body text
// is the discriminator.
func Test_gk_vibekit_u18_ServeHTTP_decodeErrorReturnsInvalidJSON(t *testing.T) {
	w := gk_vibekit_u18_serve("{not valid json")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ServeHTTP(malformed body) status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if got := w.Body.String(); !strings.Contains(got, "invalid json") {
		t.Errorf("ServeHTTP(malformed body) = %q, want body containing %q", got, "invalid json")
	}
}

// Kills command.go:176 CONDITIONALS_NEGATION on `cmd.ChatID != ""`.
// Original: a non-empty AND invalid chat id is rejected with
// "invalid chat_id". Mutant (`cmd.ChatID == ""`): for a non-empty id the
// && short-circuits to false, so the invalid id is NOT rejected and
// dispatch falls to the unknown-command body instead.
func Test_gk_vibekit_u18_ServeHTTP_invalidNonEmptyChatIDRejected(t *testing.T) {
	w := gk_vibekit_u18_serve(`{"type":"test","chat_id":"has spaces"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ServeHTTP(invalid chat_id) status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if got := w.Body.String(); !strings.Contains(got, api.ErrMsgInvalidChatID) {
		t.Errorf("ServeHTTP(invalid chat_id) = %q, want body containing %q", got, api.ErrMsgInvalidChatID)
	}
}

// gk_vibekit_u18_hookCmd builds an *api.ClientCommand carrying the marshaled
// hook payload p.
func gk_vibekit_u18_hookCmd(t *testing.T, p hookCreatePayload) *api.ClientCommand {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	return &api.ClientCommand{Payload: json.RawMessage(raw)}
}

// Test_gk_vibekit_u18_validateHookPayload kills the hooks.go mutants:
//   - 41:51/41:68/41:89 CONDITIONALS_NEGATION (the uErr/name/event_type gate):
//     a fully valid payload must pass (code 0); each negation flips one OR
//     term to true and forces a 400.
//   - 44:17/44:54/45:20/45:56/46:17/46:50/47:19 CONDITIONALS_BOUNDARY and
//     CONDITIONALS_NEGATION (the per-field `len(x) > MaxHookField` guards):
//     a field of EXACTLY MaxHookField bytes leaves the term false (original),
//     while both `>=` and `<=` flip it true -> 413, differing from the
//     original outcome (0, or 400 for name/action_type which fail later).
//   - 53:34 / 58:35 CONDITIONALS_NEGATION (the askAgent/runCommand empty
//     checks): a non-empty prompt/command passes; the negation forces 400.
func Test_gk_vibekit_u18_validateHookPayload(t *testing.T) {
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
		// line 41 gate: happy paths (code 0) kill the three negations.
		{name: "valid askAgent", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "do stuff"}, wantCode: 0, wantErr: false},
		{name: "valid runCommand", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: "make build"}, wantCode: 0, wantErr: false},
		// line 41 gate: each true branch.
		{name: "invalid json", raw: []byte(`{not json`), wantCode: http.StatusBadRequest, wantErr: true},
		{name: "empty name", p: hookCreatePayload{Name: "", EventType: validEvent, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "empty event_type", p: hookCreatePayload{Name: validName, EventType: "", ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},

		// lines 53/58: action-specific empty checks (true branch).
		{name: "askAgent blank prompt", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "   "}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "runCommand blank command", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: ""}, wantCode: http.StatusBadRequest, wantErr: true},

		// line 44 (description): no other constraint -> at max passes, over max 413.
		{name: "description at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Description: atMax}, wantCode: 0, wantErr: false},
		{name: "description over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Description: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// line 45 (event_type): non-empty, no regex -> at max passes.
		{name: "event_type at max", p: hookCreatePayload{Name: validName, EventType: atMax, ActionType: "askAgent", Prompt: "p"}, wantCode: 0, wantErr: false},
		{name: "event_type over max", p: hookCreatePayload{Name: validName, EventType: overMax, ActionType: "askAgent", Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// line 45 (action_type): at max is not a valid action -> switch default 400.
		{name: "action_type at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: atMax, Prompt: "p"}, wantCode: http.StatusBadRequest, wantErr: true},
		{name: "action_type over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: overMax, Prompt: "p"}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// line 46 (prompt): non-empty for askAgent -> at max passes.
		{name: "prompt at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: atMax}, wantCode: 0, wantErr: false},
		{name: "prompt over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// line 46 (command): non-empty for runCommand -> at max passes.
		{name: "command at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: atMax}, wantCode: 0, wantErr: false},
		{name: "command over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "runCommand", Command: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// line 47 (patterns): no other constraint -> at max passes.
		{name: "patterns at max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: atMax}, wantCode: 0, wantErr: false},
		{name: "patterns over max", p: hookCreatePayload{Name: validName, EventType: validEvent, ActionType: "askAgent", Prompt: "p", Patterns: overMax}, wantCode: http.StatusRequestEntityTooLarge, wantErr: true},

		// line 44 (name): length check precedes the regex. At max the length
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
				cmd = gk_vibekit_u18_hookCmd(t, tc.p)
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

// Kills prompt.go:29 CONDITIONALS_BOUNDARY on `len(p.Text) > maxPromptBytes`.
// A text of EXACTLY maxPromptBytes is accepted by the original (`>` is
// false at the boundary); the mutant (`>=`) rejects it with 413.
func Test_gk_vibekit_u18_validatePromptPayload_textLengthBoundary(t *testing.T) {
	cases := []struct {
		name     string
		textLen  int
		wantCode int
	}{
		{name: "exactly at cap accepted", textLen: maxPromptBytes, wantCode: 0},
		{name: "one over cap rejected", textLen: maxPromptBytes + 1, wantCode: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := api.PromptCommand{Text: strings.Repeat("a", tc.textLen), MessageID: "msg-1"}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal prompt: %v", err)
			}
			_, code, _ := validatePromptPayload(&api.ClientCommand{Payload: json.RawMessage(raw)})
			if code != tc.wantCode {
				t.Errorf("validatePromptPayload(textLen=%d) code = %d, want %d", tc.textLen, code, tc.wantCode)
			}
		})
	}
}

// Kills shell.go:33 CONDITIONALS_BOUNDARY on `remaining <= 0`. When the
// buffer is exactly full (remaining == 0) and an EMPTY slice is written,
// the original (`<= 0`) marks Truncated and returns; the mutant (`< 0`)
// falls through to the `len(p) <= remaining` (0 <= 0) fast path which
// writes without ever setting Truncated.
func Test_gk_vibekit_u18_ShellCappedBuffer_emptyWriteAtCapMarksTruncated(t *testing.T) {
	var buf ShellCappedBuffer
	if _, err := buf.Write(bytes.Repeat([]byte("x"), ShellOutputCap)); err != nil {
		t.Fatalf("fill write: %v", err)
	}
	if buf.Truncated {
		t.Fatalf("Truncated = true after exactly-cap fill, want false")
	}
	if _, err := buf.Write([]byte{}); err != nil {
		t.Fatalf("empty write at full buffer: %v", err)
	}
	if !buf.Truncated {
		t.Errorf("Truncated = false after empty write at full buffer, want true")
	}
}
