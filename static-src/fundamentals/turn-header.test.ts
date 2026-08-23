// The turn card's header band: the trigger, its meta row, and the three-line
// clamp the folded-row navigation model depends on.
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

function copyBtn(h: HTMLElement): HTMLButtonElement {
  const b = h.querySelector<HTMLButtonElement>(".turn-copy-req");
  if (b === null) {
    throw new Error("no .turn-copy-req");
  }
  return b;
}

describe("copying the sent prompt", () => {
  it("lives in the META row, where the clamp cannot reach it", () => {
    // The clamp is scoped to `.turn-req-text`; a control inside it would be
    // hidden by a long prompt folding to three lines.
    const h = buildTurnHeader(data());
    expect(h.querySelector(".turn-head-row > .turn-copy-req")).not.toBeNull();
    expect(text(h).querySelector(".turn-copy-req")).toBeNull();
  });

  it("copies the whole request, not the three lines the clamp shows", () => {
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
  // whenever a long prompt folded to three lines — and the attachments are part
  // of how a reader identifies which request this was.
  it("sits inside .turn-req but OUTSIDE the clamped text", () => {
    const h = buildTurnHeader(data({ request: LONG, attachments: [shot] }));
    expect(h.querySelector(".turn-req > .turn-req-attachments")).not.toBeNull();
    expect(text(h).querySelector(".attachment-pill")).toBeNull();
    expect(text(h).hasAttribute("data-clamped")).toBe(true);
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
