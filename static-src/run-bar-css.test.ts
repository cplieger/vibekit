// The run bar's geometry and its motion, measured against the real assembled
// cascade rather than reasoned about.
//
// Three claims a source read cannot make. THE MEASURE: the bar has to end on the
// same two edges as the prompt box and the dock, or the band reads as three
// different columns — and that alignment comes from four separate declarations
// agreeing, which is exactly the kind of thing that drifts. THE MECHANICAL
// PROPERTY: the bar grows the band upward and shrinks the transcript by exactly its
// own height, covering nothing (26-dock.css states it for the dock; this is the
// third region to rely on it). THE MOTION: `working` beats and `waiting` is the
// same ring standing still, which is the app's in-flight axis, and `getAnimations()`
// is the only honest reader of it — the animation lives on a ::before overlay, so it
// is reachable through `{ subtree: true }` and through nothing else.
//
// The glyph's LOOK is not this file's subject and deliberately not asserted here: it
// is the workflow mark's, shared by selector list with the tab strip's two marks
// (12-tabs.css "The workflow mark"), and `tab-dot.test.ts` measures that share across
// all three surfaces. What is measured here is the mark's BOX inside the row, which
// is a property of this bar's own grid.
//
// The HIT FLOOR is measured here too, because the row deliberately declares no
// `min-height` of its own: the zero-specificity floor in 61-mcp-tools.css is what
// gives it one, and a regression in that rule's coverage would otherwise only show
// up on a phone. Measuring the rendered HEIGHT against 24px is the version of that
// case which cannot fail — the row's own box is 27px, above the fine tier's 1.5rem
// floor — so what is asserted is the RESOLVED `min-height` against the token, plus
// the coarse tier, where 2.75rem is genuinely the thing deciding the target.

import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** A token's rendered length, read off a real element rather than restated. */
function lengthPx(root: HTMLElement, expr: string): number {
  const probe = document.createElement("div");
  probe.style.inlineSize = expr;
  root.appendChild(probe);
  const px = probe.getBoundingClientRect().width;
  probe.remove();
  return px;
}

/** Row state -> the workflow mark's own status, mirroring `run-bar.ts`'s
 *  `runMarkStatus`. A transcription, and it is bounded on both sides: the PRODUCER's
 *  half is pinned against real rows in `run-bar.test.ts` ("marks its glyph with the
 *  tab strip's status for every live state"), and a state absent from this map gets
 *  no attribute, which is the same "nothing to show" the unknown row is here to
 *  measure. Importing the real function instead would drag the store, the dock, the
 *  clock registry and the run view into a stylesheet test. */
const MARK_STATUS: Record<string, string | undefined> = {
  running: "working",
  waiting: "waiting",
  input: "input",
};

let style: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** The composer band as `static/index.html` builds it, with the transcript above it
 *  so the shrink is measurable. `#chat-view` is the flex column both live in. */
