// ---------------------------------------------------------------------------
// EVERY TEXT CONTROL SITS ON THE TYPE SCALE, and none of them outsizes the body
// rung.
//
// This replaced a 16px FLOOR over `:is(input, textarea, select)` in two tier arms
// (`text-field-floor-css.test.ts`, deleted with the rule). The floor existed
// because iOS Safari zooms the page when a control under 16px takes focus — but
// `static/index.html` ships `maximum-scale=1.0`, which is what suppresses that
// auto-zoom, so it defended against behaviour the document cannot exhibit while
// making every field in the app 16px on a finger against 11-14px everywhere
// around it. 61-mcp-tools.css carries the full record.
//
// Two contracts, and the first is the one the removal made visible. The reset's
// zero-specificity size is what keeps a control nothing else has sized OFF the UA
// default: Chromium gives every form control Arial at 13.3333px, and 18 of the 50
// controls the shipped page renders had no size from any component class. The
// floor was hiding that on the coarse tier by rewriting all of them. Second, no
// control computes ABOVE the body rung, which is the reported defect stated as a
// guard.
//
// The sizes are read off `:root` rather than listed, so a retune of a token moves
// the assertion with it and only a size that is on NO rung fails.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control. `vitest/browser` is the Vitest 5 spelling;
// `@vitest/browser/context` is a stub that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { allRules, loadCSS, mountAppCSS } from "./__test-helpers__/css-rules.js";

/** Every control this sweeps. */
const CONTROLS = "input, textarea, select";

/** The type scale's own token names, in rung order. */
const RUNGS = ["--fs-xs", "--fs-sm", "--fs-base", "--fs-md", "--fs-lg", "--fs-xl"] as const;

/** The rung a field may not exceed: the size the transcript reads. */
const BODY_RUNG = "--fs-md";

/** The rows measured for overflow, all present in the shipped markup. */
const TIGHT_ROWS = [
  ".prompt-pills",
  ".bottom-bar",
  ".mcp-modal-tabs",
  ".rule-form",
  ".settings-tab-bar",
];

