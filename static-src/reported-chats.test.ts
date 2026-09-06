// The two REPORTED chats, replayed through the real projection.
//
// `testdata/reported-chats.json` is copied from the live `/config/chats/` records
// the bug report named, reduced to the fields the four surfaces read: roles, ids,
// timestamps, event kinds, the durable outcome and its raw stop reason, the event
// rows' prose verbatim, and counts standing in for blocks and tool calls. User and
// assistant prose is redacted; nothing else is altered, and no field is invented.
//
// It is a FIXTURE rather than a live read on purpose: a test that reads `/config`
// passes or fails on whichever conversation the operator had open. Both records
// have in fact grown since the report — each carries a third turn now — and the
// fixture captured that, which is why it covers four outcomes across two real
// transcripts rather than the two the report described.
//
// What this file asserts is the JOIN. `turn-severity.node.test.ts` pins the table,
// `turns.node.test.ts` pins the text lookup, `tab-dot.test.ts` pins the dot and
// `fold-state.test.ts` pins the fold — and none of them can see whether a REAL
// transcript projects into turns those rules then agree about. Both symptoms were
// exactly that kind of disagreement.

import { describe, it, expect, beforeEach } from "vitest";

import fixtureRaw from "./testdata/reported-chats.json?raw";
import { projectTurns, turnFailureText, type Turn } from "./turns.js";
import { severityOf, defaultFailureReason } from "./turn-severity.js";
import { outcomeLatch } from "./store.js";
import { isTurnOpen, _resetFoldStateForTest } from "./fold-state.js";
import type { Message } from "./types.js";
import type { TurnOutcome } from "./wire/types.gen.js";

interface FixtureMessage {
  readonly id: string;
  readonly role: string;
  readonly ts: number;
  readonly content?: string;
  readonly event_kind?: string;
  readonly turn_outcome?: string;
  readonly turn_stop_reason_raw?: string;
  readonly turn_failure_reason?: string;
  readonly block_count?: number;
  readonly tool_call_count?: number;
}

interface FixtureChat {
  readonly chat_id: string;
  readonly messages: readonly FixtureMessage[];
}

const fixture = JSON.parse(fixtureRaw) as Record<string, FixtureChat>;

/** Rehydrate one fixture row into the `Message` the store would hold.
 *
 *  `block_count` and `tool_call_count` become that many blocks and tool calls,
 *  because the fold and face rules read the SHAPE of a turn's body — a turn with 26
 *  blocks and 17 tool calls hides a great deal when folded, and a fixture that
 *  dropped them would make every turn look like a one-line answer. */
function rehydrate(m: FixtureMessage): Message {
  const blocks = Array.from({ length: m.block_count ?? 0 }, (_, i) =>
    i === 0 && m.content !== undefined && m.content !== ""
      ? { type: "text", text: m.content }
      : { type: "tool_use", tool_call_id: `tc-${m.id}-${String(i)}` },
  );
  const toolCalls = Array.from({ length: m.tool_call_count ?? 0 }, (_, i) => ({
    id: `tc-${m.id}-${String(i)}`,
    title: "Work",
    kind: "other",
    status: "completed",
  }));
  const out: Record<string, unknown> = { id: m.id, role: m.role, ts: m.ts };
  if (m.content !== undefined) {
    out["content"] = m.content;
  }
  if (m.event_kind !== undefined) {
    out["event_kind"] = m.event_kind;
  }
  if (m.turn_outcome !== undefined) {
    out["turn_outcome"] = m.turn_outcome;
  }
  if (m.turn_stop_reason_raw !== undefined) {
    out["turn_stop_reason_raw"] = m.turn_stop_reason_raw;
  }
  if (m.turn_failure_reason !== undefined) {
    out["turn_failure_reason"] = m.turn_failure_reason;
  }
  if (blocks.length > 0) {
    out["blocks"] = blocks;
  }
  if (toolCalls.length > 0) {
    out["tool_calls"] = toolCalls;
  }
  return out as unknown as Message;
}

function turnsOf(name: string): Turn[] {
  const chat = fixture[name];
  if (chat === undefined) {
    throw new Error(`the fixture has no chat named ${name}`);
  }
  // `thinking: false` — both records are settled conversations read off disk, which
  // is the state a reload projects them in and the state both symptoms were seen in.
  return projectTurns(chat.messages.map(rehydrate), false);
}

