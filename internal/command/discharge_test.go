package command

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// commandTypeFloor stops a broken scan passing vacuously: the vocabulary is 24
// constants today, so a floor of 20 leaves room for four removals while sitting well
// above any count a failed parse would produce.
const commandTypeFloor = 20

// declaredCommandTypes reads the Cmd* constant names out of the vocabulary's own
// declaration, so the completeness check cannot drift from a second hand-written list.
func declaredCommandTypes(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../vibekit/commands.go", nil, 0)
	if err != nil {
		t.Fatalf("parse the command vocabulary: %v", err)
	}
	var names []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "CommandType" {
			return true
		}
		for _, name := range spec.Names {
			if strings.HasPrefix(name.Name, "Cmd") {
				names = append(names, name.Name)
			}
		}
		return true
	})
	return names
}

// TestCommandDischarges_ClassifiesEveryCommand: the table IS the discharge decision,
// and dischargeNo is its zero value, so a command nobody classified is silently
// treated as answering nothing. Only this test catches that, which is why it reads
// the constant block rather than a second list of names.
func TestCommandDischarges_ClassifiesEveryCommand(t *testing.T) {
	names := declaredCommandTypes(t)
	if len(names) < commandTypeFloor {
		t.Fatalf("the scan found %d Cmd* constants, want at least %d: the scan is broken, not the table",
			len(names), commandTypeFloor)
	}
	if len(commandDischarges) != len(names) {
		t.Errorf("the table has %d rows for %d commands", len(commandDischarges), len(names))
	}
	// Names, because the table is keyed by VALUE and a value cannot say which constant
	// it came from.
	byValue := map[vibekit.CommandType]string{
		vibekit.CmdCreateChat:          "CmdCreateChat",
		vibekit.CmdResumeSession:       "CmdResumeSession",
		vibekit.CmdForkChat:            "CmdForkChat",
		vibekit.CmdPrompt:              "CmdPrompt",
		vibekit.CmdCancel:              "CmdCancel",
		vibekit.CmdDeleteChat:          "CmdDeleteChat",
		vibekit.CmdSwitchModel:         "CmdSwitchModel",
		vibekit.CmdPermissionResponse:  "CmdPermissionResponse",
		vibekit.CmdElicitationResponse: "CmdElicitationResponse",
		vibekit.CmdUserInputResponse:   "CmdUserInputResponse",
		vibekit.CmdRewindChat:          "CmdRewindChat",
		vibekit.CmdCompact:             "CmdCompact",
		vibekit.CmdSetEffort:           "CmdSetEffort",
		vibekit.CmdSetDraft:            "CmdSetDraft",
		vibekit.CmdSetAttachments:      "CmdSetAttachments",
		vibekit.CmdSetMode:             "CmdSetMode",
		vibekit.CmdCreateHook:          "CmdCreateHook",
		vibekit.CmdSetSupervisedMode:   "CmdSetSupervisedMode",
		vibekit.CmdSteer:               "CmdSteer",
		vibekit.CmdSteerClear:          "CmdSteerClear",
		vibekit.CmdOpenTab:             "CmdOpenTab",
		vibekit.CmdCloseTab:            "CmdCloseTab",
		vibekit.CmdReorderTabs:         "CmdReorderTabs",
		vibekit.CmdPinTab:              "CmdPinTab",
	}
	classified := make(map[string]bool, len(commandDischarges))
	for value := range commandDischarges {
		name, known := byValue[value]
		if !known {
			t.Errorf("the table holds %q, which no Cmd* constant in this map names", value)
			continue
		}
		classified[name] = true
	}
	for _, name := range names {
		if !classified[name] {
			t.Errorf("%s has no discharge verdict: decide whether it is the user answering the agent", name)
		}
	}
}

// fakeChatStatus records the chats a discharge reached.
type fakeChatStatus struct{ discharged []vibekit.ChatID }

func (f *fakeChatStatus) DischargeWaiting(_ context.Context, chatID vibekit.ChatID) {
	f.discharged = append(f.discharged, chatID)
}

// TestDispatch_DischargesOnlyAnAnswer drives the dispatcher per command type, because
// the hook is the dispatcher's and no handler was edited: a rule applied once at
// dispatch cannot be pinned by testing a handler.
func TestDispatch_DischargesOnlyAnAnswer(t *testing.T) {
	cases := []struct {
		name    string
		cmdType vibekit.CommandType
		handler Handler
		want    bool
	}{
		{
			name:    "a steer is the user's own words to this agent",
			cmdType: vibekit.CmdSteer,
			want:    true,
		},
		{
			// The prompt path discharges at StartTurn, where the turn's SOURCE tells a
			// prompt from a `!cmd`; a second discharge here would fire for both.
			name:    "a prompt does not, because its turn source decides",
			cmdType: vibekit.CmdPrompt,
		},
		{
			name:    "an answer to the agent's own menu does not",
			cmdType: vibekit.CmdUserInputResponse,
		},
		{
			name:    "a permission answer does not",
			cmdType: vibekit.CmdPermissionResponse,
		},
		{
			name:    "an MCP elicitation answer does not",
			cmdType: vibekit.CmdElicitationResponse,
		},
		{
			name:    "a steer whose handler failed answered nothing",
			cmdType: vibekit.CmdSteer,
			handler: func(context.Context, *vibekit.ClientCommand) (any, error) {
				return nil, StatusError(http.StatusConflict, ErrMissingChatID)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := &fakeChatStatus{}
			d := New()
			d.status = status
			handler := tc.handler
			if handler == nil {
				handler = func(context.Context, *vibekit.ClientCommand) (any, error) { return nil, nil }
			}
			d.Register(tc.cmdType, handler)

			body := `{"type":"` + string(tc.cmdType) + `","chat_id":"c-abc123"}`
			req := httptest.NewRequest(http.MethodPost, "/api/command", strings.NewReader(body))
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)

			if tc.want {
				if len(status.discharged) != 1 || status.discharged[0] != "c-abc123" {
					t.Errorf("discharged %v, want exactly [c-abc123]", status.discharged)
				}
				return
			}
			if len(status.discharged) != 0 {
				t.Errorf("discharged %v, want nothing", status.discharged)
			}
		})
	}
}

// A dispatcher built without roles is what internal/command's own tests construct, so
// the nil guard is load-bearing rather than defensive.
func TestDispatch_NoStatusRoleIsSilent(t *testing.T) {
	d := New()
	d.Register(vibekit.CmdSteer, func(context.Context, *vibekit.ClientCommand) (any, error) { return nil, nil })
	req := httptest.NewRequest(http.MethodPost, "/api/command",
		strings.NewReader(`{"type":"steer","chat_id":"c-abc123"}`))
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
