// ---------------------------------------------------------------------------
// The turn projection.
//
// A turn is the rendering unit: ONE trigger plus everything that trigger
// caused. The store holds a flat `Message[]`; this module groups it into the
// turns the transcript actually renders, so the card structure (header / body /
// footer) has a data object to hang off instead of being inferred from DOM
// adjacency.
//
// Pure and DOM-free on purpose — the collapse model (§3.4), transcript search
// (§3.5) and the timeline rail (§3.2) all need to reason about turns without
// touching the renderer.
// ---------------------------------------------------------------------------

import { severityOf, defaultFailureReason } from "./turn-severity.js";
import type { Message, FileChange } from "./types.js";
import type { TurnOutcome } from "./wire/types.gen.js";

/** A turn's result, as scannable colour down the transcript.
 *
 *  RE-EXPORTED from the generated wire types rather than declared here: the rule
 *  that produces it is implemented in both languages, so a hand-written union
 *  would be a second enumeration of one vocabulary with nothing holding the two
 *  spellings together — the shared fixture pins the BEHAVIOUR and cannot pin a
 *  name. `running` is the one member that is never persisted; every other value
 *  arrives on the message that finalized its turn. */
export type { TurnOutcome };

export interface Turn {
  /** Reconcile key: the id of the turn's first message. Stable across
   *  repaints because a turn's opening message never changes identity. */
  id: string;
  /** 1-based ordinal within the LOADED window. Not session-absolute: the
   *  store is paginated, so turn 1 here is only turn 1 of the session when
   *  `has_more` is false. The rail resolves absolute numbers from its own
   *  session-wide fetch. */
  n: number;
  /** The user's prompt. Absent when the turn was not user-initiated (a
   *  run-completion wake, a scheduled trigger), in which case the header
   *  renders a typed trigger line instead of putting words in the user's
   *  mouth. */
  trigger: Message | undefined;
  /** Everything the trigger caused: assistant turns and inline events. */
  body: Message[];
  /** Turn start — the trigger's timestamp, else the first body message's. */
  ts: number;
  outcome: TurnOutcome;
  /** The NEXT turn's trigger — what a rewind from this turn's footer addresses.
   *
   *  Rewind reverts to the state right AFTER this turn: KAS drops the message
   *  it is given plus everything following, so keeping turn N means addressing
   *  turn N+1's user message. That is why the target lives on the previous
   *  turn rather than its own — the footer says "go back to here", and here is
   *  the line below it.
   *
   *  Undefined on the last turn (nothing after it to discard, so no button)
   *  and when the next turn has no trigger (an agent-initiated turn has no
   *  user message, and KAS refuses to revert to anything else). */
  rewindTo: Message | undefined;
}

/** Per-turn ledger inputs, summed across the turn's body messages.
 *
 *  A turn can hold more than one assistant message (a model switch mid-turn
 *  splits it, an interrupted turn leaves a partial), so the footer sums rather
 *  than reading the last one. `changedFiles` merges by path, taking the
 *  LAST-WRITTEN counts per path rather than adding them: the map is a
 *  cumulative per-turn snapshot stamped at `turn_ended`, not a delta, so
 *  adding two snapshots of the same path would double-count it. */
export interface TurnLedger {
  credits: number;
  elapsedMs: number;
  changedFiles: Record<string, FileChange>;
  /** Commands the turn ran, and files it read. Derived from the turn's tool
   *  calls, which is the only place the counts exist — nothing aggregates them,
   *  so a turn that read forty files and wrote none reported no work at all. */
  commands: number;
  reads: number;
  /** The model(s) that answered, distinct and in emission order.
   *
   *  A FOURTH aggregation strategy beside the sum, the count and the
   *  merge-by-key above, because a model is none of those: adding two model ids
   *  is meaningless and taking the last one is silently wrong on a turn a
   *  mid-turn switch split into two assistant messages. Carrying both is the
   *  only rendering that answers "which model served this turn" honestly when
   *  the answer is "two of them". Empty for every turn persisted before the
   *  field existed, which is why the footer renders nothing rather than
   *  "unknown". */
  models: string[];
}

/** Tool kinds that mean "a command ran". `execute` and `shell` are the two KAS
 *  actually emits for a shell invocation; `command` is in the wire enum and is
 *  counted for completeness rather than because it has been observed. */
