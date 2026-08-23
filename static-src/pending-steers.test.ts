// @vitest-environment happy-dom
//
// The stack is a pure projection of `session.steers`, so these tests drive the
// store the way the SSE handlers do and read the DOM the way a person does.
//
// The control set is the part worth guarding, because it is pinned to what KAS's
// wire can actually honour: two verbs, `_session/steer` and
// `_session/steer/clear`, the second taking only a sessionId. So a read row has
// no controls, Discard always clears every unread message, and Edit is offered
// only when exactly one is unread. Each of those is a case below.
import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";

// The controls dispatch an action and open a confirm; the stack's rendering and
// its wire discipline are what is under test, and the real modules would pull
// the action framework, the transport and a native <dialog> in behind them.
//
// vi.hoisted because pending-steers.js is a STATIC import below: the mock
// factories run during that import's resolution, which is before a plain
// top-level const would be initialized.
const mocks = vi.hoisted(() => ({
  clearDispatch: vi.fn(() => Promise.resolve(true)),
  confirmMock: vi.fn((_message: string) => Promise.resolve(true)),
  setComposerValueMock: vi.fn(),
}));

vi.mock("./actions/chat.js", () => ({ clearSteers: { dispatch: mocks.clearDispatch } }));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirmMock }));
vi.mock("./composer-value.js", () => ({ setComposerValue: mocks.setComposerValueMock }));

const { clearDispatch, confirmMock, setComposerValueMock } = mocks;

import { setSessions, setActive, recordSteerQueued, markSteerInjected } from "./store.js";
import { initPendingSteers } from "./pending-steers.js";
import type { Session } from "./types.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function rows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>("#steer-stack .steer-row"));
}

function firstRow(): HTMLElement {
  const el = rows()[0];
  if (el === undefined) {
    throw new Error("no row rendered");
  }
  return el;
}

function textOf(row: HTMLElement): string {
  return row.querySelector(".steer-text")?.textContent ?? "";
}

function ackOf(row: HTMLElement): string | null {
  return row.querySelector(".steer-ack")?.textContent ?? null;
}

function actions(row: HTMLElement): string[] {
  return Array.from(row.querySelectorAll<HTMLElement>(".steer-act")).map(
    (b) => b.getAttribute("aria-label") ?? "",
  );
}

function clickAction(row: HTMLElement, labelStartsWith: string): void {
  const btn = Array.from(row.querySelectorAll<HTMLButtonElement>(".steer-act")).find((b) =>
    (b.getAttribute("aria-label") ?? "").startsWith(labelStartsWith),
  );
  if (btn === undefined) {
    throw new Error(`no action button starting with ${labelStartsWith}: ${String(actions(row))}`);
  }
  btn.click();
}

