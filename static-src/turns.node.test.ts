import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import {
  projectTurns,
  turnLedger,
  turnAnchorID,
  turnFaceProse,
  turnFailureText,
  turnFoldHides,
  type Turn,
} from "./turns.js";
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

    // THE TWO DIRECTIONS of the liveness fix, over the record a mid-turn reload
    // actually produces: the user's prompt persisted, the reply still in the
    // server's in-memory buffer, so NO assistant message and no carrier.
    //
    // Absent-carrier has two meanings and only the caller can tell them apart, which
    // is why `live` composes two facts (`store.ts` `turnLive`) rather than being the
    // `thinking` flag. Both cases are one message list, so nothing but the liveness
    // input separates them.
    it("is running for a carrier-less newest turn while a turn is live", () => {
      const turns = projectTurns([user("u1", "do the thing")], true);
      expect(turns[0]?.outcome).toBe("running");
    });

    it("is unknown for a carrier-less newest turn when nothing is live", () => {
      // The direction the fix must NOT erase: after a server restart mid-turn no
      // turn is open, because the process died — so the newest turn genuinely is one
      // nothing closed, and its neutral mark is honest.
      const turns = projectTurns([user("u1", "do the thing")], false);
      expect(turns[0]?.outcome).toBe("unknown");
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

  // A turn split at a COMPACTION point: the server seals what the model said
  // before the boundary as its own message, so the summary sits between two
  // assistant segments of one turn. The pre-compaction segment carries none of
  // the turn's facts — exactly one message per turn may — so the ledger has to
  // read through the event to reach the post-compaction segment that does.
  it("reads a compaction-split turn's footer off the segment that carries it", () => {
    const [t] = projectTurns(
      [
        user("u1", "a"),
        assistant("a1"),
        event("e1", "compacted"),
        assistant("a2", {
          turn_credits: 0.75,
          turn_elapsed_ms: 2000,
          turn_model: "opus-5",
          changed_files: { "a.ts": { lines_added: 3, lines_removed: 1 } },
        }),
      ],
      false,
    );
    expect(t?.body).toHaveLength(3);
    const led = turnLedger(t!);
    expect(led.credits).toBeCloseTo(0.75);
    expect(led.elapsedMs).toBe(2000);
    expect(led.models).toEqual(["opus-5"]);
    expect(led.changedFiles["a.ts"]).toEqual({ lines_added: 3, lines_removed: 1 });
  });

  // A reply that ended exactly AT the compaction point closes with nothing after
  // the split, so the turn's outcome marker is what carries its footer. The
  // ledger reads an event row like any other message, which is what makes that
  // carrier work.
  it("reads a split turn's footer off its outcome marker when nothing followed the split", () => {
    const [t] = projectTurns(
      [
        user("u1", "a"),
        assistant("a1"),
        event("e1", "compacted"),
        {
          id: "e2",
          role: "event",
          event_kind: "turn_outcome",
          turn_outcome: "completed",
          turn_credits: 0.5,
          turn_elapsed_ms: 1200,
          changed_files: { "a.ts": { lines_added: 2, lines_removed: 0 } },
          ts: 2600,
        } as unknown as Message,
      ],
      false,
    );
    const led = turnLedger(t!);
    expect(led.credits).toBeCloseTo(0.5);
    expect(led.elapsedMs).toBe(1200);
    expect(led.changedFiles["a.ts"]).toEqual({ lines_added: 2, lines_removed: 0 });
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

interface FixtureMessage {
  role?: string;
  id?: string;
  event?: string;
  outcome?: string;
  refusal?: boolean;
}

interface OutcomeFixture {
  cases: {
    name: string;
    body: { refusal?: boolean; event?: string; outcome?: string; truncated?: boolean }[];
    is_live: boolean;
    want: string;
  }[];
  segmentation: {
    name: string;
    messages: FixtureMessage[];
    want: { id: string; agent_initiated: boolean; outcome: string }[];
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
      const extra: Partial<Message> = {};
      if (b.outcome !== undefined) {
        extra.turn_outcome = b.outcome as NonNullable<Message["turn_outcome"]>;
      }
      if (b.truncated === true) {
        extra.turn_truncated = true;
      }
      if (b.event !== undefined) {
        return { ...event(`e${String(i)}`, b.event), ...extra } as Message;
      }
      if (b.refusal === true) {
        return assistant(`a${String(i)}`, { refusal: {}, ...extra } as Partial<Message>);
      }
      return assistant(`a${String(i)}`, extra);
    });
    // projectTurns applies `is_live` to the LAST turn only, so a single turn
    // built from a trigger plus this body reproduces the Go call exactly.
    const turns = projectTurns([user("u1", "req"), ...body], c.is_live);
    expect(turns).toHaveLength(1);
    expect(turns[0]?.outcome).toBe(c.want);
  });
});

