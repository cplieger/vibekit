package vibekit

import (
	"errors"
	"slices"
	"testing"
)

// Tests for domain.go types. The single interesting behaviour here is
// Chat.Header; the rest of the types are plain structs with JSON tags.

func TestChatHeader_copies_metadata_without_messages(t *testing.T) {
	modes := []SessionMode{
		{ID: "plan", Name: "Plan", Description: "planning mode"},
		{ID: "build", Name: "Build"},
	}
	models := []SessionModel{
		{ID: "claude-sonnet-4", Name: "Claude Sonnet 4"},
		{ID: "claude-opus-4", Name: "Claude Opus 4", Description: "most capable"},
	}
	c := &Chat{
		ID:              "c1",
		Name:            "Hello",
		Model:           "claude",
		ACPSessionID:    "acp-1",
		CurrentModeID:   "plan",
		AvailableModes:  modes,
		AvailableModels: models,
		Usage:           Usage{ContextPct: 50, ContextSize: 200000, HasRealData: true},
		CreatedAt:       100,
		UpdatedAt:       200,
		Messages:        []Message{{ID: "m1"}, {ID: "m2"}},
	}

	h := c.Header()

	if h.ID != "c1" || h.Name != "Hello" || h.Model != "claude" {
		t.Errorf("header identity fields = %+v, want id=c1 name=Hello model=claude", h)
	}
	if h.ACPSessionID != "acp-1" {
		t.Errorf("ACPSessionID = %q, want acp-1", h.ACPSessionID)
	}
	if h.CurrentModeID != "plan" {
		t.Errorf("CurrentModeID = %q, want plan", h.CurrentModeID)
	}
	if len(h.AvailableModes) != 2 || h.AvailableModes[0].ID != "plan" || h.AvailableModes[1].ID != "build" {
		t.Errorf("AvailableModes = %+v, want 2 entries [plan, build]", h.AvailableModes)
	}
	if len(h.AvailableModels) != 2 || h.AvailableModels[0].ID != "claude-sonnet-4" || h.AvailableModels[1].Description != "most capable" {
		t.Errorf("AvailableModels = %+v, want 2 entries carrying ID+Description", h.AvailableModels)
	}
	if h.Usage.ContextPct != 50 || h.Usage.ContextSize != 200000 || !h.Usage.HasRealData {
		t.Errorf("Usage = %+v, want {ContextPct:50 ContextSize:200000 HasRealData:true}", h.Usage)
	}
	if h.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2", h.MessageCount)
	}
	if h.CreatedAt != 100 || h.UpdatedAt != 200 {
		t.Errorf("timestamps: created=%d updated=%d, want 100/200", h.CreatedAt, h.UpdatedAt)
	}
}

func TestChatHeader_zero_value_chat_produces_empty_header(t *testing.T) {
	c := &Chat{ID: "c-empty"}

	h := c.Header()

	if h.ID != "c-empty" {
		t.Errorf("ID = %q, want c-empty", h.ID)
	}
	if h.Name != "" || h.Model != "" || h.ACPSessionID != "" || h.CurrentModeID != "" {
		t.Errorf("leaked non-zero strings: %+v", h)
	}
	if h.MessageCount != 0 {
		t.Errorf("MessageCount = %d, want 0 for zero-value Chat", h.MessageCount)
	}
	if h.AvailableModes != nil || h.AvailableModels != nil {
		t.Errorf("leaked non-nil slices: modes=%v models=%v", h.AvailableModes, h.AvailableModels)
	}
	if h.Usage.ContextPct != 0 || h.Usage.Credits != 0 || h.Usage.TurnCount != 0 ||
		h.Usage.LastTurnMs != 0 || h.Usage.HasRealData || len(h.Usage.MeteringItems) != 0 {
		t.Errorf("Usage = %+v, want zero-value Usage", h.Usage)
	}
	if h.CreatedAt != 0 || h.UpdatedAt != 0 {
		t.Errorf("zero-value timestamps: created=%d updated=%d, want 0/0", h.CreatedAt, h.UpdatedAt)
	}
}

func TestChatHeader_copies_boolean_and_rewind_fields(t *testing.T) {
	c := &Chat{
		ID:             "rewind-1",
		Name:           "Rewind branch",
		SupervisedMode: true,
	}

	h := c.Header()

	if !h.SupervisedMode {
		t.Error("SupervisedMode not copied to header")
	}
}

