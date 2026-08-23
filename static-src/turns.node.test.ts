import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { projectTurns, turnLedger, turnAnchorID } from "./turns.js";
import type { Message } from "./types.js";

function user(id: string, content: string, ts = 1000): Message {
  return { id, role: "user", content, ts } as Message;
}

function assistant(id: string, extra: Partial<Message> = {}, ts = 2000): Message {
  return { id, role: "assistant", content: "ok", ts, ...extra } as Message;
}

function event(id: string, kind: string, ts = 2500): Message {
  return { id, role: "event", event_kind: kind, ts } as unknown as Message;
}

describe("projectTurns", () => {
  it("promotes the user message to the turn's trigger and keeps the rest as body", () => {
    const turns = projectTurns([user("u1", "do a thing"), assistant("a1")], false);
    expect(turns).toHaveLength(1);
    expect(turns[0]?.trigger?.id).toBe("u1");
    expect(turns[0]?.body.map((m) => m.id)).toEqual(["a1"]);
  });

  it("never leaves a user message in a body", () => {
    const turns = projectTurns(
      [user("u1", "one"), assistant("a1"), user("u2", "two"), assistant("a2")],
      false,
    );
    expect(turns).toHaveLength(2);
    for (const t of turns) {
      expect(t.body.some((m) => m.role === "user")).toBe(false);
    }
  });

  it("keys each turn on its opening message and numbers from 1", () => {
    const turns = projectTurns([user("u1", "a"), assistant("a1"), user("u2", "b")], false);
    expect(turns.map((t) => [t.id, t.n])).toEqual([
      ["u1", 1],
      ["u2", 2],
    ]);
  });

  it("takes the turn's start time from the trigger, not the reply", () => {
    const turns = projectTurns([user("u1", "a", 500), assistant("a1", {}, 900)], false);
    expect(turns[0]?.ts).toBe(500);
  });

  // A turn with no user row is the agent-initiated case (a run-completion wake)
  // AND the paginated-window case (the first page starts mid-turn). Both must
  // render, and neither may borrow a neighbouring turn's header.
  it("opens a headerless turn when the transcript starts without a user message", () => {
    const turns = projectTurns([assistant("a1"), user("u1", "then this"), assistant("a2")], false);
    expect(turns).toHaveLength(2);
    expect(turns[0]?.trigger).toBeUndefined();
    expect(turns[0]?.id).toBe("a1");
    expect(turns[0]?.body.map((m) => m.id)).toEqual(["a1"]);
    expect(turns[1]?.trigger?.id).toBe("u1");
  });

  it("returns nothing for an empty transcript", () => {
    expect(projectTurns([], false)).toEqual([]);
  });

  describe("outcome", () => {
    it("is completed for a clean finished turn", () => {
      expect(projectTurns([user("u1", "a"), assistant("a1")], false)[0]?.outcome).toBe("completed");
    });

    it("is running only for the LAST turn while the session is thinking", () => {
      const turns = projectTurns(
        [user("u1", "a"), assistant("a1"), user("u2", "b"), assistant("a2")],
        true,
      );
      expect(turns[0]?.outcome).toBe("completed");
      expect(turns[1]?.outcome).toBe("running");
    });

    it("is failed on a refusal", () => {
      const turns = projectTurns(
        [user("u1", "a"), assistant("a1", { refusal: { category: "x" } } as Partial<Message>)],
        false,
      );
      expect(turns[0]?.outcome).toBe("failed");
    });

    it("is failed on a safety block or a failed compaction", () => {
      for (const kind of ["infra_safety_blocked", "compaction_failed"]) {
        const turns = projectTurns([user("u1", "a"), event("e1", kind)], false);
        expect(turns[0]?.outcome, kind).toBe("failed");
      }
    });

    it("is interrupted on a cancel", () => {
      const turns = projectTurns(
        [user("u1", "a"), assistant("a1"), event("e1", "cancelled")],
        false,
      );
      expect(turns[0]?.outcome).toBe("interrupted");
    });

    // The precedence that matters: `thinking` can legitimately still be true on
    // the last turn when the NEXT stream has already opened, so trusting the
    // session flag over a terminal marker would repaint a finished failure as
    // in-progress.
    it("lets a terminal marker beat the running flag on the last turn", () => {
      const failed = projectTurns(
        [user("u1", "a"), assistant("a1", { refusal: {} } as Partial<Message>)],
        true,
      );
      expect(failed[0]?.outcome).toBe("failed");
      const stopped = projectTurns([user("u1", "a"), event("e1", "interrupted")], true);
      expect(stopped[0]?.outcome).toBe("interrupted");
    });

    it("prefers failed over interrupted when a turn carries both", () => {
      const turns = projectTurns(
        [user("u1", "a"), event("e1", "cancelled"), event("e2", "infra_safety_blocked")],
        false,
      );
      expect(turns[0]?.outcome).toBe("failed");
    });

    it("ignores a model switch, which is not an outcome", () => {
      const turns = projectTurns(
        [user("u1", "a"), event("e1", "model_switched"), assistant("a1")],
        false,
      );
      expect(turns[0]?.outcome).toBe("completed");
    });
  });
});

