// The assistant turn's action row: copy, export, and the raw-markdown toggle.
//
// The toggle's two mechanical constraints are what most of this file pins. It
// must ADD a sibling rather than replace the rendered children, because
// reconcile re-runs the body updater on every repaint; and it must hide with
// `.hidden`, because find-in-chat's walker prunes `.hidden` subtrees and would
// otherwise count every match twice.
import { describe, it, expect, vi } from "vitest";
import type { Message } from "./types.js";

const dispatch = vi.fn(
  (_text: string, opts?: { silent?: boolean; onSuccess?: () => void }): Promise<void> => {
    opts?.onSuccess?.();
    return Promise.resolve();
  },
);

let active: { id: string; name: string; messages: Message[] } = {
  id: "c1",
  name: "chat",
  messages: [],
};

vi.mock("./store.js", () => ({
  getActive: () => active,
  getActiveId: () => active.id,
}));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: {
    dispatch: (text: string, opts?: { silent?: boolean; onSuccess?: () => void }) =>
      dispatch(text, opts),
  },
}));
const downloadChatExport = vi.fn();
vi.mock("./chat-export.js", () => ({ downloadChatExport }));

const { attachTurnActions, initTurnActionCallbacks, copyWithFeedback } =
  await import("./messages-turn-actions.js");
const { KEY_ATTR } = await import("./reconcile.js");

// The real injection point is messages.ts's CSP-safe <template> clone; a plain
// element is enough here and keeps the fixture DOM readable.
initTurnActionCallbacks({
  svgTemplate: (markup: string) => () => {
    const span = document.createElement("span");
    span.dataset["icon"] = String(markup.length);
    return span;
  },
});

interface Fixture {
  wrap: HTMLElement;
  blocks: HTMLElement;
  bubble: HTMLDivElement;
}

/** The DOM shape buildAssistant + buildAssistantBody produce for one turn. */
function fixture(msg: Message, rendered = "rendered reply"): Fixture {
  document.body.innerHTML = "";
  const wrap = document.createElement("div");
  wrap.className = "msg-wrap msg-wrap-assistant";
  wrap.setAttribute(KEY_ATTR, msg.id);
  const blocks = document.createElement("div");
  blocks.className = "assistant-blocks";
  const row = document.createElement("div");
  row.className = "msg-row";
  const bubble = document.createElement("div");
  bubble.className = "message assistant";
  bubble.textContent = rendered;
  row.appendChild(bubble);
  blocks.appendChild(row);
  wrap.appendChild(blocks);
  document.body.appendChild(wrap);
  active = { id: "c1", name: "chat", messages: [msg] };
  return { wrap, blocks, bubble };
}

function assistant(over: Partial<Message> = {}): Message {
  return { id: "m1", role: "assistant", ts: 1, content: "# hi\n\nsource text", ...over };
}

function buttons(wrap: HTMLElement): HTMLButtonElement[] {
  return [...wrap.querySelectorAll<HTMLButtonElement>(".turn-actions .turn-action-btn")];
}

function sourceButton(wrap: HTMLElement): HTMLButtonElement {
  const b = wrap.querySelector<HTMLButtonElement>(".turn-action-btn[aria-pressed]");
  if (b === null) {
    throw new Error("no source toggle");
  }
  return b;
}

function raw(wrap: HTMLElement): HTMLElement | null {
  return wrap.querySelector(".turn-raw");
}

describe("attachTurnActions", () => {
  it("attaches one row with copy, markdown, source, chat id and export", () => {
    const f = fixture(assistant());
    attachTurnActions(f.bubble);
    expect(f.wrap.querySelectorAll(".turn-actions")).toHaveLength(1);
    expect(buttons(f.wrap)).toHaveLength(5);
  });

  it("is idempotent across repeated finalize / mount passes", () => {
    const f = fixture(assistant());
    attachTurnActions(f.bubble);
    attachTurnActions(f.bubble);
    attachTurnActions(f.bubble);
    expect(f.wrap.querySelectorAll(".turn-actions")).toHaveLength(1);
    expect(buttons(f.wrap)).toHaveLength(5);
  });

  it("attaches nothing for a turn with no text at all", () => {
    const f = fixture(assistant({ content: "" }), "");
    attachTurnActions(f.bubble);
    expect(f.wrap.querySelector(".turn-actions")).toBeNull();
  });

  it("copies the stored markdown, not the rendered text", () => {
    const f = fixture(assistant({ content: "**bold**" }), "bold");
    attachTurnActions(f.bubble);
    buttons(f.wrap)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });

  it("copies the rendered text for copy-as-text", () => {
    const f = fixture(assistant({ content: "**bold**" }), "bold");
    attachTurnActions(f.bubble);
    buttons(f.wrap)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("bold", expect.anything());
  });
});

