package command

// The ledger is the ONLY evidence anywhere that a mid-turn steer carries the
// user's own words, so what these tests pin is the shape of its answers: total,
// keyed by the id KAS returned, and biased toward the agent whenever it does not
// know.

import (
	"testing"
	"time"

	"github.com/cplieger/vibekit/internal/vibekit"
)

func TestSteerLedger_RecordedIDIsTheUsers(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "steer-m-1")

	if got := l.SteerOrigin("c1", "steer-m-1"); got != vibekit.SteerOriginUser {
		t.Errorf("SteerOrigin(recorded) = %q, want %q", got, vibekit.SteerOriginUser)
	}
}

// Everything the ledger has not seen is the agent's — a workflow's report, a
// run-completion nudge, a steer another device sent before this process started.
func TestSteerLedger_UnknownIDIsTheAgents(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "steer-m-1")

	for _, tc := range []struct {
		name    string
		chat    vibekit.ChatID
		steerID string
	}{
		{"never recorded", "c1", "notify-wf-9"},
		{"recorded for another chat", "c2", "steer-m-1"},
		{"empty id", "c1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := l.SteerOrigin(tc.chat, tc.steerID); got != vibekit.SteerOriginAgent {
				t.Errorf("SteerOrigin = %q, want %q", got, vibekit.SteerOriginAgent)
			}
		})
	}
}

// An empty id is not recordable either: KAS answered without one, so there is no
// name a later frame could arrive under.
func TestSteerLedger_EmptyIDIsNotRecorded(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "")

	if n := len(l.sent); n != 0 {
		t.Errorf("recorded %d entries for an empty id, want 0", n)
	}
}

func TestSteerLedger_ExpiredIDIsTheAgents(t *testing.T) {
	l := NewSteerLedger()
	now := time.Now()
	l.now = func() time.Time { return now }
	l.RecordUserSteer("c1", "steer-m-1")

	l.now = func() time.Time { return now.Add(steerTTL + time.Second) }
	if got := l.SteerOrigin("c1", "steer-m-1"); got != vibekit.SteerOriginAgent {
		t.Errorf("SteerOrigin(expired) = %q, want %q", got, vibekit.SteerOriginAgent)
	}
}

func TestSteerLedger_ForgetChatDropsOnlyThatChat(t *testing.T) {
	l := NewSteerLedger()
	l.RecordUserSteer("c1", "steer-a")
	l.RecordUserSteer("c2", "steer-b")

	l.ForgetChat("c1")

	if got := l.SteerOrigin("c1", "steer-a"); got != vibekit.SteerOriginAgent {
		t.Errorf("c1 after ForgetChat = %q, want %q", got, vibekit.SteerOriginAgent)
	}
	if got := l.SteerOrigin("c2", "steer-b"); got != vibekit.SteerOriginUser {
		t.Errorf("c2 after forgetting c1 = %q, want %q — a sibling chat's steers are untouched",
			got, vibekit.SteerOriginUser)
	}
}

// The bound is what stops a pathological producer growing the map without limit,
// and it evicts the entry closest to expiry rather than clearing the map: losing
// one record mislabels one note, losing all of them mislabels every later one.
func TestSteerLedger_BoundedByEvictingTheOldest(t *testing.T) {
	l := NewSteerLedger()
	l.maxN = 4
	now := time.Now()
	l.now = func() time.Time { return now }

	// Distinct expiries, so "closest to expiry" is well defined.
	for i, id := range []string{"a", "b", "c", "d", "e", "f"} {
		l.ttl = steerTTL + time.Duration(i)*time.Minute
		l.RecordUserSteer("c1", id)
	}

	// The sweep runs before the insert, so the bound is an inclusive ceiling.
	if n := len(l.sent); n > l.maxN {
		t.Errorf("held %d entries with maxN %d", n, l.maxN)
	}
	if got := l.SteerOrigin("c1", "a"); got != vibekit.SteerOriginAgent {
		t.Errorf("the oldest entry survived eviction: %q", got)
	}
	if got := l.SteerOrigin("c1", "f"); got != vibekit.SteerOriginUser {
		t.Errorf("the newest entry = %q, want %q", got, vibekit.SteerOriginUser)
	}
}

// A nil ledger answers rather than panicking, because the alternative is a
// nil-receiver crash on the first steer of a build whose wiring missed it.
func TestSteerLedger_NilAnswersAgent(t *testing.T) {
	var l *SteerLedger
	l.RecordUserSteer("c1", "steer-m-1")
	if got := l.SteerOrigin("c1", "steer-m-1"); got != vibekit.SteerOriginAgent {
		t.Errorf("SteerOrigin on a nil ledger = %q, want %q", got, vibekit.SteerOriginAgent)
	}
	l.ForgetChat("c1")
}
