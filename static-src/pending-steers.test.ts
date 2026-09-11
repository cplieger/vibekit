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
import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";

// The controls dispatch an action and open a confirm; the stack's rendering and
// its wire discipline are what is under test, and the real modules would pull
// the action framework, the transport and a native <dialog> in behind them.
//
// vi.hoisted because pending-steers.js is a STATIC import below: the mock
// factories run during that import's resolution, which is before a plain
// top-level const would be initialized.
const mocks = vi.hoisted(() => ({
  clearDispatch: vi.fn(() => Promise.resolve(true)),
  cancelDispatch: vi.fn(() => ({
    outcome: Promise.resolve<{ status: string }>({ status: "success" }),
  })),
  confirmMock: vi.fn((_message: string) => Promise.resolve(true)),
  setComposerValueMock: vi.fn(),
  preferMock: vi.fn(),
  forgetPrefMock: vi.fn(),
}));

vi.mock("./actions/chat.js", () => ({
  clearSteers: { dispatch: mocks.clearDispatch },
  cancelTurn: { dispatch: mocks.cancelDispatch },
}));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirmMock }));
vi.mock("./composer-value.js", () => ({ setComposerValue: mocks.setComposerValueMock }));
// The send-now arrow records WHICH ROW leads and dispatches the cancel; the boundary
// that cancel produces is what reads the messages. So what this file owns is the
// gesture — the id named, the cancel, the guard — and the payload and its order are
// steer-resend.test.ts's.
vi.mock("./steer-resend.js", () => ({
  preferSteerFirst: mocks.preferMock,
  forgetSteerPreference: mocks.forgetPrefMock,
  noteBoundaryDrop: vi.fn(),
  runArmedResend: vi.fn(),
}));

