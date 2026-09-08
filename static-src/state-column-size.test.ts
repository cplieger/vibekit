// ---------------------------------------------------------------------------
// ONE MARK SIZE PER STATE COLUMN, measured rather than read.
//
// Three surfaces render the same column — the exec tree's `.ev-state`, the run
// card's `.run-step-glyph` and the composer band's `.run-bar-glyph` — and each
// slot holds either a SETTLED silhouette (`icons.ts` `outcomeIcon`, whose disc
// spans 12 of its 24 viewBox units) or an IN-FLIGHT ring drawn as a
// pseudo-element. The two must measure the same, or the mark resizes at the
// moment a step settles and the column reads as three sizes.
//
// WHY THIS FILE EXISTS RATHER THAN A SOURCE ASSERTION. `outcome-mark.test.ts`
// already pins that every one of those rings sizes itself off `--dot-size`, and
// it passed for as long as the defect existed: the reset's `*` does not reach a
// pseudo-element, so each ring defaulted to content-box and its border landed
// OUTSIDE the token. Measured before the fix — settled disc 8px against a
// 12px exec ring, a 12px run-bar ring and a 16px run-step ring, the last of them
// filling its whole slot at twice its own settled mark. Every one of those rules
// contained the string the source test looks for. So the oracle here is the
// RENDERED extent of both marks, and nothing below restates the stylesheet's
// arithmetic: the disc's size comes from its own path bbox scaled by its viewBox,
// and the ring's from its computed box.
//
// `tool-group-mark-css.test.ts` measures the same equality for the tool-group
// header's slot, which is the one that already stated `box-sizing` and the one
// that was already right.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { outcomeIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** Off-screen, at a width that lets every column lay out normally. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:-9999px;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.documentElement.dataset["pointer"] = "fine";
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** The three columns, each as the selector chain its own stylesheet requires. The
 *  scaffolding is hand-built because what is under test is the CSS geometry a state
 *  produces, and the state is a wire string either way; the MARK is not hand-built
 *  — it comes from `outcomeIcon`, the one resolver all three surfaces write. */
interface Column {
  readonly name: string;
  readonly slot: string;
  /** Build the row for a state and return the mark slot inside it. */
  readonly mount: (state: string) => HTMLElement;
}

function el(tag: string, cls: string, attrs: Record<string, string> = {}): HTMLElement {
  const n = document.createElement(tag);
  n.className = cls;
  for (const [k, v] of Object.entries(attrs)) {
    n.setAttribute(k, v);
  }
  return n;
}

const COLUMNS: readonly Column[] = [
  {
    name: "the exec tree",
    slot: ".ev-state",
    mount: (state) => {
      const page = el("div", "ev-page");
      const row = el("div", "ev-row", { "data-state": state });
      const main = el("div", "ev-row-main");
      const slot = el("span", "ev-state");
      main.appendChild(slot);
      row.appendChild(main);
      page.appendChild(row);
      host.appendChild(page);
      return slot;
    },
  },
  {
    name: "the run card",
    slot: ".run-step-glyph",
    mount: (state) => {
      const card = el("div", "run-card");
      const step = el("div", "run-step", { "data-status": state });
      const head = el("a", "run-step-head");
      const slot = el("span", "run-step-glyph");
      head.appendChild(slot);
      step.appendChild(head);
      card.appendChild(step);
      host.appendChild(card);
      return slot;
    },
  },
  {
    name: "the run bar",
    slot: ".run-bar-glyph",
    mount: (state) => {
      const bar = el("div", "run-bar");
      const row = el("div", "run-bar-row", { "data-state": state });
      const open = el("button", "run-bar-open");
      const slot = el("span", "run-bar-glyph");
      open.appendChild(slot);
      row.appendChild(open);
      bar.appendChild(row);
      host.appendChild(bar);
      return slot;
    },
  },
];

/** The painted diameter of a settled silhouette: its path's own bbox scaled by its
 *  viewBox into the box the stylesheet gave it. Never the ratio the CSS uses, so a
 *  wrong ratio on either side fails here. */
function paintedDisc(col: Column): number {
  const slot = col.mount("ok");
  slot.appendChild(iconEl(outcomeIcon("ok")));
  const mark = slot.querySelector("svg");
  const path = mark?.querySelector("path");
  expect(mark, `${col.name}: the settled slot holds a silhouette`).not.toBeNull();
  expect(path, `${col.name}: and that silhouette is a path`).not.toBeNull();
  if (mark === null || path === null || path === undefined) {
    throw new Error("unreachable");
  }
  const units = Number.parseFloat(mark.getAttribute("viewBox")?.split(/\s+/)[2] ?? "0");
  expect(units, `${col.name}: the silhouette declares a viewBox`).toBeGreaterThan(0);
  return (path.getBBox().width / units) * mark.getBoundingClientRect().width;
}

/** The outer diameter of an in-flight ring, and the box-sizing that decides it. */
function paintedRing(col: Column, state: string): { outer: number; boxSizing: string } {
  const slot = col.mount(state);
  const ring = getComputedStyle(slot, "::before");
  expect(ring.content, `${col.name}: ${state} draws a ring`).not.toBe("none");
  expect(
    Number.parseFloat(ring.borderTopWidth),
    `${col.name}: ${state} draws it as a ring`,
  ).toBeGreaterThan(0);
  return { outer: Number.parseFloat(ring.width), boxSizing: ring.boxSizing };
}

describe("a state column's mark", () => {
  it.each(COLUMNS.map((c) => [c.name, c] as const))(
    "is one size in %s, settled or in flight",
    (_name, col) => {
      const disc = paintedDisc(col);
      expect(disc, "the settled disc has a real extent").toBeGreaterThan(0);
      host.replaceChildren();

      // Every in-flight state in that column, so a rule added for one of them
      // cannot pick its own diameter.
      const states =
        col.slot === ".ev-state"
          ? ["running", "waiting", "unknown", "pending"]
          : ["running", "waiting"];
      for (const state of states) {
        const { outer, boxSizing } = paintedRing(col, state);
        // Named, because it is the property whose absence caused the defect and a
        // content-box ring is off by exactly its border on both sides.
        expect(boxSizing, `${col.name}: ${state} states box-sizing`).toBe("border-box");
        expect(outer, `${col.name}: ${state} matches the settled disc`).toBeCloseTo(disc, 2);
        host.replaceChildren();
      }
    },
  );

  it.each(COLUMNS.map((c) => [c.name, c] as const))(
    "fits inside the slot it is centred in, in %s",
    (_name, col) => {
      // The run-step ring used to fill its slot edge to edge, which is what its
      // `margin: 0 auto` was compensating for; a mark that exactly fills its slot
      // has no room to be centred in and reads as a different component.
      const slot = col.mount("running");
      const slotWidth = slot.getBoundingClientRect().width;
      const outer = Number.parseFloat(getComputedStyle(slot, "::before").width);
      expect(slotWidth, `${col.name}: the slot has a reserved size`).toBeGreaterThan(0);
      expect(outer, `${col.name}: the ring leaves room inside its slot`).toBeLessThan(slotWidth);
    },
  );
});