func TestRPCError_Error_returns_message_verbatim(t *testing.T) {
	tests := []struct {
		name string
		err  *RPCError
		want string
	}{
		{"typical", &RPCError{Code: -32603, Message: "internal error"}, "internal error"},
		{"empty message", &RPCError{Code: -32000, Message: ""}, ""},
		{"zero code", &RPCError{Code: 0, Message: "ok but zero"}, "ok but zero"},
		{"whitespace message", &RPCError{Code: 1, Message: "  spaced  "}, "  spaced  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("RPCError{Code:%d, Message:%q}.Error() = %q, want %q",
					tt.err.Code, tt.err.Message, got, tt.want)
			}
		})
	}
}

func TestRPCError_implements_error_interface(t *testing.T) {
	// Round-trip through errors.As — the exact pattern bridge.Respond uses.
	var err error = &RPCError{Code: -32601, Message: "method not found"}

	var re *RPCError
	if !errors.As(err, &re) {
		t.Fatalf("errors.As failed to unwrap RPCError from error interface")
	}
	if re.Code != -32601 {
		t.Errorf("unwrapped Code = %d, want -32601", re.Code)
	}
	if re.Message != "method not found" {
		t.Errorf("unwrapped Message = %q, want %q", re.Message, "method not found")
	}
}

// --- The session chain: what retention is allowed to delete ---

// TestRecordSession pins the chain bookkeeping. Every case here is a way to
// LOSE a session id, and a lost id is a session directory the reaper then
// deletes as an orphan — taking that period's transcript and pre-images with
// it. The old code assigned ACPSessionID directly, which is exactly case 2.
func TestRecordSession(t *testing.T) {
	cases := []struct {
		name      string
		start     Chat
		record    string
		wantCur   string
		wantPrior []string
	}{
		{
			name:      "first session",
			record:    "sess_a",
			wantCur:   "sess_a",
			wantPrior: nil,
		},
		{
			name:      "detaching keeps the old id in the chain",
			start:     Chat{ACPSessionID: "sess_a"},
			record:    "",
			wantCur:   "",
			wantPrior: []string{"sess_a"},
		},
		{
			name:      "switching retires the old id",
			start:     Chat{ACPSessionID: "sess_a"},
			record:    "sess_b",
			wantCur:   "sess_b",
			wantPrior: []string{"sess_a"},
		},
		{
			name:      "re-attaching after a detach does not duplicate",
			start:     Chat{PriorACPSessionIDs: []string{"sess_a"}},
			record:    "sess_a",
			wantCur:   "sess_a",
			wantPrior: []string{},
		},
		{
			name:      "recording the current id is a no-op",
			start:     Chat{ACPSessionID: "sess_a", PriorACPSessionIDs: []string{"sess_0"}},
			record:    "sess_a",
			wantCur:   "sess_a",
			wantPrior: []string{"sess_0"},
		},
		{
			name:      "a third session keeps both predecessors, oldest first",
			start:     Chat{ACPSessionID: "sess_b", PriorACPSessionIDs: []string{"sess_a"}},
			record:    "sess_c",
			wantCur:   "sess_c",
			wantPrior: []string{"sess_a", "sess_b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.start
			c.RecordSession(tc.record)
			if c.ACPSessionID != tc.wantCur {
				t.Errorf("ACPSessionID = %q, want %q", c.ACPSessionID, tc.wantCur)
			}
			if !slices.Equal(c.PriorACPSessionIDs, tc.wantPrior) {
				t.Errorf("PriorACPSessionIDs = %v, want %v", c.PriorACPSessionIDs, tc.wantPrior)
			}
		})
	}
}

// TestSessionChain pins that the chain is the full keep-set and that Chat and
// ChatHeader agree on it — the sweep reads headers while the delete path reads
// chats, so a disagreement means one of them reaps what the other keeps.
func TestSessionChain(t *testing.T) {
	cases := []struct {
		name string
		chat Chat
		want []string
	}{
		{name: "no sessions", chat: Chat{}, want: nil},
		{name: "current only", chat: Chat{ACPSessionID: "sess_a"}, want: []string{"sess_a"}},
		{
			name: "detached chat still claims its past",
			chat: Chat{PriorACPSessionIDs: []string{"sess_a"}},
			want: []string{"sess_a"},
		},
		{
			name: "current last",
			chat: Chat{ACPSessionID: "sess_c", PriorACPSessionIDs: []string{"sess_a", "sess_b"}},
			want: []string{"sess_a", "sess_b", "sess_c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.chat.SessionChain(); !slices.Equal(got, tc.want) {
				t.Errorf("Chat.SessionChain() = %v, want %v", got, tc.want)
			}
			h := tc.chat.Header()
			if got := h.SessionChain(); !slices.Equal(got, tc.want) {
				t.Errorf("ChatHeader.SessionChain() = %v, want %v (Header dropped the chain)", got, tc.want)
			}
		})
	}
}
