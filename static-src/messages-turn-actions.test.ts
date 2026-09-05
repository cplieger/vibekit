// The turn footer's action buttons: copy, export, and the raw-markdown toggle.
//
// The toggle's three mechanical constraints are what most of this file pins. It
// must ADD a sibling rather than replace the rendered children, because
// reconcile re-runs the body updater on every repaint; it must hide with
// `.hidden`, because find-in-chat's walker prunes `.hidden` subtrees and would
// otherwise count every match twice; and it must hide the block CONTAINER
// rather than its children, so a block arriving after the toggle cannot leak
// into the raw view. New with the footer move: the buttons are per TURN and
// identical open or folded, the toggle operates on whichever surface is
// mounted (body open, face folded), and crossing the fold resets it.
import { describe, it, expect, vi, afterEach } from "vitest";
import type { Message } from "./types.js";
import type { Turn } from "./turns.js";

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
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./actions/messages.js", () => ({
  copyClipboard: {
    dispatch: (text: string, opts?: { silent?: boolean; onSuccess?: () => void }) =>
      dispatch(text, opts),
  },
}));
const downloadChatExport = vi.fn();
vi.mock("./chat-export.js", () => ({ downloadChatExport }));

const {
  mountTurnFooterActions,
  resetTurnSourceView,
  initTurnActionCallbacks,
  initTurnActionsBodyProbe,
  syncSourceView,
  copyWithFeedback,
} = await import("./messages-turn-actions.js");

// The real injection point is messages.ts's CSP-safe <template> clone; a plain
// element is enough here and keeps the fixture DOM readable.
initTurnActionCallbacks({
  svgTemplate: (markup: string) => () => {
    const span = document.createElement("span");
    span.dataset["icon"] = String(markup.length);
    return span;
  },
});

// The body probe is MODULE state and a plain arrow, so `mockReset` cannot restore
// it: a case arming a windowed body must not leave every later case windowed, and a
// case that FAILS while armed would. Wired here, not in each case.
afterEach(() => {
  initTurnActionsBodyProbe(() => true);
});

interface Fixture {
  card: HTMLElement;
  footer: HTMLElement;
  body: HTMLElement;
  blocks: HTMLElement;
  bubble: HTMLDivElement;
  tool: HTMLElement;
  turn: Turn;
}

/** The DOM shape buildTurn produces: a card with a body (one assistant wrap
 *  holding a text bubble and a tool card inside its `.assistant-blocks`
 *  region) and a ledger footer. */
function fixture(msg: Message, rendered = "rendered reply", outcome = "completed"): Fixture {
  document.body.innerHTML = "";
  const card = document.createElement("div");
  card.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  const wrap = document.createElement("div");
  wrap.className = "msg-wrap msg-wrap-assistant";
  const blocks = document.createElement("div");
  blocks.className = "assistant-blocks";
  const row = document.createElement("div");
  row.className = "msg-row";
  const bubble = document.createElement("div");
  bubble.className = "message assistant";
  bubble.textContent = rendered;
  row.appendChild(bubble);
  blocks.appendChild(row);
  const tool = document.createElement("div");
  tool.className = "tool-group";
  tool.textContent = "ran a command";
  blocks.appendChild(tool);
  wrap.appendChild(blocks);
  body.appendChild(wrap);
  card.appendChild(body);
  const footer = document.createElement("div");
  footer.className = "turn-footer";
  card.appendChild(footer);
  document.body.appendChild(card);
  active = { id: "c1", name: "chat", messages: [msg] };
  const turn: Turn = {
    id: msg.id,
    n: 1,
    trigger: undefined,
    body: [msg],
    ts: msg.ts,
    outcome: outcome as Turn["outcome"],
    rewindTo: undefined,
  };
  return { card, footer, body, blocks, bubble, tool, turn };
}

/** Fold the fixture card and give it the face a folded turn renders: the
 *  prose bubble plus the surface the raw toggle swaps against. */
