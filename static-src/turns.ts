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

import { parseStepSubtask } from "./step-subtask.js";
import { isSubagentInvocation } from "./tool-schema.js";
import { severityOf, defaultFailureReason } from "./turn-severity.js";
import type { Message, FileChange, ToolKind } from "./types.js";
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

/** The segmentation state at a window's LEFT EDGE, which is what lets a projection
 *  over a PAGINATED window number its turns the way a whole-session scan would.
 *  Both halves come from the window response; `store.ts` `turnBaseOf` reads them.
 *  `closed` is not redundant with `offset`: this scan carries no other state, so a
 *  window opening on a message that CONTINUES a closed segment diverges one later. */
export interface TurnWindowBase {
  /** Turns preceding the window's FIRST turn, so `offset + 1` is that turn's
   *  session-absolute ordinal. */
  readonly offset: number;
  /** Whether the segment before the window's first message had already closed. */
  readonly closed: boolean;
}

/** The base for a projection over a whole session: nothing precedes it and no
 *  segment closed before it. The default, so every caller that genuinely holds the
 *  whole array — and every caller that reads only ids — needs no argument. */
export const WHOLE_SESSION: TurnWindowBase = { offset: 0, closed: false };

export interface Turn {
  /** Reconcile key: the id of the turn's first message. Stable across
   *  repaints because a turn's opening message never changes identity. */
  id: string;
  /** The turn's 1-based ordinal: SESSION-ABSOLUTE when a base is supplied, which
   *  every paint-path caller does, and window-local under `WHOLE_SESSION`, which is
   *  correct for a caller holding the whole array or reading only ids. The base is
   *  what makes this agree with `TurnSummary.n` by construction. */
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
  /** Time the turn spent INSIDE tool calls: Σ `ToolCall.duration_ms`.
   *
   *  ZERO MEANS NOBODY STAMPED ONE, not that the tools were instant — the same
   *  absence rule `elapsedMs` follows, and the reason a reader must never be shown
   *  "0.0s of tool time". `duration_ms` is optional on the wire and absent on every
   *  call a settle never reached, so a turn can hold ten calls and report nothing.
   *
   *  It is also NOT bounded by `elapsedMs`: tool calls overlap, so the sum can
   *  exceed the turn's wall clock, and a consumer deriving model time from the
   *  difference must withhold rather than render a negative. */
  toolMs: number;
  /** How many calls of each kind the turn made.
   *
   *  PARTIAL rather than total over `ToolKind`: a kind with no calls has NO ENTRY,
   *  which is a different statement from an entry reading zero. A total record would
   *  put fifteen zeros in front of a reader for every turn that ran one command. */
  kindCounts: Partial<Record<ToolKind, number>>;
  /** Delegates the turn dispatched, and Σ their `duration_ms`.
   *
   *  Counted by `isSubagentInvocation`, so it counts the calls that OPEN a delegate
   *  and not the nested calls the delegate then made. `delegateMs` follows `toolMs`'
   *  absence rule for the same reason, and independently: a turn can dispatch three
   *  delegates and report zero milliseconds. */
  delegateCount: number;
  delegateMs: number;
  /** When the turn began and when its last message landed.
   *
   *  `startedAt` is `Turn.ts` — the trigger's timestamp, else the first body
   *  message's. `endedAt` is the LAST body message's `ts`, and 0 when the body is
   *  empty, which is a turn that opened and persisted nothing.
   *
   *  `endedAt` IS NOT `startedAt + elapsedMs` and must not be presented as derived
   *  from it: `turn_elapsed_ms` is the agent's own measured duration and excludes
   *  admission wait, while these two are wall-clock stamps. Nothing on the wire
   *  carries a turn end time, so this is the closest honest answer. */
  startedAt: number;
  endedAt: number;
  /** The wire's stop reason verbatim, and whether the model stopped at a bound.
   *
   *  Both read off the ONE message per turn the server stamps `turn_outcome` on, so
   *  they agree with the outcome rather than being scavenged separately. "" and
   *  `false` mean NOTHING STAMPED THEM — every turn persisted before the carrier
   *  existed, and every turn whose close never ran — never "the turn stopped for no
   *  reason" and never "the answer is known to be complete".
   *
   *  A `string`, not `StopReason`, because the enum is OPEN upstream: the field's own
   *  wire comment says no consumer may branch on it, so a reader renders it and
   *  `outcome` is what it decides on. */
  stopReasonRaw: string;
  truncated: boolean;
}

/** Tool kinds that mean "a command ran". `execute` and `shell` are the two KAS
 *  actually emits for a shell invocation; `command` is in the wire enum and is
 *  counted for completeness rather than because it has been observed. */
const COMMAND_KINDS = new Set(["execute", "shell", "command"]);

/** Group a flat message list into turns. A user PROMPT opens a turn; everything else
 *  joins the open one, or opens a HEADERLESS turn — the agent-initiated case and a
 *  paginated window starting mid-turn. `base` is that window's left edge (see
 *  `TurnWindowBase`), so a PAGE numbers its turns session-absolutely.
 *
 *  `live` marks the LAST turn as running, composed by the caller from this client's
 *  memory of a stream it watched PLUS the server's `turn_open` (`store.ts` `turnLive`):
 *  `thinking` alone starts false, so a mid-turn reload would paint a terminal verdict. */
