package agent

// The two live emitters in this package that complete a certified projection:
// persistDisplacedTurn's message_appended (the transcript, stamped from Mutate's
// return) and abortInFlightTools' tool_call_update burst (the live turn, stamped
// once, on the last frame, from MarkInFlightToolsAborted's return).

import (
	"encoding/json"
	"testing"

	"github.com/cplieger/vibekit/internal/subject"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// ringFrame is the envelope as the ring holds it: the type and the stamp.
type ringFrame struct {
	Subject *vibekit.SubjectStamp `json:"subject"`
	Type    string                `json:"type"`
}

// ringFramesOfType decodes every ring frame of type et, oldest first.
func ringFramesOfType(t *testing.T, h *Runtime, et vibekit.EventType) []ringFrame {
	t.Helper()
	var out []ringFrame
	for _, e := range h.bus.fanout.Snapshot() {
		var f ringFrame
		if err := json.Unmarshal(e.Event.Data, &f); err != nil {
			t.Fatalf("unmarshal ring event: %v", err)
		}
		if f.Type == string(et) {
			out = append(out, f)
		}
	}
	return out
}

func TestPersistDisplacedTurn_StampsMessageAppendedFromMutate(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "the displaced turn's reply")

	h.coord.persistDisplacedTurn(t.Context(), "c1", &vibekit.Message{
		ID: newMessageID(), Role: vibekit.RoleAssistant, Content: "the displaced turn's reply",
	})

	frames := ringFramesOfType(t, h, vibekit.EventMessageAppended)
	if len(frames) != 1 {
		t.Fatalf("message_appended frames = %d, want 1", len(frames))
	}
	// The fake mints one `chat` version per saved Mutate: startedTurnOn's seed is
	// the first, the displaced turn's insert is the second.
	want := vibekit.SubjectStamp{Kind: string(subject.KindChat), Ref: "c1", Version: "2"}
	if frames[0].Subject == nil || *frames[0].Subject != want {
		t.Errorf("message_appended Subject = %+v, want %+v", frames[0].Subject, want)
	}
}

func TestPersistDisplacedTurn_DeclinedMutateStampsNothing(t *testing.T) {
	h, _, _ := newTestHub()
	// No chat record: the mutator declines and nothing is broadcast.
	h.coord.persistDisplacedTurn(t.Context(), "absent", &vibekit.Message{
		ID: newMessageID(), Role: vibekit.RoleAssistant, Content: "orphan",
	})
	if frames := ringFramesOfType(t, h, vibekit.EventMessageAppended); len(frames) != 0 {
		t.Errorf("message_appended frames = %d, want 0 for a declined save", len(frames))
	}
}

func TestAbortInFlightTools_OnlyTheLastFrameCarriesTheLiveTurnStamp(t *testing.T) {
	h, cs, _ := newTestHub()
	startedTurnOn(t, h, cs, "c1", "reply")
	buf := h.stageTurnBuffer(t, "c1")
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t1", Status: vibekit.ToolInProgress})
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t2", Status: vibekit.ToolPending})
	buf.AppendToolCall(&vibekit.ToolCall{ID: "t3", Status: vibekit.ToolCompleted})

	h.coord.abortInFlightTools(t.Context(), "c1", buf)

	frames := ringFramesOfType(t, h, vibekit.EventToolCallUpdate)
	if len(frames) != 2 {
		t.Fatalf("tool_call_update frames = %d, want 2 (the two unsettled calls)", len(frames))
	}
	if frames[0].Subject != nil {
		t.Errorf("first frame Subject = %+v, want nil: a mid-burst observe would read unchanged for a state the client lacks", *frames[0].Subject)
	}
	want := vibekit.SubjectStamp{Kind: string(subject.KindLiveTurn), Ref: "c1", Version: buf.Version()}
	if frames[1].Subject == nil || *frames[1].Subject != want {
		t.Errorf("last frame Subject = %+v, want %+v", frames[1].Subject, want)
	}
}
