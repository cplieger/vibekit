// ---------------------------------------------------------------------------
// Two grouping rules of the block dispatcher, which are deliberately OPPOSITE
// and were documented as the same thing.
//
// A tool GROUP is contiguous: `toolGroups` is keyed by container and any text
// block calls `closeToolGroup`, so a run of tool calls broken by prose becomes
// two groups. A SUBAGENT card is not: `st.subagents` is keyed by subtask id and
// nothing closes it, so a delegate's blocks join the card it opened however much
// the parent agent emitted in between.
//
// Four comments and three steering passages all claimed the subagent case was
// contiguous too. It never was, and nothing caught it, which is why these are
// tests rather than a corrected sentence.
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import type { Message, ToolCall } from "./types.js";

// The dispatcher's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the import is evaluated.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { buildAssistantBody, updateAssistantBody, finalizeAssistantBody, liveTextAnchor } =
  await import("./messages-blocks.js");

function text(t: string, subtask = ""): Record<string, unknown> {
  return { type: "text", text: t, agent_subtask_id: subtask };
}

function toolUse(id: string, subtask = ""): Record<string, unknown> {
  return { type: "tool_use", tool_call_id: id, agent_subtask_id: subtask };
}

function call(id: string, title: string): ToolCall {
  return { id, title, kind: "execute", status: "completed" } as unknown as ToolCall;
}

function render(blocks: Record<string, unknown>[], toolCalls: ToolCall[] = []): HTMLElement {
  const wrap = document.createElement("div");
  buildAssistantBody(
    wrap,
    {
      id: `m-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: toolCalls,
    } as unknown as Message,
    false,
  );
  return wrap;
}

/** The `.assistant-blocks` children, as a readable shape. */
function shape(wrap: HTMLElement): string[] {
  return [...(wrap.querySelector(".assistant-blocks")?.children ?? [])].map((e) => {
    if (e.classList.contains("subagent-block")) {
      return `card(${String((e as HTMLElement).dataset["subtask"])})`;
    }
    if (e.classList.contains("tool-group")) {
      return `group(${String(e.querySelectorAll(".tool-call").length)})`;
    }
    return `text(${String(e.textContent).trim().slice(0, 16)})`;
  });
}

describe("subagent grouping is keyed by subtask id, NOT by contiguity", () => {
  it("puts non-adjacent same-subtask blocks in ONE card", () => {
    const wrap = render([
      text("delegate first", "sub-A"),
      text("parent interleaved"),
      text("delegate second", "sub-A"),
    ]);
    const cards = wrap.querySelectorAll(".subagent-block");
    expect(cards).toHaveLength(1);
    const body = cards[0]?.querySelector(".subagent-body")?.textContent ?? "";
    expect(body).toContain("delegate first");
    expect(body).toContain("delegate second");
  });

  it("keeps two DIFFERENT subtasks in two cards", () => {
    const wrap = render([text("a", "sub-A"), text("b", "sub-B"), text("a again", "sub-A")]);
    expect(wrap.querySelectorAll(".subagent-block")).toHaveLength(2);
  });

  it("costs ORDER, not grouping: an interleaved parent block lands after the card", () => {
    // The card is appended at the subtask's FIRST appearance and later blocks are
    // appended into it, so prose that arrived between two of a delegate's halves
    // renders below the whole card. This is the accepted consequence of keying by
    // id; assert it so a future reader meets it as a decision, not a surprise.
    const wrap = render([
      text("delegate first", "sub-A"),
      text("parent interleaved"),
      text("delegate second", "sub-A"),
    ]);
    expect(shape(wrap)).toEqual(["card(sub-A)", "text(parent interleav)"]);
  });
});

describe("tool grouping IS contiguous, which is the contrast", () => {
  it("splits a run of tool calls broken by prose into two groups", () => {
    const wrap = render(
      [toolUse("t1"), toolUse("t2"), text("prose between"), toolUse("t3")],
      [call("t1", "Run Command"), call("t2", "Run Command"), call("t3", "Run Command")],
    );
    expect(shape(wrap)).toEqual(["group(2)", "text(prose between)", "group(1)"]);
  });

  it("keeps an unbroken run in one group", () => {
    const wrap = render(
      [toolUse("t1"), toolUse("t2"), toolUse("t3")],
      [call("t1", "Run Command"), call("t2", "Run Command"), call("t3", "Run Command")],
    );
    expect(shape(wrap)).toEqual(["group(3)"]);
  });
});

// ---------------------------------------------------------------------------
// A mounted block tracks the store's text whether or not it was mounted live.
//
// The fast path for a live turn is the per-block signal effect: a chunk writes
// the signal and the DOM updates with no reconcile. That effect only exists for
// a block the renderer judged LIVE, and `store.appendChunk` falls back to
// scheduling a full repaint for any block with no signal — so the repaint is the
// only channel left for a block whose liveness was misjudged, and it used to
// mount new blocks and never revisit a mounted one. The measured symptom was an
// assistant reply frozen at its first streamed chunk (`<p>Here's v</p>`), with
// no ellipsis, healed only by reloading the page.
//
// Liveness is misjudged cheaply: `isLikelyLiveStreaming` requires the chat's
// `thinking` flag, and any mid-turn event that clears it (a `.kiro/agents` parse
// error arriving at session construction was the real one) makes every later
// block of that turn mount for replay.
// ---------------------------------------------------------------------------

describe("a mounted block picks up text that arrived after it mounted", () => {
  function growingMessage(blocks: Record<string, unknown>[]): Message {
    return {
      id: "m-grow",
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  it("renders a text block's tail on the next repaint, mounted for REPLAY", () => {
    const wrap = document.createElement("div");
    const blocks = [text("Here's v")];
    const m = growingMessage(blocks);
    // live: false — the frozen case.
    buildAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".message.assistant")?.textContent).toBe("Here's v");

    blocks[0]!["text"] = "Here's version two of the plan.";
    updateAssistantBody(wrap, m, false);
    // The grown bubble is on the incremental renderer now, which holds its last
    // character provisionally until the next write or the finalize — the same
    // contract a live turn has. What matters here is that the tail arrived at all.
    expect(wrap.querySelector(".message.assistant")?.textContent).toContain(
      "Here's version two of the plan",
    );

    finalizeAssistantBody(m.id);
    expect(wrap.querySelector(".message.assistant")?.textContent).toBe(
      "Here's version two of the plan.",
    );
  });

  it("renders a thinking block's tail the same way", () => {
    const wrap = document.createElement("div");
    const blocks: Record<string, unknown>[] = [
      { type: "thinking", thinking: "I", agent_subtask_id: "" },
    ];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".reasoning-body")?.textContent).toBe("I");

    blocks[0]!["thinking"] = "I should read the file first.";
    updateAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".reasoning-body")?.textContent).toBe(
      "I should read the file first.",
    );
  });

  it("adds nothing when the block did not grow", () => {
    // The sweep runs on every repaint of every mounted block, so a settled
    // transcript must cost a length comparison and produce no DOM churn.
    const wrap = document.createElement("div");
    const blocks = [text("settled")];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, false);
    const before = wrap.querySelector(".message.assistant")?.innerHTML;

    updateAssistantBody(wrap, m, false);
    updateAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".message.assistant")?.innerHTML).toBe(before);
  });

  it("reaches a block inside a subagent card too", () => {
    const wrap = document.createElement("div");
    const blocks = [text("delegate ", "sub-A")];
    const m = growingMessage(blocks);
    buildAssistantBody(wrap, m, false);

    blocks[0]!["text"] = "delegate finished its walk.";
    updateAssistantBody(wrap, m, false);
    expect(wrap.querySelector(".subagent-body")?.textContent).toContain(
      "delegate finished its walk",
    );
  });
});

