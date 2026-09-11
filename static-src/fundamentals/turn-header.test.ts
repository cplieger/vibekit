// The turn card's header band: the trigger, and the out-of-flow badge that lets
// the request text be the only thing in it with a height.
//
// The CLAMP is not here any more: it is CSS-only and FOLD-conditional, so there
// is nothing in this module to drive and nothing a detached element can measure.
// `disclosure-row-css.test.ts` owns it, against real layout.
import { describe, it, expect, vi } from "vitest";
import {
  buildTurnHeader,
  updateTurnHeader,
  initTurnHeaderCallbacks,
  type TurnHeaderData,
} from "./turn-header.js";
import { initAttachmentPillCallbacks } from "../attachment-pill.js";

function data(over: Partial<TurnHeaderData> = {}): TurnHeaderData {
  return {
    n: 1,
    outcome: "completed",
    ts: 0,
    request: "short request",
    attachments: [],
    ...over,
  };
}

function attachmentRow(h: HTMLElement): HTMLElement {
  const r = h.querySelector<HTMLElement>(".turn-req-attachments");
  if (r === null) {
    throw new Error("no .turn-req-attachments");
  }
  return r;
}

function attachmentPaths(h: HTMLElement): (string | null)[] {
  return [...attachmentRow(h).querySelectorAll(".attachment-pill")].map((p) =>
    p.getAttribute("title"),
  );
}

function text(h: HTMLElement): HTMLElement {
  const t = h.querySelector<HTMLElement>(".turn-req-text");
  if (t === null) {
    throw new Error("no .turn-req-text");
  }
  return t;
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
  });

  it("treats a whitespace-only request as no request", () => {
    const h = buildTurnHeader(data({ request: "   \n  " }));
    // updateTurnHeader is what branches; a blank string still reaches it as a
    // defined value, so the trim has to happen where the text is written.
    expect(text(h).textContent).toBe("");
  });

  it("puts the number, the dot, the time and the hit count in one out-of-flow badge", () => {
    // ONE box for every readout, so the first line's indent reserves one column
    // rather than four, and every one of them is reachable at a stable depth.
    const h = buildTurnHeader(data({ n: 14, ts: 1_700_000_000_000 }));
    for (const sel of [".turn-n", ".turn-dot", ".turn-ts", ".turn-hit-count"]) {
      expect(h.querySelector(`:scope > .turn-badge > ${sel}`), sel).not.toBeNull();
    }
    // And the toggle is the band's own child, which is what `wireRowToggle` and
    // `setCardFolded` (messages.ts) reach at `:scope > .turn-fold-toggle`.
    expect(h.querySelector(":scope > .turn-fold-toggle")).not.toBeNull();
  });

  it("keeps the badge out of the request's text", () => {
    // MEASURED trap: an inline badge as the text's first child contaminates
    // `textContent`, which is exactly what the copy button reads, so every copied
    // prompt would begin `#14 10:42 ` — and `linkifyPaths` rewrites text nodes in
    // there too.
    const h = buildTurnHeader(data({ n: 14, ts: 1_700_000_000_000, request: "  fix the test  " }));
    expect(h.querySelector(".turn-ts")?.textContent, "the time really is stamped").not.toBe("");
    expect(text(h).textContent).toBe("fix the test");
    expect(text(h).querySelector(".turn-badge")).toBeNull();
    expect(text(h).contains(h.querySelector(".turn-badge"))).toBe(false);
  });

  it("offers no show-more", () => {
    // The clamp is CSS-only and fold-conditional, so the control and its
    // measurement machinery are gone; this is the guard that stops them creeping
    // back in unnoticed.
    const h = buildTurnHeader(data({ request: LONG }));
    expect(h.querySelector(".turn-req-more")).toBeNull();
    expect(text(h).hasAttribute("data-clamped")).toBe(false);
  });
});

function copyBtn(h: HTMLElement): HTMLButtonElement {
  const b = h.querySelector<HTMLButtonElement>(".turn-copy-req");
  if (b === null) {
    throw new Error("no .turn-copy-req");
  }
  return b;
}

