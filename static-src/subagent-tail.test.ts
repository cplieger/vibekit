// ---------------------------------------------------------------------------
// Tests for subagent-tail.ts — the delegate card's last few lines, projected out
// of the chat's messages.
//
// The projection is a pure function over `Message[]`, so these cases are DATA and
// touch no DOM at all: what they pin is which lines a card would show and which
// blocks its subscription has to watch. The old tail was a MutationObserver over
// the card's rendered body, and its equivalent cases had to build real bubbles,
// tool cards and reasoning traces to say anything.
//
// The binding's cases at the foot are the reactive half, driven through the real
// store: the whole point of deriving the tail per delegate is that ONE delta
// repaints ONE tail.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { subagentTail, bindSubagentTail, TAIL_LINES } from "./subagent-tail.js";
import { appendChunk, setSessions, setActive } from "./store.js";
import type { Block, Message, Session, ToolCall } from "./types.js";

const SUB = "u-1";

function msg(id: string, blocks: Block[], tools: ToolCall[] = []): Message {
  return { id, role: "assistant", ts: 1, content: "", blocks, tool_calls: tools };
}

function text(t: string, subtask?: string): Block {
  return { type: "text", text: t, ...(subtask === undefined ? {} : { agent_subtask_id: subtask }) };
}

function thinking(t: string, subtask?: string): Block {
  return {
    type: "thinking",
    thinking: t,
    ...(subtask === undefined ? {} : { agent_subtask_id: subtask }),
  };
}

function toolUse(id: string, subtask?: string): Block {
  return {
    type: "tool_use",
    tool_call_id: id,
    ...(subtask === undefined ? {} : { agent_subtask_id: subtask }),
  };
}

function call(id: string, title: string, subtask: string): ToolCall {
  return { id, title, kind: "other", status: "completed", ts: 0, agent_subtask_id: subtask };
}

/** The delegate's own invocation call, whose title names the card and not the tail. */
function invocation(id: string, subtask: string): ToolCall {
  return {
    id,
    title: "Sub-agent: introspect",
    kind: "other",
    status: "in_progress",
    ts: 0,
    agent_subtask_id: subtask,
  };
}

describe("subagentTail", () => {
  it("takes the LAST lines of a block, not its first", () => {
    const t = subagentTail([msg("m1", [text("one\ntwo\nthree\nfour\nfive", SUB)])], SUB);
    expect(t.lines).toEqual(["three", "four", "five"]);
  });

  it("caps at TAIL_LINES by default and honours an explicit want", () => {
    const blocks = [text("a\nb\nc\nd", SUB)];
    expect(subagentTail([msg("m1", blocks)], SUB).lines).toHaveLength(TAIL_LINES);
    expect(subagentTail([msg("m1", blocks)], SUB, 1).lines).toEqual(["d"]);
    expect(subagentTail([msg("m1", blocks)], SUB, 0).lines).toEqual([]);
  });

  it("keeps one line per BLOCK, so two blocks never glue into one", () => {
    // The defect the DOM walk had twice: a body's `textContent` carries no
    // separator between rendered blocks, so the last three lines collapsed into one
    // line of glued words that the card then clipped at its own width.
    const t = subagentTail([msg("m1", [text("first", SUB), text("second", SUB)])], SUB);
    expect(t.lines).toEqual(["first", "second"]);
  });

  it("reads a thinking block's trace as well as a text block's prose", () => {
    const t = subagentTail(
      [msg("m1", [thinking("I need to check the build first.", SUB), text("Checking.", SUB)])],
      SUB,
    );
    expect(t.lines).toEqual(["I need to check the build first.", "Checking."]);
  });

  it("collapses whitespace runs and drops blank lines", () => {
    const t = subagentTail([msg("m1", [text("  spaced   out  \n\n\n   \nlast\n", SUB)])], SUB);
    expect(t.lines).toEqual(["spaced out", "last"]);
  });

  it("ignores the parent stream and every OTHER delegate", () => {
    const t = subagentTail(
      [msg("m1", [text("parent prose"), text("mine", SUB), text("theirs", "u-2")])],
      SUB,
    );
    expect(t.lines).toEqual(["mine"]);
  });

  it("answers nothing for an empty subtask id", () => {
    // "" is the parent stream's own stamp, so a tail for it would be the whole
    // conversation's last lines under a delegate's name.
    expect(subagentTail([msg("m1", [text("parent prose")])], "").lines).toEqual([]);
  });

  it("takes a tool call's TITLE as its line, and never the invocation's", () => {
    // A delegate that is running commands has no prose to show, and a frozen tail is
    // the one thing this region must not be. The invocation is excluded because the
    // card's own header already names it.
    const t = subagentTail(
      [
        msg(
          "m1",
          [toolUse("inv", SUB), text("looking", SUB), toolUse("t1", SUB)],
          [invocation("inv", SUB), call("t1", "Grep Search", SUB)],
        ),
      ],
      SUB,
    );
    expect(t.lines).toEqual(["looking", "Grep Search"]);
  });

  it("skips a tool block whose call has not arrived yet", () => {
    // Out-of-order SSE: the block is in the array before its call is, and there is
    // no honest line to show for it.
    const t = subagentTail([msg("m1", [text("mine", SUB), toolUse("t-late", SUB)])], SUB);
    expect(t.lines).toEqual(["mine"]);
  });

  it("spans two messages, because a mid-turn model switch splits a turn", () => {
    const t = subagentTail(
      [msg("m1", [text("one\ntwo", SUB)]), msg("m2", [text("three", SUB)])],
      SUB,
    );
    expect(t.lines).toEqual(["one", "two", "three"]);
  });

  it("names the blocks it read, and only those", () => {
    // The subscription set. A block ABOVE the ones the walk took cannot reach the
    // tail — three later lines already fill it — so subscribing to it would repaint
    // this card for growth nobody can see.
    const t = subagentTail([msg("m1", [text("old\nlines\nhere", SUB), text("a\nb\nc", SUB)])], SUB);
    expect(t.lines).toEqual(["a", "b", "c"]);
    expect(t.sources).toEqual([
      { messageID: "m1", blockIndex: 1, thinking: false, full: "a\nb\nc" },
    ]);
  });

  it("subscribes to a still-EMPTY trailing block, which is the streaming one", () => {
    // It contributes no line yet and is exactly the block the next delta lands in.
    const t = subagentTail([msg("m1", [text("done", SUB), text("", SUB)])], SUB);
    expect(t.lines).toEqual(["done"]);
    expect(t.sources.map((s) => s.blockIndex)).toEqual([0, 1]);
  });

  it("marks a thinking source as thinking, so the caller watches the right signal", () => {
    const t = subagentTail([msg("m1", [thinking("hmm", SUB)])], SUB);
    expect(t.sources).toEqual([{ messageID: "m1", blockIndex: 0, thinking: true, full: "hmm" }]);
  });
});

