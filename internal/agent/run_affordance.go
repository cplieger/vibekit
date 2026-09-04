package agent

// What may be done to one run, and why not for the rest.
//
// ONE table, and it is the server's. Two used to face each other — the client's
// status→verbs map and this package's per-verb `from` lists — and neither could
// see the third input, whether anything in this process still holds the run. So
// the pair could agree perfectly and still draw a button whose only possible
// outcome was a refusal.
//
// The client's other input was worse than stale: it derived a run's parentage
// from a map fed only by SSE frames, so every client that reloaded read a
// chat-parented run as parentless. Parentage is a durable property of the run
// and the chat store answers it with no RPC, which is why the answer is computed
// here.
//
// THE PARENTLESS-ONLY RULE IS GONE rather than moved here. It rested on "an
// agent-parented run's recovery is the agent's own", and that is false for
// exactly the statuses retry is legal from: kiro-cli's own restore pass
// considers a `running` or `paused` run only, so an aborted chat-parented run
// has no recovery path in either product. Withholding retry from it left it
// unreachable; offering it is the point.

import (
	"context"
	"slices"
	"strconv"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// The run-control verb names, as the affordance answer and the routes spell
// them. Named rather than repeated as literals because the table below, the
// route handlers and the client's label map all key on the same words.
const (
	verbPause  = "pause"
	verbResume = "resume"
	verbCancel = "cancel"
	verbRetry  = "retry"
)

// runStatusVerbs maps KAS's WorkflowStatusSchema to the verbs each status
// accepts, mirroring what KAS itself does: retry throws for anything
// non-terminal, pause sets a flag that means nothing once a run has stopped,
// resume re-drives a paused one.
//
// Cancel is absent from the terminal statuses and present on both live ones:
// every live run must offer a way out, and a finished run must not offer a stop
// that would do nothing.
//
// An UNKNOWN status is absent rather than mapped to an empty list, so a future
// KAS status degrades to a read-only view instead of a wrong control.
var runStatusVerbs = map[string][]string{
	"running":       {verbPause, verbCancel},
	runStatusPaused: {verbResume, verbCancel},
	// A completed run is a record: nothing to retry, nothing to stop.
	"completed": {},
	// Retry resets the failed and aborted nodes plus their ancestors, so the
	// completed work survives — unlike relaunching the recipe, which starts at
	// step one.
	"failed":  {verbRetry},
	"aborted": {verbRetry},
}

// hostedOnlyVerbs need the process that holds the run's registry entry, and
// cannot re-host one.
//
// KAS's pause reaches `registry.require`, which throws for a run not in the live
// in-memory registry and does not rehydrate from disk. Resume executes, so the
// utility bridge is no carrier for it either — a text-only session denies every
// permission request and errors every fs call, so a run resumed there would
// grind through its steps with no tools.
//
// Retry is deliberately NOT here: it loads the run into a fresh process first,
// which is what makes it the one verb that reaches a run nothing hosts.
var hostedOnlyVerbs = []string{verbPause, verbResume}

// runAffordance answers what may be done to one run.
type runAffordance struct {
	// Refused maps a verb this run does not offer to one sentence for the reader.
	// It carries only a verb whose absence would otherwise be unexplained: a
	// status that plainly excludes a verb already says so through the state word
	// beside the row.
	Refused map[string]string
	// ParentChat is the chat whose agent launched the run, "" when parentless.
	//
	// Part of the answer rather than a second lookup, and for two reasons: it is
	// what the refusal sentences are ABOUT, and the run page asks the same question
	// to say where a step's live transcript went. Resolving it twice would pay for
	// the run inventory twice.
	ParentChat vibekit.ChatID
	// Recipe is the run's recipe name off KAS's inventory, "" when unknown.
	//
	// Here for ParentChat's reason and off the same read: retry needs it for the
	// lease it re-arms, and the inventory that carries the parent session carries
	// the name one field away. Not part of the wire answer — handleControls maps
	// the three fields the client asked for, and this is not one of them.
	Recipe string
	// Verbs are the offered controls, in the order a row presents them.
	Verbs []string
}

// permits reports whether the run offers this verb.
func (a runAffordance) permits(verb string) bool {
	return slices.Contains(a.Verbs, verb)
}

// refusal is the sentence for a verb this run does not offer, or "" when the
// affordance has nothing to add to the run's own status.
func (a runAffordance) refusal(verb string) string {
	return a.Refused[verb]
}

// runFacts is what an affordance is decided from. All three are available
// without a KAS call beyond the one status read the caller already made:
// parentage comes from the chat store and hosting from the bridge map.
type runFacts struct {
	// status is the run's own status, as `inspect` reports it.
	status string
	// parentChat is the chat whose agent launched the run, "" when parentless.
	parentChat vibekit.ChatID
	// parentName is that chat's display name, for the refusal sentence. Empty
	// for an unnamed chat, which the sentence then omits rather than quoting
	// nothing.
	parentName string
	// recipe is the run's recipe name, read off the same inventory as parentChat.
	recipe string
	// hosted reports whether some live bridge in this process holds the run's
	// registry entry.
	hosted bool
}

// affordanceOf answers what may be done to one run. Pure, so the table is
// testable over (status × parent × hosted) without a bridge or an RPC.
func affordanceOf(f runFacts) runAffordance {
	byStatus, known := runStatusVerbs[f.status]
	if !known {
		// An unrecognised status degrades to a read-only view. The parent chat still
		// travels: the page's step-transcript note needs it whatever the status is.
		return runAffordance{ParentChat: f.parentChat, Recipe: f.recipe}
	}
	out := runAffordance{
		ParentChat: f.parentChat,
		Recipe:     f.recipe,
		Verbs:      make([]string, 0, len(byStatus)),
	}
	for _, verb := range byStatus {
		if slices.Contains(hostedOnlyVerbs, verb) && !f.hosted {
			if out.Refused == nil {
				out.Refused = map[string]string{}
			}
			out.Refused[verb] = notHostedRefusal(verb, f.parentChat, f.parentName)
			continue
		}
		out.Verbs = append(out.Verbs, verb)
	}
	return out
}

// notHostedRefusal is the sentence a hosted-only verb gets when nothing in this
// process holds the run.
//
// It names the launching chat when there is one, because that is the reader's
// remedy: opening that chat respawns its bridge and brings the run back within
// reach. A parentless run has no such door, so its sentence says what state the
// run is in and which verb still works.
func notHostedRefusal(verb string, parentChat vibekit.ChatID, parentName string) string {
	if parentChat != "" {
		return "This run is driven by an agent in " + chatLabel(parentChat, parentName) +
			", and that conversation is not open here, so it cannot be " + pastTense(verb) +
			" from this page. Open that chat to bring the run back within reach."
	}
	return "This run has no live engine on this server, so it cannot be " + pastTense(verb) +
		" from here. A run from before the last restart is in this state; Cancel still works."
}

// chatLabel names a chat for a sentence a person reads: its name when it has
// one, its id otherwise. A chat created and never named carries an empty Name,
// and quoting that would leave the reader with a sentence naming nothing.
func chatLabel(chatID vibekit.ChatID, name string) string {
	if name == "" {
		return "chat " + strconv.Quote(string(chatID))
	}
	return strconv.Quote(name)
}

// pastTense spells a verb the way the refusal sentence needs it. Two of the four
// verbs are irregular enough that appending "d" is wrong, so the mapping is
// explicit and falls back to the verb itself rather than guessing.
func pastTense(verb string) string {
	switch verb {
	case verbPause:
		return "paused"
	case verbResume:
		return "resumed"
	case verbRetry:
		return "retried"
	case verbCancel:
		return "cancelled"
	}
	return verb
}

// affordance resolves the three facts and answers what may be done to the run.
//
// status is passed in rather than read here: every caller has already made that
// `inspect` call, and a control decision must be made against one status read
// rather than two that can disagree.
//
// Costs at most ONE `workflow/list` round trip (listedRun's), and none of the
// chat-store or bridge-map reads leaves this process. The parent session and the
// recipe come off that single read, so a verb acting on this answer spends no
// further list.
func (rs *Runs) affordance(ctx context.Context, workflowID, status string) runAffordance {
	listed := rs.listedRun(ctx, workflowID)
	f := runFacts{status: status, recipe: listed.Name}
	f.parentChat, f.parentName = rs.chatForSession(ctx, listed.ParentSessionID)
	f.hosted = rs.bridges.get(runChatID(workflowID)) != nil ||
		(f.parentChat != "" && rs.bridges.get(f.parentChat) != nil)
	return affordanceOf(f)
}

// chatForSession resolves a run's parent SESSION to the chat that owns it, from
// the chat store alone.
//
// Matched against the whole session CHAIN, not the current id: a chat changes
// session on a failed session/load, a model-switch fallback and empty-turn
// recovery, so the current id alone would leave a run launched before such a
// change looking parentless — which is the population this whole answer exists
// to get right.
//
// Unlike hostBridgeChat this does NOT require the chat to have a live bridge:
// naming the chat is what makes a refusal actionable, and a closed chat is
// exactly when the reader needs to be told which one to open.
func (rs *Runs) chatForSession(
	ctx context.Context, sessionID string,
) (chatID vibekit.ChatID, name string) {
	if sessionID == "" {
		return "", ""
	}
	// Indexed: vibekit.ChatHeader is 304 bytes, which gocritic's rangeValCopy flags.
	headers := rs.chats.List(ctx)
	for i := range headers {
		if slices.Contains(headers[i].SessionChain(), sessionID) {
			return vibekit.ChatID(headers[i].ID), headers[i].Name
		}
	}
	return "", ""
}
