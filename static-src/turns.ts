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

/** A turn's result, as scannable colour down the transcript.
 *
 *  Derived from PERSISTED data only. `stop_reason` rides the live `turn_ended`
 *  SSE and is never stored, so a reloaded transcript cannot read it — the
 *  event messages and the refusal marker are what survive, and they are what
 *  this keys on. */
export type TurnOutcome = "running" | "completed" | "interrupted" | "failed";

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
  for (const m of messages) {
    const open = turns[turns.length - 1];
    if (m.role === "user" || open === undefined) {
      turns.push({
        id: m.id,
        n: turns.length + 1,
        trigger: m.role === "user" ? m : undefined,
        body: m.role === "user" ? [] : [m],
        ts: m.ts,
        outcome: "completed",
      });
      continue;
    }
    open.body.push(m);
  }
  for (let i = 0; i < turns.length; i++) {
    const t = turns[i];
    if (t === undefined) {
      continue;
    }
    t.outcome = deriveOutcome(t, thinking && i === turns.length - 1);
  }
  return turns;
}

/** A turn's outcome from its persisted body.
 *
 *  Precedence is deliberate: a terminal marker beats `running`. A turn holding
 *  a refusal or a safety block has finished badly whatever the session flag
 *  says, and the flag can legitimately still be true (the next turn's stream
 *  has already opened) — trusting it over the marker would repaint a failure
 *  as in-progress. */
function deriveOutcome(t: Turn, isLive: boolean): TurnOutcome {
  let interrupted = false;
  for (const m of t.body) {
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
  return isLive ? "running" : "completed";
}

/** Sum a turn's ledger across its assistant messages. */
export function turnLedger(t: Turn): TurnLedger {
  const led: TurnLedger = {
    credits: 0,
    elapsedMs: 0,
    changedFiles: {},
    commands: 0,
    reads: 0,
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
    for (const [path, fc] of Object.entries(m.changed_files ?? {})) {
      led.changedFiles[path] = fc;
    }
  }
  return led;
}

/** What a FOLDED turn shows on its right, in priority order.
 *
 *  The row has to earn the fold, so it shows the most concrete thing the turn
 *  actually produced:
 *
 *    1. Changed filenames. If a turn touched files, that is what it was about.
 *    2. Otherwise the first sentence of the reply — cheap, honest, and for a
 *       conversational turn it IS the answer.
 *    3. Otherwise nothing, and the footer's own cost/time line carries the row.
 *       A turn that produced no files and no prose is a turn whose only fact is
 *       that it happened.
 *
 *  NO generated summaries, and that was decided on evidence rather than cost:
 *  the ONNX runtime bundled in kiro-cli is `all-MiniLM-L6-v2`, an EMBEDDING
 *  model, and `generateSummary` / `generateTitle` / `localModel` return zero hits
 *  across the whole installed surface. There is no local generative model to
 *  call. A remote call per turn would spend money and latency on a sentence the
 *  transcript already contains.
 */
export function turnFoldSummary(t: Turn): string {
  const led = turnLedger(t);
  const paths = Object.keys(led.changedFiles);
  if (paths.length > 0) {
    const names = paths.map((p) => p.split("/").pop() ?? p).sort((a, b) => a.localeCompare(b));
    const shown = names.slice(0, 2).join(", ");
    return names.length > 2 ? `${shown} +${String(names.length - 2)} more` : shown;
  }
  return firstSentence(t);
}

/** The reply's first sentence, truncated. Reads the turn's text content, not its
 *  tool output — the answer is prose, and a tool's stdout is not a summary. */
function firstSentence(t: Turn, max = 80): string {
  for (const m of t.body) {
    const text = (m.content ?? "").trim();
    if (text === "") {
      continue;
    }
    const flat = text.replace(/\s+/g, " ");
    // A sentence end, or the whole thing if it has none.
    const stop = /[.!?](\s|$)/.exec(flat);
    const sentence = stop === null ? flat : flat.slice(0, stop.index + 1);
    return sentence.length > max ? sentence.slice(0, max) + "\u2026" : sentence;
  }
  return "";
}

/** The stable DOM id a turn permalink targets, so `/chat/{id}#turn-{n}` can
 *  address a precise point from a ledger row, a run's launch record or a search
 *  hit. Lives here rather than in the renderer because the anchor is a property
 *  of the turn, and more than one surface has to be able to compute it without
 *  reaching into the DOM. */
export function turnAnchorID(n: number): string {
  return `turn-${String(n)}`;
}
