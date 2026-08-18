package command

import "testing"

// The cases are the boundary of KAS's own parseGoalCommand, transcribed in
// static-src/chat-options.test.ts. Every `want: false` row is text KAS hands to
// the MODEL, so a turn from it carries content and empty-turn recovery is the
// right behaviour; every `want: true` row is answered by KAS with end_turn and
// no content, which recovery must not touch.
func TestKASClaimsPromptText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"plain prose", "make the tests pass", false},
		{"another command", "/compact", false},
		// Bare `/goal` returns nil from KAS's parser, so it reaches the model
		// and answers as prose. This is the case the goal row refuses to send.
		{"bare verb", "/goal", false},
		{"bare verb with trailing space", "/goal   ", false},
		{"objective", "/goal make the test suite pass", true},
		{"leading whitespace", "   /goal make the test suite pass", true},
		{"objective with bound", "/goal make the test suite pass --max 12", true},
		// KAS's `--max` regexp requires LEADING whitespace and runs against the
		// already-trimmed body, so a bound with nothing before it is not a bound
		// at all: `/goal --max 5` sets a goal literally named "--max 5". Pinned
		// because it is the one case where mirroring the parser looks wrong.
		{"bound with no objective", "/goal --max 5", true},
		{"bound mid-text", "/goal raise --max 5 in the docs", true},
		// A non-integer bound is not a bound to KAS's regexp, so the whole
		// remainder is the objective and the command is still claimed.
		{"non-numeric bound", "/goal tidy up --max soon", true},
		// The prefix must be the verb, not a word starting with it.
		{"prefix is a longer word", "/goalpost check", false},
		{"substring, not a prefix", "please /goal something", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := kasClaimsPromptText(tc.text); got != tc.want {
				t.Errorf("kasClaimsPromptText(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