const COMMAND_KINDS = new Set(["execute", "shell", "command"]);

/** Group a flat message list into turns.
 *
 *  A `user` message opens a turn. Everything else joins the open turn, or
 *  opens a headerless one when there is none — which happens two legitimate
 *  ways: an agent-initiated turn (no user row exists to promote), and a
 *  paginated window whose first page starts mid-turn. Both render the same
 *  way, so neither needs a special case here.
 *
 *  `live` marks the LAST turn as running, and it is not a message field for the
 *  same reason it is not called `thinking` any more: a turn in flight is only ever
 *  the last one, and whether one IS in flight is now TWO facts the caller composes
 *  — this client's own memory of a stream it has watched, plus the server's last
 *  statement that a turn is open (`store.ts` `turnLive`).
 *
 *  It has to be both, and `deriveOutcome`'s tail clause is why. The newest turn's
 *  carrier is absent in two completely different situations — nothing closed the
 *  turn, or the closing message is still in the server's in-memory buffer and
 *  therefore absent from `GET /api/chats/{id}` — and `unknown` is only honest for
 *  the first. `thinking` alone cannot separate them: it is client memory that starts
 *  false, so on a mid-turn reload it says "not running" about a turn the server knows
 *  is running, and the projection then paints a TERMINAL verdict during the one
 *  window in which nothing can know one. A parameter named for one of the two facts
 *  would be a lie about what the caller passes. */
export function projectTurns(messages: readonly Message[], live: boolean): Turn[] {
  const turns: Turn[] = [];
  let closed = false;
  for (const m of messages) {
    const open = turns[turns.length - 1];
    if (m.role === "user" || open === undefined || opensHeaderlessTurn(m, closed)) {
      turns.push({
        id: m.id,
        n: turns.length + 1,
        trigger: m.role === "user" ? m : undefined,
        body: m.role === "user" ? [] : [m],
        ts: m.ts,
        outcome: "completed",
        rewindTo: undefined,
      });
      closed = closesTurn(m.turn_outcome);
      continue;
    }
    open.body.push(m);
    closed = closed || closesTurn(m.turn_outcome);
  }
  for (let i = 0; i < turns.length; i++) {
    const t = turns[i];
    if (t === undefined) {
      continue;
    }
    t.outcome = deriveOutcome(t, live && i === turns.length - 1);
    // Rewinding from turn i means discarding turn i+1 onward, so the target is
    // the NEXT turn's trigger. The last turn gets none, which is what removes
    // its button.
    t.rewindTo = turns[i + 1]?.trigger;
  }
  return turns;
}

/** Whether an outcome value ENDS a segment. A settled outcome does; "unknown"
 *  does not — it marks a fragment whose end never arrived (a displaced turn's
 *  persist), and every transcript persisted before the internal-tool
 *  suppression carries one per fresh session, between the user's message and
 *  the real reply. Treating it as a terminator split that turn in two: the
 *  reply opened a phantom "Agent-initiated turn" and the rail counted one turn
 *  too many. A fragment JOINS the segment it interrupted; deriveOutcome lets
 *  the reply's settled outcome supersede its "unknown". */
function closesTurn(outcome: TurnOutcome | undefined): boolean {
  return outcome !== undefined && outcome !== "unknown";
}

/** Is this the first persisted message of a turn with NO user trigger?
 *
 *  The boundary rule, and reviewers caught it wrong in BOTH directions, which is
 *  why it is pinned by the shared fixture rather than described. A turn's
 *  outcome-bearing message CLOSES it (there is exactly one per turn — the message
 *  that finalized it), so after that a non-user message belongs to a turn nothing
 *  triggered.
 *
 *  Too narrow — only an empty turn's marker opens one — puts a NON-empty
 *  agent-initiated turn's outcome in the previous turn's body, which can flip a
 *  completed turn to failed on reload. Too broad — any non-user message opens one
 *  — splits a prompted empty turn that has a user message to attach to, and splits
 *  an interrupt divider off the turn it describes. Hence both clauses: after a
 *  close, and only an assistant message or an outcome marker.
 *
 *  A transcript persisted before `turn_outcome` existed carries none, so its turns
 *  never close and this projects exactly as it always did. */
