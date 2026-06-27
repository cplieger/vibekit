package permissions

import "testing"

func TestAutoDecideCrew(t *testing.T) {
	// Crew permissions auto-approve only when the chat opted into
	// auto-approve AND the request carries an allow_once option. Any
	// other combination surfaces the prompt to the user (DecisionNone).
	tests := []struct {
		name            string
		autoApproveCrew bool
		hasAllowOnce    bool
		want            AutoDecision
	}{
		{"enabled_with_allow_once_approves", true, true, DecisionAllow},
		{"enabled_without_allow_once_prompts", true, false, DecisionNone},
		{"disabled_with_allow_once_prompts", false, true, DecisionNone},
		{"disabled_without_allow_once_prompts", false, false, DecisionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AutoDecideCrew(tt.autoApproveCrew, tt.hasAllowOnce)
			if got != tt.want {
				t.Errorf("AutoDecideCrew(%v, %v) = %v, want %v",
					tt.autoApproveCrew, tt.hasAllowOnce, got, tt.want)
			}
		})
	}
}

func TestAutoDecideShell(t *testing.T) {
	// Pins the shellDecisionMap contract: ShellAllow needs an allow_once
	// option to auto-approve; ShellDeny always auto-denies regardless of
	// allow_once; everything else (ask / unknown) prompts the user.
	tests := []struct {
		name         string
		decision     ShellDecision
		hasAllowOnce bool
		want         AutoDecision
	}{
		{"allow_with_allow_once", ShellAllow, true, DecisionAllow},
		{"allow_without_allow_once_prompts", ShellAllow, false, DecisionNone},
		{"deny_with_allow_once_denies", ShellDeny, true, DecisionDeny},
		{"deny_without_allow_once_denies", ShellDeny, false, DecisionDeny},
		{"ask_prompts", ShellAsk, true, DecisionNone},
		{"unknown_decision_prompts", ShellDecision("future"), true, DecisionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AutoDecideShell(tt.decision, tt.hasAllowOnce)
			if got != tt.want {
				t.Errorf("AutoDecideShell(%q, %v) = %v, want %v",
					tt.decision, tt.hasAllowOnce, got, tt.want)
			}
		})
	}
}