export function projectTurns(
  messages: readonly Message[],
  live: boolean,
  base: TurnWindowBase = WHOLE_SESSION,
): Turn[] {
  const turns: Turn[] = [];
  let closed = base.closed;
  for (const m of messages) {
    if (carriesNothing(m)) {
      continue;
    }
    const open = turns[turns.length - 1];
    // A prompt opens a turn; a steer joins the one already running.
    const opens = isPrompt(m) || opensHeaderlessTurn(m, closed);
    if (opens || open === undefined) {
      turns.push({
        id: m.id,
        n: base.offset + turns.length + 1,
        trigger: isPrompt(m) ? m : undefined,
        body: isPrompt(m) ? [] : [m],
        ts: m.ts,
        outcome: "completed",
        rewindTo: undefined,
      });
      // The false arm is the FORCED-OPEN first message of a mid-turn window: it did
      // not close the segment it continues, so the base's state carries through it
      // and the next assistant message opens a turn here exactly as it does in the
      // whole-array scan. On a genuine open the segment starts fresh, and with
      // `WHOLE_SESSION`'s `closed: false` seed the two arms agree, which is what
      // keeps the default behaviour byte-identical.
      closed = opens ? closesTurn(m.turn_outcome) : closed || closesTurn(m.turn_outcome);
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

/** Whether this is a user PROMPT rather than a steer. The Go twin is
 *  `internal/chat/turns.go` `isPrompt`. */
function isPrompt(m: Message): boolean {
  return m.role === "user" && m.user_kind !== "steer";
}

/** Whether every one of this message's blocks is workflow-step content. */
function isStepMessage(m: Message): boolean {
  const blocks = m.blocks ?? [];
  // "every block parses" is vacuously true of a message with NO blocks, so without
  // this an empty assistant or event message would lose the turn it opens.
  if (blocks.length === 0) {
    return false;
  }
  return blocks.every((b) => parseStepSubtask(b.agent_subtask_id ?? "") !== null);
}

/** Whether nothing about this assistant message reaches the transcript yet. Such a
 *  message neither opens a turn nor JOINS one: joining would set `deriveOutcome`'s
 *  `sawAssistant` and flip a carrier-less turn from `unknown` to `completed`.
 *  Assistant-only, because an `event` row renders a badge and may carry the turn's
 *  outcome, and a `user` row is a trigger. */
function carriesNothing(m: Message): boolean {
  return (
    m.role === "assistant" &&
    (m.content ?? "") === "" &&
    (m.reasoning ?? "") === "" &&
    (m.blocks ?? []).length === 0 &&
    (m.tool_calls ?? []).length === 0 &&
    (m.plan ?? []).length === 0 &&
    m.refusal === undefined &&
    m.turn_outcome === undefined &&
    m.event_kind === undefined
  );
}

/** Is this the first persisted message of a turn with NO user trigger? All three clauses
 *  are load-bearing; the shared fixture's `_segmentation_comment` owns the reasoning and
 *  `closesTurn` the fragment carve-out. */
function opensHeaderlessTurn(m: Message, prevClosed: boolean): boolean {
  if (!prevClosed || isStepMessage(m)) {
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
  let cancelled = false;
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
    if (m.event_kind === "interrupted") {
      interrupted = true;
    }
    if (m.event_kind === "cancelled") {
      cancelled = true;
    }
  }
  // A fault outranks a gesture: a turn carrying both markers is one something
  // broke, and grading it `cancelled` would paint a fault as a stop the reader
  // asked for.
  if (interrupted) {
    return "interrupted";
  }
  if (cancelled) {
    return "cancelled";
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
    toolMs: 0,
    kindCounts: {},
    delegateCount: 0,
    delegateMs: 0,
    startedAt: t.ts,
    endedAt: t.body[t.body.length - 1]?.ts ?? 0,
    stopReasonRaw: "",
    truncated: false,
  };
  // A carrier whose outcome is `unknown` is a FRAGMENT's non-verdict (see
  // closesTurn), so its diagnostics are provisional exactly as its outcome is: the
  // first settled carrier supersedes it. Same precedence as deriveOutcome, or the
  // panel would report a fragment's stop reason beside the reply's outcome.
  let settledCarrier = false;
  for (const m of t.body) {
    for (const tc of m.tool_calls ?? []) {
      if (COMMAND_KINDS.has(tc.kind)) {
        led.commands++;
      } else if (tc.kind === "read") {
        led.reads++;
      }
      led.kindCounts[tc.kind] = (led.kindCounts[tc.kind] ?? 0) + 1;
      const ms = tc.duration_ms ?? 0;
      led.toolMs += ms;
      if (isSubagentInvocation(tc)) {
        led.delegateCount++;
        led.delegateMs += ms;
      }
    }
    if (m.turn_outcome !== undefined && !settledCarrier) {
      led.stopReasonRaw = m.turn_stop_reason_raw ?? "";
      led.truncated = m.turn_truncated ?? false;
      settledCarrier = m.turn_outcome !== "unknown";
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

/** The stable DOM id a turn anchor targets. Lives here rather than in the renderer
 *  because the anchor is a property of the turn, and more than one surface computes
 *  it without reaching into the DOM.
 *
 *  A genuine SESSION anchor wherever a base is supplied, so it agrees with the
 *  rail's `TurnSummary.n`. Still not a working PERMALINK: `router.ts parseHashLine`
 *  matches only `/^#L(\d+)/`, so nothing resolves a `#turn-` fragment. */
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
 *  A CANCELLED TURN HAS NO ACCOUNT TO GIVE, so it is refused ahead of all three
 *  sources: the reader caused the stop, and the footer's own outcome word reads
 *  "Cancelled" a row away, so a notice would render one fact twice. That refusal
 *  also silences the sentence already persisted on every cancelled turn written
 *  before `DefaultFailureReason` stopped supplying one.
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
  // A CANCEL SAYS NOTHING, and the test is the OUTCOME rather than the severity:
  // `severityOf` grades `cancelled` and `unknown` alike, and `unknown` must keep
  // speaking. Ahead of the three sources on purpose — this also silences the
  // sentence already persisted on every cancelled turn written before this.
  if (t.outcome === "cancelled") {
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
