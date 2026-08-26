// ---------------------------------------------------------------------------
// Tests for fundamentals/steer-note.ts: the transcript row a steer becomes once
// it leaves the dock.
//
// This is where two facts the dock used to carry now live, and that is why the
// cases read like the chip row's old ones: the READ state (the agent consumed the
// message) and the agent's own account of what it DID about it. Both were a green
// check and an extra line inside the composer, which put the state of the
// transcript in the message box and left the transcript itself unexplained.
//
// A pure view, so it is driven by its own data and read as DOM — no store, no
// dispatcher, no CSS. The clamping is CSS's, which is why "the whole text is in
// the DOM" is an assertion here rather than a length check.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import { buildSteerNote } from "./steer-note.js";

function textOf(root: HTMLElement, sel: string): string | null {
  return root.querySelector(sel)?.textContent ?? null;
}

function restoreButton(root: HTMLElement): HTMLButtonElement | null {
  return root.querySelector<HTMLButtonElement>(".steer-note-restore");
}

describe("the read state", () => {
  it("labels the message as steered mid-turn and carries the whole of it", () => {
    const note = buildSteerNote({ text: "actually target main", dropped: false });

    expect(note.dataset["state"]).toBe("read");
    expect(textOf(note, ".steer-note-label")).toBe("Steered mid-turn");
    expect(textOf(note, ".steer-note-text")).toBe("actually target main");
    // The label styling and the tint are visual, so the state has to be in the
    // accessible name too.
    expect(note.getAttribute("aria-label")).toBe("Steered mid-turn: actually target main");
  });

  // What the agent said it did, on the row where the course actually changed.
  it("renders the agent's account of what it did when one arrived", () => {
    const note = buildSteerNote({
      text: "actually target main",
      ack: "rebased onto main instead",
      dropped: false,
    });

    expect(textOf(note, ".steer-note-ack")).toBe("rebased onto main instead");
    // The steer's own text stays the message: the note has to remain
    // identifiable as the thing the user sent.
    expect(textOf(note, ".steer-note-text")).toBe("actually target main");
    expect(note.getAttribute("aria-label")).toBe(
      "Steered mid-turn: actually target main. The agent did: rebased onto main instead",
    );
  });

  // An agent that never emitted the acknowledgement marker has said nothing, and
  // an empty line would read as a verdict with nothing in it.
  it("renders no account line when the agent said nothing about it", () => {
    const note = buildSteerNote({ text: "one", dropped: false });
    expect(note.querySelector(".steer-note-ack")).toBeNull();
    expect(note.getAttribute("aria-label")).toBe("Steered mid-turn: one");
  });

  // A message the agent has already read cannot be unsent, so there is no control
  // — not even when the caller offers the callback.
  it("offers no restore control, even given the callback", () => {
    const note = buildSteerNote({ text: "one", dropped: false, onRestore: vi.fn() });
    expect(restoreButton(note)).toBeNull();
  });
});

describe("the dropped state", () => {
  // "I sent this and the agent never read it" is exactly the fact the transcript
  // is for, so the row keeps the text and says plainly that it was not delivered.
  it("says it was not delivered and keeps the text", () => {
    const note = buildSteerNote({ text: "never read this", dropped: true });

    expect(note.dataset["state"]).toBe("dropped");
    expect(textOf(note, ".steer-note-label")).toBe("Not delivered");
    expect(textOf(note, ".steer-note-text")).toBe("never read this");
    expect(note.getAttribute("aria-label")).toBe("Not delivered: never read this");
  });

  // One control, and it is the one that makes the message a click from being
  // re-sent as an ordinary prompt.
  it("offers to put the message back in the box, and calls back on the click", () => {
    const onRestore = vi.fn();
    const note = buildSteerNote({ text: "never read this", dropped: true, onRestore });

    const btn = restoreButton(note);
    expect(btn?.textContent).toBe("Put it back in the message box");
    btn?.click();
    expect(onRestore).toHaveBeenCalledTimes(1);
  });

  // The note sits inside the turn card, whose header band is a fold toggle.
  // Restoring must not also fold the turn away.
  it("keeps the click off the turn card around it", () => {
    const onRestore = vi.fn();
    const onCard = vi.fn();
    const card = document.createElement("div");
    card.addEventListener("click", onCard);
    card.appendChild(buildSteerNote({ text: "never read this", dropped: true, onRestore }));
    document.body.appendChild(card);

    restoreButton(card)?.click();
    expect(onRestore).toHaveBeenCalledTimes(1);
    expect(onCard).not.toHaveBeenCalled();
    card.remove();
  });

  // The control is what acts, so a note with nowhere to restore to renders none
  // rather than a button that does nothing.
  it("renders no control when there is nothing to restore into", () => {
    const note = buildSteerNote({ text: "never read this", dropped: true });
    expect(restoreButton(note)).toBeNull();
  });

  // An agent that never read the message cannot have said what it did about it,
  // so an ack on this state is dropped rather than rendered as a claim.
  it("renders no account line, because there is nothing it could be", () => {
    const note = buildSteerNote({
      text: "never read this",
      ack: "should not appear",
      dropped: true,
    });
    expect(note.querySelector(".steer-note-ack")).toBeNull();
    expect(note.getAttribute("aria-label")).toBe("Not delivered: never read this");
  });
});

describe("the message gets the room", () => {
  // Nothing is cut in the DOM: the row clamps to two lines in CSS, which degrades
  // to the whole text rather than to an ellipsis nothing can open.
  it("puts the whole message in the DOM and leaves the clamping to CSS", () => {
    const long =
      "stop rewriting the parser and instead widen the existing front-matter struct with the missing field";
    const note = buildSteerNote({ text: long, dropped: false });
    expect(textOf(note, ".steer-note-text")).toBe(long);
    expect(textOf(note, ".steer-note-text")).not.toContain("\u2026");
  });

  it("collapses whitespace so a multi-line message is one block", () => {
    const note = buildSteerNote({
      text: "first line\n\n   second line",
      ack: "did\n  the thing",
      dropped: false,
    });
    expect(textOf(note, ".steer-note-text")).toBe("first line second line");
    expect(textOf(note, ".steer-note-ack")).toBe("did the thing");
    expect(note.getAttribute("aria-label")).toBe(
      "Steered mid-turn: first line second line. The agent did: did the thing",
    );
  });
});
