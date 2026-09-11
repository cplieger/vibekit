// ---------------------------------------------------------------------------
// The composer's two rows are ONE band height, on whichever pointer tier is in
// force.
//
// The box is a textarea over a row of controls. Both heights used to be literals
// — `2rem` on `.pill`/`.send-btn`, `2.5rem` on `--composer-rest-h` — which agree
// on the fine tier by arithmetic (4 + 32 + 4 = 40) and cannot agree anywhere
// else: the universal hit-target floor lifts a control to 44px on a coarse
// pointer, so the pill row rendered 52px under a textarea still at 40. A
// width-keyed override in 15-input.css carried the match for a phone, which is
// exactly the axis that cannot reach a touch DESKTOP — wide and coarse.
//
// Two halves, following composer-font-css.test.ts, because neither answers the
// other's question. The SOURCE read says the height is one derived value and that
// no width query overrides it, which is the regression a computed style cannot
// see (a passing measurement says nothing about the axis it was keyed on). The
// MEASUREMENT says the two rows match at four (width, tier) combinations, the
// wide-coarse one being the reported defect.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { allRules, loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/** The composer subtree as the page ships it, sliced rather than hand-written:
 *  the pill row's height is the sum of its own controls, so a fixture with two
 *  invented pills would measure a row this app does not render. */
function composerMarkup(): string {
  const open = indexHtml.indexOf('<form id="prompt-form"');
  expect(open, 'static/index.html has no <form id="prompt-form">').toBeGreaterThan(-1);
  const close = indexHtml.indexOf("</form>", open);
  expect(close, "the prompt form is not closed").toBeGreaterThan(open);
  return indexHtml.slice(open, close + "</form>".length);
}

describe("the height's declarations, read from source", () => {
  const css = loadCSS("15-input.css");

  it("declares one control height for the box, and it is TIER-INDEPENDENT", () => {
    const form = ruleContaining(css, '[id="prompt-form"]', "top");
    // THIS PIN MOVED DELIBERATELY (amendment §B, 2026-09-10). It was
    // `max(var(--ctl-h-dense), var(--hit-floor))`, which put the 44px TARGET floor
    // into the PAINTED box — 44px of button around a 20px glyph, which is the
    // "grown buttons look empty" report. The painted box is 32 at every tier now
    // and the target is grown back by the `::after` expander the case below
    // measures, which is what R1a permits and what four other sites in this app
    // already do.
    expect(form.body).toMatch(/--composer-ctl-h:\s*2rem/);
    // The floor must NOT be read here again, or the box takes the target's measure
    // for the third time.
    expect(form.body).not.toMatch(/--composer-ctl-h:[^;]*--hit-floor/);
  });

  it("derives the textarea's resting band from that height and the row's own inset", () => {
    const input = ruleContaining(css, '[id="prompt-input"]', "top");
    // The inset term MOVED with the same amendment: the row's padding now pays for
    // the expander's block reach (`--composer-pill-pad`, 6px on coarse against
    // `--pill-inset`'s 4), so reading the old term here left the textarea 44px
    // under a 45px pill row — the disagreement this whole file exists to pin.
    expect(input.body).toMatch(
      /--composer-rest-h:\s*calc\(var\(--composer-ctl-h\)\s*\+\s*2\s*\*\s*var\(--composer-pill-pad\)\)/,
    );
  });

  it("gives the two controls that height instead of a literal", () => {
    for (const selector of [".pill", ".send-btn"]) {
      const rule = ruleContaining(css, selector, "top");
      // The control's OWN declarations, cut at its first nested block: both rules
      // carry a nested `& svg` sizing for their glyph, which is a legitimate
      // statement about the drawing rather than about the row.
      const brace = rule.body.indexOf("{");
      const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
      // Either spelling: `.send-btn` reads the term on BOTH axes (`block-size` and
      // `inline-size`) because amendment §A requires it to be square structurally
      // rather than by two literals that can drift.
      expect(own, `${selector} reads the shared height`).toMatch(
        /(?:block-size|height):\s*var\(--composer-ctl-h\)/,
      );
      // A literal here is what let the row disagree with the textarea above it.
      expect(own, `${selector} declares no literal height`).not.toMatch(/(^|[^-])height:\s*\d/);
    }
  });

  it("makes Send square from ONE term, so the two axes cannot drift", () => {
    // Amendment §A. It was 42x32 on a fine pointer and 44x44 on a coarse one: the
    // inline axis came from `padding: 0 var(--sp-3)` plus the glyph and the block
    // axis from `height`, and the coarse square was the hit floor's `min-width`
    // landing on the same number by coincidence. Both axes read `--composer-ctl-h`
    // now, `aspect-ratio` states the invariant, and the physical `min-width: 0` is
    // what lets the box be smaller than the floor at all.
    const rule = ruleContaining(css, ".send-btn", "top");
    const brace = rule.body.indexOf("{");
    const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
    expect(own).toMatch(/inline-size:\s*var\(--composer-ctl-h\)/);
    expect(own).toMatch(/block-size:\s*var\(--composer-ctl-h\)/);
    expect(own).toMatch(/aspect-ratio:\s*1/);
    expect(own, "physical, or the floor's own min-width wins by source order").toMatch(
      /min-width:\s*0/,
    );
    // The width query that broke squareness on a narrow MOUSE window is gone.
    const widthWriters = allRules(css)
      .filter((r) => /\.send-btn/.test(r.selector) && /min-width:\s*var\(--btn-h\)/.test(r.body))
      .map((r) => r.selector);
    expect(widthWriters).toEqual([]);
  });

  it("lets no rule override the resting band, least of all a width query", () => {
    // THE TIER IS THE POINTER, NOT THE WIDTH (01-tokens.css). The override this
    // replaced sat in `@media (width <= 48rem)`, so a wide coarse viewport kept
    // the fine-tier band. One derivation, no second writer, on any axis.
    const writers = allRules(css)
      .filter((r) => /--composer-rest-h:/.test(r.body))
      .map((r) => r.selector);
    expect(writers).toEqual(['[id="prompt-input"]']);
  });
});

