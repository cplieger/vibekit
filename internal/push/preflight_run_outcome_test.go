package push

import (
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The run_outcome switch has to reach the SEND gate, not just the settings file: a
// kind whose preference is not consulted by preflightSend is a toggle that changes
// nothing. preflightSend is read directly because its two answers are the contract
// (nil means do not send, a non-nil slice means every gate passed), and Send folds
// that decision into a fan-out no assertion can see through.
func TestPreflightSend_HonoursTheRunOutcomePreference(t *testing.T) {
	for name, tc := range map[string]struct {
		enabled  bool
		wantSend bool
	}{
		"off": {enabled: false, wantSend: false},
		"on":  {enabled: true, wantSend: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := New(t.Context(), t.TempDir(), testSubject)
			t.Cleanup(s.Close)
			s.SetPreferences(map[vibekit.PushKind]bool{vibekit.PushKindRunOutcome: tc.enabled})

			subs := s.preflightSend(vibekit.PushKindRunOutcome, vibekit.RunSubject("wf_x"))

			if got := subs != nil; got != tc.wantSend {
				t.Errorf("preflightSend(run_outcome) sends = %v with the preference %v, want %v",
					got, tc.enabled, tc.wantSend)
			}
		})
	}
}

// The kind is registered, which is the other half: preflightSend drops a kind its
// prefs map has no entry for, so a registry row missing while the pushKinds entry
// exists fails SILENTLY rather than at boot.
func TestNew_SeedsTheRunOutcomePreference(t *testing.T) {
	s := New(t.Context(), t.TempDir(), testSubject)
	t.Cleanup(s.Close)

	s.mu.Lock()
	on, known := s.prefs[vibekit.PushKindRunOutcome]
	s.mu.Unlock()

	if !known {
		t.Fatal("run_outcome has no preference entry, so every send of it is dropped before the wire")
	}
	if !on {
		t.Error("run_outcome defaults off; the registry row is DefaultOn like both other keyed kinds")
	}
}