describe("turnLedger", () => {
  it("sums credits and elapsed across a turn's assistant messages", () => {
    const [t] = projectTurns(
      [
        user("u1", "a"),
        assistant("a1", { turn_credits: 0.5, turn_elapsed_ms: 1000 }),
        assistant("a2", { turn_credits: 0.25, turn_elapsed_ms: 500 }),
      ],
      false,
    );
    const led = turnLedger(t!);
    expect(led.credits).toBeCloseTo(0.75);
    expect(led.elapsedMs).toBe(1500);
  });

  // changed_files is a cumulative per-turn SNAPSHOT stamped at turn_ended, not
  // a delta, so two messages reporting the same path must not have their counts
  // added — that would double-count the file.
  it("merges changed files by path instead of adding their counts", () => {
    const [t] = projectTurns(
      [
        user("u1", "a"),
        assistant("a1", { changed_files: { "a.ts": { lines_added: 3, lines_removed: 1 } } }),
        assistant("a2", {
          changed_files: {
            "a.ts": { lines_added: 5, lines_removed: 2 },
            "b.ts": { lines_added: 1, lines_removed: 0 },
          },
        }),
      ],
      false,
    );
    const led = turnLedger(t!);
    expect(Object.keys(led.changedFiles).sort()).toEqual(["a.ts", "b.ts"]);
    expect(led.changedFiles["a.ts"]).toEqual({ lines_added: 5, lines_removed: 2 });
  });

  // The model is the FOURTH aggregation strategy in this function, and it is
  // none of the other three: adding two ids is meaningless, and taking the last
  // one is silently wrong on a turn a mid-turn switch split in two.
  it("names the model that served the turn", () => {
    const [t] = projectTurns([user("u1", "a"), assistant("a1", { turn_model: "sonnet-4" })], false);
    expect(turnLedger(t!).models).toEqual(["sonnet-4"]);
  });

  it("carries both models in order when a switch split the turn", () => {
    const [t] = projectTurns(
      [
        user("u1", "a"),
        assistant("a1", { turn_model: "sonnet-4" }),
        event("e1", "model_switched"),
        assistant("a2", { turn_model: "opus-4" }),
      ],
      false,
    );
    expect(turnLedger(t!).models).toEqual(["sonnet-4", "opus-4"]);
  });

  it("does not repeat one model that answered several messages", () => {
    const [t] = projectTurns(
      [
        user("u1", "a"),
        assistant("a1", { turn_model: "sonnet-4" }),
        assistant("a2", { turn_model: "sonnet-4" }),
      ],
      false,
    );
    expect(turnLedger(t!).models).toEqual(["sonnet-4"]);
  });

  // Every message persisted before the field existed carries no model, and the
  // footer renders nothing for that rather than "unknown".
  it("reports no model for a turn that carries none", () => {
    const [t] = projectTurns([user("u1", "a"), assistant("a1")], false);
    expect(turnLedger(t!).models).toEqual([]);
  });

  it("ignores an empty model string", () => {
    const [t] = projectTurns([user("u1", "a"), assistant("a1", { turn_model: "" })], false);
    expect(turnLedger(t!).models).toEqual([]);
  });

  it("keeps the models a partly-stamped turn does have", () => {
    const [t] = projectTurns(
      [user("u1", "a"), assistant("a1"), assistant("a2", { turn_model: "opus-4" })],
      false,
    );
    expect(turnLedger(t!).models).toEqual(["opus-4"]);
  });

  it("reports a zeroed ledger for a turn that carried no data", () => {
    const [t] = projectTurns([user("u1", "a"), assistant("a1")], false);
    const led = turnLedger(t!);
    expect(led.credits).toBe(0);
    expect(led.elapsedMs).toBe(0);
    expect(led.commands).toBe(0);
    expect(led.reads).toBe(0);
    expect(Object.keys(led.changedFiles)).toEqual([]);
  });

  // Non-file work exists only on the tool calls; nothing else aggregates it, so
  // a turn that read forty files and wrote none reported no work at all.
  it("counts commands and file reads from the turn's tool calls", () => {
    const [t] = projectTurns(
      [
        user("u1", "a"),
        assistant("a1", {
          tool_calls: [
            { id: "1", kind: "execute" },
            { id: "2", kind: "shell" },
            { id: "3", kind: "command" },
            { id: "4", kind: "read" },
            { id: "5", kind: "read" },
            { id: "6", kind: "edit" },
            { id: "7", kind: "search" },
          ],
        } as Partial<Message>),
        assistant("a2", { tool_calls: [{ id: "8", kind: "read" }] } as Partial<Message>),
      ],
      false,
    );
    const led = turnLedger(t!);
    expect(led.commands).toBe(3);
    // Counted across every message in the turn; `edit` and `search` are neither.
    expect(led.reads).toBe(3);
  });
});