function fold(f: Fixture, faceText = "face prose"): HTMLElement {
  f.card.setAttribute("data-folded", "");
  const face = document.createElement("div");
  face.className = "turn-face";
  const prose = document.createElement("div");
  prose.className = "message assistant turn-face-prose";
  prose.textContent = faceText;
  face.appendChild(prose);
  f.footer.before(face);
  return face;
}

function assistant(over: Partial<Message> = {}): Message {
  return { id: "m1", role: "assistant", ts: 1, content: "# hi\n\nsource text", ...over };
}

function buttons(footer: HTMLElement): HTMLButtonElement[] {
  return [
    ...footer.querySelectorAll<HTMLButtonElement>(".turn-actions-buttons button.turn-action-btn"),
  ];
}

function sourceButton(footer: HTMLElement): HTMLButtonElement {
  const b = footer.querySelector<HTMLButtonElement>(".turn-action-btn[aria-pressed]");
  if (b === null) {
    throw new Error("no source toggle");
  }
  return b;
}

function raw(card: HTMLElement): HTMLElement | null {
  return card.querySelector(".turn-raw");
}

describe("mountTurnFooterActions", () => {
  it("mounts one slot with copy, markdown, source, chat id and export", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelectorAll(".turn-actions-buttons")).toHaveLength(1);
    expect(buttons(f.footer)).toHaveLength(5);
  });

  it("keeps copy direct and groups the four secondary actions under More", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);

    const slot = f.footer.querySelector<HTMLElement>(".turn-actions-buttons")!;
    const more = slot.querySelector<HTMLDetailsElement>(".turn-actions-more")!;
    expect(slot.querySelectorAll(":scope > button.turn-action-btn")).toHaveLength(1);
    expect(more.querySelector(":scope > summary")?.getAttribute("aria-label")).toBe(
      "More turn actions",
    );
    expect(
      more.querySelectorAll(":scope > .turn-actions-secondary > button.turn-action-btn"),
    ).toHaveLength(4);
  });

  it("closes the mobile overflow after an action", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);

    const more = f.footer.querySelector<HTMLDetailsElement>(".turn-actions-more")!;
    const summary = more.querySelector<HTMLElement>(":scope > summary")!;
    summary.click();
    expect(more.open).toBe(true);

    more.querySelector<HTMLButtonElement>('[aria-label="Copy as markdown"]')?.click();
    expect(more.open).toBe(false);
  });

  it("is idempotent across repeated paint passes", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelectorAll(".turn-actions-buttons")).toHaveLength(1);
    expect(buttons(f.footer)).toHaveLength(5);
  });

  it("mounts nothing for a turn with no text at all", () => {
    const f = fixture(assistant({ content: "" }), "");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelector(".turn-actions-buttons")).toBeNull();
  });

  it("mounts nothing while the turn is still running", () => {
    const f = fixture(assistant(), "rendered reply", "running");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(f.footer.querySelector(".turn-actions-buttons")).toBeNull();
  });

  it("mounts once the turn settles, on the pass after the running one", () => {
    const f = fixture(assistant(), "rendered reply", "running");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    mountTurnFooterActions(f.footer, f.card, { ...f.turn, outcome: "completed" });
    expect(buttons(f.footer)).toHaveLength(5);
  });

  it("copies the stored markdown, not the rendered text", () => {
    const f = fixture(assistant({ content: "**bold**" }), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });

  it("copies the rendered text for copy-as-text", () => {
    const f = fixture(assistant({ content: "**bold**" }), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("bold", expect.anything());
  });

  it("joins a split turn's assistant messages for copy-as-markdown", () => {
    // A mid-turn model switch splits one turn across two assistant messages;
    // the buttons are the TURN's, so the copy carries both halves.
    const f = fixture(assistant({ content: "first half" }));
    const second: Message = { id: "m2", role: "assistant", ts: 2, content: "second half" };
    f.turn.body.push(second);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[1]?.click();
    expect(dispatch).toHaveBeenCalledWith("first half\n\nsecond half", expect.anything());
  });

  it("copies the face prose for copy-as-text when folded", () => {
    const f = fixture(assistant({ content: "**bold**" }), "bold");
    mountTurnFooterActions(f.footer, f.card, f.turn);
    f.body.remove(); // a folded stub keeps no body
    fold(f, "face words");
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("face words", expect.anything());
  });

  it("copies the FACE, not a partial body, when the window holds only part of the turn", () => {
    // The body is mounted but WINDOWED, so its bubbles are a hole rather than the
    // turn's answer. The face is what the turn's final answer is, and the fold already
    // relies on that; copying the body here would hand over whichever blocks the
    // reader's scroll position happened to leave mounted.
    const f = fixture(assistant({ content: "**bold**" }), "bold");
    initTurnActionsBodyProbe(() => false);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    fold(f, "face words");
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("face words", expect.anything());
  });

  it("falls back to the MARKDOWN for a partial body with no face at all", () => {
    // A partial body and no face is the shape a running or faceless turn has: the store
    // is then the only complete answer, and it is the one a folded stub already gives.
    const f = fixture(assistant({ content: "**bold**" }), "bold");
    initTurnActionsBodyProbe(() => false);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    buttons(f.footer)[0]?.click();
    expect(dispatch).toHaveBeenCalledWith("**bold**", expect.anything());
  });
});

