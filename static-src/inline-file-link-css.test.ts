// ---------------------------------------------------------------------------
// A LINKIFIED PATH CHIP DOES NOT BREAK THE LEADING OF THE LINE IT SITS IN.
//
// `linkify.ts` wraps a path token in a `<button class="inline-file-link">`, and
// `61-mcp-tools.css`'s hit-target floor sizes every `button` — so with no size of
// its own the chip took the FLOOR as its box and a paragraph line holding one
// painted at the floor instead of at its own line height: 44px against 18px on a
// coarse pointer, 24px against 18px on a fine one. That file's own WCAG 2.5.8
// inline exception is written for exactly this shape and cannot reach it, because
// it is scoped to `a[href]` and this chip is a button.
//
// Numeric because none of it is visible in source: the defect is a floor arriving
// from a different stylesheet at zero specificity, so the chip's own rule reads
// correct either way. Real layout, and the paragraph is IN the viewport, because
// the claim is about a rendered line box.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");

const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** A prose paragraph with a chip in the middle of a sentence, plus a plain
 *  paragraph beside it as the control — the comparison is against the leading a
 *  line WITHOUT a chip gets, never against a literal, so a type-scale change
 *  moves both sides. */
function prose(): { withChip: HTMLElement; plain: HTMLElement; chip: HTMLElement } {
  const plain = document.createElement("p");
  plain.className = "msg-body";
  plain.textContent = "a plain line of prose with no chip in it at all";

  const withChip = document.createElement("p");
  withChip.className = "msg-body";
  withChip.append(document.createTextNode("a line of prose naming "));
  const chip = document.createElement("button");
  chip.className = "inline-file-link";
  chip.append(document.createTextNode("internal/agent/auth.go"));
  withChip.append(chip);
  withChip.append(document.createTextNode(" in the middle of a sentence"));

  host.append(plain, withChip);
  return { withChip, plain, chip };
}

describe("a path chip inside prose", () => {
  it.each(["fine", "coarse"] as const)(
    "leaves the line at the leading of a chipless line on a %s pointer",
    (tier) => {
      document.documentElement.dataset["pointer"] = tier;
      const { withChip, plain, chip } = prose();

      // One line either way, so the only thing that can differ is its height. Stated
      // as a bound on the DELTA rather than as equality, because an inline-flex box on
      // the baseline legitimately adds a fraction of a pixel: measured 0.45px here,
      // against 26px with the floor in force on a coarse pointer and 6px on a fine one.
      const line = plain.getBoundingClientRect().height;
      expect(withChip.getBoundingClientRect().height - line).toBeLessThan(1);
      // The mechanism rather than a second symptom: the chip is its own content's
      // height, so there is no floor left to push the line apart. A substitution back
      // to the floor fails here at BOTH tiers, where the delta above only catches it on
      // a pointer whose floor exceeds the line box.
      expect(chip.getBoundingClientRect().height).toBeLessThan(line + 1);
    },
  );

  it("is still reachable by the sentence around it, which is what the exception trades for", () => {
    // WCAG 2.5.8's inline exception applies because the LINE constrains the target,
    // so the honest thing to assert is that the chip is a real target inside that
    // line rather than a zero-size one.
    document.documentElement.dataset["pointer"] = "coarse";
    const { chip } = prose();
    const r = chip.getBoundingClientRect();
    expect(r.height, "a real box").toBeGreaterThan(12);
    expect(r.width, "wide enough to hit along the line").toBeGreaterThan(44);
  });
});