const {
  clearDispatch,
  cancelDispatch,
  confirmMock,
  setComposerValueMock,
  preferMock,
  forgetPrefMock,
} = mocks;

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
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
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
    cancelDispatch.mockClear();
    cancelDispatch.mockReturnValue({ outcome: Promise.resolve({ status: "success" }) });
    confirmMock.mockClear();
    setComposerValueMock.mockClear();
    preferMock.mockClear();
    forgetPrefMock.mockClear();
  });

  // --- Placement and stacking ---------------------------------------------

  // A SIBLING of the message box, not a child. The stack holds messages that
  // have already been sent, so it belongs beside the box in the bottom bar, the
  // same way a permission ask does.
  it("renders into the bottom-bar stack rather than inside the composer", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead", origin: "user" });
    expect(firstRow().closest("#steer-stack")).not.toBeNull();
    expect(firstRow().closest("#prompt-box")).toBeNull();
  });

  it("hides the stack entirely when there is nothing in it", () => {
    const stack = document.getElementById("steer-stack");
    expect(stack?.classList.contains("hidden")).toBe(true);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    expect(stack?.classList.contains("hidden")).toBe(false);
  });

  // A new message appears at the BOTTOM and pushes the older ones up, so the
  // render order is arrival order.
  it("stacks oldest first, so a new message lands at the bottom", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "third", origin: "user" });
    expect(rows().map(textOf)).toEqual(["first", "second", "third"]);
  });

  // --- The two states ------------------------------------------------------

  // "Sent" is the fact the stack exists to state: it has left, it is not a
  // draft, and the agent has not seen it yet.
  it("says a message has been sent and is waiting", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead", origin: "user" });

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
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs instead", origin: "user" });
    promoteSteer("chat-1", "steer-1", "use tabs instead", "user");

    expect(rows()).toHaveLength(0);
    // It was the last one, so the stack goes away rather than sitting empty.
    expect(stackHidden()).toBe(true);
    expect(steerMarks("chat-1")).toEqual([
      {
        id: "steer-1",
        text: "use tabs instead",
        origin: "user",
        anchor: { msgID: "", blockIndex: 0 },
      },
    ]);
    expect(steerMarks("chat-1")[0]?.ack).toBeUndefined();
  });

  // What the agent DID about a steer is still recorded — on the transcript note,
  // which is where the change of course actually happened. The dock never renders
  // it: an ack line here was the agent's own words inside the message box.
  it("carries what the agent did on the mark rather than on a dock row", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
    promoteSteer("chat-1", "steer-1", "actually target main", "user");
    promoteSteer("chat-1", "steer-1", "", "user", "rebased onto main instead");

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
    recordSteerQueued("chat-1", { id: "steer-1", text: "never read this", origin: "user" });
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

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "one", origin: "user" });
    expect(firstRow().querySelector(".steer-state-label")?.textContent).toBe("Sent");
    expect(actions(firstRow())).toEqual([
      "Send this message now",
      "Edit this message",
      "Discard this message",
    ]);
  });

  // --- The message gets the room -------------------------------------------

  // The row is full width and the text clamps in CSS, so nothing is cut in the
  // DOM. The old horizontal chip cut at 60 characters, which is what made the
  // tooltip the only way to read a normal sentence.
  it("puts the whole message in the DOM and leaves the clamping to CSS", () => {
    const long =
      "stop rewriting the parser and instead widen the existing front-matter struct with the missing field";
    recordSteerQueued("chat-1", { id: "steer-1", text: long, origin: "user" });
    expect(textOf(firstRow())).toBe(long);
    expect(textOf(firstRow())).not.toContain("\u2026");
  });

  it("collapses whitespace so a multi-line message is one block", () => {
    recordSteerQueued("chat-1", {
      id: "steer-1",
      text: "first line\n\n   second line",
      origin: "user",
    });
    expect(textOf(firstRow())).toBe("first line second line");
    expect(firstRow().getAttribute("aria-label")).toBe(
      "Sent, waiting for the agent: first line second line",
    );
  });

  // Each steer's verdict belongs to that steer. Two answered in one response is
  // the case where a shared render would put one answer on the other's message —
  // which is now a case about the marks, since both rows have left the stack.
  it("keeps each steer's acknowledgement on its own mark", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first ask", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second ask", origin: "user" });
    promoteSteer("chat-1", "steer-1", "first ask", "user", "answered the first");
    promoteSteer("chat-1", "steer-2", "second ask", "user", "answered the second");

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
    recordSteerQueued("chat-1", { id: "steer-1", text: "read one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "still waiting", origin: "user" });
    promoteSteer("chat-1", "steer-1", "read one", "user");
    expect(rows().map(textOf)).toEqual(["still waiting"]);
  });

  // A pending row has no server-side id yet, so `_session/steer/clear` has nothing
  // to address and a control there would be one that cannot act.
  it("offers no controls on a message that is still sending", () => {
    recordSteerSent("chat-1", "m-1", "one");
    expect(actions(firstRow())).toEqual([]);
  });

  it("offers Edit and Discard on the only unread message", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    expect(actions(firstRow())).toEqual([
      "Send this message now",
      "Edit this message",
      "Discard this message",
    ]);
  });

  // Edit is discard-plus-retype, so offering it with two unread would silently
  // drop the other. THE case this rule exists for.
  it("withholds Edit once more than one message is unread", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user" });
    for (const row of rows()) {
      expect(actions(row)).toEqual(["Send this message now", "Discard all 2 unread messages"]);
    }
  });

  // The count that decides Edit is the count of rows a clear would take, and a
  // steer the agent reads stops being one of them by leaving. So two unread
  // withholding Edit becomes one unread offering it the moment the first is read.
  it("counts only what is left waiting when deciding whether Edit is safe", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user" });
    for (const row of rows()) {
      expect(actions(row)).toEqual(["Send this message now", "Discard all 2 unread messages"]);
    }

    promoteSteer("chat-1", "steer-1", "one", "user");
    expect(rows()).toHaveLength(1);
    expect(actions(firstRow())).toEqual([
      "Send this message now",
      "Edit this message",
      "Discard this message",
    ]);
  });

  // A row still sending is not one a clear can address, so it does not count
  // toward the total either: one confirmed row beside one pending one still
  // offers Edit.
  it("does not count a still-sending row toward the unread total", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "confirmed", origin: "user" });
    recordSteerSent("chat-1", "m-2", "still sending");
    const [confirmed, sending] = rows();
    expect(actions(confirmed as HTMLElement)).toEqual([
      "Send this message now",
      "Edit this message",
      "Discard this message",
    ]);
    expect(actions(sending as HTMLElement)).toEqual([]);
  });

  // ---------------------------------------------------------------------------
  // SEND NOW. The wire has no force-inject and no flush — `_session/steer` and
  // `_session/steer/clear` are the whole steer surface, and neither makes the
  // running agent read a message — so the only reading this button can honour is
  // stop-the-turn-and-send-it-as-a-new-one. It arms the shared slot and dispatches
  // the cancel; the send happens at the boundary that cancel produces.
  // ---------------------------------------------------------------------------

  it("names its row and stops the turn when send-now is pressed", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
    clickAction(firstRow(), "Send this message now");

    await vi.waitFor(() => {
      expect(cancelDispatch).toHaveBeenCalledWith("chat-1");
    });
    expect(preferMock).toHaveBeenCalledWith("chat-1", "steer-1");
    // It does not send here, and it does not discard: the buffer is drained by the
    // cancel KAS handles, and the send waits for the settled turn frame.
    expect(clearDispatch).not.toHaveBeenCalled();
    expect(setComposerValueMock).not.toHaveBeenCalled();
  });

  // AN ID, NEVER A TEXT SNAPSHOT. A snapshot taken here would miss a row confirmed
  // between the click and the boundary; the boundary reads for itself, so the gesture
  // only has to say which row leads.
  it("names the pressed row when several are waiting", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "third", origin: "user" });

    const second = rows()[1];
    if (second === undefined) {
      throw new Error("no second row");
    }
    clickAction(second, "Send this message now");

    await vi.waitFor(() => {
      expect(preferMock).toHaveBeenCalled();
    });
    expect(preferMock).toHaveBeenCalledWith("chat-1", "steer-2");
  });

  // A row still sending has no server-side id, so it has no control at all.
  it("offers no send-now control on a message that is still sending", () => {
    recordSteerSent("chat-1", "m-1", "still sending");
    expect(actions(firstRow())).toEqual([]);
  });

  // A control that cannot act must not be drawn. Nothing carries an agent's own
  // notice — KAS re-wakes an undelivered one itself — so that row offers no arrow.
  it("offers no send-now control on the agent's own notice", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "workflow says hi", origin: "agent" });
    expect(actions(firstRow())).not.toContain("Send this message now");
  });

  // A cancel that never lands leaves no boundary to order, so the preference has to go
  // or an unrelated later turn end would apply an order the reader gave up on.
  it("forgets the preference when the cancel does not land", async () => {
    cancelDispatch.mockReturnValue({ outcome: Promise.resolve({ status: "error" }) });
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    clickAction(firstRow(), "Send this message now");

    await vi.waitFor(() => {
      expect(forgetPrefMock).toHaveBeenCalledWith("chat-1");
    });
    expect(preferMock).toHaveBeenCalledWith("chat-1", "steer-1");
  });

  // The tooltip is where the COST is stated, because the label cannot carry it: the
  // reader is being told the turn stops, not that the agent will read this next.
  it("says the turn stops, rather than implying the agent will read it", () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    const btn = Array.from(firstRow().querySelectorAll<HTMLButtonElement>(".steer-act")).find(
      (b) => (b.getAttribute("aria-label") ?? "") === "Send this message now",
    );
    expect(btn?.dataset["tooltip"]).toContain("Stops the turn");
  });

  it("fills the composer and clears the buffer when a message is edited", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });
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
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(clearDispatch).toHaveBeenCalledWith({ chatID: "chat-1" });
    });
    expect(confirmMock).not.toHaveBeenCalled();
  });

  // With several unread, a × beside one row looks like it removes that row, so
  // the count is named before anything goes.
  it("confirms before discarding when more than one would go", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(String(confirmMock.mock.calls[0]?.[0])).toContain("2");
  });

  it("sends nothing when the multi-message confirm is declined", async () => {
    confirmMock.mockResolvedValueOnce(false);
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "two", origin: "user" });
    clickAction(firstRow(), "Discard");
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalled();
    });
    expect(clearDispatch).not.toHaveBeenCalled();
  });
});

