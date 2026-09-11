// ---------------------------------------------------------------------------
// The dock's prose regions give up height first, and on a phone they give up
// more: `26-dock.css` caps them at 14rem and steps that down to 10rem under
// `@media (width <= 40rem)`.
//
// Its own file rather than a section of `decision-dock.test.ts`: that suite mounts
// cards and drives the queue, while this one mounts the SHIPPED stylesheet and
// resizes the real viewport — and a resize block has to sit last in its file and
// restore the size it found.
//
// Two halves, because neither answers the other's question. The source read says
// both scopes cap the SAME set of regions (a computed style is one element at one
// viewport, so it cannot see a region the override forgot), and the measurement
// says the override applies at the width it claims and not above it (a source read
// cannot evaluate a media query).
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control, for the gate that can only be measured by resizing.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub
// that throws.
import { page } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/** The member used to LOCATE each capping rule, and it is the one member of the
 *  five that can be.
 *
 *  `ruleContaining` splits the whole prelude on commas and compares each trimmed
 *  piece for equality, so the FIRST name inside the `:where(` is glued to the
 *  `.dock-card > :where(` in front of it and the LAST one to the `)` behind it —
 *  neither trims to itself. That leaves the three middle names, of which
 *  `.run-input-body` and `.user-input-body` each carry a rule of their own
 *  further down the file, and `ruleContaining` demands exactly one match. */
const ANCHOR = ".elicitation-body";

/** The regions named inside a capping rule's `:where(...)`.
 *
 *  Parsed here rather than through `ruleContaining`'s own member split, which
 *  splits the whole prelude on commas and so cannot say which names came from
 *  inside the `:where()` and which are siblings of it. */
function cappedRegions(prelude: string): string[] {
  const open = prelude.indexOf(":where(");
  expect(open, `no :where() in ${prelude}`).toBeGreaterThan(-1);
  const close = prelude.indexOf(")", open);
  return prelude
    .slice(open + ":where(".length, close)
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** The rem figure a capping rule declares, so no literal lives in this file. */
function declaredRem(body: string): number {
  const m = /max-block-size:\s*([\d.]+)rem/.exec(body);
  expect(m, `no rem max-block-size in ${body}`).not.toBeNull();
  return Number(m?.[1]);
}

const css = loadCSS("26-dock.css");
const base = ruleContaining(css, ANCHOR, "top");
const phone = ruleContaining(css, ANCHOR, "40rem");

describe("the set of regions the phone override reaches", () => {
  it("is the same set the base rule caps", () => {
    // The drift this is here for: a region added to the base list and forgotten in
    // the override keeps the desktop cap on a phone, which is silent — the card
    // still bounds itself, just 4rem higher than the step-down was sized for. No
    // measurement can see it, because a computed style answers for the one element
    // it is given.
    expect(cappedRegions(phone.selector)).toEqual(cappedRegions(base.selector));
  });

  it("steps the cap DOWN rather than up", () => {
    // A reversed override reads as correct in a diff and is the other way to get
    // this wrong: the phone, which has the least room, would get the larger cap.
    expect(declaredRem(phone.body)).toBeLessThan(declaredRem(base.body));
  });
});

describe("the phone step-down, measured at real viewport sizes", () => {
  // A media query answers about the VIEWPORT, so the only honest test of this
  // override resizes one. The block sits LAST in the file and restores the size in
  // `afterAll`. `page.viewport` has no getter, so the size is READ off the frame on
  // entry rather than copied from `vitest.config.ts`: a hand-copied pair would
  // silently leave every later file measuring at the old size if that config moved.
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterEach(() => {
    document.body.innerHTML = "";
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** The cap in force on a dock prose region at one viewport size, in rem.
   *
   *  Returned in rem against the live root size rather than in px, so a change to
   *  the root font size moves with the stylesheet instead of failing here. The
   *  resize is asserted, or a `page.viewport` that stopped moving the frame would
   *  make every case below report about the project's own size while still naming
   *  a phone. */
  async function capAt(width: number, height: number): Promise<number> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    const card = document.createElement("div");
    card.className = "dock-card";
    const region = document.createElement("div");
    region.className = "run-input-body";
    card.appendChild(region);
    document.body.appendChild(card);
    const rootPx = parseFloat(getComputedStyle(document.documentElement).fontSize);
    return parseFloat(getComputedStyle(region).maxBlockSize) / rootPx;
  }

  it("caps a region at the phone figure on a 390px viewport", async () => {
    expect(await capAt(390, 844)).toBeCloseTo(declaredRem(phone.body), 2);
  });

  it("still caps it at the phone figure at the boundary width itself", async () => {
    // `width <= 40rem` includes 640, so the boundary belongs to the phone arm. A
    // `<` written where `<=` was meant passes every other case in this file.
    expect(await capAt(640, 900)).toBeCloseTo(declaredRem(phone.body), 2);
  });

  it("caps it at the base figure one pixel wider", async () => {
    expect(await capAt(641, 900)).toBeCloseTo(declaredRem(base.body), 2);
  });
});
