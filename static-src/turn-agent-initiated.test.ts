// AGENT-INITIATED TURNS, against the shapes the LIVE WIRE actually produces.
//
// The audit that led here suspected the boundary rule collapsed them. The reasoning
// was specific and traceable: `v3_updates.go`'s attribution gate drops a workflow
// step's own `turn_end` (`attr.Step` is checked before `WireTurnEnd` runs), and
// `closesTurn` fires on a persisted `turn_outcome`, so a step-driven turn would
// carry none, never close, and the NEXT agent-initiated turn would join it instead
// of opening — one wrong count and one wrong marker, consistently on both sides.
//
// MEASURED, AND REFUTED. Projecting all 37 chat files on the live volume produced
// 131 turns, 32 of them headerless, and EVERY headerless turn closes on a settled
// outcome. The dominant shape is a two-message body `(unknown, completed)` — 23 of
// 32, with 6 more carrying a subagent id alongside — which is precisely the fragment
// case `closesTurn` documents and deliberately admits: the `unknown` marks a
// displaced turn's persist, it does NOT terminate the segment, and `deriveOutcome`
// lets the reply's settled outcome supersede it. A worked file (29 messages, 13
// turns, 5 of them agent-initiated) showed every boundary landing where it should.
// So the gate drops the step's turn_end and something else still stamps the
// launching chat's outcome; the count is right.
//
// The cases below stage those measured shapes so the refutation is a test rather
// than a note, and so the collapse becomes a REGRESSION rather than a rediscovery
// if the outcome ever stops being stamped. The two defects the same audit found and
// that were real — the trigger reaching only a border style, and a bare UA `title`
// — are the rail's, and they are pinned in `rail-labels.test.ts` and
// `turn-rail.test.ts`.
//
// A browser test rather than a `.node` one: it reads no file. `turns.node.test.ts`
// earns its suffix by loading the shared cross-language fixture off disk, and the
// segmentation contract lives there; this file is about the live PRODUCERS.
import { describe, it, expect } from "vitest";

import { projectTurns } from "./turns.js";
import { markerLabel } from "./rail-labels.js";
import type { Block, Message } from "./types.js";

let seq = 0;

/** One persisted assistant message, in the shape the volume holds: blocks stamped
 *  with an `agent_subtask_id`, and `turn_outcome` present only on the message that
 *  finalized its segment. */
function assistant(over: {
  outcome?: "completed" | "failed" | "unknown";
  subtask?: string;
  text?: string;
}): Message {
  seq += 1;
  const block: Block = { type: "text", text: over.text ?? "step output" };
  if (over.subtask !== undefined) {
    block.agent_subtask_id = over.subtask;
  }
  const m: Message = {
    id: `a${String(seq)}`,
    role: "assistant",
    content: over.text ?? "step output",
    ts: seq * 1000,
    blocks: [block],
  };
  if (over.outcome !== undefined) {
    m.turn_outcome = over.outcome;
  }
  return m;
}

function user(text: string): Message {
  seq += 1;
  return { id: `u${String(seq)}`, role: "user", content: text, ts: seq * 1000 };
}

describe("a chat-parented workflow step folding onto its launching chat", () => {
  it("opens ONE headerless turn per step pair, and closes it", () => {
    // The measured shape, 23 of 32 headerless turns on the live volume: the
    // fragment, then the reply that carries the outcome.
    const turns = projectTurns(
      [
        user("run the release workflow"),
        assistant({ outcome: "completed", subtask: "wf:wf_c45d0d" }),
        assistant({ outcome: "unknown", subtask: "wf:wf_c45d0d" }),
        assistant({ outcome: "completed", subtask: "wf:wf_c45d0d" }),
      ],
      false,
    );

    expect(turns).toHaveLength(2);
    expect(turns[0]?.trigger?.content).toBe("run the release workflow");
    // The step's own turn: no user row exists to promote, so the header is honest
    // about the trigger rather than fabricating a prompt.
    expect(turns[1]?.trigger).toBeUndefined();
    expect(turns[1]?.body).toHaveLength(2);
    // The reply's settled outcome supersedes the fragment's non-verdict.
    expect(turns[1]?.outcome).toBe("completed");
  });

  it("does NOT merge two step-driven turns into one", () => {
    // THE COLLAPSE THE AUDIT PREDICTED. It is unreachable while each pair closes,
    // and this is the case that goes red if the outcome ever stops being stamped —
    // which is the shape the attribution gate makes plausible.
    const turns = projectTurns(
      [
        assistant({ outcome: "unknown", subtask: "wf:wf_a" }),
        assistant({ outcome: "completed", subtask: "wf:wf_a" }),
        assistant({ outcome: "unknown", subtask: "wf:wf_b" }),
        assistant({ outcome: "completed", subtask: "wf:wf_b" }),
      ],
      false,
    );

    expect(turns).toHaveLength(2);
    expect(turns.map((t) => t.n)).toEqual([1, 2]);
    expect(turns.every((t) => t.trigger === undefined)).toBe(true);
    expect(turns.map((t) => t.outcome)).toEqual(["completed", "completed"]);
  });

  it("DOES merge when nothing closes the segment, which is the failure to watch", () => {
    // The negative half, stated so the guard above cannot pass for the wrong reason.
    // With no settled outcome the two producers really do collapse — so this case
    // documents the mechanism AND proves the case above is measuring something.
    const turns = projectTurns(
      [assistant({ subtask: "wf:wf_a" }), assistant({ subtask: "wf:wf_b" })],
      false,
    );

    expect(turns).toHaveLength(1);
    expect(turns[0]?.body).toHaveLength(2);
  });

  it("keeps a step's output out of the PREVIOUS turn's body", () => {
    // The direction `internal/chat/turns.go` records as the original defect: without
    // `opensHeaderlessTurn` an agent-initiated turn's reply landed in the turn above
    // it, which can flip a completed turn's marker on reload.
    const turns = projectTurns(
      [
        user("ask something"),
        assistant({ outcome: "completed" }),
        assistant({ outcome: "completed", subtask: "wf:wf_a", text: "step ran" }),
      ],
      false,
    );

    expect(turns).toHaveLength(2);
    expect(turns[0]?.body).toHaveLength(1);
    expect(turns[1]?.body.map((m) => m.content)).toEqual(["step ran"]);
  });
});