// ---------------------------------------------------------------------------
// The BOUNDARY half of the same pin, over the same fixture.
//
// Where the table above asks how a turn ended, this asks which turn a message
// belongs to. Reviewers caught the rule wrong in both directions — too narrow
// puts a non-empty agent-initiated turn's outcome in the previous turn's body,
// too broad splits a prompted empty turn off its own prompt — so both languages
// answer the same cases rather than describing the rule twice.
// ---------------------------------------------------------------------------

describe("the turn-segmentation contract shared with the Go implementation", () => {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const fx = JSON.parse(raw) as OutcomeFixture;

  it("carries segmentation cases (an empty table would pass forever)", () => {
    expect(fx.segmentation.length).toBeGreaterThan(0);
  });

  it.each(fx.segmentation.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    const msgs: Message[] = c.messages.map((fm) => {
      const extra: Partial<Message> = {};
      if (fm.outcome !== undefined) {
        extra.turn_outcome = fm.outcome as NonNullable<Message["turn_outcome"]>;
      }
      if (fm.refusal === true) {
        (extra as { refusal?: unknown }).refusal = {};
      }
      if (fm.role === "user") {
        return { ...user(fm.id ?? "", "req"), ...extra } as Message;
      }
      if (fm.role === "event") {
        return { ...event(fm.id ?? "", fm.event ?? ""), ...extra } as Message;
      }
      return assistant(fm.id ?? "", extra);
    });
    const turns = projectTurns(msgs, false);
    expect(turns.map((t) => t.id)).toEqual(c.want.map((w) => w.id));
    expect(turns.map((t) => t.trigger === undefined)).toEqual(c.want.map((w) => w.agent_initiated));
    expect(turns.map((t) => t.outcome)).toEqual(c.want.map((w) => w.outcome));
  });
});

// ---------------------------------------------------------------------------
// The collapsed turn's FACE content: input in the header, these in the footer.
// ---------------------------------------------------------------------------

describe("turnFaceProse", () => {
  function blocks(id: string, bs: Record<string, unknown>[]): Message {
    return assistant(id, { blocks: bs } as unknown as Partial<Message>);
  }

  it("takes the last non-empty top-level text block", () => {
    const t = projectTurns(
      [
        user("u1", "q"),
        blocks("a1", [
          { type: "text", text: "working on it" },
          { type: "tool_use", tool_call_id: "t1" },
          { type: "text", text: "the final answer" },
        ]),
      ],
      false,
    )[0];
    expect(t === undefined ? "" : turnFaceProse(t)).toBe("the final answer");
  });

  it("skips a delegate's prose — a delegate's report is not the turn's answer", () => {
    const t = projectTurns(
      [
        user("u1", "q"),
        blocks("a1", [
          { type: "text", text: "the parent's answer" },
          { type: "text", text: "delegate report", agent_subtask_id: "sub-A" },
        ]),
      ],
      false,
    )[0];
    expect(t === undefined ? "" : turnFaceProse(t)).toBe("the parent's answer");
  });

  it("answers empty for a turn with no prose at all", () => {
    const t = projectTurns(
      [user("u1", "q"), blocks("a1", [{ type: "tool_use", tool_call_id: "t1" }])],
      false,
    )[0];
    expect(t === undefined ? "x" : turnFaceProse(t)).toBe("");
  });
});

// ---------------------------------------------------------------------------
// Whether the fold would hide anything — the gate on the fold affordance.
// ---------------------------------------------------------------------------

