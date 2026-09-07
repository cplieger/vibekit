// What a rail row says, pinned string by string.
//
// The module is pure, so this needs no DOM and no fixture — which is exactly why
// it is NOT a `*.node.test.ts`: that suffix selects the node project and is for a
// test needing genuine Node capabilities (a filesystem or process read), not for a
// subject that happens not to want a browser. `turn-severity.node.test.ts` earns
// its suffix by reading the shared cross-language fixture off disk; there is
// nothing here to read.
//
// EXPECTATIONS ARE HARDCODED, including a second copy of every clause the module
// composes. Deriving them from `turn-severity.ts`'s tables would make the assertion
// `f(x) === f(x)`: the outcome sentence would agree with itself whatever it said,
// and a table edit would pass silently. The copies below are the assertion.
import { describe, it, expect } from "vitest";

import { clusterLabel, markerLabel, zoomOutLabel, type MarkerSubject } from "./rail-labels.js";
import type { TurnOutcome } from "./turns.js";

/** Every member of the wire union, so the sweeps below are a partition rather
 *  than a sample. Spelled out here for the same reason as the clauses: importing a
 *  list from the module under test proves nothing about its completeness. */
const ALL_OUTCOMES: TurnOutcome[] = [
  "running",
  "completed",
  "cancelled",
  "interrupted",
  "refused",
  "unknown",
  "failed",
];

/** The sentence each outcome contributes to a TOOLTIP, hardcoded. `completed`
 *  contributes nothing, which is the rule the whole design rests on. */
const TOOLTIP_CLAUSE: Record<TurnOutcome, string> = {
  running: "This turn is still running",
  completed: "",
  cancelled: "You stopped this turn",
  interrupted: "This turn was interrupted before it finished",
  refused: "The model declined to continue",
  unknown: "This turn's end could not be read",
  failed: "This turn failed",
};

/** The word each outcome contributes to an accessible NAME, hardcoded. */
const NAME_CLAUSE: Record<TurnOutcome, string> = {
  running: "running",
  completed: "",
  cancelled: "cancelled",
  interrupted: "interrupted",
  refused: "refused",
  unknown: "unknown",
  failed: "failed",
};

function subject(over: Partial<MarkerSubject> = {}): MarkerSubject {
  return { n: 14, outcome: "completed", ...over };
}

describe("a marker's tooltip names its state", () => {
  // One row per outcome, both triggers, with the transient flags off — so every
  // entry in both tables above is asserted exactly once against a full string.
  const cases: {
    name: string;
    subject: MarkerSubject;
    tooltip: string;
    ariaLabel: string;
  }[] = [
    {
      name: "a clean user turn says only what it was about",
      subject: subject({ outcome: "completed", first_line: "add a health endpoint" }),
      tooltip: "add a health endpoint",
      ariaLabel: "Go to turn 14",
    },
    {
      name: "a running user turn",
      subject: subject({ outcome: "running", first_line: "add a health endpoint" }),
      tooltip: "add a health endpoint \u00b7 This turn is still running",
      ariaLabel: "Go to turn 14, running",
    },
    {
      name: "a cancelled user turn",
      subject: subject({ outcome: "cancelled", first_line: "add a health endpoint" }),
      tooltip: "add a health endpoint \u00b7 You stopped this turn",
      ariaLabel: "Go to turn 14, cancelled",
    },
    {
      name: "an interrupted user turn",
      subject: subject({ outcome: "interrupted", first_line: "add a health endpoint" }),
      tooltip: "add a health endpoint \u00b7 This turn was interrupted before it finished",
      ariaLabel: "Go to turn 14, interrupted",
    },
    {
      name: "a refused user turn",
      subject: subject({ outcome: "refused", first_line: "add a health endpoint" }),
      tooltip: "add a health endpoint \u00b7 The model declined to continue",
      ariaLabel: "Go to turn 14, refused",
    },
    {
      name: "a user turn whose end could not be read",
      subject: subject({ outcome: "unknown", first_line: "add a health endpoint" }),
      tooltip: "add a health endpoint \u00b7 This turn's end could not be read",
      ariaLabel: "Go to turn 14, unknown",
    },
    {
      name: "a failed user turn",
      subject: subject({ outcome: "failed", first_line: "add a health endpoint" }),
      tooltip: "add a health endpoint \u00b7 This turn failed",
      ariaLabel: "Go to turn 14, failed",
    },
    {
      name: "a clean agent-initiated turn",
      subject: subject({ outcome: "completed", agent_initiated: true }),
      tooltip: "Agent-initiated turn",
      ariaLabel: "Go to turn 14, agent-initiated",
    },
    {
      name: "a running agent-initiated turn",
      subject: subject({ outcome: "running", agent_initiated: true }),
      tooltip: "Agent-initiated turn \u00b7 This turn is still running",
      ariaLabel: "Go to turn 14, running, agent-initiated",
    },
    {
      name: "a cancelled agent-initiated turn",
      subject: subject({ outcome: "cancelled", agent_initiated: true }),
      tooltip: "Agent-initiated turn \u00b7 You stopped this turn",
      ariaLabel: "Go to turn 14, cancelled, agent-initiated",
    },
    {
      name: "an interrupted agent-initiated turn",
      subject: subject({ outcome: "interrupted", agent_initiated: true }),
      tooltip: "Agent-initiated turn \u00b7 This turn was interrupted before it finished",
      ariaLabel: "Go to turn 14, interrupted, agent-initiated",
    },
    {
      name: "a refused agent-initiated turn",
      subject: subject({ outcome: "refused", agent_initiated: true }),
      tooltip: "Agent-initiated turn \u00b7 The model declined to continue",
      ariaLabel: "Go to turn 14, refused, agent-initiated",
    },
    {
      name: "an agent-initiated turn whose end could not be read",
      subject: subject({ outcome: "unknown", agent_initiated: true }),
      tooltip: "Agent-initiated turn \u00b7 This turn's end could not be read",
      ariaLabel: "Go to turn 14, unknown, agent-initiated",
    },
    {
      name: "a failed agent-initiated turn",
      subject: subject({ outcome: "failed", agent_initiated: true }),
      tooltip: "Agent-initiated turn \u00b7 This turn failed",
      ariaLabel: "Go to turn 14, failed, agent-initiated",
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(markerLabel(c.subject, { pending: false, hit: false })).toEqual({
        tooltip: c.tooltip,
        ariaLabel: c.ariaLabel,
      });
    });
  }

  it("falls back to the turn number when a user turn has no first line", () => {
    // A user turn CAN reach this: the server records `first_line` from the opening
    // message, and an empty prompt or a whitespace-only one leaves it blank. Saying
    // "Agent-initiated turn" there would be a claim about the trigger that is false.
    expect(markerLabel(subject({ first_line: "   " }), { pending: false, hit: false })).toEqual({
      tooltip: "Turn 14",
      ariaLabel: "Go to turn 14",
    });
  });
});

