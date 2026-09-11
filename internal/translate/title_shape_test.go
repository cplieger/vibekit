package translate

import (
	"strings"
	"testing"
)

// liveRefusalTitle is the string that motivated the door rule, verbatim from
// /config/chats/c-9917935fff16574cef206f8acc09a592.json's "name" field on the live
// instance. It is 77 characters plus KAS's own ellipsis, which is exactly what Ete
// produces at its 80-rune cap — so it arrives UNDER the rune cap and only a shape
// rule can stop it.
const liveRefusalTitle = "I need more context to generate a title. Could you share the user's first mes..."

// The refusal above is what the model said when KAS asked it to title a conversation
// whose first prompt was the single word "test". vibekit cannot fix KAS's guard, so the
// door's job is to refuse the answer, and asserting the reason per case is what stops a
// later edit collapsing the rules into one unhelpful string.
//
// The three measured live agent titles at the bottom are the over-filtering guard: a
// rule that starts refusing one of them has been tightened past usefulness, because
// they are exactly what this channel exists to deliver.
func TestTitleRefusal(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{
			// The live case, and the reason the rule exists at all. It breaks two
			// rules at once, so the sentence-break rule is red-checked against the
			// under-cap case below rather than against this one.
			name:  "the_live_refusal_is_refused",
			title: liveRefusalTitle,
			want:  refusalTruncated,
		},
		{
			// Under the rune cap, so no other rule sees it. KAS's sanitize strips
			// only TRAILING punctuation, which is why a prose reply arrives still
			// carrying its internal sentence break.
			name:  "a_multi_sentence_title_under_the_rune_cap",
			title: "I cannot title this. Please provide the first message",
			want:  refusalMultiSentence,
		},
		{
			// A question mid-string, the other half of the sentence-break rule.
			name:  "an_internal_question_mark_is_a_sentence_break",
			title: "Which file did you mean? I could not tell",
			want:  refusalMultiSentence,
		},
		{
			// The terminators are not Latin-only: the model answers about the
			// user's own message and can follow its language.
			name:  "an_ideographic_full_stop_is_a_sentence_break",
			title: "。 more context please",
			want:  refusalMultiSentence,
		},
		{
			name:  "a_thirteen_word_title_is_too_long_to_be_a_title",
			title: "Add a retry with exponential backoff to the upload path in the worker",
			want:  refusalTooManyWords,
		},
		{
			// The word cap is inclusive at 12: refusing a title that sits exactly
			// on it would discard a legitimately descriptive agent title, and 12 is
			// already double upstream's own 3-to-6-word instruction.
			name:  "exactly_twelve_words_is_adopted",
			title: "one two three four five six seven eight nine ten eleven twelve",
			want:  "",
		},
		{
			name:  "a_truncated_title_is_never_a_title",
			title: "Fix the flaky retry test in the scheduler package and also the...",
			want:  refusalTruncated,
		},
		{
			name:  "exactly_at_the_rune_cap_is_adopted",
			title: strings.Repeat("t", maxTitleRunes),
			want:  "",
		},
		{
			name:  "one_rune_past_the_cap_is_refused",
			title: strings.Repeat("t", maxTitleRunes+1),
			want:  refusalTooLong,
		},
		{
			// KAS's placeholder is well-SHAPED; what disqualifies it is whose
			// placeholder it is. Its own arm is what keeps the two facts apart.
			name:  "kas_s_placeholder_is_refused_as_a_placeholder",
			title: KASDefaultSessionTitle,
			want:  refusalKASPlaceholder,
		},
		{
			// A trailing period with nothing after it is not a sentence BREAK, and
			// KAS's sanitize would have stripped it anyway. Refusing it here would
			// be a second rule for a shape upstream already handles.
			name:  "a_trailing_period_alone_is_not_a_break",
			title: "Fix the retry test.",
			want:  "",
		},
		{
			// A version or a package name carries a period with no whitespace after
			// it, so the break rule must not fire on one.
			name:  "a_mid_word_period_is_not_a_break",
			title: "Upgrade Node.js to v22.1 in the builder",
			want:  "",
		},
		{name: "a_live_agent_title_title_case", title: "Fix ResizeObserver Error In Safari", want: ""},
		{name: "a_live_agent_title_sentence_case", title: "Safari ResizeObserver loop in vibekit", want: ""},
		{name: "a_live_agent_title_seven_words", title: "Fix Race Condition In Vibekit Page Titles", want: ""},
		{
			// The sentence-break scan needs a three-rune window, so a shorter title
			// has no bound to find. Unreachable from the focus door, which refuses
			// an empty title first, but both doors call this from another package.
			name:  "a_title_too_short_to_hold_a_sentence_break",
			title: "Go",
			want:  "",
		},
		{name: "the_empty_string_carries_no_break", title: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TitleRefusal(tc.title); got != tc.want {
				t.Errorf("TitleRefusal(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}

// Both doors run this before any rule, so a title carrying ANSI, a hidden rune or a
// newline is compared and stored in its clean form rather than verbatim. The stored
// rung needs it as much as the live one: KAS persisted that string and nothing on the
// way in bounded or sanitized it.
func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "plain_text_is_unchanged", raw: "Fix the retry test", want: "Fix the retry test"},
		{name: "ansi_is_stripped", raw: "\x1b[31mRelease\x1b[0m check", want: "Release check"},
		{name: "an_embedded_newline_becomes_a_space", raw: "Release\ncheck", want: "Release check"},
		{name: "surrounding_whitespace_goes", raw: "  Release check  ", want: "Release check"},
		{
			// A bidi override renders the title reversed. sanitize.Output runs
			// first and DELETES a hidden rune, so it never reaches displayText's
			// replace-with-a-space policy — the title keeps the reversed text and
			// loses the control that caused it, which is why the reversal below
			// reads as ordinary words rather than as two extra spaces.
			name: "a_bidi_override_is_deleted",
			raw:  "Run \u202Ednuof-eman\u202C now",
			want: "Run dnuof-eman now",
		},
		{
			// The bound the rune cap then refuses: nothing on the wire limits this
			// field, so an unbounded string must not reach a chat record or a log.
			name: "an_unbounded_title_is_capped_and_marked",
			raw:  strings.Repeat("x", 700),
			want: strings.Repeat("x", maxDisplayTextBytes) + "...",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeTitle(tc.raw); got != tc.want {
				t.Errorf("SanitizeTitle(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
