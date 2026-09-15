// The sidebar's connection MARK is STILL when the connection is healthy, and it is
// a decorative span rather than a control.
//
// `#status-dot` reports the SSE transport and has never reported a turn, but it
// used to run a `vk-ping` ripple on a `::after` overlay every four seconds
// forever — a dot pulsing in the healthy steady state, in the same green the tab
// dot uses for a finished turn, next to a card carrying credits and account
// usage. That is what a work indicator looks like, so it read as one.
//
// The dot had no test of any kind, which is how an animation nobody wanted stayed
// for a year. These are the two halves worth pinning: that the settled state
// carries no motion, and that `connecting` keeps its breathe, because motion
// meaning an UNSETTLED state is the app's own motion axis and removing it would
// be the opposite mistake.
//
// THE ELEMENT CHANGED IN 2026-09 and three of its mechanisms went with it. The mark
// and the address are ONE `<button id="account-btn">` now, which owns the target,
// the press and the focus ring, so the mark is a `<span aria-hidden="true">` with
// no `all: unset` to strip button chrome, no `::after` hit expander, and no
// phone-only 44px treatment. TWO cases are deleted with those mechanisms rather
// than rewritten:
//
//   - "paints and animates nothing on its ::after overlay" would pass VACUOUSLY
//     against a pseudo that no longer generates, which is worse than failing.
//   - "grows its hit target past its 8px disc" described the expander itself.
//
// And the whole phone `describe` went with the `width <= 48rem` block's four
// `::before` rules — the mark's size is now ONE rule at every tier, which is
// asserted below as SOURCE (what that block CONTAINS) plus a rendered size at both
// pointer tiers.
//
// Computed rather than read out of the source for the motion half: the `::after`
// was removed wholesale rather than just its `animation` declaration (`vk-ping`
// was the only thing fading an otherwise opaque green disc at `inset: 0`), and
// "does the pseudo generate at all" is a question only a real cascade answers.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS, atRuleBody, loadCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

/** The mark as `static/index.html` authors it: a decorative SPAN. It goes straight
 *  in `<body>`, which is what makes the size case below meaningful — the old rule
 *  rendered its disc only through flex-item blockification, so an unparented fixture
 *  measured nothing and no case in this file ever asserted the 8px box. */
function mountDot(cls: string): HTMLElement {
  const dot = document.createElement("span");
  dot.className = cls;
  dot.setAttribute("aria-hidden", "true");
  document.body.replaceChildren(dot);
  return dot;
}

describe("the sidebar connection mark", () => {
  it("carries no animation once connected", () => {
    const dot = mountDot("status-dot connected");
    expect(getComputedStyle(dot).animationName).toBe("none");
    expect(dot.getAnimations({ subtree: true })).toHaveLength(0);
  });

  it("still breathes while connecting, on its own animation", () => {
    // The beat is the mark's OWN animation, created only while it is unsettled, so
    // an idle app runs none at all. Driven to two known phases rather than sampled
    // over time — deterministic, and it proves the mark follows the beat rather than
    // merely naming it. The OPACITIES are unchanged from the shared-clock era because
    // `vk-dot-beat` leaves `from`/`to` implicit, so they are the element's own
    // opacity and 50% is its declared peak.
    const dot = mountDot("status-dot");
    const beat = dot.getAnimations()[0];
    expect(beat, "an unsettled mark must carry its own beat").toBeDefined();
    beat!.pause();

    // The midpoint is READ off the animation rather than restated: this used to be a
    // literal 1200 commented as "half of --dot-beat-dur", which is a transcription
    // that a token retune silently invalidates — and did, when the period halved to
    // match web-terminal-kiro in 2026-09, leaving the sample on the cycle's END where
    // the implicit `to` reads 1 and the assertion was one more retune from passing
    // for the wrong reason.
    const period = beat!.effect?.getComputedTiming().duration;
    expect(typeof period, "the beat declares a resolved duration").toBe("number");
    const midpoint = (period as number) / 2;

    beat!.currentTime = 0; // rest -> the element's own opacity
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(1, 2);

    beat!.currentTime = midpoint; // 50% -> the declared peak
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(0.45, 2);
  });

  it.each(["connected", "error"])("carries no beat at all when %s", (state) => {
    // BOTH settled states, because the two are separate rules and only one of them
    // being wrong is the live shape of this bug. An animation OUTRANKS a normal
    // declaration, so a settled state cannot answer a beat with `opacity: 1` — it
    // would breathe a still disc, which is what shipped for one build. The beat is
    // scoped to `:not(.connected, .error)` instead, so the settled states carry no
    // animation to outrank them. Asserting absence AND the resting value: either
    // alone passes while the other is broken.
    const dot = mountDot(`status-dot ${state}`);
    expect(dot.getAnimations(), `a ${state} mark must not beat`).toEqual([]);
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(1, 3);
  });
});

// ONE MARK RULE AT EVERY TIER, which is what the deleted phone block bought.
//
// The rendered half and the source half are both needed and neither is sufficient.
// A rendered size at both pointer tiers cannot see a rule sitting inside a
// width-keyed at-rule (the browser project's viewport is fixed at 1280px, so that
// query never matches under `mountAppCSS` alone), and a source assertion cannot see
// whether the base rule actually paints an 8px box.
describe("the mark's size is one declaration", () => {
  it.each(["fine", "coarse"])("renders --dot-size on both axes at the %s tier", (tier) => {
    // Impossible to assert before this change: `all: unset` left the unparented
    // fixture `display: inline`, on which width and height do not apply, so the disc
    // rendered only inside a flex container. `display: block` is DECLARED now.
    document.documentElement.dataset["pointer"] = tier;
    try {
      const dot = mountDot("status-dot connected");
      const box = dot.getBoundingClientRect();
      const probe = document.createElement("div");
      probe.style.setProperty("inline-size", "var(--dot-size)");
      document.body.appendChild(probe);
      const token = probe.getBoundingClientRect().width;
      probe.remove();
      expect(token, "--dot-size resolves").toBeGreaterThan(0);
      expect(box.width, `the mark is ${box.width}px wide at the ${tier} tier`).toBeCloseTo(
        token,
        1,
      );
      expect(box.height).toBeCloseTo(token, 1);
    } finally {
      delete document.documentElement.dataset["pointer"];
    }
  });

  it("leaves no status-dot selector inside the width <= 48rem block", () => {
    // A SOURCE assertion about what that block CONTAINS, not an attempt to render
    // it. It is what makes "one mark rule at every tier" checkable, and it is the
    // assertion that fails if someone reintroduces a phone-only mark — the block
    // itself must survive (it still carries the footer's phone `gap`), and
    // `atRuleBody` requires exactly one such block, so a file with none fails here
    // outright rather than passing over nothing.
    const body = atRuleBody(loadCSS("10-shell-app.css"), "width <= 48rem");
    expect(body).toContain(".sidebar-footer");
    expect(body, "the phone block names no mark selector").not.toContain("status-dot");
  });
});
