// ---------------------------------------------------------------------------
// ONE SIZE CLASS FOR EVERY TOOLTIP IN THE APP, and the only admissible
// difference is the VIEW (user ruling, 2026-09-12).
//
// 139 sites write `data-tooltip`, one delegated controller renders them, and one
// rule sizes them — so the property is stated once here rather than per site.
// Before this the clamp was 6 lines: measured against the shipped stylesheet, an
// agent's front-matter description rendered 352x98 while the theme button's
// rendered 87x23, 17x the area for one hover idiom.
//
// THREE HALVES, because none answers another's question. The source read says the
// two numbers are TOKENS with no second writer (a computed style cannot see a
// per-site override that is not in force). The desktop measurement says the cap
// actually binds over content up to the longest a tooltip carries in production
// (a source read cannot evaluate wrapping). The narrow measurement says the
// viewport term is real (a `min()` reads as a literal at one size).
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll } from "vitest";
// `vitest/browser` is the Vitest 5 spelling; `@vitest/browser/context` is a stub
// that throws.
import { page } from "vitest/browser";

import {
  allRules,
  loadCSS,
  manifestSheets,
  mountAppCSS,
  ruleBody,
} from "./__test-helpers__/css-rules.js";

/** The one rule that sizes every tooltip. */
const TIP = ".uip-tooltip";

/** Real tooltip content, worst case first. Each is a live `data-tooltip` shape:
 *  an agent description (role-picker.ts), a typed steer (pending-steers.ts), a
 *  docs badge sentence (docs.ts), the longest file path measured on the live
 *  volume (tool-card.ts — 121 chars, against a p50 of 49 and a p99 of 85 over
 *  28,841 file-path tool inputs), and two icon-button hints. */
const SAMPLES: readonly { readonly what: string; readonly text: string }[] = [
  {
    what: "an agent's front-matter description",
    text:
      "L2 feature owner in the app-review skill: the decision half of the feature layer, " +
      "ONE per feature after the 3-family tracer wave. The feature's PM with stated priors: " +
      "leanness wins ties, deletion outranks addition, zoom out and run greenfield before " +
      "any addition, the boundary is exempt from lean pressure, and posture scales with cost " +
      "against value. Re-reads the code at every site it rules on, never from a summary.",
  },
  {
    what: "a typed steer",
    text:
      "actually wait, before you do that: check whether the rail already carries the turn " +
      "number as an absolute value, because if it does the whole join is unnecessary",
  },
  {
    what: "a docs badge sentence",
    text:
      "A symlink. Editing it writes the file it points to; deleting it would remove that " +
      "file, so delete is disabled here",
  },
  {
    what: "the longest file path on the live volume",
    text: "/workspace/vibekit/.worktrees/tool-card-bare-disclosure/static-src/node_modules/@cplieger/ui-primitives/src/disclosure.ts",
  },
  { what: "a two-clause icon hint", text: "Mouse mode. Switch to touch mode" },
  { what: "an icon hint", text: "Toggle theme" },
];

/** The width cap in force, read off a rendered tooltip. */
function capPx(): number {
  const tip = document.createElement("div");
  tip.className = "uip-tooltip";
  document.body.appendChild(tip);
  const cap = Number.parseFloat(getComputedStyle(tip).maxWidth);
  tip.remove();
  expect(Number.isFinite(cap), "a tooltip's max-width resolves to a length").toBe(true);
  return cap;
}

/** The longest file path the clamped box holds without clipping, in characters.
 *  Built out of real path segments, because `overflow-wrap: anywhere` breaks a
 *  path at its slashes and a single unbroken run would wrap differently. */
function longestFittingPath(): number {
  const at = (n: number): string => {
    let s = "/workspace";
    while (s.length < n) {
      s += "/abcdefgh";
    }
    return s.slice(0, n);
  };
  let fit = 0;
  for (let n = 20; n <= 220; n++) {
    const tip = document.createElement("div");
    tip.className = "uip-tooltip";
    tip.textContent = at(n);
    document.body.appendChild(tip);
    const clipped = tip.scrollHeight - tip.clientHeight > 1;
    tip.remove();
    if (clipped) {
      break;
    }
    fit = n;
  }
  return fit;
}

/** A tooltip built the way the controller builds one: `.uip-tooltip`, newlines as
 *  `<br>`. Returns its rendered box after layout. */
function measure(text: string): { width: number; height: number } {
  const tip = document.createElement("div");
  tip.className = "uip-tooltip";
  tip.setAttribute("role", "tooltip");
  text.split("\n").forEach((line, i) => {
    if (i > 0) {
      tip.appendChild(document.createElement("br"));
    }
    tip.appendChild(document.createTextNode(line));
  });
  document.body.appendChild(tip);
  const r = tip.getBoundingClientRect();
  const box = { width: r.width, height: r.height };
  tip.remove();
  return box;
}