describe("the raw-markdown toggle", () => {
  it("starts closed and reports so on the button", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(raw(f.card)).toBeNull();
    expect(sourceButton(f.footer).getAttribute("aria-pressed")).toBe("false");
  });

  it("shows the source and hides the whole rendered body with .hidden", () => {
    const f = fixture(assistant({ content: "# hi" }));
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    expect(raw(f.card)?.textContent).toBe("# hi");
    // The CONTAINER hides, so prose and evidence go together. `.hidden`
    // specifically: find-in-chat prunes it, so exactly one of the two
    // renderings is searchable and matches are never double-counted.
    expect(f.blocks.classList.contains("hidden")).toBe(true);
    expect(raw(f.card)?.classList.contains("hidden")).toBe(false);
    expect(sourceButton(f.footer).getAttribute("aria-pressed")).toBe("true");
  });

  it("takes the tool cards with it, so the raw view is source and nothing else", () => {
    // The source is one document: every word the model wrote lands at the top
    // of it, while a tool card left behind keeps the position it had between
    // two paragraphs. Half a swap shows one turn in two orders at once.
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    expect(f.tool.closest(".hidden")).toBe(f.blocks);
  });

  it("toggles back to the rendering", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const btn = sourceButton(f.footer);
    btn.click();
    btn.click();
    expect(raw(f.card)?.classList.contains("hidden")).toBe(true);
    expect(f.blocks.classList.contains("hidden")).toBe(false);
    expect(f.tool.closest(".hidden")).toBeNull();
    expect(btn.getAttribute("aria-pressed")).toBe("false");
  });

  it("ADDS the source beside the rendering rather than replacing it", () => {
    // The replacement shape is what a repaint undoes; this is the structural
    // guarantee that makes the toggle survive one.
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    expect(f.bubble.isConnected).toBe(true);
    expect(f.blocks.isConnected).toBe(true);
    // Inside the body, above the messages it stands in for.
    expect(raw(f.card)?.parentElement).toBe(f.body);
  });

  it("swallows a block that arrives after the toggle", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    // What updateAssistantBody does on every repaint: append newly-arrived
    // blocks into `.assistant-blocks`. Nothing here may disturb the raw view,
    // and the late block must not surface inside it — which is the whole
    // reason the container hides instead of its children.
    const later = document.createElement("div");
    later.className = "tool-call";
    f.blocks.appendChild(later);
    mountTurnFooterActions(f.footer, f.card, f.turn);
    expect(raw(f.card)).not.toBeNull();
    expect(raw(f.card)?.classList.contains("hidden")).toBe(false);
    expect(later.closest(".hidden")).toBe(f.blocks);
    expect(f.card.querySelectorAll(".turn-raw")).toHaveLength(1);
    expect(f.footer.querySelectorAll(".turn-actions-buttons")).toHaveLength(1);
  });

  it("swallows a whole ROW that arrives after the toggle, not just a block", () => {
    // The case above covers a block landing in an already-hidden region. A window move
    // or a mid-turn model switch mounts a new ROW instead, and it brings its own
    // unhidden block region with it — rendered output beside the raw text.
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    const wrap = document.createElement("div");
    wrap.className = "msg-wrap msg-wrap-assistant";
    const region = document.createElement("div");
    region.className = "assistant-blocks";
    region.textContent = "the second model's reply";
    wrap.appendChild(region);
    f.body.appendChild(wrap);
    expect(region.classList.contains("hidden")).toBe(false);

    syncSourceView(f.body);

    expect(region.classList.contains("hidden")).toBe(true);
    // And the raw view is untouched: this only ever hides, and only while raw shows.
    expect(raw(f.card)?.classList.contains("hidden")).toBe(false);
  });

  it("leaves a new row alone while the rendering is what is showing", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    syncSourceView(f.body);
    expect(f.blocks.classList.contains("hidden")).toBe(false);
  });

  it("re-reads the source at click time, so a later refreshed turn wins", () => {
    // The slot mounts once and is idempotent, so a source captured at mount
    // time would go stale the moment message_appended replaced the content.
    const f = fixture(assistant({ content: "streamed" }));
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const freshened: Turn = {
      ...f.turn,
      body: [assistant({ content: "server sanitized" })],
    };
    mountTurnFooterActions(f.footer, f.card, freshened);
    sourceButton(f.footer).click();
    expect(raw(f.card)?.textContent).toBe("server sanitized");
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
    mountTurnFooterActions(f.footer, f.card, f.turn);
    sourceButton(f.footer).click();
    expect(raw(f.card)?.textContent).toBe("first\n\nsecond");
  });

  it("renames itself so the button says what the next press does", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const btn = sourceButton(f.footer);
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
    btn.click();
    expect(btn.getAttribute("aria-label")).toBe("View rendered reply");
    btn.click();
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
  });

  it("swaps the FACE prose when the turn is folded", () => {
    const f = fixture(assistant({ content: "# hi" }));
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const face = fold(f);
    const prose = face.querySelector(".turn-face-prose");
    sourceButton(f.footer).click();
    const pre = raw(f.card);
    expect(pre?.parentElement).toBe(face);
    expect(pre?.textContent).toBe("# hi");
    expect(prose?.classList.contains("hidden")).toBe(true);
    // The body's regions hide with it: one rendering at a time, everywhere.
    expect(f.blocks.classList.contains("hidden")).toBe(true);
  });

  it("resets when the fold state changes, so the button never lies", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    const btn = sourceButton(f.footer);
    btn.click();
    expect(f.blocks.classList.contains("hidden")).toBe(true);
    // What setCardFolded does on any fold flip.
    resetTurnSourceView(f.card);
    expect(raw(f.card)).toBeNull();
    expect(f.blocks.classList.contains("hidden")).toBe(false);
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(btn.getAttribute("aria-label")).toBe("View markdown source");
  });

  it("reset is a no-op when no source view is open", () => {
    const f = fixture(assistant());
    mountTurnFooterActions(f.footer, f.card, f.turn);
    resetTurnSourceView(f.card);
    expect(f.blocks.classList.contains("hidden")).toBe(false);
    expect(sourceButton(f.footer).getAttribute("aria-pressed")).toBe("false");
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