// THE ROW SURVIVES ITS OWN CONFIRMATION, measured against real layout under the
// shipped stylesheet, because the defect was invisible to every assertion about
// what a row SAYS.
//
// One Send produces two renders a round trip apart — `recordSteerSent` draws the
// pending row, `recordSteerQueued` confirms it off the POST's own reply — and
// `.steer-row` enters through `@starting-style` (26-dock.css), which supplies a
// before-change style to any element being rendered for the first time. A render
// that rebuilt the row therefore replayed that entry fade over a row already on
// screen, interrupting the first fade mid-flight: the row appeared, dropped back
// to invisible and appeared again, which is what a reader reported as a flicker.
//
// So the subject here is the NODE rather than its content, and the stack element
// is the module's own (captured at init), re-parented into a host with the
// stylesheet mounted the way the clamp block below does it.
describe("the row across its own confirmation", () => {
  let styleEl: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    host = document.createElement("div");
    host.style.inlineSize = "600px";
    document.body.appendChild(host);
    const stack = document.getElementById("steer-stack");
    if (stack === null) {
      throw new Error("no #steer-stack");
    }
    host.appendChild(stack);
  });

  beforeEach(() => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
  });

  afterAll(() => {
    styleEl.remove();
    // The tier cases below write this, and it changes `--hit-floor` and every
    // control height for the whole document, so it must not reach the clamp block.
    delete document.documentElement.dataset["pointer"];
    // Back to the body rather than removed with the host: the module renders into
    // this element for the rest of the file, and the clamp block below looks it up
    // by id.
    const stack = document.getElementById("steer-stack");
    if (stack !== null) {
      document.body.appendChild(stack);
    }
    host.remove();
  });

  /** Two frames span one full layout-and-resize delivery. */
  async function settles(): Promise<void> {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });
  }

  /** The row's own ENTRY transitions, which are the two `@starting-style` declares.
   *  Filtered rather than the whole list, because a confirmation deliberately
   *  animates the row's ink — so "nothing is running" would forbid the settle as
   *  well as the re-entry, and `border-color` alone reports one per side. */
  function entryAnimations(row: HTMLElement): Animation[] {
    return row
      .getAnimations()
      .filter((a) =>
        ["opacity", "transform"].includes((a as CSSTransition).transitionProperty ?? ""),
      );
  }

  /** The entry transition `@starting-style` starts, once it is running. */
  async function entering(row: HTMLElement): Promise<Animation[]> {
    await vi.waitFor(() => {
      expect(entryAnimations(row).length, "the row fades in on its first paint").toBeGreaterThan(0);
    });
    return entryAnimations(row);
  }

  // The premise, asserted rather than assumed: without an entry transition on the
  // row there is nothing for a rebuild to replay, and the two cases below would
  // pass over a stylesheet that had lost it.
  it("enters through a starting style, so a replacement would re-animate", async () => {
    expect(ruleBody(loadCSS("26-dock.css"), ".steer-row")).toContain("@starting-style");

    recordSteerSent("chat-1", "m-1", "use tabs instead");
    const anims = await entering(firstRow());
    expect(anims.map((a) => (a as CSSTransition).transitionProperty).sort()).toEqual([
      "opacity",
      "transform",
    ]);
  });

  it("updates the same element rather than replacing it on the confirmation", () => {
    recordSteerSent("chat-1", "m-1", "use tabs instead");
    const sending = firstRow();
    expect(sending.dataset["state"]).toBe("sending");

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "use tabs instead", origin: "user" });

    expect(firstRow(), "the node is updated in place, not rebuilt").toBe(sending);
    expect(sending.dataset["state"]).toBe("sent");
    expect(actions(sending)).toEqual([
      "Send this message now",
      "Edit this message",
      "Discard this message",
    ]);
  });

  it("does not fade a second time when the confirmation lands", async () => {
    recordSteerSent("chat-1", "m-1", "use tabs instead");
    const row = firstRow();
    await Promise.all((await entering(row)).map((a) => a.finished));

    recordSteerQueued("chat-1", { id: "steer-m-1", text: "use tabs instead", origin: "user" });

    expect(entryAnimations(row), "no second fade over a row already on screen").toEqual([]);
    expect(getComputedStyle(row).opacity, "and it stays fully painted").toBe("1");
  });

  // A steer arriving beside one already on screen is the other render that used to
  // rebuild every row: the new one is entitled to its entry fade, the settled one
  // is not.
  it("leaves an established row alone when a second message arrives", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    const first = firstRow();
    await Promise.all((await entering(first)).map((a) => a.finished));

    recordSteerSent("chat-1", "m-2", "second");

    expect(rows()).toHaveLength(2);
    expect(rows()[0], "the established row is the same node").toBe(first);
    expect(entryAnimations(first), "and it does not re-enter").toEqual([]);
  });

  // THE ROW MUST NOT CHANGE HEIGHT WHEN IT CONFIRMS, and this is measured at both
  // pointer tiers because the size of the jump was the hit-target floor: the
  // controls arrive with the confirmation and each is floored to `--hit-floor`, so
  // the row went 34px -> 42px on a mouse and 34px -> 62px on a phone. The bar grows
  // UPWARD, so that reached the reader as the transcript jumping by the same amount
  // in the same frame the row was still fading in.
  //
  // `.steer-actions` reserves that height while it is empty, so what the
  // confirmation changes is the buttons' opacity and the row's ink.
  for (const tier of ["fine", "coarse"] as const) {
    it(`keeps its height across the confirmation on a ${tier} pointer`, async () => {
      document.documentElement.dataset["pointer"] = tier;
      recordSteerSent("chat-1", "m-1", "use tabs instead");
      const row = firstRow();
      await settles();
      const before = row.getBoundingClientRect().height;
      // The floor is what makes the two tiers different measurements rather than
      // one measurement run twice, so the premise is asserted.
      expect(
        getComputedStyle(document.documentElement).getPropertyValue("--hit-floor").trim(),
        "the tier is in force",
      ).toBe(tier === "fine" ? "1.5rem" : "2.75rem");

      recordSteerQueued("chat-1", { id: "steer-m-1", text: "use tabs instead", origin: "user" });
      await settles();

      expect(actions(row), "the controls did arrive").toHaveLength(3);
      expect(
        row.getBoundingClientRect().height,
        "and the row did not grow under the reader",
      ).toBeCloseTo(before, 2);
    });
  }

  // The clamp keys its state to the text element, so keeping the node keeps a
  // measured verdict AND an expansion the reader asked for. Rebuilding threw both
  // away and re-guessed from character count on a detached node.
  it("keeps a message the reader opened open through the confirmation", async () => {
    const long = "rebase onto main and re-run the census against both bundles first ".repeat(6);
    recordSteerSent("chat-1", "m-1", long);
    const row = firstRow();
    const more = row.querySelector<HTMLButtonElement>(".steer-more");
    if (more === null) {
      throw new Error("no .steer-more");
    }
    await vi.waitFor(() => {
      expect(more.hidden, "the opener is offered for a message past four lines").toBe(false);
    });
    more.click();
    expect(row.querySelector(".steer-text")?.hasAttribute("data-clamped")).toBe(false);

    recordSteerQueued("chat-1", { id: "steer-m-1", text: long, origin: "user" });

    expect(firstRow()).toBe(row);
    expect(
      row.querySelector(".steer-text")?.hasAttribute("data-clamped"),
      "still open, and the opener still says so",
    ).toBe(false);
    expect(more.textContent).toBe("Show less");
  });
});