describe("turnAnchorID", () => {
  it("builds the permalink fragment target", () => {
    expect(turnAnchorID(14)).toBe("turn-14");
  });
});

// ---------------------------------------------------------------------------
// The cross-language pin.
//
// The outcome rule exists in two implementations and neither can go: the server
// derives it for the whole session (internal/chat/turns.go), because the rail
// must describe turns this paginated store does not hold, and the client derives
// it for the IN-FLIGHT turn, which no fetched summary can know. This runs the
// same fixture Go's TestTurnOutcomeContract runs, so a rule changed in one
// language fails here.
// ---------------------------------------------------------------------------

interface OutcomeFixture {
  cases: {
    name: string;
    body: { refusal?: boolean; event?: string }[];
    is_live: boolean;
    want: string;
  }[];
}

const FIXTURE_PATH = "../internal/chat/testdata/turn_outcomes.json";

describe("the turn-outcome contract shared with the Go implementation", () => {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const fx = JSON.parse(raw) as OutcomeFixture;

  it("carries cases (an empty table would pass forever)", () => {
    expect(fx.cases.length).toBeGreaterThan(0);
  });

  it.each(fx.cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    const body: Message[] = c.body.map((b, i) => {
      if (b.event !== undefined) {
        return event(`e${String(i)}`, b.event);
      }
      if (b.refusal === true) {
        return assistant(`a${String(i)}`, { refusal: {} } as Partial<Message>);
      }
      return assistant(`a${String(i)}`);
    });
    // projectTurns applies `is_live` to the LAST turn only, so a single turn
    // built from a trigger plus this body reproduces the Go call exactly.
    const turns = projectTurns([user("u1", "req"), ...body], c.is_live);
    expect(turns).toHaveLength(1);
    expect(turns[0]?.outcome).toBe(c.want);
  });
});