// ---------------------------------------------------------------------------
// The binding, over the real store.
// ---------------------------------------------------------------------------

const CHAT = "c-tail";

function session(messages: Message[]): Session {
  return {
    id: CHAT,
    name: "tail",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: messages.length,
    messages,
    has_more: false,
    thinking: true,
    working_label: "Thinking",
  } as Session;
}

describe("bindSubagentTail", () => {
  beforeEach(() => {
    setSessions([]);
    setActive("");
  });

  it("paints the delegate's current tail on install", () => {
    setSessions([session([msg("m1", [text("first line", SUB)])])]);
    const paint = vi.fn();
    const stop = bindSubagentTail(CHAT, SUB, paint);
    expect(paint).toHaveBeenCalledTimes(1);
    expect(paint.mock.calls[0]?.[0]).toEqual(["first line"]);
    stop();
  });

  it("repaints ONE delegate's tail for that delegate's own delta", () => {
    // The whole reason the tail is derived per delegate. The sibling's walk re-runs
    // — a new block is a fact no per-block signal can carry — but its lines are
    // unchanged, so its card is not touched.
    setSessions([session([msg("m1", [text("mine", SUB), text("theirs", "u-2")])])]);
    const mine = vi.fn();
    const theirs = vi.fn();
    const stopMine = bindSubagentTail(CHAT, SUB, mine);
    const stopTheirs = bindSubagentTail(CHAT, "u-2", theirs);
    mine.mockClear();
    theirs.mockClear();

    appendChunk(CHAT, "m1", " and more", false, 0, SUB);

    expect(mine).toHaveBeenCalledTimes(1);
    expect(mine.mock.calls[0]?.[0]).toEqual(["mine and more"]);
    expect(theirs).not.toHaveBeenCalled();
    stopMine();
    stopTheirs();
  });

  it("picks up a NEW block of its delegate's, on the store's own tick", async () => {
    // The case the per-block signals cannot answer on their own: nothing has read
    // block 1 yet, so no signal for it exists to fire. The chat's transcript
    // version carries it instead, and that bump is COALESCED per microtask
    // (`scheduleMessages`), which is why this one awaits and the delta case above
    // does not — a per-block write lands in its own tick.
    setSessions([session([msg("m1", [text("one", SUB)])])]);
    const paint = vi.fn();
    const stop = bindSubagentTail(CHAT, SUB, paint);
    paint.mockClear();

    appendChunk(CHAT, "m1", "two", false, 1, SUB);
    await Promise.resolve();

    expect(paint).toHaveBeenCalledTimes(1);
    expect(paint.mock.calls[0]?.[0]).toEqual(["one", "two"]);
    stop();
  });

  it("stops painting once disposed", () => {
    setSessions([session([msg("m1", [text("one", SUB)])])]);
    const paint = vi.fn();
    bindSubagentTail(CHAT, SUB, paint)();
    paint.mockClear();

    appendChunk(CHAT, "m1", " more", false, 0, SUB);

    expect(paint).not.toHaveBeenCalled();
  });
});
