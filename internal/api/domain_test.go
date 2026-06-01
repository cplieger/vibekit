package api

import (
	"errors"
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
		Agent:           "kiro",
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

	if h.ID != "c1" || h.Name != "Hello" || h.Agent != "kiro" || h.Model != "claude" {
		t.Errorf("header identity fields = %+v, want id=c1 name=Hello agent=kiro model=claude", h)
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
	if h.Name != "" || h.Agent != "" || h.Model != "" || h.ACPSessionID != "" || h.CurrentModeID != "" {
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

func TestChatHeader_copies_boolean_and_tangent_fields(t *testing.T) {
	c := &Chat{
		ID:              "tangent-1",
		Name:            "Side conversation",
		ParentChatID:    "parent-1",
		SupervisedMode:  true,
		AutoApproveCrew: true,
	}

	h := c.Header()

	if !h.SupervisedMode {
		t.Error("SupervisedMode not copied to header")
	}
	if !h.AutoApproveCrew {
		t.Error("AutoApproveCrew not copied to header")
	}
	if h.ParentChatID != "parent-1" {
		t.Errorf("ParentChatID = %q, want parent-1", h.ParentChatID)
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
