// ---------------------------------------------------------------------------
// Tests for fundamentals/steer-note.ts: the transcript row a steer becomes once
// it leaves the dock.
//
// Two things live here that the dock used to carry: the READ state (the agent
// consumed the message) and the agent's own account of what it DID about it.
// Both were a green check and an extra line inside the composer, which put the
// state of the transcript in the message box and left the transcript itself
// unexplained.
//
// And one thing that is new: WHOSE words the row holds. KAS's steering buffer is
// the only inbound channel into a live turn, so a workflow's report arrives on it
// beside the user's corrections — with one label the report read as something the
// reader had typed, which is the reported defect these label cases pin.
//
// A pure view, driven by its own data and read as DOM. The clamp is measured
// against real layout at the bottom of the file, because "does this overflow four
// lines" has no honest answer without it.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";
import { buildSteerNote, type SteerNoteData } from "./steer-note.js";
import { mountAppCSS } from "../__test-helpers__/css-rules.js";

function note(over: Partial<SteerNoteData> = {}): HTMLElement {
  return buildSteerNote({ text: "actually target main", origin: "user", dropped: false, ...over });
}

function textOf(root: HTMLElement, sel: string): string | null {
  return root.querySelector(sel)?.textContent ?? null;
}

/** Any control at all. The note carries none in either state now: the boundary
 *  resend sends an undelivered message for the reader, so the button that used to
 *  ask them to do it by hand is gone (steer-note.ts's header records the reasoning),
 *  and the CLAMP's opener is the one button the note may hold. */
function controls(root: HTMLElement): HTMLButtonElement[] {
  return Array.from(root.querySelectorAll<HTMLButtonElement>("button")).filter(
    (b) => !b.classList.contains("steer-note-more"),
  );
}

describe("the read state", () => {
  it("names the reader's own mid-turn message and carries the whole of it", () => {
    const n = note();

    expect(n.dataset["state"]).toBe("read");
    expect(n.dataset["origin"]).toBe("user");
    expect(textOf(n, ".steer-note-label")).toBe("Mid-turn message");
    expect(textOf(n, ".steer-note-text")).toBe("actually target main");
    // The label and the glyph are both visual, so the state has to be in the
    // accessible name too.
    expect(n.getAttribute("aria-label")).toBe("Mid-turn message: actually target main");
  });

  // What the agent said it did, on the row where the course actually changed.
  it("renders the agent's account of what it did when one arrived", () => {
    const n = note({ ack: "rebased onto main instead" });

    expect(textOf(n, ".steer-note-ack")).toBe("rebased onto main instead");
    // The steer's own text stays the message: the note has to remain
    // identifiable as the thing that was sent.
    expect(textOf(n, ".steer-note-text")).toBe("actually target main");
    expect(n.getAttribute("aria-label")).toBe(
      "Mid-turn message: actually target main. The agent did: rebased onto main instead",
    );
  });

  // An agent that never emitted the acknowledgement marker has said nothing, and
  // an empty line would read as a verdict with nothing in it.
  it("renders no account line when the agent said nothing about it", () => {
    const n = note({ text: "one" });
    expect(n.querySelector(".steer-note-ack")).toBeNull();
    expect(n.getAttribute("aria-label")).toBe("Mid-turn message: one");
  });

  it("carries no control", () => {
    expect(controls(note({ text: "one" }))).toEqual([]);
  });
});

describe("the dropped state", () => {
  // THE LABEL SAYS WHAT HAPPENED NEXT. "Not delivered" alone left the reader looking
  // at their own words twice — once as this mark, once as the turn the resend opened
  // under it — with nothing joining them. Both halves are stated: not read, and sent.
  it("says it was not read and was sent as a new turn, and keeps the text", () => {
    const n = note({ text: "never read this", dropped: true });

    expect(n.dataset["state"]).toBe("dropped");
    expect(textOf(n, ".steer-note-label")).toBe("Not read — sent as a new turn");
    expect(textOf(n, ".steer-note-text")).toBe("never read this");
    expect(n.getAttribute("aria-label")).toBe("Not read — sent as a new turn: never read this");
  });

  // THE MARK IS A RECORD, NOT AN OFFER. It used to carry "Put it back in the
  // message box", which was the reader's only route to recovering an unread
  // message; the resend carries the text into the next turn for them now, so a
  // button here would ask for a job already done.
  it("carries no control, so the mark is a record rather than an offer", () => {
    const n = note({ text: "never read this", dropped: true });
    expect(controls(n)).toEqual([]);
    expect(n.textContent).not.toContain("message box");
  });

  // An agent that never read the message cannot have said what it did about it,
  // so an ack on this state is dropped rather than rendered as a claim.
  it("renders no account line, because there is nothing it could be", () => {
    const n = note({ text: "never read this", ack: "should not appear", dropped: true });
    expect(n.querySelector(".steer-note-ack")).toBeNull();
    expect(n.getAttribute("aria-label")).toBe("Not read — sent as a new turn: never read this");
  });
});

