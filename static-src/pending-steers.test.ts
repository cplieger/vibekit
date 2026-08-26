//
// The stack is a pure projection of `session.steers`, so these tests drive the
// store the way the submit path and the SSE handlers do and read the DOM the way
// a person does.
//
// IT HOLDS ONLY WHAT THE AGENT HAS NOT READ, and that is the invariant most of
// these cases are about: a steer LEAVES the stack the moment it is read
// (`promoteSteer`) or dropped at a turn boundary (`dropSteers`), and reappears
// inside the turn transcript as a note. So there is no read row, no checkmark and
// no ack line here — the count falling to zero is the whole read signal, and the
// ack rides the transcript mark, which several cases below assert on directly
// because that is where the fact moved rather than a fact that stopped existing.
//
// The two states that remain are both "not read yet": `pending` (this device's
// own claim that a POST is in flight, drawn on submit) and confirmed by KAS's
// `steer_queued`.
//
// The control set is the other part worth guarding, because it is pinned to what
// KAS's wire can actually honour: two verbs, `_session/steer` and
// `_session/steer/clear`, the second taking only a sessionId. So a pending row
// has no controls (there is no server-side id to clear yet), Discard always
// clears every unread message, and Edit is offered only when exactly one is
// unread. Each of those is a case below.
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

import {
  setSessions,
  setActive,
  recordSteerQueued,
  recordSteerSent,
  promoteSteer,
  dropSteers,
  steerMarks,
} from "./store.js";
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

/** The dock's ack line, which no longer exists. Kept as the guard that it does
 *  not come back: what the agent did belongs on the transcript note, not on a row
 *  sitting inside the composer. */
function ackOf(row: HTMLElement): string | null {
  return row.querySelector(".steer-ack")?.textContent ?? null;
}

function stackHidden(): boolean {
  return document.getElementById("steer-stack")?.classList.contains("hidden") ?? false;
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

  // "Sending" is the in-flight claim: the POST has gone, KAS has not confirmed it
  // yet, and the row exists so pressing Send draws something on the keystroke
  // rather than after a round trip.
  it("says a message is still sending before KAS confirms it", () => {
    recordSteerSent("chat-1", "m-1", "use tabs instead");

    const row = firstRow();
    expect(row.dataset["state"]).toBe("sending");
    expect(row.querySelector(".steer-state-label")?.textContent).toBe("Sending");
    expect(row.getAttribute("aria-label")).toBe(
      "Sending, not in the agent's buffer yet: use tabs instead",
    );
  });

  // The read state has no row at all: the stack is what the agent has NOT read,
  // so the message leaves it and lands in the transcript instead. Its being read
  // with nothing said about it is a mark with no ack.
  it("takes a message the agent has read out of the stack entirely", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead" });
    promoteSteer("chat-1", "steer-1", "use tabs instead");

    expect(rows()).toHaveLength(0);
    // It was the last one, so the stack goes away rather than sitting empty.
    expect(stackHidden()).toBe(true);
    expect(steerMarks("chat-1")).toEqual([
      { id: "steer-1", text: "use tabs instead", anchor: { msgID: "", blockIndex: 0 } },
    ]);
    expect(steerMarks("chat-1")[0]?.ack).toBeUndefined();
  });

  // What the agent DID about a steer is still recorded — on the transcript note,
  // which is where the change of course actually happened. The dock never renders
  // it: an ack line here was the agent's own words inside the message box.
  it("carries what the agent did on the mark rather than on a dock row", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main" });
    promoteSteer("chat-1", "steer-1", "actually target main");
    promoteSteer("chat-1", "steer-1", "", "rebased onto main instead");

    expect(rows()).toHaveLength(0);
    expect(document.querySelectorAll("#steer-stack .steer-ack")).toHaveLength(0);
    const mark = steerMarks("chat-1")[0];
    // The steer's own text survives the ack frame: the note has to stay
    // identifiable as the thing the user sent.
    expect(mark?.text).toBe("actually target main");
    expect(mark?.ack).toBe("rebased onto main instead");
  });

  // A dropped steer leaves the stack the same way a read one does — the dock's
  // lifetime is the turn, and the transcript keeps the record.
  it("takes a message dropped at a turn boundary out of the stack", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "never read this" });
    dropSteers("chat-1", ["steer-1"]);

    expect(rows()).toHaveLength(0);
    expect(stackHidden()).toBe(true);
    expect(steerMarks("chat-1")[0]?.dropped).toBe(true);
  });

  // The repaint key has to include the SENDING state. The computed dedups by
  // string value, so a confirmation that changes no text would otherwise paint
  // nothing and the row would keep saying "Sending" — with no controls — for the
  // rest of the turn.
  it("repaints when only the sending state changed", () => {
    recordSteerSent("chat-1", "m-1", "one");
    expect(firstRow().querySelector(".steer-state-label")?.textContent).toBe("Sending");
    expect(actions(firstRow())).toEqual([]);

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "one" });
    expect(firstRow().querySelector(".steer-state-label")?.textContent).toBe("Sent");
    expect(actions(firstRow())).toEqual(["Edit this message", "Discard this message"]);
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
  // the case where a shared render would put one answer on the other's message —
  // which is now a case about the marks, since both rows have left the stack.
  it("keeps each steer's acknowledgement on its own mark", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first ask" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second ask" });
    promoteSteer("chat-1", "steer-1", "first ask", "answered the first");
    promoteSteer("chat-1", "steer-2", "second ask", "answered the second");

    expect(rows()).toHaveLength(0);
    expect(steerMarks("chat-1").map((m) => [m.text, m.ack])).toEqual([
      ["first ask", "answered the first"],
      ["second ask", "answered the second"],
    ]);
  });

  // --- Controls, bounded by what the wire can honour -----------------------

  // A read steer cannot be unsent and cannot be changed, and the stack does not
  // offer a control that lies about it — because it does not keep the row at all.
  // The other row is left standing so the emptiness is the promotion's doing
  // rather than an empty stack.
  it("offers no controls on a message the agent has read, having no row for it", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "read one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "still waiting" });
    promoteSteer("chat-1", "steer-1", "read one");
    expect(rows().map(textOf)).toEqual(["still waiting"]);
  });

  // A pending row has no server-side id yet, so `_session/steer/clear` has nothing
  // to address and a control there would be one that cannot act.
  it("offers no controls on a message that is still sending", () => {
    recordSteerSent("chat-1", "m-1", "one");
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

  // The count that decides Edit is the count of rows a clear would take, and a
  // steer the agent reads stops being one of them by leaving. So two unread
  // withholding Edit becomes one unread offering it the moment the first is read.
  it("counts only what is left waiting when deciding whether Edit is safe", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two" });
    for (const row of rows()) {
      expect(actions(row)).toEqual(["Discard all 2 unread messages"]);
    }

    promoteSteer("chat-1", "steer-1", "one");
    expect(rows()).toHaveLength(1);
    expect(actions(firstRow())).toEqual(["Edit this message", "Discard this message"]);
  });

  // A row still sending is not one a clear can address, so it does not count
  // toward the total either: one confirmed row beside one pending one still
  // offers Edit.
  it("does not count a still-sending row toward the unread total", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "confirmed" });
    recordSteerSent("chat-1", "m-2", "still sending");
    const [confirmed, sending] = rows();
    expect(actions(confirmed as HTMLElement)).toEqual([
      "Edit this message",
      "Discard this message",
    ]);
    expect(actions(sending as HTMLElement)).toEqual([]);
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