function opensHeaderlessTurn(m: Message, prevClosed: boolean): boolean {
  if (!prevClosed) {
    return false;
  }
  return m.role === "assistant" || m.turn_outcome !== undefined;
}

/** A turn's outcome from its persisted body.
 *
 *  Precedence is deliberate: a terminal marker beats `running`. A turn holding
 *  a refusal or a safety block has finished badly whatever the session flag
 *  says, and the flag can legitimately still be true (the next turn's stream
 *  has already opened) — trusting it over the marker would repaint a failure
 *  as in-progress.
 *
 *  THE TAIL CLAUSE IS `unknown`, NOT `completed`, and it is the reader half of one
 *  cross-language change. The server now persists a carrier for EVERY close, so an
 *  absent carrier is a fact — nothing closed this turn — rather than a gap the
 *  reader has to guess at. Three server sites produce that shape (a prompt refused
 *  after its user row landed, a cancel during the spawn window, a process death
 *  mid-turn) and every one of them used to read as a turn that answered.
 *
 *  The predicate is "no ASSISTANT message", never "empty body". A turn persisted
 *  before the outcome field existed still holds one — the chat file only ever gained
 *  an assistant message at finalize — so a legacy transcript keeps reading
 *  `completed` instead of turning into a wall of failures. It also catches the
 *  event-only turn a mid-turn compaction leaves behind.
 *
 *  PROPOSAL, not a defect, and left open on purpose: a FAILED TOOL CALL does not
 *  fail its turn. The three sources above are a refusal, `compaction_failed` and
 *  `infra_safety_blocked`; nothing here reads tool status, so a turn whose only
 *  problem is a command that exited non-zero renders `completed`, with the failure
 *  visible only inside the expanded card. The rail marker is green.
 *
 *  Three things to weigh before changing it, and the first is a product question
 *  rather than an implementation one. An agent that runs a failing command, reads
 *  the error and succeeds on the retry has NOT failed its turn, so "any failed
 *  tool" is the wrong rule and the right one needs a definition nobody has written
 *  (the last tool? one whose failure ended the turn? a tool the agent did not
 *  follow up?). Second, the rule exists in Go AND TypeScript — the server derives
 *  it session-wide for the timeline rail, this function derives the in-flight turn
 *  no fetched summary can know — and both halves are pinned by ONE shared fixture,
 *  `internal/chat/testdata/turn_outcomes.json`, read by Go's
 *  `TestTurnOutcomeContract` (internal/chat/turns_test.go) and by
 *  `turns.node.test.ts`. So it is a cross-language contract change, landing in one
 *  commit or not at all. Third, it was found while tracing something else and is
 *  not part of any kiro-cli release, so it has never been scheduled.
 *
 *  What is NOT wrong today: a failed tool is not silent. `messages-tools.ts`
 *  auto-expands a failed card and adds an explain button, `tool-card.ts` gives it a
 *  distinct fail badge, and a tool GROUP holding a failure refuses to auto-collapse
 *  and re-opens itself if it was already shut. The gap is the turn-level summary
 *  and the rail, not the evidence. */
function deriveOutcome(t: Turn, isLive: boolean): TurnOutcome {
  let interrupted = false;
  let sawUnknown = false;
  let sawAssistant = false;
  for (const m of t.body) {
    if (m.role === "assistant") {
      sawAssistant = true;
    }
    // The DURABLE outcome, when the turn carries one: since P9 the message that
    // finalized a turn records the wire's own verdict, so a reloaded transcript
    // reads it instead of inferring one from whichever markers survived. The
    // inference below is the fallback for every turn persisted before that.
    // "unknown" is a fragment's non-verdict (see closesTurn): remembered as the
    // fallback rather than returned, because the segment usually continues into
    // the real reply, whose settled outcome is the turn's.
    if (m.turn_outcome === "unknown") {
      sawUnknown = true;
      continue;
    }
    if (m.turn_outcome !== undefined) {
      return m.turn_outcome;
    }
    if (m.refusal !== undefined) {
      return "failed";
    }
    if (m.event_kind === "compaction_failed" || m.event_kind === "infra_safety_blocked") {
      return "failed";
    }
    if (m.event_kind === "cancelled" || m.event_kind === "interrupted") {
      interrupted = true;
    }
  }
  if (interrupted) {
    return "interrupted";
  }
  if (isLive) {
    return "running";
  }
  return sawUnknown || !sawAssistant ? "unknown" : "completed";
}

