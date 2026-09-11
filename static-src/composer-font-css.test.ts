// ---------------------------------------------------------------------------
// THE COMPOSER READS THE TRANSCRIPT'S OWN RUNG, at every pointer tier.
//
// It used to read `--fs-field`, a token raised to 1rem on the coarse tier as an
// iOS floor: iOS Safari zooms the page when a text control under 16px takes
// focus, and `static/index.html` carries `autofocus` on `#prompt-input`. That
// premise was false for this document — the page ships `maximum-scale=1.0` in its
// viewport meta, which is what suppresses the auto-zoom — so the floor bought
// nothing and cost 16px of composer text against 11-12px pill labels one row
// below it, which is how it was reported. The token, the app-wide floor it fed
// (61-mcp-tools.css) and three hardcoded 16px find-input overrides all went with
// it; `text-field-scale-css.test.ts` is the sweep over every OTHER control.
//
// Two halves, because neither answers the other's question. The SOURCE read says
// the control reads the scale rather than a literal, and that no `--fs-field`
// token has come back to carry a floor silently. The MEASUREMENT says the
// composer and the transcript compute the SAME size — the contract in the
// direction a reader sees it — and that the font change moves the resting box by
// nothing, because the padding is derived from `--composer-rest-h` minus `1lh`
// and absorbs the line-height difference.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control, for the arms that can only be measured by resizing.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub
// that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { allRules, loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

/** The rung the composer and the transcript share. */
const RUNG = "--fs-md";

/** The composer subtree as the page ships it — the form, its box, the textarea
 *  and the real pill row. Sliced rather than hand-written: the pill row's height
 *  is the sum of its own controls, so a fixture with two invented pills would
 *  measure a row this app does not render. `#prompt-form` is the first `<form>`
 *  in the document and its `</form>` is the first close. */
function composerMarkup(): string {
  const open = indexHtml.indexOf('<form id="prompt-form"');
  expect(open, 'static/index.html has no <form id="prompt-form">').toBeGreaterThan(-1);
  const close = indexHtml.indexOf("</form>", open);
  expect(close, "the prompt form is not closed").toBeGreaterThan(open);
  return indexHtml.slice(open, close + "</form>".length);
}

describe("the declarations, read from source", () => {
  const inputRule = allRules(loadCSS("15-input.css")).find(
    (r) => r.selector === '[id="prompt-input"]',
  );

  it("reads the type scale's own rung, never a literal", () => {
    expect(inputRule, '15-input.css has no [id="prompt-input"] rule').toBeDefined();
    expect(inputRule?.body).toMatch(new RegExp(`font-size:\\s*var\\(${RUNG}\\)`));
    // A literal would put a platform fact in a component file, which is what the
    // three find inputs carried until this change; --fs-lg would pin the composer
    // to a type-scale rung that has nothing to do with the transcript's.
    expect(inputRule?.body).not.toMatch(/font-size:\s*1rem/);
    expect(inputRule?.body).not.toMatch(/font-size:\s*\d+px/);
    expect(inputRule?.body).not.toMatch(/font-size:\s*var\(--fs-lg\)/);
  });

  it("declares no --fs-field token, so a tier floor cannot come back silently", () => {
    // The token existed only to carry a per-tier platform floor, and its own
    // comment said so. A re-added token would deliver 16px to this control through
    // a file this test does not read, so the absence is asserted where the token
    // lived rather than at the consumer.
    expect(loadCSS("01-tokens.css")).not.toContain("--fs-field");
  });
});

