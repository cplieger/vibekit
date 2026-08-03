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
    expect(led.contributors).toBe(2);
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

  it("reports no contributors for a turn that carried no ledger data", () => {
    const [t] = projectTurns([user("u1", "a"), assistant("a1")], false);
    const led = turnLedger(t!);
    expect(led.contributors).toBe(0);
    expect(led.credits).toBe(0);
    expect(Object.keys(led.changedFiles)).toEqual([]);
  });

  it("does not count a zero-valued credit or elapsed as a contribution", () => {
    const [t] = projectTurns(
      [user("u1", "a"), assistant("a1", { turn_credits: 0, turn_elapsed_ms: 0 })],
      false,
    );
    expect(turnLedger(t!).contributors).toBe(0);
  });
});

describe("turnAnchorID", () => {
  it("builds the permalink fragment target", () => {
    expect(turnAnchorID(14)).toBe("turn-14");
  });
});