/** Sum a turn's ledger across its assistant messages. */
export function turnLedger(t: Turn): TurnLedger {
  const led: TurnLedger = {
    credits: 0,
    elapsedMs: 0,
    changedFiles: {},
    commands: 0,
    reads: 0,
    models: [],
  };
  for (const m of t.body) {
    for (const tc of m.tool_calls ?? []) {
      if (COMMAND_KINDS.has(tc.kind)) {
        led.commands++;
      } else if (tc.kind === "read") {
        led.reads++;
      }
    }
    if (m.turn_credits !== undefined && m.turn_credits > 0) {
      led.credits += m.turn_credits;
    }
    if (m.turn_elapsed_ms !== undefined && m.turn_elapsed_ms > 0) {
      led.elapsedMs += m.turn_elapsed_ms;
    }
    const model = m.turn_model ?? "";
    if (model !== "" && !led.models.includes(model)) {
      led.models.push(model);
    }
    for (const [path, fc] of Object.entries(m.changed_files ?? {})) {
      led.changedFiles[path] = fc;
    }
  }
  return led;
}

/** The stable DOM id a turn permalink targets, so `/chat/{id}#turn-{n}` can
 *  address a precise point from a ledger row, a run's launch record or a search
 *  hit. Lives here rather than in the renderer because the anchor is a property
 *  of the turn, and more than one surface has to be able to compute it without
 *  reaching into the DOM.
 *
 *  THE `n` HERE IS WINDOW-LOCAL (`Turn.n`), NOT the rail's session-absolute
 *  `TurnSummary.n`. Two numbering spaces, one spelling, and conflating them is
 *  what made the rail's click land on the wrong card: the error is exactly the
 *  number of turns paged out, so it is zero on a short chat. This function cannot
 *  learn the absolute number — `projectTurns` is pure and DOM-free and the
 *  absolute index only exists behind the rail's own fetch — which is why anything
 *  addressing a turn ACROSS that boundary joins on the opening message id instead
 *  (`turn-rail.ts` `keyOf` / `turnCard`).
 *
 *  Recorded because it is a trap rather than a defect: `router.ts` parses no
 *  `#turn-` fragment at all (`parseHashLine` matches only `/^#L(\d+)/`), so the
 *  documented `/chat/{id}#turn-{n}` permalink is aspirational. If it is ever built
 *  it must resolve through the rail's index, or it inherits that exact bug. */
export function turnAnchorID(n: number): string {
  return `turn-${String(n)}`;
}

/** Whether folding this turn would HIDE anything.
 *
 *  The face shows the turn's run cards, its final top-level prose in full and a
 *  failed turn's error row; the fold hides everything else — tool cards,
 *  reasoning, delegate output, intermediate prose, plan cards and event rows. A
 *  turn with none of those (one prose answer and nothing more) folds to a face
 *  identical to its open body, so the toggle would animate and change nothing.
 *  The renderer offers no fold for such a turn instead of a control that lies. */
export function turnFoldHides(t: Turn): boolean {
  let texts = 0;
  for (const m of t.body) {
    if (m.role === "event" || (m.plan ?? []).length > 0) {
      return true;
    }
    for (const b of m.blocks ?? []) {
      if (b.type === "tool_use" || b.type === "thinking") {
        return true;
      }
      if ((b.agent_subtask_id ?? "") !== "") {
        return true;
      }
      if (b.type === "text" && (b.text ?? "").trim() !== "") {
        texts++;
      }
    }
  }
  return texts > 1;
}

/** The turn's final answer: the last non-empty TOP-LEVEL text block across the
 *  turn's messages. A collapsed turn's face renders it in full — input in the
 *  header, this in the footer. Delegate prose (a non-empty subtask id) is a
 *  delegate's report, not the turn's answer, so it never qualifies. */