describe("the composer, measured at real viewport sizes", () => {
  // The block sits LAST in the file and restores the size it found in `afterAll`.
  // `page.viewport` has no getter, so the entry size is READ off the frame rather
  // than copied from `vitest.config.ts`, which would silently leave every later
  // file measuring at the old size if that config moved.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
    document.documentElement.style.removeProperty("font-size");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The composer mounted at one viewport size under one pointer tier, with a
   *  transcript bubble beside it so the two can be compared as rendered.
   *
   *  `pointer` is the value of the tier attribute the app's own `pointer-tier.ts`
   *  writes, or null for the no-JS case the width fallback exists to serve. The
   *  resize is asserted, or a `page.viewport` that stopped moving the frame would
   *  make every case below report about the project's own size while still naming
   *  a phone. */
  async function mountAt(
    width: number,
    height: number,
    pointer: "coarse" | "fine" | null,
  ): Promise<{ input: HTMLTextAreaElement; pills: HTMLElement; prose: HTMLElement }> {
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
    document.body.innerHTML =
      `<div class="message assistant"><p id="probe-prose">prose</p></div>` + composerMarkup();
    const input = document.getElementById("prompt-input");
    const pills = document.querySelector<HTMLElement>(".prompt-pills");
    const prose = document.getElementById("probe-prose");
    if (!(input instanceof HTMLTextAreaElement) || pills === null || prose === null) {
      throw new Error("the composer subtree did not mount");
    }
    return { input, pills, prose };
  }

  /** The control's computed font size in px. */
  function fontPx(el: Element): number {
    return Number.parseFloat(getComputedStyle(el).fontSize);
  }

  it.each([
    ["a phone with a coarse pointer", 390, 844, "coarse" as const],
    // An iPad in landscape: a finger past 48rem, where the no-JS width fallback
    // does not reach. THE TIER IS THE POINTER, NOT THE WIDTH, and this is the case
    // that says so for this control.
    ["a WIDE coarse viewport", 1024, 768, "coarse" as const],
    // `pointer-tier.ts` seeds the tier from a capability query and corrects it on
    // the first real PointerEvent, so there is a window with no attribute at all.
    ["a phone with no pointer tier resolved yet", 390, 844, null],
    ["a desktop with a fine pointer", 1280, 800, "fine" as const],
    ["a desktop with no pointer tier resolved yet", 1280, 800, null],
  ])("computes the transcript's own size on %s", async (_label, w, h, pointer) => {
    const { input, prose } = await mountAt(w, h, pointer);
    // Compared against the RENDERED transcript rather than against 14, so a retune
    // of --fs-md moves both and this case keeps meaning "the two agree". The
    // absolute value is pinned once, below.
    //
    // THE COARSE ARMS ARE EXCLUDED FROM THE AGREEMENT, deliberately (amendment §D,
    // 2026-09-10): the 16px iOS text-entry floor is restored in 61-mcp-tools.css
    // and beats this control's (0,1,0) declaration on that tier, so the composer
    // reads 16px there against the transcript's 14. This file's own header states
    // the premise that changed — `static/index.html` no longer ships
    // `maximum-scale=1.0`, so the auto-zoom it suppressed is live and
    // `#prompt-input` carries `autofocus`. The next case pins that side.
    if (pointer === "coarse" || (pointer === null && w <= 768)) {
      expect(fontPx(input), "the coarse floor is in force").toBe(16);
      return;
    }
    expect(fontPx(input)).toBe(fontPx(prose));
  });

  it("is 14px on a mouse today, so a retune of the rung is a visible change rather than a silent one", async () => {
    // Moved from 390 coarse to a mouse desktop with the floor's restoration: on a
    // coarse pointer this control is 16 by platform requirement rather than by this
    // app's type scale, so the rung is only observable on the fine tier.
    const { input } = await mountAt(1280, 800, "fine");
    expect(fontPx(input)).toBe(14);
  });

  it("is 16px on a finger, which is iOS's auto-zoom threshold rather than a rung", async () => {
    const { input } = await mountAt(390, 844, "coarse");
    expect(fontPx(input)).toBe(16);
  });

  // The coarse pair moved 52/53 -> 44/45 with amendment §B, which took the painted
  // control box off the 44px target floor; composer-row-height-css.test.ts owns that
  // relationship and its own pins carry the reasoning. What this case is about is
  // unchanged: the FONT does not move the box.
  it.each([
    [390, 844, "coarse" as const, 44, 45],
    [1280, 800, "fine" as const, 40, 41],
  ])(
    "leaves the resting box and the pill row alone at %ix%i on a %s pointer",
    async (w, h, pointer, boxH, rowH) => {
      // The consequence worth measuring rather than reasoning about. The padding is
      // `calc((var(--composer-rest-h) - 1lh) / 2)`, so a line-height change eats
      // exactly the padding it adds and the resting height is unchanged BY
      // CONSTRUCTION — which is what made the font change safe to ship on its own.
      // A two-line composer does grow by the line-height delta, which is the honest
      // consequence of the text size.
      const { input, pills } = await mountAt(w, h, pointer);
      expect(input.getBoundingClientRect().height).toBe(boxH);
      // The pill row's BOX is one hairline taller than its band: the
      // `border-block-start` is the boundary between the two rows rather than part
      // of either. composer-row-height-css.test.ts owns that relationship.
      expect(pills.getBoundingClientRect().height, "the two rows are one band").toBe(rowH);

      // The same box at the size the control read BEFORE this change, set ON the
      // element. Not through the root font size and not through --fs-md: both
      // --composer-rest-h and the box controls beside it are `rem`-derived, so
      // moving the root moves the very height being compared, and --fs-md is now
      // shared with the transcript. An inline size on the control alone is the
      // pre-change condition exactly, and it leaves --composer-rest-h fixed while
      // `1lh` — the term the padding subtracts — follows the font.
      input.style.setProperty("font-size", "16px");
      expect(fontPx(input), "the pre-change size is what is being compared").toBe(16);
      expect(input.getBoundingClientRect().height, "raising the font moved the box").toBe(boxH);
    },
  );
});
