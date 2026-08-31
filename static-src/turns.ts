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
 *  `thinking` marks the LAST turn as running. It is the session's flag rather
 *  than anything on the message, because a turn in flight is only ever the
 *  last one. */
export function projectTurns(messages: readonly Message[], thinking: boolean): Turn[] {
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
    t.outcome = deriveOutcome(t, thinking && i === turns.length - 1);
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
  for (const m of t.body) {
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
  return sawUnknown ? "unknown" : "completed";
}

/** Every workflow run a turn launched, from its body's tool calls.
 *
 *  A launch is the only tool call carrying a `workflow_id` (the server decodes it
 *  off the invocation's `rawOutput`), so this is exact rather than a heuristic. Its
 *  one consumer is the fold rule: a turn holding a run that is still going must not
 *  fold, and the turn's own outcome cannot answer that because `run_workflow`
 *  returns as soon as the run is created, so the turn completes long before the run
 *  does. Returns the ids rather than a boolean because only the caller can say
 *  whether a run is still live — that answer lives in `run-store.ts`, which this
 *  DOM-free projection must not import. */
export function turnRunIDs(t: Turn): string[] {
  const out: string[] = [];
  for (const m of t.body) {
    for (const tc of m.tool_calls ?? []) {
      const id = tc.workflow_id ?? "";
      if (id !== "" && !out.includes(id)) {
        out.push(id);
      }
    }
  }
  return out;
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
 *  reaching into the DOM. */
export function turnAnchorID(n: number): string {
  return `turn-${String(n)}`;
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

/** What a collapsed FAILED or INTERRUPTED turn shows as its output: the last
 *  event row's text — the error is the turn's real result. Falls back to the
 *  event kind's own words when the row carries no prose, and to "" for a turn
 *  that ended cleanly (the face shows the answer instead). */
export function turnFaceError(t: Turn): string {
  const o = t.outcome;
  if (o !== "failed" && o !== "interrupted" && o !== "cancelled" && o !== "refused") {
    return "";
  }
  for (let i = t.body.length - 1; i >= 0; i--) {
    const m = t.body[i];
    if (m?.role !== "event") {
      continue;
    }
    const text = (m.content ?? "").trim();
    if (text !== "") {
      return text;
    }
    const kind = (m.event_kind ?? "").replaceAll("_", " ");
    if (kind !== "") {
      return kind.charAt(0).toUpperCase() + kind.slice(1);
    }
  }
  return "";
}