// The clamp, measured against real layout under the shipped stylesheet — the only
// thing that can answer "does this row overflow four lines". The stack element is
// the module's own (captured at init), so these cases re-parent it into a narrow
// host rather than building a second one.
describe("the row's clamp", () => {
  let styleEl: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    host = document.createElement("div");
    host.style.inlineSize = "320px";
    document.body.appendChild(host);
    const stack = document.getElementById("steer-stack");
    if (stack === null) {
      throw new Error("no #steer-stack");
    }
    host.appendChild(stack);
  });

  beforeEach(() => {
    // Same one-line reset as the suite above: a fresh session has no steers, so
    // the render empties the stack. Repeated rather than hoisted because these
    // cases are a sibling describe with their own layout host.
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
  });

  afterAll(() => {
    styleEl.remove();
    host.remove();
  });

  function textEl(row: HTMLElement): HTMLElement {
    const t = row.querySelector<HTMLElement>(".steer-text");
    if (t === null) {
      throw new Error("no .steer-text");
    }
    return t;
  }

  function moreEl(row: HTMLElement): HTMLButtonElement {
    const b = row.querySelector<HTMLButtonElement>(".steer-more");
    if (b === null) {
      throw new Error("no .steer-more");
    }
    return b;
  }

  /** Wait for the opener to reach `hidden`, for a verdict that must CHANGE. */
  async function settles(row: HTMLElement, hidden: boolean, why: string): Promise<void> {
    await vi.waitFor(() => {
      expect(moreEl(row).hidden, why).toBe(hidden);
    });
  }

  /** Two frames span one full resize delivery, for a verdict that must NOT
   *  change. */
  async function observerRuns(): Promise<void> {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    });
  }

  const LONG = "rebase onto main and re-run the census against both bundles first ".repeat(6);

  it("offers no opener for a message that fits", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: "use tabs", origin: "user" });
    await observerRuns();
    expect(moreEl(firstRow()).hidden).toBe(true);
  });

  it("offers one for a message that does not, with the whole text still in the DOM", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: LONG, origin: "user" });
    const row = firstRow();
    await settles(row, false, "offered for a message past four lines");
    expect(textEl(row).textContent).toContain("bundles first");
    expect(textEl(row).scrollHeight).toBeGreaterThan(textEl(row).clientHeight);
  });

  it("un-clamps on the click", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: LONG, origin: "user" });
    const row = firstRow();
    await settles(row, false, "offered");

    moreEl(row).click();
    expect(textEl(row).hasAttribute("data-clamped")).toBe(false);
    expect(moreEl(row).textContent).toBe("Show less");
  });

  // The row is a grid whose middle track is `minmax(0, 1fr)`, and that is what
  // keeps the actions on the row: an `auto` middle track sizes to the text and
  // pushes them off. Opening the clamp grows the row's HEIGHT, so the controls
  // have to still be in their own column afterwards.
  it("keeps every control on the row at the expanded height", async () => {
    recordSteerQueued("chat-1", { id: "steer-1", text: LONG, origin: "user" });
    const row = firstRow();
    await settles(row, false, "offered");
    expect(actions(row).length, "one unread message, so Edit is offered too").toBe(3);

    const before = row.getBoundingClientRect().height;
    moreEl(row).click();
    await observerRuns();

    expect(row.getBoundingClientRect().height, "the row grew").toBeGreaterThan(before);
    const textBox = textEl(row).getBoundingClientRect();
    for (const btn of row.querySelectorAll<HTMLElement>(".steer-act")) {
      const box = btn.getBoundingClientRect();
      expect(box.x, "still in the actions column, right of the message").toBeGreaterThan(textBox.x);
      expect(box.right, "and still inside the row").toBeLessThanOrEqual(
        row.getBoundingClientRect().right + 1,
      );
    }
  });
});
