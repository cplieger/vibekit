package command

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"vibekit/internal/api"
)

func TestValidatePromptPayload(t *testing.T) {
	valid := func(text, msgID, agent, model string) []byte {
		b, _ := json.Marshal(api.PromptCommand{Text: text, MessageID: msgID, Agent: agent, Model: model})
		return b
	}

	tests := []struct {
		name       string
		payload    []byte
		wantStatus int
		wantErr    bool
	}{
		{"valid minimal", valid("hello", "msg-1", "", ""), 0, false},
		{"valid with agent/model", valid("hi", "msg-2", "default", "claude"), 0, false},
		{"empty text", valid("", "msg-1", "", ""), http.StatusBadRequest, true},
		{"oversized text", valid(strings.Repeat("x", maxPromptBytes+1), "msg-1", "", ""), http.StatusRequestEntityTooLarge, true},
		{"missing message_id", valid("hi", "", "", ""), http.StatusBadRequest, true},
		{"invalid message_id", valid("hi", "msg id/bad", "", ""), http.StatusBadRequest, true},
		{"invalid agent", valid("hi", "msg-1", "bad agent!", ""), http.StatusBadRequest, true},
		{"invalid model", valid("hi", "msg-1", "", "bad model!"), http.StatusBadRequest, true},
		{"malformed json", []byte(`{not json`), http.StatusBadRequest, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &api.ClientCommand{Payload: tc.payload}
			_, status, err := validatePromptPayload(cmd)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}
