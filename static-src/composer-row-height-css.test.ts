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

import { buildAttachmentPill } from "./attachment-pill.js";
import { ICON_SEND } from "./icons.js";
import { iconEl } from "./icon-el.js";
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

/** Stage two attachments in the mounted composer, through the REAL builder.
 *
 *  `static/index.html` ships `#attachment-row` empty and `hidden`, so the chip cases
 *  have to populate it — and a hand-written `<li>` would measure a chip this app does
 *  not render (the `×` is an opt-in the composer alone passes, and the glyph inside it
 *  is what that button's own box is sized around). */
function stageAttachments(): void {
  const row = document.getElementById("attachment-row");
  if (row === null) {
    throw new Error("the composer subtree did not mount");
  }
  row.classList.remove("hidden");
  for (const name of ["screenshot.png", "15-input.css"]) {
    row.appendChild(
      buildAttachmentPill(
        { path: `/workspace/${name}`, name },
        {
          onRemove: () => {
            /* the row only has to render */
          },
        },
      ),
    );
  }
}

/** The value of one declaration in `rule`, paren-balanced.
 *
 *  A regex cannot read these any more: `--composer-ctl-h` is a `max()` over a token,
 *  so `[^)]+` stops at the first inner `)` and a `[^;]+` form reads a whole nested
 *  expression correctly only by accident of ordering. */
function declaration(body: string, property: string): string | null {
  const at = body.indexOf(`${property}:`);
  if (at === -1) {
    return null;
  }
  let depth = 0;
  for (let i = at + property.length + 1; i < body.length; i += 1) {
    const ch = body[i];
    if (ch === "(") {
      depth += 1;
    } else if (ch === ")") {
      depth -= 1;
    } else if (ch === ";" && depth === 0) {
      return body.slice(at + property.length + 1, i).trim();
    }
  }
  return null;
}