beforeEach(() => {
  localStorage.clear();
  _resetFoldStateForTest();
});

describe("the reported chats project into the turns the report described", () => {
  it("reads chat 1 as failed, failed, completed", () => {
    expect(turnsOf("reported-failed-no-reason").map((t) => t.outcome)).toEqual([
      "failed",
      "failed",
      "completed",
    ]);
  });

  it("reads chat 2 as interrupted, cancelled", () => {
    // Two turns, not three: the `interrupted` divider stays with the turn it
    // describes rather than opening one of its own, which is the boundary rule the
    // shared outcome fixture pins.
    expect(turnsOf("reported-interrupted-hollow-dot").map((t) => t.outcome)).toEqual([
      "interrupted",
      "cancelled",
    ]);
  });
});

describe("symptom 1: every failed turn now SAYS something", () => {
  it("gives chat 1 turn 1 a reason, where the record holds none at all", () => {
    // THE REPORTED TURN. On disk: a settled `failed`, 26 blocks, 17 tool calls, 3
    // changed files, and no prose anywhere — no text block, no event row, no
    // `turn_failure_reason` (that field did not exist when this was written). Its
    // card rendered a red footer mark over an empty body, and the only account of
    // the failure was a transient toast.
    const turn = turnsOf("reported-failed-no-reason")[0];
    expect(turn?.outcome).toBe("failed");
    expect(turn === undefined ? "" : turnFailureText(turn)).toBe(
      "The agent reported an error and the turn stopped.",
    );
  });

  it("gives chat 1 turn 2 a reason, where the only trace is a skipped marker", () => {
    // The second reported turn: an empty turn whose sole persisted row is a
    // `turn_outcome` event with empty content, which EVENT_RENDER_MAP skips — so
    // the body rendered literally nothing.
    const turn = turnsOf("reported-failed-no-reason")[1];
    expect(turn?.outcome).toBe("failed");
    expect(turn === undefined ? "" : turnFailureText(turn)).not.toBe("");
  });

  it("keeps chat 2's own upstream sentence rather than replacing it", () => {
    // The one reason that WAS durable, and the fallback must not outrank it: this
    // chat's interrupted divider carries KAS's own text, which is more specific
    // than anything vibekit can say.
    const turn = turnsOf("reported-interrupted-hollow-dot")[0];
    expect(turn === undefined ? "" : turnFailureText(turn)).toBe(
      "A network error occurred. Please check your connection and try again.",
    );
  });

  it("says nothing for the turn that ended cleanly", () => {
    const turn = turnsOf("reported-failed-no-reason")[2];
    expect(turn?.outcome).toBe("completed");
    expect(turn === undefined ? "x" : turnFailureText(turn)).toBe("");
  });
});

describe("symptom 2: the tab dot", () => {
  it("latches chat 2's interrupted turn as a FAILURE, not as nothing", () => {
    // THE REPORTED DEFECT. The newest outcome in this record was `interrupted`,
    // every latch writer mapped it to nothing, and `tabStatusFor` fell through to
    // `idle` — which 12-tabs.css paints as a transparent disc with a hairline ring.
    // The user saw that empty circle beside a chat carrying a clear inline error.
    const turns = turnsOf("reported-interrupted-hollow-dot");
    expect(outcomeLatch(turns[0]?.outcome)).toBe("failed");
  });

  it("latches chat 1's failed turns as failures and its clean turn as done", () => {
    const [a, b, c] = turnsOf("reported-failed-no-reason");
    expect([outcomeLatch(a?.outcome), outcomeLatch(b?.outcome), outcomeLatch(c?.outcome)]).toEqual([
      "failed",
      "failed",
      "done",
    ]);
  });

  it("latches chat 2's cancelled turn as DONE, not as a failure and not as nothing", () => {
    // The control, and it has to say two things now. A cancel the user asked for is
    // not a FAILURE — that is why the interrupted turn above had to be tested on its
    // own outcome rather than on the chat's. But it is not `""` either: the hollow
    // ring means the chat has not initiated (user ruling, 2026-09-04), and this chat
    // ran two turns. `done` is the transport's "a turn finished here".
    const turns = turnsOf("reported-interrupted-hollow-dot");
    expect(outcomeLatch(turns[1]?.outcome)).toBe("done");
  });
});