export function turnFaceProse(t: Turn): string {
  for (let i = t.body.length - 1; i >= 0; i--) {
    const m = t.body[i];
    if (m?.role !== "assistant") {
      continue;
    }
    const blocks = m.blocks ?? [];
    for (let j = blocks.length - 1; j >= 0; j--) {
      const b = blocks[j];
      if (b?.type !== "text" || (b.agent_subtask_id ?? "") !== "") {
        continue;
      }
      const text = (b.text ?? "").trim();
      if (text !== "") {
        return text;
      }
    }
  }
  return "";
}

/** What a turn that did not end cleanly SAYS, in the words of whoever knows the
 *  cause. "" for a clean or running turn, so a caller can read the empty string as
 *  "there is nothing to report" without asking the severity again.
 *
 *  THIS FUNCTION IS THE FIX FOR A TURN THAT FAILED AND SAID NOTHING. Its
 *  predecessor read the newest `event` row's text and nothing else, so a turn that
 *  failed on the wire's own `turn_end` had no source at all: `closeWithOutcome`
 *  wrote no prose and, when the turn had streamed something, wrote no event message
 *  either. Measured on a live chat file: 26 blocks, a settled `failed` outcome,
 *  three changed files — and no sentence anywhere in the record. The reader's only
 *  account of it was a 12-second toast.
 *
 *  THIS NOTICE OWNS THE PROSE, and the body divider that used to repeat it does
 *  not. One rule, stated here and at `messages-events.ts`'s `interrupted` entry:
 *  the card-level notice is the account of WHY, a divider marks the BOUNDARY and
 *  names its kind. The notice wins because it is present in BOTH fold states,
 *  which is the same argument that moved it out of the collapsed face.
 *
 *  Three sources, in falling order of specificity, and each one exists because the
 *  one above it is legitimately absent for some path:
 *
 *   1. The newest `interrupted` event row's content. This is the prompt-failure and
 *      bridge-death path, where that row carries the upstream sentence, and it is
 *      the most specific text available.
 *   2. The carrier's own `turn_failure_reason`. The server stamps it beside
 *      `turn_outcome` on the ONE message that finalized the turn, so it is there
 *      whether or not a divider was written — which is what closes source 1's gap.
 *   3. The outcome's default sentence. For every turn already on disk, which
 *      carries neither: the population symptom 1 was reported against.
 *
 *  SOURCE 1 IS SCOPED TO `event_kind === "interrupted"`, and the scope is exact
 *  rather than a heuristic: that is the only kind whose content is AUTHORED as the
 *  turn's stop account (`closeAsInterrupted` passes `reason` as the row's content,
 *  and `internal/command/prompt.go`'s respawn-failure site appends the same kind).
 *  Unscoped, "the newest event row's content" reached five other kinds that persist
 *  content which is NOT an account of the turn's stop, and each one is a distinct
 *  wrong answer: `compaction_failed` and `infra_safety_blocked` make the notice
 *  repeat their own divider's prose (item 7's defect again, one kind over),
 *  `step_notice` attributes a workflow step's message to the turn's failure,
 *  `model_switched` renders as a bare model id, and `compacted` renders THE WHOLE
 *  CONVERSATION SUMMARY as a failure reason. (`cancelled` and `turn_outcome`
 *  persist "" and were already skipped by the trim check.)
 *
 *  Nothing is lost for the population source 1 exists for. A `compaction_failed`
 *  turn now falls through to source 2 (absent — that outcome is a client-side
 *  inference with no carrier) and then to source 3, so the notice reads the
 *  outcome's default sentence while the divider keeps the specific compaction
 *  reason. Honest, and non-duplicating.
 *
 *  The event-kind humanisation the old version fell back to is gone with the need
 *  for it: `defaultFailureReason` says the same thing in a sentence rather than
 *  title-casing an enum member at a reader. */
export function turnFailureText(t: Turn): string {
  const severity = severityOf(t.outcome);
  if (severity === "clean" || severity === "running") {
    return "";
  }
  for (let i = t.body.length - 1; i >= 0; i--) {
    const m = t.body[i];
    if (m?.role !== "event" || m.event_kind !== "interrupted") {
      continue;
    }
    const text = (m.content ?? "").trim();
    if (text !== "") {
      return text;
    }
  }
  for (let i = t.body.length - 1; i >= 0; i--) {
    const reason = (t.body[i]?.turn_failure_reason ?? "").trim();
    if (reason !== "") {
      return reason;
    }
  }
  return defaultFailureReason(t.outcome);
}