// The reported defect: a workflow injects its result through the same steering
// tools the user's own message rides, and the note said the same thing about
// both. Nothing on the wire separated them either — see vibekit.SteerOrigin.
describe("whose words the note holds", () => {
  it("names a workflow's report as one, and never as the reader's message", () => {
    const n = note({ text: "The review finished with 3 findings.", origin: "agent" });

    expect(n.dataset["origin"]).toBe("agent");
    expect(textOf(n, ".steer-note-label")).toBe("Workflow result");
    expect(n.getAttribute("aria-label")).toBe(
      "Workflow result: The review finished with 3 findings.",
    );
  });

  it("says a workflow's report was not delivered, in its own words", () => {
    const n = note({ text: "step failed", origin: "agent", dropped: true });
    expect(textOf(n, ".steer-note-label")).toBe("Workflow result not delivered");
  });

  it("gives the two origins different titles and different glyphs", () => {
    const mine = note();
    const theirs = note({ origin: "agent" });

    expect(textOf(mine, ".steer-note-label")).not.toBe(textOf(theirs, ".steer-note-label"));
    // The glyph is the second channel, so a reader who cannot tell the labels
    // apart at a glance still has a mark — and neither is colour alone.
    const glyph = (n: HTMLElement): string | undefined =>
      n.querySelector(".tool-icon svg path")?.getAttribute("d") ?? undefined;
    expect(glyph(mine)).toBeDefined();
    expect(glyph(theirs)).toBeDefined();
    expect(glyph(mine)).not.toBe(glyph(theirs));
  });
});

describe("the message keeps its shape", () => {
  // Nothing is cut in the DOM: the clamp is an OPENER, so every character stays
  // where find-in-chat can match it.
  it("puts the whole message in the DOM whatever its length", () => {
    const long =
      "stop rewriting the parser and instead widen the existing front-matter " +
      "struct with the missing field, then re-run the census against both bundles";
    const n = note({ text: long });
    expect(textOf(n, ".steer-note-text")).toBe(long);
    expect(textOf(n, ".steer-note-text")).not.toContain("\u2026");
  });

  // It used to collapse whitespace, which destroyed the shape of anything typed
  // as more than one line. With the text fully openable there is nothing to buy.
  it("keeps the newlines the reader typed", () => {
    const n = note({ text: "first line\n\nsecond line", ack: "did\n  the thing" });
    expect(textOf(n, ".steer-note-text")).toBe("first line\n\nsecond line");
    // The single-string surfaces still collapse: an accessible name is announced
    // as one string and the ack is a one-line verdict.
    expect(textOf(n, ".steer-note-ack")).toBe("did the thing");
    expect(n.getAttribute("aria-label")).toBe(
      "Mid-turn message: first line second line. The agent did: did the thing",
    );
  });
});

// The clamp against REAL layout, which is the only thing that can answer "does
// this overflow four lines". Everything above builds a detached note, where both
// scrollHeight and clientHeight read 0 and the verdict can only be the character
// guess; these mount it under the shipped stylesheet.
describe("the clamp measured on the page", () => {
  let styleEl: HTMLStyleElement;
  let host: HTMLElement;

  beforeAll(() => {
    styleEl = mountAppCSS();
    host = document.createElement("div");
    document.body.appendChild(host);
  });

  afterAll(() => {
    styleEl.remove();
    host.remove();
  });

  afterEach(() => {
    host.replaceChildren();
  });

  function mount(d: Partial<SteerNoteData>, width = 700): HTMLElement {
    host.style.inlineSize = `${String(width)}px`;
    const n = note(d);
    host.replaceChildren(n);
    return n;
  }

  function textEl(n: HTMLElement): HTMLElement {
    const t = n.querySelector<HTMLElement>(".steer-note-text");
    if (t === null) {
      throw new Error("no .steer-note-text");
    }
    return t;
  }

  function moreEl(n: HTMLElement): HTMLButtonElement {
    const b = n.querySelector<HTMLButtonElement>(".steer-note-more");
    if (b === null) {
      throw new Error("no .steer-note-more");
    }
    return b;
  }

  /** Wait for the opener to reach `hidden`, for a verdict that must CHANGE. */
  async function settles(n: HTMLElement, hidden: boolean, why: string): Promise<void> {
    await vi.waitFor(() => {
      expect(moreEl(n).hidden, why).toBe(hidden);
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

  const FORTY_LINES = Array.from({ length: 40 }, (_, i) => `line ${String(i + 1)}`).join("\n");

  it("offers no opener for a one-line message", async () => {
    const n = mount({ text: "use tabs" });
    await observerRuns();
    expect(moreEl(n).hidden).toBe(true);
  });

  it("offers one for a forty-line message, with every line still in the DOM", async () => {
    const n = mount({ text: FORTY_LINES });
    await settles(n, false, "offered for a message past four lines");
    // The whole point of `data-clamped` over an ellipsis: the text is clipped by
    // overflow, not truncated, so find-in-chat can still match the last line.
    expect(textEl(n).textContent).toContain("line 40");
    expect(textEl(n).scrollHeight).toBeGreaterThan(textEl(n).clientHeight);
  });

  it("opens on the click and closes again", async () => {
    const n = mount({ text: FORTY_LINES });
    await settles(n, false, "offered");
    const btn = moreEl(n);
    expect(btn.getAttribute("aria-expanded")).toBe("false");

    btn.click();
    expect(textEl(n).hasAttribute("data-clamped")).toBe(false);
    expect(btn.textContent).toBe("Show less");
    expect(btn.getAttribute("aria-expanded")).toBe("true");

    btn.click();
    expect(textEl(n).hasAttribute("data-clamped")).toBe(true);
    expect(btn.textContent).toBe("Show more");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
  });

  // The other half of measuring once: four lines of a wide card become more of a
  // narrow one, and a measure-once clamp cut the difference away silently.
  it("re-decides at every width", async () => {
    const body =
      "widen the existing front-matter struct with the missing field instead of " +
      "rewriting the parser, and re-run the census against both bundles first";
    const n = mount({ text: body }, 1100);
    await settles(n, true, "fits wide");

    host.style.inlineSize = "150px";
    await settles(n, false, "offered once it no longer fits");
  });
});
