// Which turns are open. The rule has three overriding layers and the ORDER
// between them is the whole design, so each is pinned against the others.
import { describe, it, expect, beforeEach } from "vitest";

import {
  isTurnOpen,
  setTurnOpen,
  openForSearch,
  clearSearchOpened,
  forgetChatFolds,
} from "./fold-state.js";
import type { Turn, TurnOutcome } from "./turns.js";

function turn(id: string, outcome: TurnOutcome = "completed"): Turn {
  return { id, n: 1, trigger: undefined, body: [], ts: 0, outcome, rewindTo: undefined };
}

/** A list of `n` completed turns, so index/total positioning is easy to state. */
function turns(n: number): Turn[] {
  return Array.from({ length: n }, (_, i) => turn(`t${String(i)}`));
}

beforeEach(() => {
  localStorage.clear();
  forgetChatFolds("c1");
});

describe("the automatic rule", () => {
  // Two rather than one: the previous turn is usually the context for the
  // current request, so folding it would force an expand on nearly every read.
  it("keeps exactly the last two turns open", () => {
    const list = turns(6);
    const open = list.map((t, i) => isTurnOpen("c1", t, i, list.length));
    expect(open).toEqual([false, false, false, false, true, true]);
  });

  it("keeps everything open when there are only two turns", () => {
    const list = turns(2);
    expect(list.map((t, i) => isTurnOpen("c1", t, i, list.length))).toEqual([true, true]);
  });

  it("keeps a single turn open", () => {
    expect(isTurnOpen("c1", turn("only"), 0, 1)).toBe(true);
  });
});

describe("sticky failure", () => {
  // Errors are the last thing that should hide themselves, so this is checked
  // BEFORE the position rule rather than after — a failure stays open however
  // far back it is.
  it.each(["failed", "interrupted"] as const)("never auto-folds a %s turn", (outcome) => {
    const list = [turn("bad", outcome), ...turns(10)];
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("keeps a running turn open wherever it sits", () => {
    const list = [turn("live", "running"), ...turns(10)];
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("still folds a completed turn in the same position", () => {
    const list = [turn("fine"), ...turns(10)];
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(false);
  });

  // The reader outranks the stickiness: a failure they deliberately folded away
  // stays folded, or the UI is arguing with them.
  it("lets the reader fold a failed turn anyway", () => {
    const t = turn("bad", "failed");
    setTurnOpen("c1", t.id, false);
    expect(isTurnOpen("c1", t, 0, 10)).toBe(false);
  });
});

describe("the reader's own choice", () => {
  it("opens an old turn and keeps it open", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("folds a recent turn against the two-newest rule", () => {
    const list = turns(6);
    setTurnOpen("c1", list[5]!.id, false);
    expect(isTurnOpen("c1", list[5]!, 5, list.length)).toBe(false);
  });

  it("persists across a fresh read of the module's state", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    // A different chat must not inherit it — overrides are per chat.
    expect(isTurnOpen("c2", list[0]!, 0, list.length)).toBe(false);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("is dropped with the chat", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    forgetChatFolds("c1");
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(false);
  });
});

describe("search reveal", () => {
  it("opens a turn holding a hit", () => {
    const list = turns(6);
    openForSearch("c1", list[0]!.id);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  // A search must not permanently rearrange the transcript as a side effect.
  it("re-folds when the search closes", () => {
    const list = turns(6);
    openForSearch("c1", list[0]!.id);
    expect(clearSearchOpened("c1")).toBe(true);
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(false);
  });

  // ...but a turn the reader opened by hand is left alone, and the ordering in
  // isTurnOpen is what implements that: the persisted override is consulted
  // BEFORE the search set.
  it("leaves a hand-opened turn open after the search closes", () => {
    const list = turns(6);
    setTurnOpen("c1", list[0]!.id, true);
    openForSearch("c1", list[0]!.id);
    clearSearchOpened("c1");
    expect(isTurnOpen("c1", list[0]!, 0, list.length)).toBe(true);
  });

  it("reports nothing to clear when no search reveal is active", () => {
    expect(clearSearchOpened("c1")).toBe(false);
  });

  it("does not persist a search reveal", () => {
    const list = turns(6);
    openForSearch("c1", list[0]!.id);
    expect(JSON.stringify(localStorage)).not.toContain(list[0]!.id);
  });
});
