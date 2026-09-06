// The rewind TARGET on the live 12-message layout, pinned against the projection
// that resolves it.
//
// Rewind's footer button sends `turns[i].rewindTo`, which is the NEXT turn's user
// message, because KAS drops the addressed message inclusive — so keeping turn N
// means addressing turn N+1. That makes the target a property of the PROJECTION,
// and a change to the turn-boundary rules moves it silently: the button keeps
// working, it just reverts to a different point in the conversation.
//
// The layout is the shape a real chat had when a rewind to the end of turn 3
// failed (its own cause was server-side; see the Go suite). It is the interesting
// case for this projection because it holds every boundary shape at once: two
// interrupted turns closed by an event, one completed turn closed by nothing at
// all, and two turns whose prompt failed and left only an outcome marker with no
// assistant message. If `opensHeaderlessTurn` ever widened enough to split turn 4
// off its own user message, the target would move from turn 4's prompt to a
// fabricated headerless turn — and this file is what fails instead.
//
// `.node` because projectTurns is pure: no DOM, no layout, nothing a real browser
// would answer differently.
import { describe, it, expect } from "vitest";
import { projectTurns } from "./turns.js";
import type { Message } from "./types.js";

/** The snapshot's structure with its words stripped: roles, event kinds, turn
 *  outcomes, and which rows carry no `content` at all. */
function liveLayout(): Message[] {
  const interrupted = (id: string, ts: number): Message => ({
    id,
    role: "assistant",
    ts,
    turn_outcome: "interrupted",
    turn_stop_reason_raw: "interrupted",
  });
  const failedMarker = (id: string, ts: number): Message => ({
    id,
    role: "event",
    ts,
    event_kind: "turn_outcome",
    turn_outcome: "failed",
    turn_stop_reason_raw: "error",
  });
  return [
    { id: "m-u1", role: "user", content: "first", ts: 100 },
    interrupted("a1", 200),
    { id: "e1", role: "event", event_kind: "interrupted", content: "Interrupted", ts: 201 },
    { id: "m-u2", role: "user", content: "resume", ts: 300 },
    interrupted("a2", 400),
    { id: "e2", role: "event", event_kind: "interrupted", content: "Interrupted", ts: 401 },
    { id: "m-u3", role: "user", content: "resume", ts: 500 },
    {
      id: "a3",
      role: "assistant",
      content: "the reply",
      ts: 600,
      turn_outcome: "completed",
      turn_stop_reason_raw: "end_turn",
    },
    { id: "m-u4", role: "user", content: "carry on", ts: 700 },
    failedMarker("e3", 701),
    { id: "m-u5", role: "user", content: "carry on", ts: 800 },
    failedMarker("e4", 801),
  ];
}

describe("the rewind target on the live layout", () => {
  it("projects five turns, one per user message", () => {
    const turns = projectTurns(liveLayout(), false);

    expect(turns.map((t) => t.id)).toEqual(["m-u1", "m-u2", "m-u3", "m-u4", "m-u5"]);
    // Every turn has a trigger: none of the twelve rows opens a headerless one,
    // which is what keeps turn 4's own prompt addressable.
    expect(turns.map((t) => t.trigger?.id)).toEqual(["m-u1", "m-u2", "m-u3", "m-u4", "m-u5"]);
  });

  it("addresses turn 3's rewind at the NINTH message, turn 4's prompt", () => {
    const messages = liveLayout();
    const turns = projectTurns(messages, false);
    const third = turns[2];

    expect(third?.n).toBe(3);
    expect(third?.rewindTo?.id).toBe("m-u4");
    // Stated as an index too, because that is what the server resolves the id to
    // and what decides how much history the revert discards.
    expect(messages.findIndex((m) => m.id === third?.rewindTo?.id)).toBe(8);
  });

  it("offers no rewind on the last turn", () => {
    const turns = projectTurns(liveLayout(), false);

    // Nothing after it to discard, so the footer renders no button.
    expect(turns[4]?.rewindTo).toBeUndefined();
  });
});