describe("a marker's transient state", () => {
  const s = subject({ outcome: "failed", first_line: "rename the module" });

  it("says the jump is loading history in", () => {
    expect(markerLabel(s, { pending: true, hit: false }).tooltip).toBe(
      "rename the module \u00b7 This turn failed \u00b7 Loading this turn\u2026",
    );
  });

  it("says the turn holds a search match", () => {
    expect(markerLabel(s, { pending: false, hit: true }).tooltip).toBe(
      "rename the module \u00b7 This turn failed \u00b7 Contains a search match",
    );
  });

  it("orders identity, outcome, pending, hit", () => {
    expect(markerLabel(s, { pending: true, hit: true }).tooltip).toBe(
      "rename the module \u00b7 This turn failed \u00b7 Loading this turn\u2026 \u00b7 Contains a search match",
    );
  });

  it("keeps both transient facts out of the accessible NAME", () => {
    // The name is read on every focus, so it stays the turn's identity plus its
    // durable state. Pending is a fetch in flight and a hit belongs to a search the
    // reader started; neither is a property of the turn.
    expect(markerLabel(s, { pending: true, hit: true }).ariaLabel).toBe("Go to turn 14, failed");
  });
});

describe("the marker vocabulary is total over TurnOutcome", () => {
  it("gives every outcome a tooltip", () => {
    for (const outcome of ALL_OUTCOMES) {
      const label = markerLabel(subject({ outcome, first_line: "do the thing" }), {
        pending: false,
        hit: false,
      });
      expect(label.tooltip, outcome).not.toBe("");
      expect(label.ariaLabel, outcome).toContain("Go to turn 14");
    }
  });

  it("names the state for every outcome except completed", () => {
    // The case that fails when the wire adds an eighth outcome — the same guard
    // `turn-outcome-css.test.ts` gives the stylesheet. A value with no clause would
    // paint a marker whose only channel is colour, which is where this started.
    for (const outcome of ALL_OUTCOMES) {
      const label = markerLabel(subject({ outcome, first_line: "do the thing" }), {
        pending: false,
        hit: false,
      });
      const clause = TOOLTIP_CLAUSE[outcome];
      const word = NAME_CLAUSE[outcome];
      if (outcome === "completed") {
        expect(clause, "the test's own table agrees completed says nothing").toBe("");
        expect(label.tooltip).toBe("do the thing");
        expect(label.ariaLabel).toBe("Go to turn 14");
        continue;
      }
      expect(label.tooltip, outcome).toBe(`do the thing \u00b7 ${clause}`);
      expect(label.ariaLabel, outcome).toBe(`Go to turn 14, ${word}`);
    }
  });

  it("composes every combination of the two transient flags, for every outcome", () => {
    // The cross-product, expectations built from the test's OWN clause tables. It
    // is the reachability half: any combination a live rail can produce composes
    // into exactly these strings, in this order.
    for (const outcome of ALL_OUTCOMES) {
      for (const agentInitiated of [false, true]) {
        for (const pending of [false, true]) {
          for (const hit of [false, true]) {
            const s = subject(
              agentInitiated ? { outcome, agent_initiated: true } : { outcome, first_line: "ask" },
            );
            const parts = [agentInitiated ? "Agent-initiated turn" : "ask"];
            if (TOOLTIP_CLAUSE[outcome] !== "") {
              parts.push(TOOLTIP_CLAUSE[outcome]);
            }
            if (pending) {
              parts.push("Loading this turn\u2026");
            }
            if (hit) {
              parts.push("Contains a search match");
            }
            const names = ["Go to turn 14"];
            if (NAME_CLAUSE[outcome] !== "") {
              names.push(NAME_CLAUSE[outcome]);
            }
            if (agentInitiated) {
              names.push("agent-initiated");
            }
            const label = markerLabel(s, { pending, hit });
            const where = `${outcome}/${String(agentInitiated)}/${String(pending)}/${String(hit)}`;
            expect(label.tooltip, where).toBe(parts.join(" \u00b7 "));
            expect(label.ariaLabel, where).toBe(names.join(", "));
          }
        }
      }
    }
  });
});

