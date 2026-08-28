package command

// KAS's `autopilot` config option is a SELECT over the strings "on" and "off".
// This command sent a JSON boolean, which satisfies neither arm of the request
// union without a `type:"boolean"` discriminator, so every live toggle was
// refused with -32602 and the session stayed in autopilot — in BOTH directions.
// The only signal was one log line, and nothing asserted the value's shape, so
// what is pinned here is the byte that goes on the wire.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cplieger/vibekit/internal/testsupport"
	"github.com/cplieger/vibekit/internal/vibekit"
)

func supervisedReq(t *testing.T, chatID vibekit.ChatID, enabled bool) *vibekit.ClientCommand {
	t.Helper()
	payload, err := json.Marshal(vibekit.SetSupervisedModeCommand{Enabled: enabled})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &vibekit.ClientCommand{
		Type:    vibekit.CmdSetSupervisedMode,
		ChatID:  chatID,
		Payload: payload,
	}
}

func TestCmdSetSupervisedMode_SendsAutopilotAsAString(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		wantValue string
	}{
		// Supervised on means autopilot off: the option names the behaviour
		// being turned off, not the switch the user flipped.
		{name: "enabling supervised turns autopilot off", enabled: true, wantValue: vibekit.ConfigValueAutopilotOff},
		{name: "disabling supervised turns autopilot on", enabled: false, wantValue: vibekit.ConfigValueAutopilotOn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := testsupport.NewInMemoryChatStore()
			seedEmptyChat(t, store, "c1")
			b := &recordingBridge{result: map[string]any{}, sessionID: "sess-1"}
			host := newBridgeHost(store, b)

			_, err := CmdSetSupervisedMode(t.Context(), host, host, supervisedReq(t, "c1", tc.enabled))

			if statusOf(err) != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", statusOf(err), errText(err))
			}
			if b.callCount != 1 {
				t.Fatalf("bridge calls = %d, want 1", b.callCount)
			}
			if b.gotMethod != vibekit.MethodSetConfigOption {
				t.Errorf("method = %q, want %q", b.gotMethod, vibekit.MethodSetConfigOption)
			}
			if b.gotParams["configId"] != vibekit.ConfigOptionAutopilot {
				t.Errorf("configId = %v, want %q", b.gotParams["configId"], vibekit.ConfigOptionAutopilot)
			}
			got, ok := b.gotParams["value"].(string)
			if !ok {
				t.Fatalf("value = %#v (%T), want the string %q; a bare boolean is refused with -32602 and autopilot stays on",
					b.gotParams["value"], b.gotParams["value"], tc.wantValue)
			}
			if got != tc.wantValue {
				t.Errorf("value = %q, want %q", got, tc.wantValue)
			}
		})
	}
}

// The record still carries the user's choice, whatever the wire value maps to.
func TestCmdSetSupervisedMode_PersistsTheChoiceOnTheChat(t *testing.T) {
	store := testsupport.NewInMemoryChatStore()
	seedEmptyChat(t, store, "c1")
	host := newBridgeHost(store, &recordingBridge{result: map[string]any{}, sessionID: "s"})

	if _, err := CmdSetSupervisedMode(t.Context(), host, host, supervisedReq(t, "c1", true)); err != nil {
		t.Fatalf("CmdSetSupervisedMode: %v", err)
	}

	c, ok := store.Get(t.Context(), "c1")
	if !ok {
		t.Fatal("chat vanished")
	}
	if !c.SupervisedMode {
		t.Error("SupervisedMode = false, want true; the choice has to survive to reach StartOpts.Supervised")
	}
}
