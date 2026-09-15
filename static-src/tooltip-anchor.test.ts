// A TOOLTIP POINTS AT INK, NOT AT A HIT BOX.
//
// Three of this app's tooltip triggers are rows whose box is the layout's rather
// than their content's: the turn footer's ledger button (the band IS its hit box,
// `turn-footer-inset.test.ts`), that footer's changed-file rows, and the git
// Changes tab's file rows. All three paint their ink at the leading edge, so a tip
// centred on the trigger lands in empty space — measured on the live instance at
// 269px, 264px and 243px from the ink respectively, over 4, 19 and 320 rows.
//
// `data-tooltip-anchor` on the ink is what fixes it, and the attribute is DERIVED
// from the `attribute` option ui-primitives is configured with (`tooltip.ts` passes
// `data-tooltip`), so the app's own wiring is part of the claim: a rename that moved
// one and not the other would leave every mark inert with nothing else failing.
// That is why this measures through `./tooltip.js` rather than calling the library
// directly.
//
// The library owns the mechanism (mark clipping, the fallbacks, the nested-trigger
// rule) and pins it; what is measured here is that a real row built by a real
// builder ends up with its tip over its own ink.
//
// It therefore has a FLOOR: the installed `@cplieger/ui-primitives` must be a build
// whose tooltip reads the mark. A failure here where the marks are present in the
// builders is that pin being behind, not a regression in this app.
import { describe, it, expect, vi, beforeAll, afterAll, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { FileChange } from "./types.js";

vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: `navigate.js` imports
  // these names and Browser Mode links for real rather than reading properties off
  // a namespace object.
  openFile: undefined,
  openFileDiff: undefined,
  openFileGitDiff: undefined,
}));

const { buildTurnFooter, updateTurnFooter } = await import("./fundamentals/turn-footer.js");
const { initTooltips } = await import("./tooltip.js");
const { _resetForTest } = await import("@cplieger/ui-primitives/tooltip");

let style: HTMLStyleElement;
let card: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  _resetForTest();
  vi.useRealTimers();
  card.remove();
});

/** A turn card carrying its footer, panel open, at the transcript's own measure.
 *  `--content-max-w` rather than the viewport, because the row's slack — the empty
 *  space the tip used to land in — is what the column's width decides. */
function mountFooter(files: Record<string, FileChange>): HTMLElement {
  card = document.createElement("div");
  card.className = "turn";
  card.style.inlineSize = "800px";
  // Where the centred transcript column actually sits at this viewport. A card at
  // x=0 puts every tip against the viewport clamp (`margin: 4`), which is the
  // positioner working and would hide the property under it: the tip's centre would
  // read 60 whatever it was asked for.
  card.style.marginInlineStart = "240px";
  document.body.appendChild(card);
  const d = {
    outcome: "completed" as const,
    elapsedMs: 12_000,
    credits: 0.5,
    changedFiles: files,
  };
  const footer = buildTurnFooter(d);
  card.appendChild(footer);
  updateTurnFooter(footer, d);
  footer.dataset["info"] = "open";
  return footer;
}

/** Hover a trigger the way a pointer does — over a child, so the delegated
 *  controller has to resolve the trigger itself — and return the tip it shows. */
function hover(trigger: HTMLElement, over: Element = trigger): HTMLElement {
  vi.useFakeTimers();
  over.dispatchEvent(new Event("pointerover", { bubbles: true }));
  vi.advanceTimersByTime(600);
  const tip = document.querySelector<HTMLElement>(".uip-tooltip:not(.is-leaving)");
  if (tip === null) {
    throw new Error("no tooltip shown");
  }
  return tip;
}

/** The tip's centre on the inline axis, from the position the controller wrote. */
function tipCenterX(tip: HTMLElement): number {
  return parseFloat(tip.style.left) + tip.getBoundingClientRect().width / 2;
}

function centerX(el: Element): number {
  const r = el.getBoundingClientRect();
  return r.left + r.width / 2;
}

/** The tip is centred ON `mark` rather than on `trigger`.
 *
 *  The killing assertion is the EQUALITY, not a containment: a tip merely
 *  overlapping the ink passes for a row whose box happens to be near its content,
 *  and the ledger button's width is exactly that — layout the footer's grid decides
 *  (it took the row's whole slack on the build the complaint was made against, and
 *  its own content width on this one). Centre-on-the-mark can only hold when the
 *  mark placed it, at either width.
 *
 *  The premise beside it is the reason the property matters at all: the trigger IS
 *  bigger than the ink, so its centre is not the ink's. */
function expectAnchoredTo(tip: HTMLElement, mark: Element, trigger: HTMLElement): void {
  const m = mark.getBoundingClientRect();
  const t = trigger.getBoundingClientRect();
  const cx = tipCenterX(tip);
  expect(t.width, "the trigger is wider than its ink, which is the premise").toBeGreaterThan(
    m.width + 4,
  );
  expect(
    cx,
    `tip centre ${cx.toFixed(1)} against ink centre ${centerX(mark).toFixed(1)} ` +
      `and the row's own ${centerX(trigger).toFixed(1)}`,
  ).toBeCloseTo(centerX(mark), 0);
}

describe("the turn footer's ledger row", () => {
  it("points its tooltip at the `i`, not at the middle of the band", () => {
    const footer = mountFooter({});
    const ledger = footer.querySelector<HTMLElement>(".turn-ledger-summary");
    const info = footer.querySelector<HTMLElement>(".turn-ledger-info");
    expect(ledger).not.toBeNull();
    expect(info).not.toBeNull();
    if (ledger === null || info === null) {
      return;
    }
    initTooltips();
    expect(ledger.getAttribute("data-tooltip")).toBe("Show turn details");
    // Entering over the glyph, which is what a pointer reaching for the `i` does.
    expectAnchoredTo(hover(ledger, info), info, ledger);
  });
});

describe("the turn footer's changed-file rows", () => {
  it("points each row's tooltip at the path it opens", () => {
    const footer = mountFooter({
      "vibekit/internal/translate/streaming_tools.go": { lines_added: 6, lines_removed: 2 },
    });
    const row = footer.querySelector<HTMLElement>(".turn-file-row");
    expect(row).not.toBeNull();
    if (row === null) {
      return;
    }
    const path = row.querySelector<HTMLElement>(".turn-file-path");
    expect(path).not.toBeNull();
    if (path === null) {
      return;
    }
    initTooltips();
    expect(row.getAttribute("data-tooltip")).toContain("Open the diff for");
    expectAnchoredTo(hover(row, path), path, row);
  });
});