describe("the four surfaces agree, per turn, across both records", () => {
  it("never shows a failure a reader could mistake for nothing happening", () => {
    // The join, stated as one property over every turn in both real transcripts:
    // whenever the severity is `broken`, ALL FOUR surfaces must say so — the turn
    // has text to show, it refuses to auto-fold, and the tab latches a failure. A
    // surface disagreeing with the other three is what both symptoms were.
    for (const name of Object.keys(fixture)) {
      const turns = turnsOf(name);
      for (const [i, t] of turns.entries()) {
        const severity = severityOf(t.outcome);
        const where = `${name} turn ${String(i + 1)} (${t.outcome})`;
        if (severity !== "broken") {
          continue;
        }
        expect(turnFailureText(t), `${where}: has inline text`).not.toBe("");
        expect(outcomeLatch(t.outcome), `${where}: latches a failure`).toBe("failed");
        expect(isTurnOpen("c1", t, i, turns.length), `${where}: does not auto-fold`).toBe(true);
      }
    }
  });

  it("does not overstate the turns that merely stopped, and does not erase them either", () => {
    // The other direction, and it is what keeps the property above from being
    // satisfiable by painting everything red: a `cancelled` or `unknown` turn is
    // never latched as a FAILURE, folds like any other, and still has something to
    // say. What it may not do is latch nothing — the hollow ring means the chat has
    // not initiated (user ruling, 2026-09-04), so a turn that ended has to register
    // as one whatever became of it.
    for (const name of Object.keys(fixture)) {
      const turns = turnsOf(name);
      for (const [i, t] of turns.entries()) {
        if (severityOf(t.outcome) !== "stopped") {
          continue;
        }
        const where = `${name} turn ${String(i + 1)} (${t.outcome})`;
        expect(outcomeLatch(t.outcome), `${where}: not a failure`).not.toBe("failed");
        expect(outcomeLatch(t.outcome), `${where}: not the hollow ring either`).toBe("done");
        expect(turnFailureText(t), `${where}: still says something`).not.toBe("");
      }
    }
  });

  it("covers at least one broken and one stopped turn, or the two above are vacuous", () => {
    // Both properties are `for` loops with a `continue`, so an empty match set
    // passes them. This is the guard that makes them mean something, and it is what
    // would fail if the fixture were ever reduced to clean turns.
    const severities = Object.keys(fixture)
      .flatMap((name) => turnsOf(name))
      .map((t) => severityOf(t.outcome));
    expect(severities).toContain("broken");
    expect(severities).toContain("stopped");
    expect(severities).toContain("clean");
  });
});

// ---------------------------------------------------------------------------
// The four later items, over the same two real records.
//
// Each of these is a rule three unit suites already pin in isolation; what none of
// them can see is whether a REAL transcript projects into turns those rules then
// agree about, which is the only thing this file exists for.
// ---------------------------------------------------------------------------

describe("a turn NOTHING closed reads unknown and still says something", () => {
  /** Chat 1's own first turn with its carrier removed — the shape the three server
   *  sites produce (a prompt refused after its user row landed, a cancel during the
   *  spawn window, a process death mid-turn): the user message persisted and nothing
   *  else. Derived from the real record rather than authored, so the ids, roles and
   *  timestamps are the ones a live chat file carries. */
  function carrierlessFirstTurn(name: string): Turn {
    const chat = fixture[name];
    if (chat === undefined) {
      throw new Error(`the fixture has no chat named ${name}`);
    }
    const first = chat.messages[0];
    if (first?.role !== "user") {
      throw new Error(`${name} does not open on a user message, so this shape is not derivable`);
    }
    const turns = projectTurns([rehydrate(first)], false);
    const turn = turns[0];
    if (turns.length !== 1 || turn === undefined) {
      throw new Error(`projected ${String(turns.length)} turns, want 1`);
    }
    return turn;
  }

  it("reads unknown rather than completed", () => {
    // The whole of item 2, on a real record: `completed` reported a turn nobody
    // closed as one that answered.
    expect(carrierlessFirstTurn("reported-failed-no-reason").outcome).toBe("unknown");
  });

  it("carries inline text, so the turn is not a mark over an empty body", () => {
    expect(turnFailureText(carrierlessFirstTurn("reported-failed-no-reason"))).toBe(
      "The turn ended for a reason vibekit could not read.",
    );
  });

  it("latches DONE rather than a failure or the hollow ring", () => {
    // `unknown` is graded `stopped`, so it must not paint red — the wire never
    // reported a failure — and must not paint the hollow ring either, because the
    // chat did run a turn.
    expect(outcomeLatch(carrierlessFirstTurn("reported-failed-no-reason").outcome)).toBe("done");
  });

  it("does not read the same for a turn that DID answer", () => {
    // The carve-out that stops the clause widening: chat 1's real first turn holds an
    // assistant message, so it keeps its own settled outcome. Without this the case
    // above would pass for a rule that graded every turn `unknown`.
    expect(turnsOf("reported-failed-no-reason")[0]?.outcome).toBe("failed");
  });
});