describe("a hook-driven turn and a scheduled run", () => {
  it("open a headerless turn with no user row anywhere before them", () => {
    // Both producers reach the transcript identically — an assistant message with no
    // preceding user row — which is why they need no case of their own in the
    // projection and get one here instead.
    const turns = projectTurns([assistant({ outcome: "completed", text: "hook fired" })], false);

    expect(turns).toHaveLength(1);
    expect(turns[0]?.trigger).toBeUndefined();
    expect(turns[0]?.n).toBe(1);
    expect(turns[0]?.id).toBe(turns[0]?.body[0]?.id);
  });

  it("are numbered in the same sequence as prompted turns", () => {
    // The rail's marker digits come from this numbering (via the server's own copy
    // of the rule), so an agent-initiated turn silently not counting would shift
    // every later marker.
    const turns = projectTurns(
      [
        user("first"),
        assistant({ outcome: "completed" }),
        assistant({ outcome: "completed", text: "scheduled run" }),
        user("second"),
        assistant({ outcome: "completed" }),
      ],
      false,
    );

    expect(turns.map((t) => t.n)).toEqual([1, 2, 3]);
    expect(turns.map((t) => t.trigger === undefined)).toEqual([false, true, false]);
  });

  it("cannot be a rewind target, because KAS refuses to revert to a non-user message", () => {
    // Not new behaviour — `rewindTo` is the NEXT turn's trigger — but it is the one
    // place an agent-initiated turn changes a control the reader can see, so it is
    // worth a case beside the rest of the audit.
    const turns = projectTurns(
      [
        user("first"),
        assistant({ outcome: "completed" }),
        assistant({ outcome: "completed", text: "agent did this by itself" }),
      ],
      false,
    );

    expect(turns[0]?.rewindTo).toBeUndefined();
    expect(turns[1]?.rewindTo).toBeUndefined();
  });
});

describe("what the rail says about an agent-initiated turn", () => {
  it("names the trigger in words, not only in a border style", () => {
    // Defect 3a, closed. `data-trigger="system"` rendered as a dashed italic border
    // and nothing else, and the server leaves `first_line` EMPTY for a non-user turn
    // (`FirstLine` is set only inside the `RoleUser` branch), so the old hover fell
    // back to `Turn 4` and said nothing either. A screen-reader user was told
    // nothing at all.
    const label = markerLabel(
      { n: 4, outcome: "completed", agent_initiated: true },
      {
        pending: false,
        hit: false,
      },
    );

    expect(label.ariaLabel).toBe("Go to turn 4, agent-initiated");
    expect(label.tooltip).toBe("Agent-initiated turn");
    // Never the bare digit restated: that is what the button's own text already is.
    expect(label.tooltip).not.toBe("Turn 4");
  });

  it("still names the outcome, so the two facts do not compete for one channel", () => {
    const label = markerLabel(
      { n: 4, outcome: "failed", agent_initiated: true },
      {
        pending: false,
        hit: false,
      },
    );

    expect(label.ariaLabel).toBe("Go to turn 4, failed, agent-initiated");
    expect(label.tooltip).toBe("Agent-initiated turn \u00b7 This turn failed");
  });
});