function mountBand(states: readonly string[]): {
  bar: HTMLUListElement;
  wrap: HTMLElement;
  box: HTMLElement;
  dock: HTMLElement;
  rows: HTMLElement[];
} {
  host = document.createElement("div");
  host.id = "chat-view";
  host.className = "view";
  // A definite height, or the flex column has nothing to divide between the
  // transcript and the bar and the shrink cannot be observed.
  host.style.blockSize = "600px";
  host.style.inlineSize = "1000px";
  host.style.display = "flex";
  host.style.flexDirection = "column";

  // The real nesting: `#messages-wrap-outer` is the FLEX CHILD that the band's
  // height is taken out of, and `#messages-wrap` is `position: absolute; inset: 0`
  // inside it, so the scroller follows the wrapper exactly. Measuring the scroller
  // measures the shrink.
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const wrap = document.createElement("div");
  wrap.id = "messages-wrap";
  outer.appendChild(wrap);

  const form = document.createElement("form");
  form.id = "prompt-form";
  form.className = "bottom-bar";

  const dock = document.createElement("div");
  dock.id = "decision-dock";
  dock.className = "decision-dock hidden";

  const bar = document.createElement("ul");
  bar.id = "run-bar";
  bar.className = "run-bar";

  const rows = states.map((state) => {
    const li = document.createElement("li");
    li.className = "run-bar-row";
    li.dataset["state"] = state;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "run-bar-open";
    for (const cls of [
      "run-bar-glyph",
      "run-bar-name",
      "run-bar-state",
      "run-bar-steps",
      "run-bar-clock",
    ]) {
      const span = document.createElement("span");
      span.className = cls;
      // THE GLYPH IS KEYED ON ITS OWN `data-status`, in the WORKFLOW MARK's
      // vocabulary rather than the row's — the two differ for the state that matters
      // most (`running` on the row, `working` on the mark, which is the tab strip's
      // word), and 12-tabs.css paints all three surfaces off that attribute. It
      // carries no text, so the mark is the whole content.
      if (cls === "run-bar-glyph") {
        const mark = MARK_STATUS[state];
        if (mark !== undefined) {
          span.dataset["status"] = mark;
        }
      } else {
        span.textContent = cls === "run-bar-name" ? "nightly sweep" : "x";
      }
      btn.appendChild(span);
    }
    li.appendChild(btn);
    bar.appendChild(li);
    return li;
  });

  const box = document.createElement("div");
  box.id = "prompt-box";
  box.className = "prompt-box";
  const ta = document.createElement("textarea");
  ta.id = "prompt-input";
  box.appendChild(ta);

  form.append(dock, bar, box);
  host.append(outer, form);
  document.body.appendChild(host);
  return { bar, wrap, box, dock, rows };
}

afterAll(() => {
  host?.remove();
});

describe("the run bar's geometry", () => {
  it("ends on the same edges as the prompt box and the dock", () => {
    const { bar, box, dock } = mountBand(["running"]);
    dock.classList.remove("hidden");

    const barRect = bar.getBoundingClientRect();
    const boxRect = box.getBoundingClientRect();
    const dockRect = dock.getBoundingClientRect();

    expect(barRect.left).toBeCloseTo(boxRect.left, 1);
    expect(barRect.right).toBeCloseTo(boxRect.right, 1);
    expect(barRect.left).toBeCloseTo(dockRect.left, 1);
    expect(barRect.right).toBeCloseTo(dockRect.right, 1);
    host.remove();
  });

  it("shrinks the transcript by exactly its own height and covers nothing", () => {
    const { bar, wrap } = mountBand(["running", "waiting"]);
    const shown = wrap.getBoundingClientRect();
    const barRect = bar.getBoundingClientRect();
    expect(barRect.height).toBeGreaterThan(0);
    // The MARGIN box is what the band grows by: the region carries the same
    // `margin-block-end` the dock and the steer stack do, which is the gap between
    // them rather than part of the bar.
    const margin = Number.parseFloat(getComputedStyle(bar).marginBlockEnd);
    expect(margin).toBeGreaterThan(0);

    // The `.hidden` utility is `display: none !important`, so the region's whole box
    // leaves the flex column and the transcript takes the space back.
    bar.classList.add("hidden");
    const hidden = wrap.getBoundingClientRect();

    expect(hidden.height - shown.height).toBeCloseTo(barRect.height + margin, 0);
    // And while it is shown, the two boxes do not overlap: the bar is a sibling that
    // grows the band, not an overlay.
    expect(shown.bottom).toBeLessThanOrEqual(barRect.top + 0.5);
    host.remove();
  });

  it("takes its hit target from the shared floor, at whichever tier is in force", () => {
    const { rows } = mountBand(["running"]);
    const btn = rows[0]?.querySelector<HTMLElement>(".run-bar-open") ?? null;
    expect(btn).not.toBeNull();
    if (btn === null) {
      return;
    }

    // The row declares no `min-height`, so it RESOLVES 61-mcp-tools.css's
    // `:where(button, …)` token. Measured with that rule deleted the property reads
    // 0px, while the rendered height still clears 24px off the row's own 27px box —
    // which is why the height is not what this asserts on the fine tier.
    expect(Number.parseFloat(getComputedStyle(btn).minHeight)).toBeCloseTo(
      lengthPx(host, "var(--hit-floor)"),
      1,
    );

    // The coarse tier is where the floor BINDS: 2.75rem is above the row's own box,
    // so it is the thing deciding the rendered target. The tier is the pointer rather
    // than a width (01-tokens.css), so `data-pointer` is how a test reaches it.
    const root = document.documentElement;
    const had = root.getAttribute("data-pointer");
    root.setAttribute("data-pointer", "coarse");
    try {
      const floor = lengthPx(host, "var(--hit-floor)");
      expect(floor, "the coarse tier's floor is above the row's own height").toBeGreaterThan(27);
      expect(btn.getBoundingClientRect().height).toBeCloseTo(floor, 1);
    } finally {
      if (had === null) {
        root.removeAttribute("data-pointer");
      } else {
        root.setAttribute("data-pointer", had);
      }
    }
    host.remove();
  });
});

