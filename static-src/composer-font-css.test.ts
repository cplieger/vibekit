// ---------------------------------------------------------------------------
// The composer's font size on a COARSE pointer.
//
// iOS zooms the page when a text control under 16px takes focus, and
// `static/index.html` carries `autofocus` on `#prompt-input`, so a fresh load on
// a finger zoomed the PWA before anything was typed. The fix is a token on the
// pointer tier (`--fs-field`, 01-tokens.css) that the control reads once, which
// is how every other platform floor in this app is delivered.
//
// Two halves, following effort-pill-css.test.ts, because neither answers the
// other's question. The SOURCE read says the token is declared in the base block
// and in BOTH tier arms carrying the same value, so one arm cannot be quietly
// weakened. The MEASUREMENT says each arm applies at the size it claims and —
// the part the whole change turns on — that raising the font moved the composer's
// resting box by nothing, because the padding is derived from
// `--composer-rest-h` minus `1lh` and absorbs the line-height change.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control, for the arms that can only be measured by resizing.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub
// that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { allRules, loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

/** The token the composer reads. */
const TOKEN = "--fs-field";

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

describe("the token's declarations, read from source", () => {
  // A computed style answers for ONE viewport, so it cannot see an arm it is not
  // sized for. This half is what fails when an arm is dropped or its value drifts
  // away from its twin.
  const declaring = allRules(loadCSS("01-tokens.css")).filter((r) =>
    new RegExp(`${TOKEN}:`).test(r.body),
  );

  it("declares the token in the base block and in both tier arms, and nowhere else", () => {
    expect(declaring.map((r) => r.selector)).toEqual([
      ":root",
      ':root[data-pointer="coarse"]',
      ':root:not([data-pointer="fine"])',
    ]);
  });

  it("defaults to the type scale's own rung and raises it to 1rem on both arms", () => {
    // NOT --fs-lg, which is 1rem today: that is a type-scale rung, and a retune
    // of it would silently move a platform threshold.
    const [base, coarse, noJS] = declaring;
    expect(base?.body).toMatch(/--fs-field:\s*var\(--fs-md\)/);
    expect(coarse?.body).toMatch(/--fs-field:\s*1rem/);
    expect(noJS?.body, "both arms raise it the same way").toMatch(/--fs-field:\s*1rem/);
  });

  it("sits beside the hit floor, so one tier decides both", () => {
    // The co-location IS the contract: --fs-field follows the pointer tier that
    // --ctl-h and --hit-floor already move on, rather than growing a breakpoint
    // of its own. A second breakpoint is what once gave the 640-768px band 44px
    // controls beside a 24px hit floor.
    const [, coarse, noJS] = declaring;
    expect(coarse?.body).toMatch(/--hit-floor:\s*2\.75rem/);
    expect(noJS?.body).toMatch(/--hit-floor:\s*2\.75rem/);
  });

  it("is read by the composer as a token, never as a literal", () => {
    const input = allRules(loadCSS("15-input.css")).find(
      (r) => r.selector === '[id="prompt-input"]',
    );
    expect(input, '15-input.css has no [id="prompt-input"] rule').toBeDefined();
    expect(input?.body).toMatch(/font-size:\s*var\(--fs-field\)/);
    // A literal in the component file would put a platform fact in the wrong
    // file, and --fs-lg would pin the threshold to a type-scale rung.
    expect(input?.body).not.toMatch(/font-size:\s*1rem/);
    expect(input?.body).not.toMatch(/font-size:\s*var\(--fs-lg\)/);
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
    document.documentElement.style.removeProperty(TOKEN);
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The composer mounted at one viewport size under one pointer tier.
   *
   *  `pointer` is the value of the tier attribute the app's own `pointer-tier.ts`
   *  writes, or null for the no-JS case the fallback arm exists to serve. The
   *  resize is asserted, or a `page.viewport` that stopped moving the frame would
   *  make every case below report about the project's own size while still naming
   *  a phone. */
  async function mountAt(
    width: number,
    height: number,
    pointer: "coarse" | "fine" | null,
  ): Promise<{ input: HTMLTextAreaElement; pills: HTMLElement }> {
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
    if (!(input instanceof HTMLTextAreaElement) || pills === null) {
      throw new Error("the composer subtree did not mount");
    }
    return { input, pills };
  }

  /** The control's computed font size in px. */
  function fontPx(el: Element): number {
    return Number.parseFloat(getComputedStyle(el).fontSize);
  }

  it("computes 16px on a phone with a coarse pointer, clearing the iOS threshold", async () => {
    const { input } = await mountAt(390, 844, "coarse");
    expect(fontPx(input)).toBe(16);
  });

  it("computes 16px on a WIDE coarse viewport, which is the tier arm's own population", async () => {
    // An iPad in landscape: a finger past 48rem, where the no-JS width fallback
    // does not reach and only the pointer-tier arm can carry the token. THE TIER
    // IS THE POINTER, NOT THE WIDTH, and this is the case that says so — at 390px
    // the fallback arm alone would answer 16px.
    const { input } = await mountAt(1024, 768, "coarse");
    expect(fontPx(input)).toBe(16);
  });

  it("computes 16px on a phone with no pointer tier resolved yet", async () => {
    // The no-JS fallback arm. `pointer-tier.ts` seeds the attribute from a
    // capability query and corrects it on the first real PointerEvent, so there
    // is a window with no attribute at all — and the zoom fires on the autofocus
    // inside it.
    const { input } = await mountAt(390, 844, null);
    expect(fontPx(input)).toBe(16);
  });

  it("keeps 14px on a desktop with a fine pointer", async () => {
    const { input } = await mountAt(1280, 800, "fine");
    expect(fontPx(input)).toBe(14);
  });

  it("keeps 14px on a desktop with no pointer tier resolved yet", async () => {
    // The other side of the fallback arm: it is width-gated, so it must not leak
    // onto a mouse-driven window that has not been classified yet.
    const { input } = await mountAt(1280, 800, null);
    expect(fontPx(input)).toBe(14);
  });

  it("leaves the resting box and the pill row exactly as they were at 390px coarse", async () => {
    // The consequence worth measuring rather than reasoning about. The padding is
    // `calc((var(--composer-rest-h) - 1lh) / 2)`, so a larger line-height eats
    // exactly the padding it adds and the resting height is unchanged BY
    // CONSTRUCTION — which is what makes the font change safe to ship on its own.
    // A two-line composer does grow by the line-height delta, which is the honest
    // consequence of larger text.
    const { input, pills } = await mountAt(390, 844, "coarse");
    const raised = input.getBoundingClientRect().height;
    const row = pills.getBoundingClientRect().height;
    expect(fontPx(input)).toBe(16);
    expect(raised).toBe(53);
    expect(row, "the textarea's band still matches the pill row's").toBe(53);

    // The same box with the token forced back to what the control read before
    // this change. An inline declaration on :root outranks both tier arms, so
    // this is the pre-change composer measured in the post-change tree.
    document.documentElement.style.setProperty(TOKEN, "var(--fs-md)");
    expect(fontPx(input)).toBe(14);
    expect(input.getBoundingClientRect().height, "raising the font moved the box").toBe(raised);
    expect(pills.getBoundingClientRect().height).toBe(row);
  });

  it("leaves the resting box alone at desktop width too", async () => {
    const { input } = await mountAt(1280, 800, "fine");
    const before = input.getBoundingClientRect().height;
    expect(before).toBe(40);
    document.documentElement.style.setProperty(TOKEN, "var(--fs-md)");
    expect(input.getBoundingClientRect().height).toBe(before);
  });
});
