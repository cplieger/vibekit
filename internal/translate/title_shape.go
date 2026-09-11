package translate

// The one door treatment for a string from KAS that wants to become a chat name,
// shared by the focus channel (focus.go) and the session/load metadata title
// (agent.adoptKASTitle): a poisoned title KAS STORED is re-offered on every resume, so
// one rung gated alone lets the same string in through the other.

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
// model output cannot be partitioned. Two residuals no shape rule closes — a one-clause
// refusal under the cap is structurally a sentence-case title, since KAS's sanitize
// already stripped its trailing `?`, and an unspaced script yields one Fields entry
// however long. The escalation for either is withholding the session_title_llm
// capability row or KIRO_DISABLE_SESSION_TITLE_LLM, not another guess at the text.
func TitleRefusal(title string) string {
	switch {
	// Well-SHAPED; what disqualifies it is whose placeholder it is.
	case title == KASDefaultSessionTitle:
		return refusalKASPlaceholder
	case utf8.RuneCountInString(title) > maxTitleRunes:
		return refusalTooLong
	// Ete is the only producer of this suffix here and an agent's own title is never
	// Ete-capped, so a truncated title is a derivation or an over-long model reply.
	// Refusing costs nothing: vibekit's own first-prompt label is the better name.
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