describe("turnFoldHides", () => {
  function blocks(id: string, bs: Record<string, unknown>[]): Message {
    return assistant(id, { blocks: bs } as unknown as Partial<Message>);
  }

  function first(msgs: Message[]): Turn {
    const t = projectTurns(msgs, false)[0];
    if (t === undefined) {
      throw new Error("no turn projected");
    }
    return t;
  }

  it("a prose-only turn hides nothing — its face IS its body", () => {
    // The user-reported shape (2026-08-31): one answer, no tools. Folding it
    // animated and changed nothing, so such a turn offers no fold.
    const t = first([user("u1", "q"), blocks("a1", [{ type: "text", text: "the answer" }])]);
    expect(turnFoldHides(t)).toBe(false);
  });

  it("a tool call is hidden by the fold", () => {
    const t = first([
      user("u1", "q"),
      blocks("a1", [
        { type: "tool_use", tool_call_id: "t1" },
        { type: "text", text: "done" },
      ]),
    ]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("reasoning is hidden by the fold", () => {
    const t = first([
      user("u1", "q"),
      blocks("a1", [
        { type: "thinking", text: "hmm" },
        { type: "text", text: "done" },
      ]),
    ]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("intermediate prose is hidden — the face shows only the final answer", () => {
    const t = first([
      user("u1", "q"),
      blocks("a1", [
        { type: "text", text: "working on it" },
        { type: "text", text: "the final answer" },
      ]),
    ]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("a delegate's output is hidden by the fold", () => {
    const t = first([
      user("u1", "q"),
      blocks("a1", [
        { type: "text", text: "delegate report", agent_subtask_id: "sub-A" },
        { type: "text", text: "the answer" },
      ]),
    ]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("an event row is hidden by the fold", () => {
    const t = first([
      user("u1", "q"),
      blocks("a1", [{ type: "text", text: "partial" }]),
      event("e1", "cancelled"),
    ]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("a plan card is hidden by the fold", () => {
    const withPlan = {
      ...blocks("a1", [{ type: "text", text: "done" }]),
      plan: [{ id: "p1", text: "step", status: "done" }],
    } as unknown as Message;
    const t = first([user("u1", "q"), withPlan]);
    expect(turnFoldHides(t)).toBe(true);
  });

  it("empty text blocks do not count as intermediate prose", () => {
    const t = first([
      user("u1", "q"),
      blocks("a1", [
        { type: "text", text: "  " },
        { type: "text", text: "the answer" },
      ]),
    ]);
    expect(turnFoldHides(t)).toBe(false);
  });

  it("a bodyless turn hides nothing", () => {
    const t = first([user("u1", "q")]);
    expect(turnFoldHides(t)).toBe(false);
  });
});

describe("turnFailureText", () => {
  function textOf(msgs: readonly Message[]): string {
    const t = projectTurns(msgs, false)[0];
    if (t === undefined) {
      throw new Error("the fixture produced no turn");
    }
    return turnFailureText(t);
  }

  it("prefers the newest INTERRUPTED event row's own prose", () => {
    // The most specific source available, and the one the prompt-failure and
    // bridge-death closers write. THIS is the half that makes the divider's own
    // labelFn removable: the sentence still reaches the reader, on the durable
    // card-level surface, instead of on both at once
    // (messages-events.test.ts holds the divider's side of the rule).
    expect(
      textOf([
        user("u1", "q"),
        { ...event("e1", "interrupted"), content: "the bridge died" } as Message,
        assistant("a1", { turn_outcome: "interrupted" }),
      ]),
    ).toBe("the bridge died");
  });

  it("reads NO other event kind's content, however new", () => {
    // Source 1 is scoped to `event_kind === "interrupted"`, because that is the only
    // kind whose content is authored as the turn's stop account. Five others persist
    // content that is not, and each one is its own wrong answer — the worst being
    // `compacted`, whose content is the WHOLE conversation summary.
    //
    // Each case falls through to source 2 (absent here) and then to the outcome's
    // default sentence, which is honest and does not duplicate the divider.
    const cases: { kind: string; content: string }[] = [
      { kind: "compacted", content: "A very long conversation summary about everything." },
      { kind: "model_switched", content: "claude-opus-5" },
      { kind: "step_notice", content: "Which branch should I target?" },
      { kind: "compaction_failed", content: "context window exceeded during compaction" },
      { kind: "infra_safety_blocked", content: "violated: no-prod-writes" },
    ];
    for (const c of cases) {
      const text = textOf([
        user("u1", "q"),
        { ...event("e1", c.kind), content: c.content } as Message,
        assistant("a1", { turn_outcome: "failed" }),
      ]);
      expect(text, c.kind).not.toContain(c.content);
      expect(text, c.kind).toBe("The agent reported an error and the turn stopped.");
    }
  });

  it("reaches PAST a newer non-interrupted row to the interrupted one", () => {
    // The scope is a filter, not an early stop: a plan row, a compaction watermark
    // or a step notice can legitimately land after the interrupt divider inside one
    // turn, and the interrupt is still the turn's account of itself.
    expect(
      textOf([
        user("u1", "q"),
        { ...event("e1", "interrupted"), content: "the bridge died" } as Message,
        { ...event("e2", "model_switched"), content: "claude-opus-5" } as Message,
        assistant("a1", { turn_outcome: "interrupted" }),
      ]),
    ).toBe("the bridge died");
  });

  it("falls back to the carrier's persisted reason when no divider was written", () => {
    // `closeWithOutcome` writes no divider once the turn has streamed anything, so
    // the message that finalized the turn is the only place the reason can live.
    expect(
      textOf([
        user("u1", "q"),
        assistant("a1", {
          turn_outcome: "failed",
          turn_failure_reason: "The upstream connection dropped.",
        }),
      ]),
    ).toBe("The upstream connection dropped.");
  });

  it("speaks for a failed turn that recorded no reason at all", () => {
    // THE SYMPTOM-1 SHAPE, taken from the reported chat file: a settled `failed`
    // outcome on an assistant message with blocks, no event row, and no reason
    // anywhere in the record. It rendered a red footer mark over an empty body, and
    // the only account of the failure was a 12-second toast.
    const text = textOf([user("u1", "q"), assistant("a1", { turn_outcome: "failed" })]);
    expect(text).not.toBe("");
    expect(text).toBe("The agent reported an error and the turn stopped.");
  });

  it("speaks for an empty turn whose only trace is an outcome marker", () => {
    // Turn 2 of the same chat: a `turn_outcome` event carrying no content, which
    // EVENT_RENDER_MAP skips, so the body rendered nothing whatsoever.
    expect(
      textOf([
        user("u2", "resume"),
        { ...event("e1", "turn_outcome"), turn_outcome: "failed" } as Message,
      ]),
    ).toBe("The agent reported an error and the turn stopped.");
  });

  it("distinguishes a refusal from a dropped connection", () => {
    // Both are `broken`, and the default is keyed per OUTCOME precisely so the
    // severity's own coarseness does not reach the reader.
    const refused = textOf([user("u1", "q"), assistant("a1", { turn_outcome: "refused" })]);
    const interrupted = textOf([user("u2", "q"), assistant("a2", { turn_outcome: "interrupted" })]);
    expect(refused).not.toBe(interrupted);
    expect(refused).toBe("The model declined to continue.");
  });

  it("answers empty for a clean turn — the card shows the answer instead", () => {
    expect(
      textOf([
        user("u1", "q"),
        event("e1", "model_switched"),
        assistant("a1", { turn_outcome: "completed" }),
      ]),
    ).toBe("");
  });

  it("answers empty for a turn still running", () => {
    const t = projectTurns([user("u1", "q"), assistant("a1")], true)[0];
    expect(t?.outcome).toBe("running");
    expect(t === undefined ? "x" : turnFailureText(t)).toBe("");
  });

  it("still speaks for a stopped turn, which is what the footer glyph cannot", () => {
    // `cancelled` and `unknown` are `stopped`: not failures, but a footer glyph with
    // no words beside it is the same silence one severity down. The notice tints
    // itself yellow for these rather than red.
    expect(textOf([user("u1", "q"), assistant("a1", { turn_outcome: "unknown" })])).toBe(
      "The turn ended for a reason vibekit could not read.",
    );
  });
});