describe("a cluster's labels", () => {
  const range = { from: 30, to: 38, count: 9 };

  it("names its range and its size", () => {
    expect(clusterLabel({ ...range, outcome: "completed" }, { containsCurrent: false })).toEqual({
      tooltip: "Turns 30\u201338 \u00b7 9 turns",
      ariaLabel: "Zoom to turns 30 to 38",
    });
  });

  it("names the worst outcome inside it, which the rail carries as ink alone", () => {
    expect(clusterLabel({ ...range, outcome: "failed" }, { containsCurrent: false })).toEqual({
      tooltip: "Turns 30\u201338 \u00b7 9 turns \u00b7 Worst outcome: Failed",
      ariaLabel: "Zoom to turns 30 to 38, worst outcome failed",
    });
  });

  it("says when it holds the reader's current turn", () => {
    // Past capacity every turn is inside a cluster, so this is the only statement
    // of position a long session's rail can make.
    expect(clusterLabel({ ...range, outcome: "completed" }, { containsCurrent: true })).toEqual({
      tooltip: "Turns 30\u201338 \u00b7 9 turns \u00b7 Contains the current turn",
      ariaLabel: "Zoom to turns 30 to 38, contains the current turn",
    });
  });

  it("says nothing about a clean range's outcome", () => {
    // Same rule as the marker's: the absence of a state clause IS the clean case,
    // and `running` is not clean — a range still working is worth knowing about.
    const clean = clusterLabel({ ...range, outcome: "completed" }, { containsCurrent: false });
    expect(clean.tooltip).not.toContain("Worst outcome");
    const live = clusterLabel({ ...range, outcome: "running" }, { containsCurrent: false });
    expect(live.tooltip).toContain("Worst outcome: Running");
  });

  it("counts a one-turn range in the singular", () => {
    expect(
      clusterLabel({ from: 7, to: 7, count: 1, outcome: "completed" }, { containsCurrent: false })
        .tooltip,
    ).toBe("Turns 7\u20137 \u00b7 1 turn");
  });
});

describe("the zoom-out row's labels", () => {
  it("puts the action in the NAME and the range in the description", () => {
    // The tooltip controller publishes its text as the anchor's `aria-describedby`
    // on show, so the two channels must not be one sentence twice: a keyboard user
    // would hear the name read again as its own description. The name carries what
    // the row's three characters cannot say; the tooltip carries the range, which
    // the name deliberately leaves out.
    expect(zoomOutLabel({ from: 30, to: 38 })).toEqual({
      tooltip: "Showing turns 30\u201338",
      ariaLabel: "Show the whole session",
    });
  });

  it("never repeats itself across the two channels", () => {
    // The property behind the case above, over a range whose numbers cannot make the
    // two agree by accident.
    const label = zoomOutLabel({ from: 1, to: 9 });
    expect(label.tooltip).not.toBe(label.ariaLabel);
    expect(label.tooltip).toContain("1\u20139");
    expect(label.ariaLabel).not.toContain("1");
  });
});
