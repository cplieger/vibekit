// ---------------------------------------------------------------------------
// THE TEXT-ENTRY FLOOR: no focusable text control renders under 16px on a coarse
// pointer, because iOS zooms the page when one takes focus.
//
// Raising the composer (composer-font-css.test.ts) closed the composer's own focus
// sites and nothing else — this app focuses around eighteen other text controls
// programmatically, several declaring --fs-sm or --fs-xs. So the answer is a
// mechanical floor, and this is the guard that stops the next control arriving
// under the threshold: it enumerates every control the shipped markup produces
// rather than naming any of them, which is the correction the hit floor's own
// history records for its hand-kept allow-list.
//
// A sibling of composer-font-css.test.ts rather than a section of it: the subject
// is every control rather than one, and each file's resize block has to sit last
// and restore the size it found.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control. `vitest/browser` is the Vitest 5 spelling;
// `@vitest/browser/context` is a stub that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { allRules, loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

/** iOS's focus-zoom threshold, and the whole point of the rule. */
const THRESHOLD_PX = 16;

/** Every control the floor governs. */
const CONTROLS = "input, textarea, select";

/** The rows measured for overflow, all present in the shipped markup. */
const TIGHT_ROWS = [
  ".prompt-pills",
  ".bottom-bar",
  ".mcp-modal-tabs",
  ".rule-form",
  ".settings-tab-bar",
];

describe("the floor's declarations, read from source", () => {
  const arms = allRules(loadCSS("61-mcp-tools.css")).filter((r) =>
    /:is\(input, textarea, select\)/.test(r.selector),
  );

  it("is one rule per tier arm, and the two carry the same declaration", () => {
    // The token alone is not enough: --fs-field is 1rem in both arms, but a rule
    // has to APPLY for a control to read it. `allRules` flattens at-rules, so the
    // no-JS arm's own media gate is proved by the measurement below rather than
    // here.
    expect(arms.map((r) => r.selector)).toEqual([
      ':root[data-pointer="coarse"] :is(input, textarea, select)',
      ':root:not([data-pointer="fine"]) :is(input, textarea, select)',
    ]);
    const [coarse, noJS] = arms;
    expect(coarse?.body).toMatch(/font-size:\s*max\(var\(--fs-field\), 1em\)/);
    // Trimmed, because the no-JS arm is nested in its media block and so carries
    // its indentation; the DECLARATION is what may not differ.
    expect(noJS?.body.trim(), "both arms floor it the same way").toBe(coarse?.body.trim());
  });

  it("carries real specificity, because font-size is a property components declare", () => {
    // The asymmetry with the hit floor beside it: that one sets min-width and
    // min-height, which NO component declares, so it wins at zero specificity by
    // being the only writer. A `:where()` floor here would lose to every
    // component class that sets a font size.
    for (const arm of arms) {
      expect(arm.selector).not.toContain(":where(");
    }
  });

  it("has no range input to protect, which is why it excludes no type", () => {
    // The measured premise for the plain selector. A native checkbox and radio
    // paint at a font-relative size and would grow under a floor — but both carry
    // an explicit `rem` box in this app (asserted below and measured at runtime),
    // and `type="range"` has no instance at all.
    expect(indexHtml).not.toContain('type="range"');
  });

  it("sizes the box controls in rem, so the type scale cannot reach them", () => {
    const reset = allRules(loadCSS("02-reset.css")).find(
      (r) => r.selector === 'input[type="checkbox"]',
    );
    const radio = allRules(loadCSS("61-mcp-tools.css")).find(
      (r) => r.selector === 'input[type="radio"]',
    );
    expect(reset?.body).toMatch(/width:\s*1rem/);
    expect(reset?.body).toMatch(/height:\s*1rem/);
    expect(radio?.body).toMatch(/width:\s*1rem/);
    expect(radio?.body).toMatch(/height:\s*1rem/);
  });
});