describe("the height's declarations, read from source", () => {
  const css = loadCSS("15-input.css");

  it("declares one control height for the box, and it takes ONE rung on touch", () => {
    const form = ruleContaining(css, '[id="prompt-form"]', "top");
    // THIS PIN HAS MOVED TWICE, and the two moves are opposite halves of one rule.
    // It was `max(var(--ctl-h-dense), var(--hit-floor))`, which put the 44px TARGET
    // floor into the PAINTED box — 44px of button around a 20px glyph, the "grown
    // buttons look empty" report (amendment §B, 2026-09-10). That went to a flat
    // `2rem`, and 32-on-a-finger was reported as the controls not looking great
    // (2026-09-12), so the box now takes ONE rung up on the coarse tier through
    // `--ctl-h-sm` — 32 under a mouse, 36 under a finger — and still stops well
    // short of the floor.
    expect(form.body).toMatch(/--composer-ctl-h:\s*max\(2rem,\s*var\(--ctl-h-sm\)\)/);
    // The floor must NOT be read here, or the box takes the TARGET's measure again
    // and both reports come back. This is the half that survived both moves.
    expect(form.body).not.toMatch(/--composer-ctl-h:[^;]*--hit-floor/);
    // ONE declaration, so the tier cannot be re-decided by a second selector. That
    // is what reading a tier TOKEN buys over a `[data-pointer="coarse"]` override,
    // and it is also what keeps `data-touched` (which moves --hit-floor alone) from
    // growing the painted box on a mouse-driven touch laptop.
    const writers = allRules(css)
      .filter((r) => /--composer-ctl-h:/.test(r.body))
      .map((r) => r.selector);
    expect(writers).toEqual(['[id="prompt-form"]']);
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
      // Either spelling: `.pill` says `height`, `.send-btn` says `block-size`. It used
      // to read the term on BOTH axes because amendment §A required a structural
      // square; that was overturned on 2026-09-12 and only the BLOCK axis is shared
      // now, the inline one coming from the padding its icon-only sibling uses.
      expect(own, `${selector} reads the shared height`).toMatch(
        /(?:block-size|height):\s*var\(--composer-ctl-h\)/,
      );
      // A literal here is what let the row disagree with the textarea above it.
      expect(own, `${selector} declares no literal height`).not.toMatch(/(^|[^-])height:\s*\d/);
    }
  });

  it("takes Send's width from ONE mechanism, so it cannot drift from the height", () => {
    // What amendment §A was actually defending, kept after its square was overturned
    // (2026-09-12). The original defect was 42x32 on a fine pointer and 44x44 on a
    // coarse one: the inline axis came from `padding: 0 var(--sp-3)` plus the glyph,
    // the block axis from `height`, and the coarse square was the hit floor's
    // `min-width` landing on the same number by coincidence rather than by a rule.
    // §A closed that by making both axes one term. The width is a mechanism again —
    // but the SAME padding `.pill` takes, declared in one place per tier and shared
    // with it by selector, so there is still no second literal to drift.
    const rule = ruleContaining(css, ".send-btn", "top");
    const brace = rule.body.indexOf("{");
    const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
    expect(own).toMatch(/block-size:\s*var\(--composer-ctl-h\)/);
    // No literal width, and no `aspect-ratio` re-squaring it.
    expect(own, "no literal inline size").not.toMatch(/inline-size:\s*\d/);
    expect(own, "the square is overturned, not re-stated").not.toMatch(/aspect-ratio/);
    // Both icon-only controls derive their inline padding from ONE token, which is what
    // makes "Send is the same box as its sibling" structural rather than two numbers
    // that happen to agree. Send adds half its glyph deficit on top, so the expression
    // differs while the source of truth does not.
    expect(own, "Send derives its padding from the row's token").toMatch(
      /padding-inline:\s*max\(\s*var\(--pill-pad-inline\)/,
    );
    const pill = ruleContaining(css, ".pill", "top");
    expect(pill.body, ".pill spends the same token").toMatch(
      /padding:\s*0\s+var\(--pill-pad-inline\)/,
    );
    // And no rule may re-pad either of them with a literal, which is the drift the
    // token exists to prevent — a phone override on `.pill` alone is exactly how Send
    // ended up 32 wide against a 46px sibling.
    const rePadders = allRules(css)
      .filter((r) =>
        r.selector
          .split(",")
          .map((sel) => sel.trim())
          .some((sel) => sel === ".pill" || sel === ".send-btn"),
      )
      .filter((r) => /padding(-inline)?:[^;]*var\(--sp-/.test(r.body))
      .map((r) => r.selector);
    expect(rePadders, "no rule re-pads either control off a spacing token").toEqual([]);
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
    expect(control, "one rung up on a coarse pointer, still short of the 44px floor").toBe(36);
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

  /** The send button as the RUNNING app has it. `static/index.html` ships it empty
   *  — `prompt-input.ts` does `sendBtn.replaceChildren(iconEl(STATE_ICON.idle))` at
   *  boot — so a fixture sliced from that page measures a 26px button with no ink in
   *  it. Driving the production glyph rather than a hand-written one means an
   *  upstream path change moves these two cases with it. */
  function withSendGlyph(): HTMLElement {
    const send = document.getElementById("send-btn");
    if (send === null) {
      throw new Error("the composer subtree did not mount");
    }
    send.replaceChildren(iconEl(ICON_SEND));
    return send;
  }

  it("keeps Send wider than tall on every tier and width", async () => {
    // OVERTURNS amendment §A's square (user ruling, 2026-09-12). The square made Send
    // the only control in the row whose width was its height, which read as a
    // different size class — measured in Chromium AND WebKit as the NARROWEST control
    // in the row while reading as the biggest. The surplus width is free touch area,
    // which is why the direction is asserted rather than a number: an edit that
    // squares it again fails here instead of passing a height-only check.
    //
    // Box-IDENTICAL to `#chat-options-btn` was tried and reverted — it cost 6px of a
    // row with 4px of slack — so only the HEIGHT is compared against the sibling. The
    // ink case below is the half the report was actually about.
    //
    // What §A was ACTUALLY defending survives and is asserted in the source read: the
    // width comes from one mechanism rather than a literal that can drift.
    for (const [w, h, tier] of [
      [320, 568, "coarse"],
      [390, 844, "coarse"],
      [430, 932, "coarse"],
      [844, 390, "coarse"],
      [1440, 900, "fine"],
      [390, 844, "fine"],
    ] as const) {
      await bandsAt(w, h, tier);
      const send = withSendGlyph();
      const sibling = document.getElementById("chat-options-btn");
      if (sibling === null) {
        throw new Error("the composer subtree did not mount");
      }
      const s = send.getBoundingClientRect();
      const o = sibling.getBoundingClientRect();
      const at = `${String(w)}x${String(h)} ${tier}`;
      expect(s.height, `${at}: Send's height is the row's`).toBe(o.height);
      expect(s.width, `${at}: wider than tall, which is the touch area`).toBeGreaterThan(s.height);
    }
  });

  it("paints the same INK in Send as in the control beside it", async () => {
    // The other half of the reported defect, and the one a box measurement misses.
    // `--icon-ui` sizes a glyph's BOX; the eye compares the INK inside it, and Lucide
    // draws this arrow edge to edge — PATH_SEND spans 20 of 24 viewBox units against
    // PATH_PLUS's 14 — so at an identical box it painted 43% more ink. `.send-btn`
    // scales its glyph by 14/20 to normalise that, which is why this compares rendered
    // ink rather than the `width` either rule declares.
    await bandsAt(390, 844, "coarse");
    withSendGlyph();
    const ink = (id: string): number => {
      const svg = document.querySelector(`#${id} svg`);
      if (!(svg instanceof SVGGraphicsElement)) {
        throw new Error(`${id} has no glyph`);
      }
      const box = svg.getBoundingClientRect();
      const bb = svg.getBBox();
      // getBBox is in viewBox units; every glyph here declares a 24-unit viewBox.
      return Math.max(bb.width, bb.height) * (box.width / 24);
    };
    const send = ink("send-btn");
    const sibling = ink("chat-options-btn");
    expect(send, `Send paints ${send.toFixed(1)}px against ${sibling.toFixed(1)}px`).toBeCloseTo(
      sibling,
      0,
    );
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

// ---------------------------------------------------------------------------
// The staged-attachment chip is the box's THIRD row, and it was the one control
// left out of amendment §B.
//
// Its height came from its CHILDREN, and both children are `<button>`s, so the
// zero-specificity hit floor put the 44px TARGET in the painted box: measured at
// 390x844 coarse, a 50.78px chip around a 15px label, against the pill row's own
// controls beside it and 30.78px for the same chip on a mouse. That is the same
// "grown buttons look empty" shape §B closed for `.pill` and `.send-btn`, reached
// through the one selector that does not declare a height.
//
// Both halves again, for the reasons the two blocks above give: the source read says
// the height is the box's derived term and that the floor is opted out of PHYSICALLY,
// and the measurement says the chip agrees with the row under it while every one of
// its targets still clears the floor without reaching into a neighbour's.
// ---------------------------------------------------------------------------

describe("the attachment chip's declarations, read from source", () => {
  const css = loadCSS("15-input.css");

  it("gives the chip the box's control height, with the second home's fallback", () => {
    const rule = ruleContaining(css, ".attachment-pill", "top");
    const brace = rule.body.indexOf("{");
    const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
    expect(declaration(own, "block-size")).toMatch(/^var\(--composer-ctl-h,/);
    // The block padding that used to add 4.8px on top of the children.
    expect(declaration(own, "padding-block")).toBe("0");
    expect(own, "no literal height beside the derived one").not.toMatch(/(^|[^-])height:\s*\d/);
  });

  it("keeps that fallback the same EXPRESSION the box declares, not the same number", () => {
    // One value, two homes, and nothing else holds them together: this component's
    // other home is a sent turn's header (29-turns.css), outside `#prompt-form`, where
    // the property does not resolve — and a `var()` with nothing behind it invalidates
    // the declaration, which would put the chip back on its children's height.
    //
    // EXPRESSION rather than value, because `--composer-ctl-h` is tier-dependent again
    // (`max(2rem, var(--ctl-h-sm))`): a `2rem` fallback would agree with the pill row
    // inside the composer and disagree with it in a turn header, on the coarse tier
    // only, which is exactly the shape this whole file exists to catch.
    const form = ruleContaining(css, '[id="prompt-form"]', "top");
    const declared = declaration(form.body, "--composer-ctl-h");
    const chip = ruleContaining(css, ".attachment-pill", "top");
    const size = declaration(chip.body, "block-size") ?? "";
    const fallback = /^var\(--composer-ctl-h,\s*(.*)\)$/s.exec(size)?.[1]?.trim();
    expect(declared, "the box declares a control height").not.toBeNull();
    expect(fallback).toBe(declared);
  });

  it("pays for the chips' target reach out of the row's own padding", () => {
    // The term `.prompt-pills` reads, for the same reason: without it a chip's expander
    // overhangs into `#prompt-input`'s box above and into the pill row's own controls'
    // targets below. `--pill-inset` (4px) is short of the reach on a coarse pointer,
    // and it also left the chips inside the controls' inline edge.
    const rule = ruleContaining(css, ".attachment-row", "top");
    expect(declaration(rule.body, "padding")).toBe("var(--composer-pill-pad)");
    const writers = allRules(css)
      .filter((r) => /\.attachment-row/.test(r.selector) && /padding:/.test(r.body))
      .map((r) => r.selector);
    expect(writers, "one padding writer, on no width axis").toEqual([".attachment-row"]);
  });

  it("opts both chip buttons out of the floor PHYSICALLY and grows their target instead", () => {
    for (const selector of [".attachment-open", ".attachment-close"]) {
      const rule = ruleContaining(css, selector, "top");
      const brace = rule.body.indexOf("{");
      const own = brace === -1 ? rule.body : rule.body.slice(0, brace);
      // Physical, or the floor's own `min-height` wins by source order — 61-mcp-tools
      // sorts later, and a logical property is a DIFFERENT property to it.
      expect(declaration(own, "min-height"), `${selector} drops the floor's min-height`).toBe("0");
      expect(declaration(own, "min-width"), `${selector} drops the floor's min-width`).toBe("0");
      expect(declaration(own, "position"), `${selector} is the expander's containing block`).toBe(
        "relative",
      );
    }
    const expander = ruleContaining(css, ".attachment-open::after", "top");
    expect(expander.selector).toContain(".attachment-close::after");
    expect(declaration(expander.body, "inset-block")).toBe(
      "min(0px, calc((100% - var(--hit-floor)) / 2))",
    );
    // BLOCK ONLY. An inline expander on the `×` would put a DESTRUCTIVE target over
    // the last characters of the filename, so a tap meant to open the file would
    // remove it.
    expect(
      declaration(expander.body, "inset-inline"),
      "no inline reach into the sibling control",
    ).toBe("0");
  });
});

describe("the attachment chip, measured at real viewport sizes", () => {
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

  async function chipsAt(
    width: number,
    height: number,
    pointer: "coarse" | "fine",
  ): Promise<{ chip: number; control: number; floor: number }> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    document.documentElement.setAttribute("data-pointer", pointer);
    document.body.innerHTML = composerMarkup();
    stageAttachments();
    const chip = document.querySelector<HTMLElement>("#attachment-row .attachment-pill");
    const control = document.getElementById("chat-options-btn");
    if (chip === null || control === null) {
      throw new Error("the composer subtree did not mount");
    }
    const root = getComputedStyle(document.documentElement);
    return {
      chip: chip.getBoundingClientRect().height,
      control: control.getBoundingClientRect().height,
      floor:
        Number.parseFloat(root.getPropertyValue("--hit-floor")) * Number.parseFloat(root.fontSize),
    };
  }

  it("matches the chip to the pill row's controls on a phone, which is the reported defect", async () => {
    const { chip, control, floor } = await chipsAt(390, 844, "coarse");
    expect(floor, "the coarse floor, in px").toBe(44);
    expect(chip, "a 50.78px chip before the fix").toBe(36);
    expect(chip).toBe(control);
  });

  it("matches it on a touch DESKTOP, the tier a width query cannot see", async () => {
    const { chip, control } = await chipsAt(1440, 900, "coarse");
    expect(chip).toBe(36);
    expect(chip).toBe(control);
  });

  it("matches it on a mouse, where the chip was 30.78 against a 32px row", async () => {
    const { chip, control } = await chipsAt(1440, 900, "fine");
    expect(chip).toBe(32);
    expect(chip).toBe(control);
  });

  it("holds every chip button's TARGET at the floor, and lets none reach a neighbour", async () => {
    // The half the painted-box change is only safe with, and a hit test rather than a
    // style read for §B's own reason: the target and the overhang are both properties
    // of what `elementFromPoint` answers.
    const { floor } = await chipsAt(390, 844, "coarse");
    const input = document.getElementById("prompt-input");
    const control = document.getElementById("chat-options-btn");
    if (input === null || control === null) {
      throw new Error("the composer subtree did not mount");
    }
    const owns = (el: Element, x: number, y: number): boolean => {
      const hit = document.elementFromPoint(x, y);
      return hit === el || (hit !== null && el.contains(hit));
    };
    /** How far `el` owns the vertical line through its centre, in `dir`. */
    const reach = (el: Element, dir: -1 | 1): number => {
      const r = el.getBoundingClientRect();
      const cx = r.left + r.width / 2;
      const cy = r.top + r.height / 2;
      let last = 0;
      for (let t = 0.25; t < 80; t += 0.25) {
        if (!owns(el, cx, cy + dir * t)) {
          break;
        }
        last = t;
      }
      return last;
    };

    const buttons = [
      ...document.querySelectorAll<HTMLElement>(
        "#attachment-row .attachment-open, #attachment-row .attachment-close",
      ),
    ];
    expect(buttons.length, "two chips, each with a label and a remove").toBe(4);

    const inputBottom = input.getBoundingClientRect().bottom;
    // The pill row's own controls grow upward by the same rule, so the boundary a chip
    // may not cross is that target's top edge rather than the control's box.
    const controlRect = control.getBoundingClientRect();
    const controlTargetTop = controlRect.top + controlRect.height / 2 - reach(control, -1);
    const short: string[] = [];
    const theft: string[] = [];
    for (const el of buttons) {
      const r = el.getBoundingClientRect();
      const cy = r.top + r.height / 2;
      const up = reach(el, -1);
      const down = reach(el, 1);
      const name = el.className;
      if (up + down + 0.25 < floor - 0.5) {
        short.push(`${name} target ${String(up + down)}px tall`);
      }
      if (cy - up < inputBottom - 1) {
        theft.push(`${name} reaches ${String(inputBottom - (cy - up))}px into the textarea`);
      }
      if (cy + down > controlTargetTop + 1) {
        theft.push(`${name} reaches into the pill row's own target`);
      }
    }
    expect(short, "every chip button clears the coarse floor by hit test").toEqual([]);
    expect(theft, "no chip target overhangs a neighbour").toEqual([]);
  });

  it("paints nothing that could reveal the remove button's asymmetric target", async () => {
    // THE CONDITION THE ASYMMETRY WAS ACCEPTED ON (user ruling, 2026-09-12): the `×`
    // target is the full floor tall and 24px wide, and that is fine only while nothing
    // on screen draws the difference. Two things have to hold, and neither is
    // automatic — a fill on either box would make the target's shape visible.
    //
    // The background half is a CSSOM WALK rather than a computed-style read, because
    // computed style answers for ONE state and the question is about every state a rule
    // can put this button in. `CSS.forcePseudoState` is a devtools call a test page
    // cannot make, and a synthetic hover drives no style recalc (chromium-sidecar.md
    // "Synthetic hover").
    await chipsAt(390, 844, "coarse");
    const close = document.querySelector<HTMLElement>("#attachment-row .attachment-close");
    const label = document.querySelector<HTMLElement>("#attachment-row .attachment-open");
    if (close === null || label === null) {
      throw new Error("the chip did not mount");
    }

    /** Every rule in the bundle that writes `background*` and whose selector this
     *  element matches once its pseudo-CLASSES are stripped, in document order.
     *
     *  IT DESCENDS INTO NESTED RULES, and that is what makes the sweep exhaustive
     *  rather than merely plausible. Measured in Chromium 152: a `CSSStyleRule` is NOT
     *  a `CSSGroupingRule` (`instanceof` is false) but it DOES carry `cssRules`, so a
     *  walk that only recurses through grouping rules sees a top-level selector and
     *  none of its `&:hover` children. `.icon-btn` — which this button wears — holds
     *  four of them, the fill among them, so without the descent the resting assertion
     *  below would be asserting over one rule instead of five. */
    const fillWriters = (el: Element, pseudo: string): { selector: string; value: string }[] => {
      const out: { selector: string; value: string }[] = [];
      /** `&` resolved against the enclosing selector list, which is all these
       *  stylesheets' nesting is (a state or a variant on the parent). */
      const resolve = (selector: string, parents: readonly string[]): string[] =>
        parents.length === 0
          ? [selector]
          : parents.map((p) => selector.replaceAll("&", `:is(${p})`));

      const walk = (list: CSSRuleList, parents: readonly string[]): void => {
        for (const rule of list) {
          if (rule instanceof CSSGroupingRule && !(rule instanceof CSSStyleRule)) {
            walk(rule.cssRules, parents);
            continue;
          }
          if (!(rule instanceof CSSStyleRule)) {
            continue;
          }
          const selectors = rule.selectorText
            .split(",")
            .flatMap((part) => resolve(part.trim(), parents));
          const value =
            rule.style.getPropertyValue("background") ||
            rule.style.getPropertyValue("background-color");
          if (value !== "") {
            for (const raw of selectors) {
              // Keep only the selectors aimed at the pseudo-element under test, then
              // strip the state so a `:hover` rule is counted for the element it would
              // paint.
              const wantsPseudo = raw.includes("::after") || raw.includes("::before");
              if (pseudo === "" ? wantsPseudo : !raw.includes(pseudo)) {
                continue;
              }
              const bare = raw
                .replaceAll("::after", "")
                .replaceAll("::before", "")
                .replace(/:(?:hover|active|focus|focus-visible|focus-within)\b/g, "")
                .trim();
              if (bare === "") {
                continue;
              }
              let matches: boolean;
              try {
                matches = el.matches(bare);
              } catch {
                // A selector this browser cannot parse cannot be one it applies.
                matches = false;
              }
              if (matches) {
                out.push({ selector: raw, value });
                break;
              }
            }
          }
          walk(rule.cssRules, selectors);
        }
      };
      for (const sheet of document.styleSheets) {
        try {
          walk(sheet.cssRules, []);
        } catch {
          continue;
        }
      }
      return out;
    };

    // 1. THE PAINTED BOX. Every rule that fills it carries a STATE, so at rest there is
    //    no shape on screen at all — and each of those paints the button's OWN box,
    //    which is square. A fill on a stateless rule, or a non-square box, is what
    //    would make the asymmetric target visible.
    //
    //    `.icon-btn`'s `&:hover` fill is the one live writer, and it is UNGATED by
    //    `any-hover` — an app-wide property of that shared class rather than this
    //    button's, so a tap latches it on a touch device. It still cannot reveal the
    //    asymmetry, because what it paints is the square box asserted below.
    const onElement = fillWriters(close, "");
    expect(onElement.length, "the sweep reaches the nested state rules").toBeGreaterThan(1);
    const resting = onElement.filter(
      (w) => !/:(?:hover|active|focus)/.test(w.selector) && !/^none$/.test(w.value.trim()),
    );
    expect(
      resting.map((w) => `${w.selector} { background: ${w.value} }`),
      "no stateless rule fills the remove button",
    ).toEqual([]);
    const r = close.getBoundingClientRect();
    expect(r.width, "the painted box is square, so no state fill can be asymmetric").toBe(r.height);
    expect(getComputedStyle(close).backgroundColor, "transparent at rest").toBe("rgba(0, 0, 0, 0)");

    // 2. THE TARGET. The expander is the only box carrying the asymmetry, and it is
    //    unpainted in every state: no rule gives it a background, and it declares no
    //    border of its own. The label's expander takes the same guarantee.
    for (const el of [close, label]) {
      expect(fillWriters(el, "::after"), "nothing fills the expander, in any state").toEqual([]);
      const after = getComputedStyle(el, "::after");
      expect(after.backgroundColor).toBe("rgba(0, 0, 0, 0)");
      expect(after.borderTopWidth).toBe("0px");
      expect(after.borderLeftWidth).toBe("0px");
    }
  });

  it("never lets the remove button's target cover the filename", async () => {
    // The one asymmetry in the expander: `×` is destructive, so its target may not
    // reach the label's. They touch across the chip's own 4px gap, which belongs to
    // neither.
    await chipsAt(390, 844, "coarse");
    const label = document.querySelector<HTMLElement>("#attachment-row .attachment-open");
    const close = document.querySelector<HTMLElement>("#attachment-row .attachment-close");
    if (label === null || close === null) {
      throw new Error("the chip did not mount");
    }
    const lr = label.getBoundingClientRect();
    const cr = close.getBoundingClientRect();
    // The label's own trailing edge, and its whole trailing half at the chip's vertical
    // centre: the remove button must own none of it.
    for (const x of [lr.right - 1, lr.right - 4, lr.left + lr.width * 0.75]) {
      const hit = document.elementFromPoint(x, lr.top + lr.height / 2);
      expect(
        hit !== null && close.contains(hit),
        `the remove button owns the label at x=${String(Math.round(x - lr.left))}`,
      ).toBe(false);
    }
    // And the remove button's own target still clears WCAG 2.5.8's 24px on the axis it
    // may not grow, which is why it declares a 24px box.
    expect(cr.width).toBe(24);
  });
});
