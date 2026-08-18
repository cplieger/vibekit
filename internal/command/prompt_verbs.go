package command

// Prompt text KAS claims before the model.
//
// `session/prompt` inspects the text and handles a few verbs ITSELF, answering
// `stopReason: end_turn` without ever invoking the model. Nothing streams, so
// the turn produces no content — which is indistinguishable from a dead turn by
// the only signal isEmptyTurn has, and that misreading is expensive: recovery
// closes the bridge the launched run is parented on, detaches the ACP session,
// records the turn as interrupted, and re-sends the verb, which launches a
// SECOND run. Measured on `/goal` (kiro-cli 2.18.1).
//
// `/goal` is currently the only member, and it is a member only because vibekit
// sends the `goal` setting at the connection door (see internal/kascap). With
// that key absent KAS never reaches parseGoalCommand, the text goes to the model
// as prose, and the turn carries content like any other — so the guard only ever
// suppresses recovery for a turn whose emptiness was expected.
//
// This is deliberately NOT a general slash-command table. vibekit does not
// enumerate KAS's command list (vibekit.md "Slash commands"), and the question
// this file answers is narrower: does KAS answer this text without a model call.

import (
	"regexp"
	"strings"
)

// goalPrefix is the only spelling KAS's parser accepts before an objective.
const goalPrefix = "/goal "

// goalMaxSuffix matches the trailing `--max N` bound on a goal objective.
//
// Anchored at the end and requiring leading whitespace, exactly as KAS's own
// parser does, so a `--max` earlier in the text is part of the objective.
var goalMaxSuffix = regexp.MustCompile(`\s+--max\s+(\d+)$`)

// kasClaimsPromptText reports whether KAS's prompt path answers this text
// without invoking the model.
//
// The client half of this contract lives in static-src/chat-options.ts
// (goalCommand composes what parseGoalCommand accepts) and its test transcribes
// KAS's parser verbatim. Keep the three in agreement: a divergence here reads as
// the empty-turn bug coming back.
func kasClaimsPromptText(text string) bool {
	return parsesAsGoalCommand(text)
}

// parsesAsGoalCommand mirrors KAS's parseGoalCommand, reduced to the one bit
// vibekit needs: whether it returns a goal rather than nil.
//
// The bound itself is deliberately not returned. KAS owns the clamp and the
// default, and a second copy of those numbers here would be a claim about KAS's
// behaviour that nothing in this process can verify.
func parsesAsGoalCommand(text string) bool {
	trimmed := strings.TrimSpace(text)
	// A bare `/goal` carries no objective, so KAS returns nil and the text
	// reaches the model. Only the prefixed form is claimed.
	if !strings.HasPrefix(trimmed, goalPrefix) {
		return false
	}
	body := strings.TrimSpace(strings.TrimPrefix(trimmed, goalPrefix))
	if body == "" {
		return false
	}
	// With a `--max N` suffix the objective is what precedes it. The regexp
	// requires leading whitespace and runs on the TRIMMED body, so a bound with
	// nothing before it never matches and stays part of the objective — which is
	// why this branch can only be reached with a non-empty prefix.
	if m := goalMaxSuffix.FindStringIndex(body); m != nil {
		return strings.TrimSpace(body[:m[0]]) != ""
	}
	return true
}