describe("the floor, measured over the shipped markup", () => {
  // The block sits LAST in the file and restores the size it found in `afterAll`.
  // `page.viewport` has no getter, so the entry size is READ off the frame rather
  // than copied from `vitest.config.ts`.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
    document.documentElement.removeAttribute("data-pointer");
    document.documentElement.style.removeProperty("--fs-field");
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The app's own page at one viewport size under one pointer tier.
   *
   *  The whole body, with every `hidden` container revealed and every dialog
   *  shown: a control the floor must reach is usually behind a modal or a settings
   *  panel, so a fixture holding one field would measure a page this app does not
   *  render. The resize is asserted, or a `page.viewport` that stopped moving the
   *  frame would leave every case reporting about the project's own size. */
  async function mountPage(
    width: number,
    height: number,
    pointer: "coarse" | "fine" | null,
  ): Promise<void> {
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
    const body = indexHtml.slice(indexHtml.indexOf("<body"), indexHtml.indexOf("</body>"));
    document.body.innerHTML = body.slice(body.indexOf(">") + 1);
    for (const el of document.querySelectorAll<HTMLElement>("[hidden], .hidden")) {
      el.removeAttribute("hidden");
      el.classList.remove("hidden");
    }
    for (const dlg of document.querySelectorAll("dialog")) {
      dlg.show();
    }
  }

  /** Every control that computes under the threshold, named so a failure says
   *  WHICH one arrived under it rather than how many. */
  function belowThreshold(): string[] {
    const out: string[] = [];
    for (const el of document.querySelectorAll(CONTROLS)) {
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      if (px < THRESHOLD_PX) {
        const type = el instanceof HTMLInputElement ? `[${el.type}]` : "";
        out.push(`${el.tagName.toLowerCase()}${type}#${el.id} = ${px}px`);
      }
    }
    return out;
  }

  /** Every control that computes ABOVE the threshold, named for the same reason
   *  the list below it names its members: the day one appears, the failure has to
   *  say WHICH control wants more than the floor. */
  function aboveThreshold(): string[] {
    const out: string[] = [];
    for (const el of document.querySelectorAll(CONTROLS)) {
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      if (px > THRESHOLD_PX) {
        const type = el instanceof HTMLInputElement ? `[${el.type}]` : "";
        out.push(`${el.tagName.toLowerCase()}${type}#${el.id} = ${px}px`);
      }
    }
    return out;
  }

  it("puts every text control at or over 16px on a coarse pointer", async () => {
    await mountPage(390, 844, "coarse");
    // Enumerated, never listed: the count is asserted only as a floor, so a
    // control added to the page joins this case without an edit here — and a
    // markup change that emptied the page could not make it pass vacuously.
    expect(document.querySelectorAll(CONTROLS).length).toBeGreaterThan(40);
    expect(belowThreshold()).toEqual([]);
  });

  it("OWNS the size too, so no coarse-tier control computes above the floor", async () => {
    // The rule's ownership runs UP as well as down (why: the note at the rule in
    // `61-mcp-tools.css`), so this case is what fails the day a control wants more
    // than the floor and needs a carve-out selector. ONE viewport, unlike the
    // below-floor case: both tier arms carry a byte-identical declaration, so a
    // second adds no discriminating power for THIS assertion.
    await mountPage(390, 844, "coarse");
    expect(document.querySelectorAll(CONTROLS).length).toBeGreaterThan(40);
    expect(aboveThreshold()).toEqual([]);
  });

  it("does the same on a WIDE coarse viewport, which is the arm's own population", async () => {
    // An iPad in landscape: a finger on a viewport past 48rem, so the width
    // fallback does not reach it and the pointer-tier arm is the only thing that
    // can. THE TIER IS THE POINTER, NOT THE WIDTH — the same reason --ctl-h and
    // --hit-floor are declared twice — and this is the case that says so, because
    // at 390px the fallback arm would carry the floor on its own.
    await mountPage(1024, 768, "coarse");
    expect(belowThreshold()).toEqual([]);
  });

  it("does the same before the pointer tier has been resolved", async () => {
    // The no-JS fallback arm. `pointer-tier.ts` seeds the tier from a capability
    // query and corrects it on the first real PointerEvent, so there is a window
    // with no attribute at all — and a focus inside it zooms just the same.
    await mountPage(390, 844, null);
    expect(belowThreshold()).toEqual([]);
  });

  it("leaves a desktop with a fine pointer entirely alone", async () => {
    await mountPage(1280, 800, "fine");
    // The floor is a coarse-tier rule, so the small controls stay small here.
    // Asserted as a POPULATION rather than on one id: if the floor leaked onto the
    // fine tier this list would be empty.
    expect(belowThreshold().length).toBeGreaterThan(10);
  });

  it("leaves a desktop with no pointer tier alone too, because the fallback is width-gated", async () => {
    await mountPage(1280, 800, null);
    expect(belowThreshold().length).toBeGreaterThan(10);
  });

  it("moves no box control, however far the type scale is pushed", async () => {
    // The measured premise for a selector that excludes no input type. Pushing
    // --fs-field to 3rem is deliberately absurd: a control whose box tracked the
    // font would triple, and these do not move at all.
    await mountPage(390, 844, "coarse");
    const boxes = [...document.querySelectorAll<HTMLElement>('[type="checkbox"], [type="radio"]')];
    expect(boxes.length).toBeGreaterThan(10);
    const before = boxes.map((b) => {
      const r = b.getBoundingClientRect();
      return `${r.width.toFixed(2)}x${r.height.toFixed(2)}`;
    });

    document.documentElement.style.setProperty("--fs-field", "3rem");
    expect(Number.parseFloat(getComputedStyle(boxes[0] as Element).fontSize)).toBe(48);
    const after = boxes.map((b) => {
      const r = b.getBoundingClientRect();
      return `${r.width.toFixed(2)}x${r.height.toFixed(2)}`;
    });
    expect(after).toEqual(before);
  });

  it("overflows none of the tight rows at 390px coarse", async () => {
    // The gate the floor had to clear before landing: raising the type scale of
    // every field in the app is only safe if no row it sits in starts clipping.
    // Measured as content-wider-than-box per row, which is what a reader sees as
    // a cut label or a control pushed off the edge.
    await mountPage(390, 844, "coarse");
    const overflowing: string[] = [];
    for (const sel of TIGHT_ROWS) {
      const found = [...document.querySelectorAll<HTMLElement>(sel)];
      expect(found.length, `${sel} is not in the shipped markup`).toBeGreaterThan(0);
      for (const el of found) {
        const over = el.scrollWidth - el.clientWidth;
        if (over > 0) {
          overflowing.push(`${sel}${el.id === "" ? "" : `#${el.id}`} over by ${over}px`);
        }
      }
    }
    expect(overflowing).toEqual([]);
  });

  it("leaves the composer's own row at the height the pill row reads", async () => {
    // The one row whose height is a stated number elsewhere in the app
    // (15-input.css derives the textarea's resting band from it), so a drift here
    // is the composer and the pills disagreeing again.
    await mountPage(390, 844, "coarse");
    const pills = document.querySelector(".prompt-pills");
    expect(pills?.getBoundingClientRect().height).toBe(53);
  });
});