describe("the steer stack", () => {
  // The stack element is captured once, by the module's own idempotent init, so
  // it has to outlive every case: replacing it per test would leave the effect
  // painting into a detached node. The prompt input is here because Edit focuses
  // it after filling the composer.
  beforeAll(() => {
    document.body.innerHTML = `
      <ul id="steer-stack" class="steer-stack hidden"></ul>
      <textarea id="prompt-input"></textarea>`;
    initPendingSteers();
  });

  beforeEach(() => {
    // A fresh session has no steers, so the render empties the stack: the store
    // is the only input, which is what makes the reset one line.
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    expect(rows()).toHaveLength(0);
    clearDispatch.mockClear();
    confirmMock.mockClear();
    setComposerValueMock.mockClear();
  });

  // --- Placement and stacking ---------------------------------------------

  // A SIBLING of the message box, not a child. The stack holds messages that
  // have already been sent, so it belongs beside the box in the bottom bar, the
  // same way a permission ask does.
  it("renders into the bottom-bar stack rather than inside the composer", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    expect(firstRow().closest("#steer-stack")).not.toBeNull();
    expect(firstRow().closest("#prompt-box")).toBeNull();
  });

  it("hides the stack entirely when there is nothing in it", () => {
    const stack = document.getElementById("steer-stack");
    expect(stack?.classList.contains("hidden")).toBe(true);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    expect(stack?.classList.contains("hidden")).toBe(false);
  });

  // A new message appears at the BOTTOM and pushes the older ones up, so the
  // render order is arrival order.
  it("stacks oldest first, so a new message lands at the bottom", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "third" });
    expect(rows().map(textOf)).toEqual(["first", "second", "third"]);
  });

  // --- The two states ------------------------------------------------------

  // "Sent" is the fact the stack exists to state: it has left, it is not a
  // draft, and the agent has not seen it yet.
  it("says a message has been sent and is waiting", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });

    const row = firstRow();
    expect(row.dataset["state"]).toBe("sent");
    expect(row.querySelector(".steer-state-label")?.textContent).toBe("Sent");
    expect(ackOf(row)).toBeNull();
    expect(row.getAttribute("aria-label")).toBe("Sent, waiting for the agent: use tabs instead");
  });

  it("flips to read with no verdict when the agent said nothing about it", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    markSteerInjected("chat-1", "steer-1", "use tabs instead");

    const row = firstRow();
    expect(row.dataset["state"]).toBe("read");
    expect(row.querySelector(".steer-state-label")?.textContent).toBe("Read");
    expect(ackOf(row)).toBeNull();
    expect(row.getAttribute("aria-label")).toBe("Read by the agent: use tabs instead");
  });

  it("carries what the agent did once the acknowledgement lands", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main" });
    markSteerInjected("chat-1", "steer-1", "actually target main");
    markSteerInjected("chat-1", "steer-1", "", "rebased onto main instead");

    const row = firstRow();
    expect(ackOf(row)).toBe("rebased onto main instead");
    // The steer's own text is still the message: the row has to stay
    // identifiable as the thing the user sent.
    expect(textOf(row)).toBe("actually target main");
    expect(row.getAttribute("aria-label")).toBe(
      "Read by the agent: actually target main. The agent did: rebased onto main instead",
    );
    expect(row.getAttribute("title")).toBe("actually target main\n\nrebased onto main instead");
  });

  // The repaint key has to include the ack. The computed dedups by string value,
  // so an ack arriving on an already-read steer changes nothing else about the
  // session and would otherwise paint nothing.
  it("repaints when only the ack changed", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    markSteerInjected("chat-1", "steer-1", "one");
    expect(ackOf(firstRow())).toBeNull();

    markSteerInjected("chat-1", "steer-1", "", "did the thing");
    expect(ackOf(firstRow())).toBe("did the thing");
  });

  // --- The message gets the room -------------------------------------------

  // The row is full width and the text clamps in CSS, so nothing is cut in the
  // DOM. The old horizontal chip cut at 60 characters, which is what made the
  // tooltip the only way to read a normal sentence.
  it("puts the whole message in the DOM and leaves the clamping to CSS", () => {
    const long =
      "stop rewriting the parser and instead widen the existing front-matter struct with the missing field";
    recordSteerQueued("chat-1", { id: "steer-1", text: long });
    expect(textOf(firstRow())).toBe(long);
    expect(textOf(firstRow())).not.toContain("\u2026");
  });

  it("collapses whitespace so a multi-line message is one block", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first line\n\n   second line" });
    expect(textOf(firstRow())).toBe("first line second line");
    expect(firstRow().getAttribute("aria-label")).toBe(
      "Sent, waiting for the agent: first line second line",
    );
  });

  // Each steer's verdict belongs to that steer. Two answered in one response is
  // the case where a shared render would put one answer on the other's message.
  it("keeps each steer's acknowledgement on its own row", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first ask" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second ask" });
    markSteerInjected("chat-1", "steer-1", "first ask", "answered the first");
    markSteerInjected("chat-1", "steer-2", "second ask", "answered the second");

    expect(rows().map((r) => [textOf(r), ackOf(r)])).toEqual([
      ["first ask", "answered the first"],
      ["second ask", "answered the second"],
    ]);
  });

  // --- Controls, bounded by what the wire can honour -----------------------

  // A read steer cannot be unsent and cannot be changed. No control, rather
  // than a disabled one implying the operation exists somewhere.
  it("offers no controls on a message the agent has read", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    markSteerInjected("chat-1", "steer-1", "one");
    expect(actions(firstRow())).toEqual([]);
  });

  it("offers Edit and Discard on the only unread message", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    expect(actions(firstRow())).toEqual(["Edit this message", "Discard this message"]);
  });

  // Edit is discard-plus-retype, so offering it with two unread would silently
  // drop the other. THE case this rule exists for.
  it("withholds Edit once more than one message is unread", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    for (const row of rows()) {
      expect(actions(row)).toEqual(["Discard all 2 unread messages"]);
    }
  });

  // A read row alongside an unread one does not count toward the unread total,
  // so the single-unread case still offers Edit.
  it("counts only unread messages when deciding whether Edit is safe", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    markSteerInjected("chat-1", "steer-1", "one");
    const [read, unread] = rows();
    expect(actions(read as HTMLElement)).toEqual([]);
    expect(actions(unread as HTMLElement)).toEqual(["Edit this message", "Discard this message"]);
  });

  it("fills the composer and clears the buffer when a message is edited", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main" });
    clickAction(firstRow(), "Edit");
    await vi.waitFor(() => {
      expect(clearDispatch).toHaveBeenCalledWith({ chatID: "chat-1" });
    });
    expect(setComposerValueMock).toHaveBeenCalledWith("actually target main");
    // No dialog: taking back the only unread message is exactly what the button
    // says it does.
    expect(confirmMock).not.toHaveBeenCalled();
  });

  // One unread message, so the label and the effect already agree and a dialog
  // would be a click that teaches nothing.
  it("discards a single unread message without asking", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(clearDispatch).toHaveBeenCalledWith({ chatID: "chat-1" });
    });
    expect(confirmMock).not.toHaveBeenCalled();
  });

  // With several unread, a × beside one row looks like it removes that row, so
  // the count is named before anything goes.
  it("confirms before discarding when more than one would go", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(String(confirmMock.mock.calls[0]?.[0])).toContain("2");
  });

  it("sends nothing when the multi-message confirm is declined", async () => {
    confirmMock.mockResolvedValueOnce(false);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(clearDispatch).not.toHaveBeenCalled();
  });
});
