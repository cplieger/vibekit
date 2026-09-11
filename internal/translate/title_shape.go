package translate

// The one door treatment for a string from KAS that wants to become a chat name,
// shared by the focus channel (focus.go) and the session/load metadata title
// (agent.adoptKASTitle).
//
// It is BOUNDARY VALIDATION of untrusted foreign text that becomes LATCHED local
// state, and both halves earn it. The text is a model reply nothing upstream bounds
// or sanitizes; the latch is that a chat name only ever moves UP the precedence, so
// the first string through owns the chat until an agent declares a title, and no
// pass repairs a name after the fact. A surface that re-derives its title on every
// poll takes transient damage instead and needs no gate — the gate is earned by the
// latch, not by the text being foreign.
//
// Two doors rather than one because a poisoned title KAS STORED is re-offered on
// every resume, so gating one rung alone lets the same string in through the other.
//
// Nothing vibekit sends can stop the producer, measured on the pinned bundle: the
// LLM titler's kickoff reads a feature-config registry built from two providers,
// the KAS process environment and upstream's experiment service, so no client
// capability key reaches it. This door is the only lever vibekit holds.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cplieger/vibekit/internal/sanitize"
)

// KASDefaultSessionTitle is KAS's placeholder (DEFAULT_SESSION_TITLE), returned by
// every session/new and re-emitted on the focus channel after a revert empties the
// transcript. Adopting it swaps vibekit's own placeholder for a worse one AND makes
// the chat non-default-named, which locks out the real title arriving later.
const KASDefaultSessionTitle = "New Session"

// derivedTitleEllipsis is the suffix KAS's Ete appends when it caps a string at 80
// runes: `t.length<=80 ? t : t.slice(0,77)+"..."`.
const derivedTitleEllipsis = "..."

// maxTitleRunes caps an adopted title. KAS caps at 80 itself, so this is a guard
// against a malformed frame rather than a formatter.
const maxTitleRunes = 80

// maxTitleWords bounds an adopted title. Upstream's own prompt says 3 to 6 words and
// never more than 8, but an agent's update_session_information title is bound by no
// prompt and measured live ones run to 7, so 8 is too tight to be safe here.
const maxTitleWords = 12

// Refusal reasons, so a door's one log line names WHICH rule fired and a shape the
// gate gets wrong is diagnosable instead of presenting as a chat that silently keeps
// its placeholder.
const (
	refusalKASPlaceholder = "kas placeholder"
	refusalTooLong        = "over the rune cap"
	refusalTruncated      = "truncated"
	refusalMultiSentence  = "multi-sentence"
	refusalTooManyWords   = "too many words"
)

// SanitizeTitle prepares one upstream title for a chat name: ANSI and hidden runes
// out, then the single-line display policy, then trimmed. Empty means there is
// nothing to adopt.
func SanitizeTitle(raw string) string {
	return strings.TrimSpace(displayText(sanitize.Output(raw)))
}

// TitleRefusal names the rule a sanitized title breaks, or "" when it breaks none.
//
// Every rule asserts SHAPE and none asserts content: a substring list over free-form
// model output cannot be partitioned.
//
// There is exactly ONE unconditional escalation, KIRO_DISABLE_SESSION_TITLE_LLM=true
// in the bridge environment, which the kickoff tests before consulting anything else.
// The session_title_llm capability row is not a switch in either direction, for the
// reason the package comment gives; an earlier version of this comment offered it as
// one, and that was wrong.
//
// Two residuals no shape rule closes: a ONE-clause refusal under the rune cap is
// structurally a sentence-case title, since KAS's sanitize already stripped its
// trailing "?", and an unspaced script yields one Fields entry however long, so the
// word cap cannot see a CJK sentence.
func TitleRefusal(title string) string {
	switch {
	// Well-SHAPED; what disqualifies it is whose placeholder it is.
	case title == KASDefaultSessionTitle:
		return refusalKASPlaceholder
	case utf8.RuneCountInString(title) > maxTitleRunes:
		return refusalTooLong
	// Ete is the only producer of this suffix here and an agent's own title is never
	// Ete-capped, so a truncated title is a derivation or an over-long model reply.
	// Unconditional, where a producer-side rule needs a word-count conjunct: see
	// hasInternalSentenceBreak for the asymmetry both choices rest on.
	case strings.HasSuffix(title, derivedTitleEllipsis):
		return refusalTruncated
	case hasInternalSentenceBreak(title):
		return refusalMultiSentence
	case len(strings.Fields(title)) > maxTitleWords:
		return refusalTooManyWords
	}
	return ""
}

// sentenceTerminators can close a sentence mid-string: the Latin three plus their
// fullwidth and ideographic forms, so the rule is not Latin-only.
const sentenceTerminators = ".?!。？！"

// hasInternalSentenceBreak reports whether title carries a terminator, then
// whitespace, then a letter. That triplet is what separates prose from a title: a
// title's own trailing punctuation is stripped upstream, and a mid-word period
// (Node.js, v2.0) has no whitespace after it.
//
// Measured over 788 real titles on the live volume (150 chat names, 638 stored
// session titles): it fires 20 times and every one is a refusal the truncation rule
// or the word cap already refuses, so its marginal yield is ZERO — and no legitimate
// title in that population carries an internal break either, so its false-positive
// class (a Title Case title holding an abbreviation, "Fix Dr. Smith Profile Import")
// is equally unobserved.
//
// Kept on the ASYMMETRY rather than on yield, which is what a latch door turns on.
// Refusing wrongly leaves a real name and any later agent declaration still wins;
// adopting wrongly installs the refusal AS the name, and the conversations that make
// the model refuse — a one-word first prompt — are exactly the ones no agent ever
// titles. So refusing is the cheap failure here. A PRODUCER-side rule inverts that,
// because there a false positive denies every consumer the title with no local label
// to fall back on, so the two rule sets are deliberately different: do not align
// them.
func hasInternalSentenceBreak(title string) bool {
	runes := []rune(title)
	// len-2 because the triplet needs a rune after the whitespace.
	for i := range len(runes) - 2 {
		if !strings.ContainsRune(sentenceTerminators, runes[i]) {
			continue
		}
		if letterFollowsSpace(runes[i+1:]) {
			return true
		}
	}
	return false
}

// letterFollowsSpace reports whether rest opens with whitespace and then a letter.
func letterFollowsSpace(rest []rune) bool {
	if len(rest) == 0 || !unicode.IsSpace(rest[0]) {
		return false
	}
	for _, r := range rest {
		if unicode.IsSpace(r) {
			continue
		}
		return unicode.IsLetter(r)
	}
	return false
}
