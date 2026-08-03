// @vitest-environment happy-dom
// The turn card's header band: the trigger, its meta row, and the three-line
// clamp the folded-row navigation model depends on.
import { describe, it, expect } from "vitest";
import { buildTurnHeader, updateTurnHeader, type TurnHeaderData } from "./turn-header.js";

function data(over: Partial<TurnHeaderData> = {}): TurnHeaderData {
  return { n: 1, outcome: "completed", ts: 0, request: "short request", ...over };
}

function text(h: HTMLElement): HTMLElement {
  const t = h.querySelector<HTMLElement>(".turn-req-text");
  if (t === null) {
    throw new Error("no .turn-req-text");
  }
  return t;
}

function more(h: HTMLElement): HTMLButtonElement {
  const b = h.querySelector<HTMLButtonElement>(".turn-req-more");
  if (b === null) {
    throw new Error("no .turn-req-more");
  }
  return b;
}

const LONG = "x".repeat(400);

describe("buildTurnHeader", () => {
  it("renders the turn number, the request and an outcome dot", () => {
    const h = buildTurnHeader(data({ n: 14, request: "add the pause state" }));
    expect(h.querySelector(".turn-n")?.textContent).toBe("#14");
    expect(text(h).textContent).toBe("add the pause state");
    expect(h.querySelector(".turn-dot")?.getAttribute("aria-label")).toBe("Completed");
  });

  // The dot is colour-only in CSS, so the accessible name is the non-colour
  // channel that makes outcome readable at all.
  it("names every outcome on the dot rather than relying on colour", () => {
    for (const [outcome, label] of [
      ["running", "Running"],
      ["completed", "Completed"],
      ["interrupted", "Interrupted"],
      ["failed", "Failed"],
    ] as const) {
      const h = buildTurnHeader(data({ outcome }));
      expect(h.dataset["outcome"], outcome).toBe(outcome);
      expect(h.querySelector(".turn-dot")?.getAttribute("aria-label"), outcome).toBe(label);
    }
  });

  it("renders a typed trigger line instead of inventing a user message", () => {
    const h = buildTurnHeader(data({ request: undefined }));
    expect(h.dataset["trigger"]).toBe("system");
    expect(text(h).textContent).toBe("Agent-initiated turn");
    expect(more(h).hidden).toBe(true);
  });

  it("treats a whitespace-only request as no request", () => {
    const h = buildTurnHeader(data({ request: "   \n  " }));
    // updateTurnHeader is what branches; a blank string still reaches it as a
    // defined value, so the trim has to happen where the text is written.
    expect(text(h).textContent).toBe("");
  });
});

describe("the three-line clamp", () => {
  // Load-bearing rather than cosmetic: the folded rows are the session's
  // navigation surface, so one pasted stack trace in a prompt would push every
  // neighbouring row off-screen without this.
  it("clamps the request text by default", () => {
    const h = buildTurnHeader(data());
    expect(text(h).hasAttribute("data-clamped")).toBe(true);
  });

  it("offers a show-more only when the text overflows", () => {
    expect(more(buildTurnHeader(data({ request: "one line" }))).hidden).toBe(true);
    expect(more(buildTurnHeader(data({ request: LONG }))).hidden).toBe(false);
  });

  it("counts newlines, not just length, when deciding overflow", () => {
    const fourShortLines = "a\nb\nc\nd";
    expect(more(buildTurnHeader(data({ request: fourShortLines }))).hidden).toBe(false);
    expect(more(buildTurnHeader(data({ request: "a\nb" }))).hidden).toBe(true);
  });

  it("releases the clamp on show-more and restores it on show-less", () => {
    const h = buildTurnHeader(data({ request: LONG }));
    const btn = more(h);
    expect(btn.textContent).toBe("Show more");

    btn.click();
    expect(text(h).hasAttribute("data-clamped")).toBe(false);
    expect(h.dataset["expanded"]).toBe("");
    expect(btn.textContent).toBe("Show less");
    expect(btn.getAttribute("aria-expanded")).toBe("true");

    btn.click();
    expect(text(h).hasAttribute("data-clamped")).toBe(true);
    expect(h.dataset["expanded"]).toBeUndefined();
    expect(btn.textContent).toBe("Show more");
    expect(btn.getAttribute("aria-expanded")).toBe("false");
  });

  // A repaint must not fold a prompt the reader deliberately opened.
  it("survives a same-content update while expanded", () => {
    const h = buildTurnHeader(data({ request: LONG }));
    more(h).click();
    updateTurnHeader(h, data({ request: LONG, outcome: "interrupted" }));
    expect(text(h).hasAttribute("data-clamped")).toBe(false);
    expect(more(h).hidden).toBe(false);
    expect(h.dataset["outcome"]).toBe("interrupted");
  });

  // ...but new content is a new request, and its expansion belonged to the old.
  it("re-clamps when the request text changes", () => {
    const h = buildTurnHeader(data({ request: LONG }));
    more(h).click();
    updateTurnHeader(h, data({ request: LONG + "tail" }));
    expect(text(h).hasAttribute("data-clamped")).toBe(true);
  });
});

describe("updateTurnHeader", () => {
  it("is idempotent", () => {
    const h = buildTurnHeader(data({ n: 3, request: "hello" }));
    const before = h.outerHTML;
    updateTurnHeader(h, data({ n: 3, request: "hello" }));
    expect(h.outerHTML).toBe(before);
  });

  it("renumbers and re-tints in place", () => {
    const h = buildTurnHeader(data({ n: 3 }));
    updateTurnHeader(h, data({ n: 4, outcome: "failed" }));
    expect(h.querySelector(".turn-n")?.textContent).toBe("#4");
    expect(h.dataset["outcome"]).toBe("failed");
  });

  it("stamps a machine-readable timestamp when one is known", () => {
    const h = buildTurnHeader(data({ ts: 1_700_000_000_000 }));
    const t = h.querySelector<HTMLTimeElement>(".turn-ts");
    expect(t?.getAttribute("datetime")).toBe(new Date(1_700_000_000_000).toISOString());
    expect(t?.textContent).not.toBe("");
  });

  it("leaves the time empty rather than rendering the epoch", () => {
    const h = buildTurnHeader(data({ ts: 0 }));
    expect(h.querySelector(".turn-ts")?.textContent).toBe("");
  });
});