describe("a DISCARDED turn is a stop, not a failure and not a silence", () => {
  it("latches done and is never broken", () => {
    // Item 1's direction, kept as its own case rather than left to the stopped
    // property below: the model-switch closer used to conclude `interrupted`, which
    // grades BROKEN, so the tab dot went red for a switch the reader asked for.
    const cancelled = turnsOf("reported-interrupted-hollow-dot")[1];
    expect(cancelled?.outcome).toBe("cancelled");
    expect(severityOf(cancelled?.outcome)).not.toBe("broken");
    expect(outcomeLatch(cancelled?.outcome)).toBe("done");
  });
});

describe("the push gate has words for every turn it speaks for", () => {
  it("never has to push an empty sentence", () => {
    // Item 4's property, and the reason it belongs here rather than in the handler's
    // own suite: both push gates build their body from `defaultFailureReason`, so a
    // `broken` or `stopped` outcome with no sentence behind it would notify a reader
    // with nothing at all — a worse failure than the "Agent finished" lie it replaced.
    for (const name of Object.keys(fixture)) {
      for (const [i, t] of turnsOf(name).entries()) {
        const severity = severityOf(t.outcome);
        if (severity !== "broken" && severity !== "stopped") {
          continue;
        }
        expect(
          defaultFailureReason(t.outcome),
          `${name} turn ${String(i + 1)} (${t.outcome}) has no sentence to push`,
        ).not.toBe("");
      }
    }
  });

  it("says nothing for a turn that ended cleanly", () => {
    // The other direction: "" is how both gates spell "notify nothing", so a clean
    // turn gaining a sentence would make the empty string stop meaning that.
    const clean = turnsOf("reported-failed-no-reason")[2];
    expect(clean?.outcome).toBe("completed");
    expect(defaultFailureReason(clean?.outcome)).toBe("");
  });
});

describe("the fixture is the real record, not a hand-written one", () => {
  it("carries both reported chat ids", () => {
    expect(fixture["reported-failed-no-reason"]?.chat_id).toBe(
      "c-c4b7910007414d0e0759fc263cbb2146",
    );
    expect(fixture["reported-interrupted-hollow-dot"]?.chat_id).toBe(
      "c-a7f83c9ff14d180d6cf44e9caea44f45",
    );
  });

  it("still holds the two properties that made the report reproducible", () => {
    // If either of these stops being true the fixture has been edited into
    // something that no longer reproduces the bug, and every assertion above is
    // measuring a different transcript.
    const chat1 = fixture["reported-failed-no-reason"]?.messages ?? [];
    const failed = chat1.find((m) => m.turn_outcome === "failed" && m.role === "assistant");
    expect(failed, "chat 1 has a failed assistant carrier").toBeDefined();
    expect(failed?.turn_failure_reason, "and it records NO reason").toBeUndefined();
    expect(failed?.content, "and no prose either").toBeUndefined();

    const chat2 = fixture["reported-interrupted-hollow-dot"]?.messages ?? [];
    const interrupted = chat2.find((m) => m.turn_outcome === "interrupted");
    expect(interrupted, "chat 2 has an interrupted carrier").toBeDefined();
    expect(
      chat2.find((m) => m.event_kind === "interrupted")?.content,
      "and its reason is on the divider, durably",
    ).toContain("network error");
  });
});

/** Type-only: the fixture's outcome strings must all be real `TurnOutcome`s. A
 *  compile-time assertion rather than a runtime one — if the fixture ever carried a
 *  value the wire cannot send, the casts in `rehydrate` would hide it at runtime
 *  while this line keeps the vocabulary honest. */
const _outcomeVocabulary: readonly TurnOutcome[] = [
  "failed",
  "completed",
  "interrupted",
  "cancelled",
];
void _outcomeVocabulary;
