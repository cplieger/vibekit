import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { allRules, loadCSS } from "./__test-helpers__/css-rules.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// Every rotating ring turns at ONE period.
//
// `vk-spin` had nine consumers across three periods (0.6s, 0.8s, 0.9s) and the
// period tracked nothing measurable: a 12px ring ran at both 0.6s and 0.8s and a
// 16px ring at both, so the spinner was one idiom at three speeds and two rings
// of the same size could sit side by side visibly drifting apart. This is the
// other half of the dot consolidation, whose own token comment names "the run
// card's spinners" among the periods it did not reach.
//
// Two halves, because either alone passes while the app is broken. The SWEEP is
// over every stylesheet rather than the four that carry spinners today, so a
// tenth consumer written with a literal fails here instead of shipping. And a
// literal is not the only way to get this wrong: `var(--spin-dur)` with no
// declaration behind it is valid CSS text that resolves to nothing at
// computed-value time, which makes the whole `animation` shorthand invalid and
// runs NO animation — a ring that has stopped rather than one at the wrong speed,
// and nothing to grep for. So the second half reads the period off a real
// element.
// ---------------------------------------------------------------------------

/** Every shipped stylesheet, so the sweep cannot miss a file. */
const sheets = import.meta.glob<string>("./css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

describe("the spinner period", () => {
  it("is read from one token by every vk-spin consumer, in every stylesheet", () => {
    const offenders: string[] = [];
    let consumers = 0;

    for (const [path, css] of Object.entries(sheets)) {
      for (const { selector, body } of allRules(css)) {
        // `allRules` descends into at-rules and hands back style rules only, so
        // the `@keyframes vk-spin` definition itself is never a subject here.
        if (!body.includes("vk-spin")) {
          continue;
        }
        consumers++;
        if (!/animation:\s*vk-spin\s+var\(--spin-dur\)/.test(body)) {
          offenders.push(`${path} { ${selector} }`);
        }
      }
    }

    expect(offenders, "every vk-spin rule takes its period from --spin-dur").toEqual([]);
    // A sweep that matched nothing would pass vacuously.
    expect(consumers, "the sweep found the spinner rules").toBeGreaterThanOrEqual(9);
  });

  it("declares that token, so the animation actually runs", () => {
    // The declaration and its value, from source: an element cannot tell a
    // missing token from one whose value happens to be the default.
    expect(loadCSS("01-tokens.css")).toContain("--spin-dur:");

    const el = document.createElement("span");
    el.className = "spinner";
    stage.appendChild(el);

    const [anim] = el.getAnimations();
    expect(anim, "the .spinner ring is animating").toBeDefined();
    // `animationName` is CSSAnimation's, not the Animation base's — narrowing
    // also asserts this is a CSS animation rather than a WAAPI one.
    expect(anim).toBeInstanceOf(CSSAnimation);
    if (anim instanceof CSSAnimation) {
      expect(anim.animationName).toBe("vk-spin");
    }
    expect(anim?.effect?.getTiming().duration).toBe(600);
    expect(anim?.effect?.getTiming().iterations).toBe(Infinity);
  });

  it("gives two rings of different sizes the same period", () => {
    // The defect in one assertion: `.spinner` (16px) ran at 0.8s while
    // `.btn-loading`'s ring (12px) ran at 0.6s, so a save button beside a
    // loading list drifted against it.
    const big = document.createElement("span");
    big.className = "spinner";
    const small = document.createElement("span");
    small.className = "spinner-sm";
    stage.append(big, small);

    const durOf = (e: Element): EffectTiming["duration"] =>
      e.getAnimations()[0]?.effect?.getTiming().duration;

    // Both halves stated, or the case passes vacuously the moment the token goes
    // missing and each ring reports `undefined` — equal, and both stopped.
    expect(durOf(big)).toBe(600);
    expect(durOf(small)).toBe(600);
    expect(durOf(big)).toBe(durOf(small));
  });
});