describe("copying the sent prompt", () => {
  it("lives outside the request text, where the clamp cannot reach it", () => {
    // The clamp is scoped to `.turn-req-text`; a control inside it would be
    // hidden by a folded turn's four-line clamp.
    const h = buildTurnHeader(data());
    expect(h.querySelector(":scope > .turn-copy-req")).not.toBeNull();
    expect(text(h).querySelector(".turn-copy-req")).toBeNull();
  });

  it("copies the whole request, not the four lines a folded turn shows", () => {
    const copy = vi.fn();
    initTurnHeaderCallbacks({ copy });
    const h = buildTurnHeader(data({ request: LONG }));
    copyBtn(h).click();
    expect(copy).toHaveBeenCalledWith(copyBtn(h), LONG);
  });

  it("copies the trimmed request", () => {
    const copy = vi.fn();
    initTurnHeaderCallbacks({ copy });
    const h = buildTurnHeader(data({ request: "  fix the composer  " }));
    copyBtn(h).click();
    expect(copy).toHaveBeenCalledWith(copyBtn(h), "fix the composer");
  });

  it("reads the text at CLICK time, so a repaint cannot leave it stale", () => {
    const copy = vi.fn();
    initTurnHeaderCallbacks({ copy });
    const h = buildTurnHeader(data({ request: "first" }));
    updateTurnHeader(h, data({ request: "second" }));
    copyBtn(h).click();
    expect(copy).toHaveBeenCalledWith(copyBtn(h), "second");
  });

  it("is hidden on a turn the user did not ask for", () => {
    const h = buildTurnHeader(data({ request: undefined }));
    expect(copyBtn(h).hidden).toBe(true);
  });

  it("appears and disappears with the request across updates", () => {
    const h = buildTurnHeader(data({ request: "ask" }));
    expect(copyBtn(h).hidden).toBe(false);
    updateTurnHeader(h, data({ request: undefined }));
    expect(copyBtn(h).hidden).toBe(true);
    updateTurnHeader(h, data({ request: "ask again" }));
    expect(copyBtn(h).hidden).toBe(false);
  });

  it("carries an accessible name", () => {
    const h = buildTurnHeader(data());
    expect(copyBtn(h).getAttribute("aria-label")).toBe("Copy this prompt");
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

// The files the user attached, drawn as the composer's own pill so a sent request
// is identifiable by what went with it. The server stamps them on the user
// message because BuildPromptBlocks consumes them on the way out — an image or a
// document attachment never appears in the request text, so there is nothing to
// parse back out of it.
describe("the request's attachments", () => {
  const shot = { path: "out/shot.png", name: "shot.png" };
  const spec = { path: "docs/spec.md", name: "spec.md" };

  it("renders one pill per attachment", () => {
    const h = buildTurnHeader(data({ attachments: [shot, spec] }));
    expect(attachmentPaths(h)).toEqual(["out/shot.png", "docs/spec.md"]);
    expect(attachmentRow(h).classList.contains("hidden")).toBe(false);
  });

  it("hides the row when the request carried none", () => {
    const h = buildTurnHeader(data());
    expect(attachmentPaths(h)).toEqual([]);
    expect(attachmentRow(h).classList.contains("hidden")).toBe(true);
  });

  // The clamp is scoped to `.turn-req-text`. A pill inside it would vanish
  // whenever a folded turn's prompt clipped to four lines — and the attachments
  // are part of how a reader identifies which request this was.
  it("sits inside .turn-req but OUTSIDE the clamped text", () => {
    const h = buildTurnHeader(data({ request: LONG, attachments: [shot] }));
    expect(h.querySelector(".turn-req > .turn-req-attachments")).not.toBeNull();
    expect(text(h).querySelector(".attachment-pill")).toBeNull();
    expect(attachmentPaths(h)).toEqual(["out/shot.png"]);
  });

  it("stays clickable, opening the attachment it names", () => {
    const opened: string[] = [];
    initAttachmentPillCallbacks({
      open: (p) => {
        opened.push(p);
      },
    });
    const h = buildTurnHeader(data({ attachments: [shot, spec] }));
    const bodies = [...attachmentRow(h).querySelectorAll<HTMLButtonElement>(".attachment-open")];
    expect(bodies).toHaveLength(2);
    for (const b of bodies) {
      b.click();
    }
    expect(opened).toEqual(["out/shot.png", "docs/spec.md"]);
  });

  // A sent attachment cannot be un-sent, so the header's pill carries no `×` —
  // unlike the composer's, which is the same component with a remover passed in.
  it("offers no remove control", () => {
    const h = buildTurnHeader(data({ attachments: [shot] }));
    expect(attachmentRow(h).querySelector(".attachment-close")).toBeNull();
  });

  // updateTurnHeader runs on every repaint, streaming chunks included. Rebuilding
  // the row each time would destroy a pill the user is tabbed onto.
  it("keeps the same pill elements across a repaint with the same list", () => {
    const h = buildTurnHeader(data({ attachments: [shot, spec] }));
    const before = [...attachmentRow(h).children];
    updateTurnHeader(h, data({ attachments: [shot, spec], outcome: "failed" }));
    expect([...attachmentRow(h).children]).toEqual(before);
  });

  it("rebuilds when the list actually changes", () => {
    const h = buildTurnHeader(data({ attachments: [shot] }));
    updateTurnHeader(h, data({ attachments: [shot, spec] }));
    expect(attachmentPaths(h)).toEqual(["out/shot.png", "docs/spec.md"]);
    updateTurnHeader(h, data({ attachments: [] }));
    expect(attachmentPaths(h)).toEqual([]);
    expect(attachmentRow(h).classList.contains("hidden")).toBe(true);
  });

  // The row is synced ahead of the request-text branch, which returns early for an
  // agent-initiated turn — otherwise a repaint could leave pills describing a
  // request that is no longer there.
  it("clears the row even when the trigger becomes a system one", () => {
    const h = buildTurnHeader(data({ attachments: [shot] }));
    updateTurnHeader(h, data({ request: undefined, attachments: [] }));
    expect(attachmentPaths(h)).toEqual([]);
  });
});