describe("the declarations, read from source", () => {
  it("declares the 16px text-entry floor, in both tier arms", () => {
    // THIS PIN IS INVERTED, DELIBERATELY (amendment §D, 2026-09-10). It used to
    // assert the floor ABSENT, on the premise stated in this file's header: that
    // `static/index.html` ships `maximum-scale=1.0`, which suppresses iOS's
    // focus auto-zoom, so the floor defended against behaviour the document could
    // not exhibit. THAT CLAUSE IS GONE — the viewport meta lost it for WCAG 1.4.4
    // and 02-reset.css now allows `pinch-zoom` to match — so the premise is false
    // on disk and the zoom is live again for any focused control under 16px.
    // `#prompt-input` carries `autofocus`, so the app would load already zoomed.
    const floors = allRules(loadCSS("61-mcp-tools.css")).filter(
      (r) => /:is\(input, textarea, select\)/.test(r.selector) && /font-size:/.test(r.body),
    );
    expect(floors.map((r) => r.selector)).toEqual([
      ':root[data-pointer="coarse"] :is(input, textarea, select)',
      ':root:not([data-pointer="fine"]) :is(input, textarea, select)',
    ]);
    // `max(…, 1em)` so a control inheriting something LARGER keeps it rather than
    // being clamped down to the floor.
    for (const rule of floors) {
      expect(rule.body).toMatch(/font-size:\s*max\(1rem,\s*1em\)/);
    }
  });

  it("sizes an unstyled text control from the reset, at zero specificity", () => {
    // Zero specificity is the whole design: it beats the UA sheet and loses to
    // every component class, so it reaches exactly the controls nothing else has
    // sized. A floor with real specificity is what this replaced.
    const rule = allRules(loadCSS("02-reset.css")).find(
      (r) => r.selector === ":where(input, textarea, select)",
    );
    expect(rule, "02-reset.css has no :where(input, textarea, select) rule").toBeDefined();
    expect(rule?.body).toMatch(new RegExp(`font-size:\\s*var\\(${BODY_RUNG}\\)`));
  });

  it("keeps `button` out of that selector", () => {
    // Several icon buttons are sized against the UA font-size, so a size here moves
    // them — the same reason the family rule one line above it is family-only.
    const rule = allRules(loadCSS("02-reset.css")).find(
      (r) => r.selector === ":where(input, textarea, select)",
    );
    expect(rule?.selector).not.toContain("button");
  });

  it("leaves no hardcoded 16px on a text control", () => {
    // The three find inputs (24-find.css, 20-editor.css) and the settings number
    // input (18-pages.css) each carried a literal `16px` or `1rem` at a mobile
    // breakpoint, with a comment naming the iOS zoom. Swept as a population,
    // because the next one would be written the same way.
    const offenders: string[] = [];
    for (const name of ["24-find.css", "20-editor.css", "18-pages.css"]) {
      for (const rule of allRules(loadCSS(name))) {
        if (
          /-input|-find-input/.test(rule.selector) &&
          /font-size:\s*(16px|1rem)\b/.test(rule.body)
        ) {
          offenders.push(`${name} ${rule.selector}`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });
});

describe("the controls, measured over the shipped markup", () => {
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
   *  shown: most of these controls live behind a modal or a settings panel, so a
   *  fixture holding one field would measure a page this app does not render. The
   *  resize is asserted, or a `page.viewport` that stopped moving the frame would
   *  leave every case reporting about the project's own size. */
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

  /** The type scale as this page resolves it, in px. Read rather than restated, so
   *  a token retune moves every assertion below with it. */
  function scalePx(): Map<number, string> {
    const root = getComputedStyle(document.documentElement);
    const out = new Map<number, string>();
    for (const rung of RUNGS) {
      const raw = root.getPropertyValue(rung).trim();
      const rem = Number.parseFloat(raw);
      expect(Number.isNaN(rem), `${rung} is not declared on :root`).toBe(false);
      out.set(rem * Number.parseFloat(root.fontSize), rung);
    }
    return out;
  }

  /** Every control whose size is on no rung of the scale, named so a failure says
   *  WHICH one drifted rather than how many. The UA default (Arial 13.3333px) is
   *  the shape this catches. */
  function offScale(): string[] {
    const scale = scalePx();
    const out: string[] = [];
    for (const el of document.querySelectorAll(CONTROLS)) {
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      if (!scale.has(px)) {
        const type = el instanceof HTMLInputElement ? `[${el.type}]` : "";
        out.push(`${el.tagName.toLowerCase()}${type}#${el.id || "?"} = ${px}px`);
      }
    }
    return out;
  }

  /** Every control that computes above the body rung — the reported defect. */
  function aboveBody(): string[] {
    const root = getComputedStyle(document.documentElement);
    const bodyPx =
      Number.parseFloat(root.getPropertyValue(BODY_RUNG)) * Number.parseFloat(root.fontSize);
    const out: string[] = [];
    for (const el of document.querySelectorAll(CONTROLS)) {
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      if (px > bodyPx) {
        const type = el instanceof HTMLInputElement ? `[${el.type}]` : "";
        out.push(`${el.tagName.toLowerCase()}${type}#${el.id || "?"} = ${px}px`);
      }
    }
    return out;
  }

  const TIERS: readonly [string, number, number, "coarse" | "fine" | null][] = [
    ["a phone with a coarse pointer", 390, 844, "coarse"],
    // An iPad in landscape: a finger past 48rem, where the width fallback does not
    // reach and only the pointer tier can carry a rule.
    ["a WIDE coarse viewport", 1024, 768, "coarse"],
    ["a phone with no pointer tier resolved yet", 390, 844, null],
    ["a desktop with a fine pointer", 1280, 800, "fine"],
    ["a desktop with no pointer tier resolved yet", 1280, 800, null],
  ];

  it.each(TIERS)("puts every control on a rung of the scale on %s", async (_l, w, h, pointer) => {
    await mountPage(w, h, pointer);
    // Enumerated, never listed: the count is asserted only as a floor, so a control
    // added to the page joins this case without an edit here — and a markup change
    // that emptied the page could not make it pass vacuously.
    expect(document.querySelectorAll(CONTROLS).length).toBeGreaterThan(40);
    expect(offScale()).toEqual([]);
  });

  // THE COARSE ARMS LEFT THIS CASE (amendment §D). The 16px floor is deliberately
  // ABOVE the body rung — 16 against `--fs-md`'s 14 — because 16 is iOS's
  // auto-zoom threshold rather than a rung of this app's scale, and the cost is
  // stated at the rule in 61-mcp-tools.css. What the case still pins is the tier
  // where no floor applies: nothing there may outsize the transcript, which is the
  // reported defect the type scale exists to answer.
  // THE POINTER decides the floor, never the width — so a WIDE coarse viewport (an
  // iPad in landscape) is a floored arm, and an unresolved tier is floored only
  // below the 48rem no-JS fallback.
  const FINE_TIERS = TIERS.filter(
    ([, w, , pointer]) => pointer === "fine" || (pointer === null && w > 768),
  );

  it.each(FINE_TIERS)("outsizes the body rung with no control on %s", async (_l, w, h, pointer) => {
    await mountPage(w, h, pointer);
    expect(document.querySelectorAll(CONTROLS).length).toBeGreaterThan(40);
    expect(aboveBody()).toEqual([]);
  });

  it.each([
    ["a phone with a coarse pointer", 390, 844, "coarse" as const],
    ["a WIDE coarse viewport", 1024, 768, "coarse" as const],
    ["a phone with no pointer tier resolved yet", 390, 844, null],
  ])("floors every text control at 16px on %s", async (_l, w, h, pointer) => {
    // The other half of amendment §D, and the five controls it names by hand are
    // asserted as a POPULATION instead: every text-entry control on the shipped
    // page, so a field added later joins the case without an edit here. The box
    // controls are excluded — a checkbox paints at its own `rem` size and the floor
    // reaches its label, not its box (the last case in this file measures that).
    await mountPage(w, h, pointer);
    const under: string[] = [];
    for (const el of document.querySelectorAll<HTMLElement>(CONTROLS)) {
      if (el instanceof HTMLInputElement && (el.type === "checkbox" || el.type === "radio")) {
        continue;
      }
      const px = Number.parseFloat(getComputedStyle(el).fontSize);
      if (px < 16) {
        const type = el instanceof HTMLInputElement ? `[${el.type}]` : "";
        under.push(`${el.tagName.toLowerCase()}${type}#${el.id || "?"} = ${String(px)}px`);
      }
    }
    expect(under, "iOS zooms the page for a focused control under 16px").toEqual([]);
    // The five the amendment names, asserted by id so a failure says which.
    for (const id of ["prompt-input", "fb-path", "tool-search", "tool-sort"]) {
      const el = document.getElementById(id);
      expect(el, `#${id} is not in the shipped markup`).not.toBeNull();
      if (el !== null) {
        expect(Number.parseFloat(getComputedStyle(el).fontSize), `#${id}`).toBeGreaterThanOrEqual(
          16,
        );
      }
    }
  });

  it("overflows none of the tight rows at 390px coarse", async () => {
    // The gate any change to the type scale of every field has to clear: it is only
    // safe if no row a field sits in starts clipping. Measured as
    // content-wider-than-box per row, which is what a reader sees as a cut label or
    // a control pushed off the edge.
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

  it("moves no box control, because the box controls carry a rem size of their own", async () => {
    // The premise for a reset selector that excludes no input type: a native
    // checkbox and radio paint at a font-relative size and would move under a
    // type-scale change, but both carry an explicit `rem` box (02-reset.css and
    // 61-mcp-tools.css). Pushing the RUNG to 3rem is deliberately absurd — a
    // control whose box tracked the font would triple, and these do not move at
    // all. The rung rather than the root font size, because a `rem` box is
    // root-relative: moving the root would move the boxes for a reason that has
    // nothing to do with the type scale.
    // ON THE FINE TIER, because amendment §D's 16px floor pins a coarse control's
    // font-size outright — which would make the rung override below reach nothing
    // and the case pass for a reason that says nothing about the box.
    await mountPage(1280, 800, "fine");
    const boxes = [...document.querySelectorAll<HTMLElement>('[type="checkbox"], [type="radio"]')];
    expect(boxes.length).toBeGreaterThan(10);
    const geom = (): string[] =>
      boxes.map((b) => {
        const r = b.getBoundingClientRect();
        return `${r.width.toFixed(2)}x${r.height.toFixed(2)}`;
      });
    const before = geom();

    document.documentElement.style.setProperty(BODY_RUNG, "3rem");
    try {
      const first = boxes[0];
      expect(first, "the page renders at least one box control").toBeDefined();
      if (first !== undefined) {
        expect(
          Number.parseFloat(getComputedStyle(first).fontSize),
          "the rung override actually reached the control",
        ).toBe(48);
      }
      expect(geom()).toEqual(before);
    } finally {
      document.documentElement.style.removeProperty(BODY_RUNG);
    }
  });
});