// ---------------------------------------------------------------------------
// The streaming caret has an END, and there is exactly one of it.
//
// The caret is a single CSS pseudo-element on `.message.assistant.streaming`,
// added by `buildAssistantBubble(initial, live)` and removed only by that
// bubble's `end()`. `renderRange` decides liveness as `live && i === lastIdx`,
// evaluated once when a block MOUNTS, and mounting is append-only — so a turn
// shaped "prose → tool call → prose → tool call → prose" opened three text
// blocks, each of which was the tail when it mounted, and nothing ever sealed
// the one that stopped being the tail. The reader saw a caret per block for the
// whole turn (3, 4, 5 of them), and a leaked class also pins the transcript's
// `:has(.message.assistant.streaming)` 50dvh viewport reserve on permanently.
// ---------------------------------------------------------------------------

describe("exactly one streaming caret, and it ends", () => {
  const CARET = ".message.assistant.streaming";

  function liveMessage(blocks: Record<string, unknown>[]): Message {
    return {
      id: `m-caret-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  it("moves the caret to the new tail instead of accumulating one per block", () => {
    const wrap = document.createElement("div");
    const blocks = [text("first paragraph")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    // Three more text blocks arrive, each becoming the tail in turn. This is the
    // ordinary shape of a turn that interleaves prose and tool calls.
    for (const next of ["second paragraph", "third paragraph", "fourth paragraph"]) {
      blocks.push(text(next));
      updateAssistantBody(wrap, m, true);
      expect(wrap.querySelectorAll(CARET)).toHaveLength(1);
    }
    // ...and it is the LAST bubble that carries it.
    const bubbles = [...wrap.querySelectorAll(".message.assistant")];
    expect(bubbles).toHaveLength(4);
    expect(bubbles.at(-1)?.classList.contains("streaming")).toBe(true);
  });

  it("seals the previous text block when the new tail is a tool call", () => {
    const wrap = document.createElement("div");
    const blocks = [text("about to run something")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    blocks.push(toolUse("t1"));
    updateAssistantBody(wrap, m, true);
    // Prose that has been superseded by a tool call is not streaming; a tool
    // card carries its own status affordance and never a caret.
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("leaves no caret behind after finalize", () => {
    const wrap = document.createElement("div");
    const blocks = [text("one")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, true);
    blocks.push(text("two"));
    updateAssistantBody(wrap, m, true);

    finalizeAssistantBody(m.id);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("is idempotent: finalize twice, and a repaint after it, stay caret-free", () => {
    const wrap = document.createElement("div");
    const blocks = [text("one")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, true);

    finalizeAssistantBody(m.id);
    finalizeAssistantBody(m.id);
    updateAssistantBody(wrap, m, false);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("never opens a caret on a replayed message", () => {
    const wrap = document.createElement("div");
    const blocks = [text("a"), toolUse("t1"), text("b")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, false);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });

  it("holds one caret across the whole message when a subagent streams", () => {
    // `mountText` is the same function for a top-level bubble and one inside a
    // SubagentBlock body, so a delegate's tail block used to add a caret of its
    // own nested inside the collapsible box.
    const wrap = document.createElement("div");
    const blocks = [text("parent prose")];
    const m = liveMessage(blocks);
    buildAssistantBody(wrap, m, true);

    blocks.push(text("delegate prose", "sub-A"));
    updateAssistantBody(wrap, m, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    blocks.push(text("parent again"));
    updateAssistantBody(wrap, m, true);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(1);

    finalizeAssistantBody(m.id);
    expect(wrap.querySelectorAll(CARET)).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// Which bubble Following pins to.
//
// The pin puts the anchor's bottom at the viewport's bottom, so an anchor that
// sits ABOVE the live edge parks the reader mid-transcript with the newest work
// off-screen — and, before the scroll listener learned to ignore the controller's
// own landing, latched the auto-scroll off entirely.
// ---------------------------------------------------------------------------
describe("liveTextAnchor", () => {
  const CARETS = ".message.assistant.streaming";

  function liveMsg(blocks: Record<string, unknown>[]): Message {
    return {
      id: `m-${String(Math.random())}`,
      role: "assistant",
      content: "",
      blocks,
      tool_calls: [],
    } as unknown as Message;
  }

  it("has no anchor when nothing is streaming", () => {
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, liveMsg([text("done")]), false);
    expect(liveTextAnchor(wrap)).toBeNull();
  });

  it("takes the LAST live bubble, not the first in document order", () => {
    // One turn, two assistant messages: a mid-turn model switch splits it, and
    // the seal that drops `.streaming` is per-MESSAGE state, so the earlier
    // message's trailing bubble stays marked until the turn finalizes. A
    // `querySelector` would hand back that one, which is the older of the two.
    const wrap = document.createElement("div");
    buildAssistantBody(wrap, liveMsg([text("the earlier message")]), true);
    buildAssistantBody(wrap, liveMsg([text("the message still streaming")]), true);

    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(live).toHaveLength(2);
    expect(liveTextAnchor(wrap)).toBe(live[1]);
  });

  it("has no anchor when only a DELEGATE is streaming", () => {
    // The dispatcher seals the parent's bubble when the delegate's block
    // appends, so this is the ordinary shape of a running subagent. Null is the
    // answer rather than the delegate's bubble: that bubble sits inside a box
    // collapsed to `height: 0` with `overflow: hidden`, which clips it without
    // taking it out of layout, so its offsets describe a position the reader
    // cannot see. The document bottom is where the box's tail and footer are.
    const wrap = document.createElement("div");
    const blocks = [text("parent prose")];
    const m = liveMsg(blocks);
    buildAssistantBody(wrap, m, true);
    blocks.push(text("delegate prose", "sub-A"));
    updateAssistantBody(wrap, m, true);

    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(live).toHaveLength(1);
    expect(live[0]?.closest(".subagent-body")).not.toBeNull();
    expect(liveTextAnchor(wrap)).toBeNull();
  });

  it("prefers a top-level bubble over a later delegate bubble", () => {
    // Two messages again, and this is the case that makes "last match" and
    // "skip delegates" two separate rules rather than one: the delegate's bubble
    // is the LAST in document order while the top-level one is what the reader
    // is following.
    const wrap = document.createElement("div");
    const withDelegate = liveMsg([text("older parent prose")]);
    const blocks = withDelegate.blocks as unknown as Record<string, unknown>[];
    buildAssistantBody(wrap, withDelegate, true);
    blocks.push(text("delegate prose", "sub-A"));
    updateAssistantBody(wrap, withDelegate, true);
    buildAssistantBody(wrap, liveMsg([text("newer parent prose")]), true);

    const live = [...wrap.querySelectorAll<HTMLElement>(CARETS)];
    expect(live).toHaveLength(2);
    expect(live[1]?.closest(".subagent-body")).toBeNull();
    expect(liveTextAnchor(wrap)).toBe(live[1]);
  });
});