describe("the two numbers that size a tooltip have one writer", () => {
  it("reads both off tokens rather than declaring a literal", () => {
    const body = ruleBody(loadCSS("04-uip-skin.css"), TIP);
    expect(body, "the width cap is the token").toMatch(/max-width:\s*var\(--tooltip-max-w\)/);
    // Both spellings, or the standard property drifts from the prefixed one the
    // way it did while only `-webkit-line-clamp` was declared here.
    expect(body).toMatch(/-webkit-line-clamp:\s*var\(--tooltip-lines\)/);
    expect(body).toMatch(/\n\s*line-clamp:\s*var\(--tooltip-lines\)/);
  });

  it("has no second writer anywhere in the bundle", () => {
    // The property is that ONE rule decides the size, so a per-site override is
    // what this fails on — which a computed style cannot see, since an override
    // out of force resolves to the same value as no override at all.
    const offenders = manifestSheets()
      .flatMap((s) => allRules(s.css))
      .filter((r) => r.selector.includes("uip-tooltip"))
      .filter((r) => /max-width|max-inline-size|line-clamp/.test(r.body))
      .filter((r) => !/var\(--tooltip-(max-w|lines)\)/.test(r.body))
      .map((r) => r.selector);
    expect(offenders, "a tooltip's size is one rule's, in 04-uip-skin.css").toEqual([]);
  });

  it("declares the viewport term the narrow measurement below proves", () => {
    // Source AND measurement, because the measurement alone would pass against a
    // flat cap that happens to be under the narrow viewport's width.
    expect(loadCSS("01-tokens.css")).toMatch(/--tooltip-max-w:\s*min\([^;]*100vw/);
  });
});

describe("every tooltip lands in one size class", () => {
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    styleEl = mountAppCSS();
  });

  afterAll(() => {
    styleEl?.remove();
  });

  it("bounds the tallest content to the shortest hint plus one line", () => {
    const boxes = SAMPLES.map((s) => ({ ...s, ...measure(s.text) }));
    const heights = boxes.map((b) => b.height);
    const shortest = Math.min(...heights);
    const tallest = Math.max(...heights);
    // One line of `--fs-xs` at the sheet's line-height, read off the page rather
    // than computed, so a type-scale change moves the bound with it.
    const line = measure("x").height;
    const twoLines = tallest - shortest;
    expect(
      twoLines,
      `tooltips span ${String(shortest)}px to ${String(tallest)}px: ` +
        boxes.map((b) => `${b.what} ${String(Math.round(b.height))}px`).join(", "),
    ).toBeLessThanOrEqual(line * 1.5);
  });

  it("holds every sample inside the width cap", () => {
    // Off a RENDERED tooltip, not the custom property: an unregistered custom
    // property's `getPropertyValue` answers the specified `min(...)` text rather
    // than a length, so parsing it yields NaN and the comparison passes vacuously.
    for (const s of SAMPLES) {
      const box = measure(s.text);
      expect(box.width, `${s.what}: ${String(Math.round(box.width))}px`).toBeLessThanOrEqual(
        capPx() + 0.5,
      );
    }
  });

  it("holds a real file path inside the clamped box", () => {
    // The measurement that decided the line count, and the reason it is 2 rather
    // than 1. Over 28,870 file-path tool inputs on the live volume: at one line
    // (57 chars fit) 19.26% of real paths clip, at two (114) 6 of 28,870 = 0.02%,
    // at three 0. So two lines is where the clip rate stops paying for a taller
    // box, and the residual is the six longest paths ever seen — each a
    // `.worktrees/<branch>/static-src/node_modules/...` chain.
    //
    // The threshold is MEASURED rather than asserted as a literal, so a type-scale
    // or cap change moves it here instead of silently clipping paths in the app.
    expect(longestFittingPath(), "two lines no longer hold a real path").toBeGreaterThanOrEqual(
      100,
    );
  });
});

// LAST in the file, and it restores the size it found: a resize leaks into every
// later file in the same browser session.
describe("the per-view difference, measured at a narrow viewport", () => {
  let entry: { readonly width: number; readonly height: number } | null = null;
  let styleEl: HTMLStyleElement | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    styleEl = mountAppCSS();
  });

  afterAll(async () => {
    styleEl?.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  it("narrows the cap below the desktop value on a small phone", async () => {
    // 320px is the narrowest viewport in the current iPhone range, and the size at
    // which a flat 22rem tooltip overflows: the library clamps a tooltip's
    // POSITION and not its width, so the cap is the only thing that can.
    await page.viewport(320, 640);
    const cap = capPx();
    expect(cap, "the cap tracks the viewport").toBeLessThan(320);
    for (const s of SAMPLES) {
      expect(measure(s.text).width, s.what).toBeLessThanOrEqual(cap + 0.5);
    }
  });
});