describe("the run bar's state column", () => {
  it("beats the working mark and holds the waiting one still", () => {
    // The IN-FLIGHT AXIS, restated for the mark: motion means work is moving, and
    // `waiting` is the same ring standing still. What CHANGED is the motion itself —
    // it was a conic arc spinning at `--spin-dur`, and it is now the activity dot's
    // own glow beat on a masked overlay at `--dot-beat-dur`, because the bar's glyph
    // shares 12-tabs.css's rules rather than carrying a look of its own. Asserted at
    // the KEYFRAME name and the shared period, so a beat retuned in that block moves
    // the token and this stays true, while a second period declared here fails.
    const { rows } = mountBand(["running", "waiting"]);
    const [running, waiting] = rows;
    const glyph = (row: HTMLElement | undefined): Element | null =>
      row?.querySelector(".run-bar-glyph") ?? null;

    // `{ subtree: true }` because the animation is on the ::before overlay, which is
    // the only way a ring can beat without its bright core filling its own hole.
    const beating = glyph(running)?.getAnimations({ subtree: true }) ?? [];
    expect(beating.length, "the working mark beats").toBe(1);
    const anim = beating[0];
    expect(anim === undefined ? "" : (anim as CSSAnimation).animationName).toBe("vk-dot-beat");
    const seconds = Number.parseFloat(
      getComputedStyle(document.documentElement).getPropertyValue("--dot-beat-dur"),
    );
    expect(seconds, "--dot-beat-dur resolves").toBeGreaterThan(0);
    expect(anim?.effect?.getComputedTiming().duration).toBe(seconds * 1000);

    expect(glyph(waiting)?.getAnimations({ subtree: true }).length).toBe(0);
    host.remove();
  });

  it("draws the mark itself at the dot token's size, and reserves that box with no state", () => {
    // The MARK is the element now, not a ::before ring inside it: `box-sizing:
    // border-box` is what lets a 2px band paint inside `--dot-size` rather than
    // around it, so the element's own box is the assertion. The stateless row is in
    // the sweep deliberately — that is what "reserved box, nothing to show" means,
    // and it is what keeps the name from stepping sideways when the first fetch
    // lands.
    const { rows } = mountBand(["running", "waiting", "input", "unknown"]);
    const dot = lengthPx(host, "var(--dot-size)");
    expect(dot).toBeGreaterThan(0);
    for (const row of rows) {
      const glyph = row.querySelector(".run-bar-glyph");
      expect(glyph).not.toBeNull();
      if (glyph === null) {
        continue;
      }
      const box = glyph.getBoundingClientRect();
      expect(box.width, `${row.dataset["state"]} width`).toBeCloseTo(dot, 1);
      expect(box.height, `${row.dataset["state"]} height`).toBeCloseTo(dot, 1);
    }
    host.remove();
  });
});