describe("the composer, measured at real viewport sizes", () => {
  // The block sits last in the file and restores the size it found, like
  // composer-font-css.test.ts. `page.viewport` has no getter, so the entry size
  // is read off the frame rather than copied from vitest.config.ts.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The composer's two bands at one viewport size under one pointer tier.
   *
   *  `band` is the pill row MINUS its `border-block-start`: that hairline is the
   *  boundary between the two rows rather than part of either, so it is the row's
   *  band the textarea has to match. The resize is asserted, or a `page.viewport`
   *  that stopped moving the frame would leave every case below reporting about
   *  the project's own size while still naming a device. */
  async function bandsAt(
    width: number,
    height: number,
    pointer: "coarse" | "fine" | null,
  ): Promise<{ textarea: number; band: number; row: number; control: number }> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    if (pointer === null) {
      document.documentElement.removeAttribute("data-pointer");
    } else {
      document.documentElement.setAttribute("data-pointer", pointer);
    }
    document.body.innerHTML = composerMarkup();
    const input = document.getElementById("prompt-input");
    const pills = document.querySelector<HTMLElement>(".prompt-pills");
    const pill = document.getElementById("chat-options-btn");
    if (input === null || pills === null || pill === null) {
      throw new Error("the composer subtree did not mount");
    }
    const row = pills.getBoundingClientRect().height;
    const divider = Number.parseFloat(getComputedStyle(pills).borderTopWidth);
    return {
      textarea: input.getBoundingClientRect().height,
      band: row - divider,
      row,
      control: pill.getBoundingClientRect().height,
    };
  }

  it("matches the two rows on a touch DESKTOP, which is the case a width query cannot reach", async () => {
    // The reported defect: 40px of textarea over a 52px pill row, because the
    // controls take the coarse hit floor and the band was a fine-tier literal.
    //
    // THE THREE NUMBERS MOVED DELIBERATELY (amendment §B): 44 -> 32 for the control
    // and 52 -> 44 for both bands, because the painted box no longer carries the
    // target's measure. What this case is ABOUT is unchanged and is why it still
    // exists — the two rows agree, on the tier a width query cannot see.
    const { textarea, band, control } = await bandsAt(1440, 900, "coarse");
    expect(control, "the painted box is 32 at every tier now").toBe(32);
    expect(textarea).toBe(44);
    expect(band).toBe(44);
  });

  it("matches them on a mouse desktop, at the size that did not change", async () => {
    const { textarea, band, control } = await bandsAt(1440, 900, "fine");
    expect(control).toBe(32);
    expect(textarea).toBe(40);
    expect(band).toBe(40);
  });

  it("matches them on a phone", async () => {
    // 52 -> 44 with the amendment, for the reason above.
    const { textarea, band } = await bandsAt(390, 844, "coarse");
    expect(textarea).toBe(44);
    expect(band).toBe(44);
  });

  it("holds every control's TARGET at the hit floor, and lets none reach the textarea", async () => {
    // THE HALF THE PAINTED-BOX CHANGE IS ONLY SAFE WITH, and the reason it is a hit
    // test rather than a style read: amendment §B1 makes the 44px target
    // non-negotiable and §B2 forbids a target overhanging into its neighbour's box,
    // which is the D4 bug. Both are properties of what `elementFromPoint` answers,
    // and item 18's own history is what says a declaration read cannot stand in for
    // one — that defect survived precisely because the source assertion looked right.
    await bandsAt(390, 844, "coarse");
    const row = document.querySelector<HTMLElement>(".prompt-pills");
    const input = document.getElementById("prompt-input");
    if (row === null || input === null) {
      throw new Error("the composer subtree did not mount");
    }
    const root = getComputedStyle(document.documentElement);
    // The token is authored in `rem`, so it is resolved against the root size here
    // rather than restated as a number.
    const floor =
      Number.parseFloat(root.getPropertyValue("--hit-floor")) * Number.parseFloat(root.fontSize);
    expect(floor, "the coarse floor, in px").toBe(44);

    const controls = [...row.querySelectorAll<HTMLElement>(".pill:not(.hidden), .send-btn")];
    expect(controls.length, "the row renders its controls").toBeGreaterThan(3);

    const owns = (el: Element, x: number, y: number): boolean => {
      const hit = document.elementFromPoint(x, y);
      return hit === el || (hit !== null && el.contains(hit));
    };
    const inputBottom = input.getBoundingClientRect().bottom;
    const short: string[] = [];
    const theft: string[] = [];
    for (const el of controls) {
      const r = el.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      const cy = r.top + r.height / 2;
      let up = 0;
      let down = 0;
      for (let t = 0.25; t < 60; t += 0.25) {
        if (!owns(el, cx, cy - t)) {
          break;
        }
        up = t;
      }
      for (let t = 0.25; t < 60; t += 0.25) {
        if (!owns(el, cx, cy + t)) {
          break;
        }
        down = t;
      }
      const name = el.id === "" ? el.className : el.id;
      // The scan resolves the boundary to a quarter pixel, so the measured height is
      // the floor to within one step.
      if (up + down + 0.25 < floor - 0.5) {
        short.push(`${name} target ${String(up + down)}px tall`);
      }
      // The row's own padding is what pays for the expander's upward reach.
      if (cy - up < inputBottom) {
        theft.push(`${name} reaches ${String(inputBottom - (cy - up))}px into the textarea`);
      }
    }
    expect(short, "every control clears the coarse floor by hit test").toEqual([]);
    expect(theft, "no target overhangs into #prompt-input's box").toEqual([]);
  });

  it("keeps Send square at every tier and every width", async () => {
    // Amendment §A, measured rather than read: the two axes are one term, so this
    // fails the moment either is spelled separately again.
    for (const [w, h, tier] of [
      [320, 568, "coarse"],
      [390, 844, "coarse"],
      [430, 932, "coarse"],
      [844, 390, "coarse"],
      [1440, 900, "fine"],
      [390, 844, "fine"],
    ] as const) {
      await bandsAt(w, h, tier);
      const send = document.getElementById("send-btn");
      if (send === null) {
        throw new Error("the composer subtree did not mount");
      }
      const r = send.getBoundingClientRect();
      expect(r.width, `${String(w)}x${String(h)} ${tier}: Send is square`).toBe(r.height);
    }
  });

  it("matches them on a wide viewport with no pointer tier resolved yet", async () => {
    // The no-JS fallback arm is width-gated, so a wide window keeps mouse sizing
    // until `pointer-tier.ts` classifies it. Both rows have to follow it together.
    const { textarea, band } = await bandsAt(1440, 900, null);
    expect(textarea).toBe(40);
    expect(band).toBe(40);
  });

  it("centres the composer on its own divider, which is what the match buys", async () => {
    // The consequence worth measuring rather than reasoning about: with equal
    // bands the distance from the box's top edge to the text's centre equals the
    // distance from the controls' centre to its bottom edge. Counting the divider
    // into the textarea's band instead puts this half a pixel out.
    const boxOf = (): DOMRect => {
      const box = document.getElementById("prompt-box");
      if (box === null) {
        throw new Error("no prompt box");
      }
      return box.getBoundingClientRect();
    };
    for (const tier of ["fine", "coarse"] as const) {
      await bandsAt(1440, 900, tier);
      const box = boxOf();
      const input = document.getElementById("prompt-input");
      const pill = document.getElementById("chat-options-btn");
      if (input === null || pill === null) {
        throw new Error("the composer subtree did not mount");
      }
      const cs = getComputedStyle(input);
      const textCentre =
        input.getBoundingClientRect().y +
        Number.parseFloat(cs.paddingTop) +
        Number.parseFloat(cs.lineHeight) / 2;
      const pillRect = pill.getBoundingClientRect();
      expect(textCentre - box.y, `${tier}: text centre from the top edge`).toBeCloseTo(
        box.y + box.height - (pillRect.y + pillRect.height / 2),
        1,
      );
    }
  });
});