describe("the raw-markdown toggle", () => {
  it("starts closed and reports so on the button", () => {
    const f = fixture(assistant());
    attachTurnActions(f.bubble);
    expect(raw(f.wrap)).toBeNull();
    expect(sourceButton(f.wrap).getAttribute("aria-pressed")).toBe("false");
  });

  it("shows the source and hides the rendered bubble with .hidden", () => {
    const f = fixture(assistant({ content: "# hi" }));
    attachTurnActions(f.bubble);
    sourceButton(f.wrap).click();
    expect(raw(f.wrap)?.textContent).toBe("# hi");
    // `.hidden` specifically: find-in-chat prunes it, so exactly one of the two
    // renderings is searchable and matches are never double-counted.
    expect(f.bubble.classList.contains("hidden")).toBe(true);
    expect(raw(f.wrap)?.classList.contains("hidden")).toBe(false);
    expect(sourceButton(f.wrap).getAttribute("aria-pressed")).toBe("true");
  });

  it("toggles back to the rendering", () => {
    const f = fixture(assistant());
    attachTurnActions(f.bubble);
    const btn = sourceButton(f.wrap);
    btn.click();
    btn.click();
    expect(raw(f.wrap)?.classList.contains("hidden")).toBe(true);
    expect(f.bubble.classList.contains("hidden")).toBe(false);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
  });

  it("ADDS the source beside the rendering rather than replacing it", () => {
    // The replacement shape is what a repaint undoes; this is the structural
    // guarantee that makes the toggle survive one.
    const f = fixture(assistant());
    attachTurnActions(f.bubble);
    sourceButton(f.wrap).click();
    expect(f.bubble.isConnected).toBe(true);
    expect(raw(f.wrap)?.parentElement).toBe(f.blocks);
  });

  it("survives a repaint that appends a new block", () => {
    const f = fixture(assistant());
    attachTurnActions(f.bubble);
    sourceButton(f.wrap).click();
    // What updateAssistantBody does on every repaint: append newly-arrived
    // blocks into `.assistant-blocks`. Nothing here may disturb the raw view.
    const later = document.createElement("div");
    later.className = "tool-call";
    f.blocks.appendChild(later);
    attachTurnActions(f.bubble);
    expect(raw(f.wrap)).not.toBeNull();
    expect(raw(f.wrap)?.classList.contains("hidden")).toBe(false);
    expect(f.wrap.querySelectorAll(".turn-raw")).toHaveLength(1);
    expect(f.wrap.querySelectorAll(".turn-actions")).toHaveLength(1);
  });

  it("re-reads the source at click time, so a later sanitized content wins", () => {
    // The row attaches once and is idempotent, so a source captured at attach
    // time would go stale the moment message_appended replaced the content.
    const f = fixture(assistant({ content: "streamed" }));
    attachTurnActions(f.bubble);
    active.messages = [assistant({ content: "server sanitized" })];
    sourceButton(f.wrap).click();
    expect(raw(f.wrap)?.textContent).toBe("server sanitized");
  });

  it("falls back to the block text when the message carries no top-level content", () => {
    const f = fixture(
      assistant({
        content: "",
        blocks: [
          { type: "text", text: "first" },
          { type: "tool_use", tool_call_id: "t1" },
          { type: "text", text: "second" },
        ],
      }),
    );
    attachTurnActions(f.bubble);
    sourceButton(f.wrap).click();
    expect(raw(f.wrap)?.textContent).toBe("first\n\nsecond");
  });

  it("renames itself so the button says what the next press does", () => {
    const f = fixture(assistant());
    attachTurnActions(f.bubble);
    const btn = sourceButton(f.wrap);
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
    btn.click();
    expect(btn.getAttribute("aria-label")).toBe("View rendered reply");
    btn.click();
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
  });
});

describe("copyWithFeedback", () => {
  it("dispatches the copy silently and flashes the button", () => {
    const btn = document.createElement("button");
    copyWithFeedback(btn, "hello");
    expect(dispatch).toHaveBeenCalledWith("hello", expect.objectContaining({ silent: true }));
    expect(btn.classList.contains("copied")).toBe(true);
  });

  it("does nothing for empty text", () => {
    const btn = document.createElement("button");
    copyWithFeedback(btn, "");
    expect(dispatch).not.toHaveBeenCalled();
  });

  it("clears the flash after the confirmation window", () => {
    vi.useFakeTimers();
    try {
      const btn = document.createElement("button");
      copyWithFeedback(btn, "hello");
      vi.advanceTimersByTime(1600);
      expect(btn.classList.contains("copied")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });
});
