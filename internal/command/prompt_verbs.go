package command

// Prompt text KAS claims before the model.
//
// `session/prompt` inspects the text and handles a few verbs itself,
// answering `stopReason: end_turn` without invoking the model. Nothing
// streams, so the turn produces no content — indistinguishable from a dead
// turn by the empty-turn recovery's only signal, and that misreading is
// expensive: recovery closes the bridge, detaches the ACP session, records
// the turn as interrupted, and re-sends the verb, launching a second run.
// Measured on `/goal` (kiro-cli 2.18.1).
//
// `/goal` is the only member, and only because vibekit sends the `goal`
// setting at the connection door. With that key absent KAS never reaches
// parseGoalCommand and the turn carries content like any other.
//
// Deliberately not a general slash-command table: vibekit does not
// enumerate KAS's command list. The question here is narrower — does KAS
// answer this text without a model call.

import (
	"regexp"
	"strings"
)

// goalPrefix is the only spelling KAS's parser accepts before an objective.
const goalPrefix = "/goal "

// goalMaxSuffix matches the trailing `--max N` bound on a goal objective.
// Anchored at the end with leading whitespace required, exactly as KAS's
// own parser.
var goalMaxSuffix = regexp.MustCompile(`\s+--max\s+(\d+)$`)

// kasClaimsPromptText reports whether KAS's prompt path answers this text
// without invoking the model.
//
// The client half of this contract lives in static-src/chat-options.ts and
// its test transcribes KAS's parser verbatim. Keep the three in agreement:
// a divergence reads as the empty-turn bug coming back.
func kasClaimsPromptText(text string) bool {
	return parsesAsGoalCommand(text)
}

// parsesAsGoalCommand mirrors KAS's parseGoalCommand, reduced to whether it
// returns a goal rather than nil. The bound itself is deliberately not
// returned — KAS owns the clamp and default.
func parsesAsGoalCommand(text string) bool {
	trimmed := strings.TrimSpace(text)
	// A bare `/goal` carries no objective, so KAS returns nil.
	if !strings.HasPrefix(trimmed, goalPrefix) {
		return false
	}
	body := strings.TrimSpace(strings.TrimPrefix(trimmed, goalPrefix))
	if body == "" {
		return false
	}
	// With a `--max N` suffix the objective is what precedes it; the
	// regexp runs on the trimmed body, so a bound with nothing before it
	// never matches and stays part of the objective.
	if m := goalMaxSuffix.FindStringIndex(body); m != nil {
		return strings.TrimSpace(body[:m[0]]) != ""
	}
	return true
}
