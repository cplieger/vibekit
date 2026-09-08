// ---------------------------------------------------------------------------
// The model pill's reasoning tier is hidden on a PHONE-SHAPED viewport, which is
// narrow OR short.
//
// Its own file rather than a section of `effort-pill.test.ts`: that suite mounts a
// hand-written fixture and mocks six modules to follow the readout end to end,
// while this one mounts the SHIPPED stylesheet and resizes the real viewport — and
// a resize block has to sit last in its file and restore the size it found.
//
// Two halves, because neither answers the other's question. The source read says
// both arms exist and carry the same declaration (a computed style is one viewport
// at a time, so it cannot see an arm it is not sized for), and the measurement says
// the rule applies at the sizes it claims to (a source read cannot evaluate a media
// query).
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
// The viewport control, for the gate that can only be measured by resizing.
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub
// that throws.
import { page } from "vitest/browser";
import indexHtml from "../static/index.html?raw";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

/** The class every rule below keys on. */
const TIER = ".pill-model-effort";

describe("the markup the phone rule keys on", () => {
  it("marks the tier span with the class the stylesheet hides and the class JS toggles", () => {
    // Without this the CSS guard could pass against an element the page no longer
    // marks: `status.ts` writes the tier through the registry's id, and the class
    // is what the phone rule and the `.hidden` gate both reach it by.
    const at = indexHtml.indexOf('id="ctx-effort-pill"');
    expect(at, 'static/index.html has no id="ctx-effort-pill"').toBeGreaterThan(-1);
    const open = indexHtml.lastIndexOf("<span", at);
    const end = indexHtml.indexOf(">", at);
    const tag = indexHtml.slice(open, end + 1);

    expect(tag, "the phone rule's hook").toContain("pill-model-effort");
    expect(tag, "the JS gate's channel").toContain("hidden");
  });
});

describe("the phone-shaped arm of the tier's visibility rule", () => {
  it("is display:none under BOTH the narrow and the short arm", () => {
    // The viewport condition is CSS's, so it tracks a rotation and a window resize
    // for free. `ruleContaining` demands exactly one match per scope, so asking it
    // under each arm is what fails when an arm is dropped — which is the whole
    // property, and it is what today's width-only rule would fail.
    //
    // Comparing the two bodies then says both arms carry the SAME declaration, so
    // one cannot be quietly weakened. It does NOT prove they share one prelude: two
    // rules with identical bodies would pass, and they would behave identically, so
    // the shape is not what is being pinned.
    //
    // The nested `& .pill-model-effort` rule inside `.pill` is invisible to this
    // reader — its selector-list member is `& .pill-model-effort` — which is why
    // "exactly one" holds.
    const narrow = ruleContaining(loadCSS("15-input.css"), TIER, "48rem");
    const short = ruleContaining(loadCSS("15-input.css"), TIER, "30rem");
    expect(narrow.body).toMatch(/display:\s*none/);
    expect(short.body, "both arms hide it the same way").toBe(narrow.body);
  });
});

describe("the phone-shaped gate, measured at real viewport sizes", () => {
  // A media query answers about the VIEWPORT, so the only honest test of this gate
  // resizes one. The block sits LAST in the file and restores the size in
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

  /** The tier's computed `display` at one viewport size.
   *
   *  The span carries the class and NOT `.hidden`: that utility is the JS gate's
   *  channel and carries `display: none !important`, so leaving it on would answer
   *  "none" for every case and the CSS gate — the subject here — would go
   *  unmeasured. The resize is asserted, or a `page.viewport` that stopped moving
   *  the frame would make every case below report about the project's own size
   *  while still naming a phone. */
  async function displayAt(width: number, height: number): Promise<string> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    const span = document.createElement("span");
    span.className = "pill-model-effort";
    span.textContent = "· max";
    document.body.appendChild(span);
    return getComputedStyle(span).display;
  }

  it("hides the tier on a narrow, tall viewport — a phone in portrait", async () => {
    expect(await displayAt(360, 800)).toBe("none");
  });

  it("hides the tier on a wide, SHORT viewport — the same phone rotated", async () => {
    // The shape the narrow arm alone misses: past 48rem wide, so that arm stops
    // matching, on a device where the pill row is still full and the model name is
    // what pays for the tier.
    expect(await displayAt(900, 400)).toBe("none");
  });

  it("shows the tier on a viewport that is neither narrow nor short", async () => {
    // A tablet in landscape clears both arms, which is what measuring the SHORT
    // edge buys over measuring the width: 1024x768 is wide and tall, 900x400 is
    // wide and short.
    expect(await displayAt(1024, 768)).not.toBe("none");
  });
});
