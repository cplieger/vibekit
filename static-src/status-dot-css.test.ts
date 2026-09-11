// The sidebar's connection dot is STILL when the connection is healthy.
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
// Computed rather than read out of the source: the `::after` was removed
// wholesale rather than just its `animation` declaration (`vk-ping` was the only
// thing fading an otherwise opaque green disc at `inset: 0`), and "does the
// pseudo generate at all" is a question only a real cascade answers.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

function mountDot(cls: string): HTMLElement {
  const dot = document.createElement("button");
  dot.className = cls;
  document.body.replaceChildren(dot);
  return dot;
}

describe("the sidebar connection dot", () => {
  it("carries no animation once connected", () => {
    const dot = mountDot("status-dot connected");
    expect(getComputedStyle(dot).animationName).toBe("none");
    expect(dot.getAnimations({ subtree: true })).toHaveLength(0);
  });

  it("paints and animates nothing on its ::after overlay", () => {
    // The ripple lived on the pseudo, so a rule that survived with its animation
    // stripped would leave an opaque green disc covering the dot.
    //
    // The pseudo EXISTS again, and this assertion is narrowed to the property that
    // was ever the defect rather than to the box. `all: unset` on this control
    // resets `min-width`/`min-height` at class specificity, which makes it
    // invisible to the app-wide zero-specificity hit-target floor rather than an
    // override of it — an 8px button at every tier. The mark's size is its
    // meaning, so the TARGET grows past the paint through an `::after` expander
    // (10-shell-app.css). Transparent and inert: no background, no animation.
    const dot = mountDot("status-dot connected");
    const after = getComputedStyle(dot, "::after");
    expect(after.backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(after.animationName).toBe("none");
    expect(dot.getAnimations({ subtree: true })).toHaveLength(0);
  });

  it("grows its hit target past its 8px disc", () => {
    // The whole point of the expander: the painted mark stays 8px while the target
    // reaches the tier's floor. `inset` is negative by half the difference, so the
    // target follows `--hit-floor` with no second declaration to keep in step.
    const dot = mountDot("status-dot connected");
    const inset = getComputedStyle(dot, "::after").insetBlockStart;
    expect(parseFloat(inset)).toBeLessThan(0);
  });

  it("still breathes while connecting, on its own animation", () => {
    // The beat is the dot's OWN animation now, created only while it is unsettled,
    // so an idle app runs none at all. Driven to two known phases rather than
    // sampled over time — deterministic, and it proves the dot follows the beat
    // rather than merely naming it. The numbers are unchanged from the shared-clock
    // era because `vk-dot-beat` leaves `from`/`to` implicit, so they are the
    // element's own opacity and 50% is its declared peak.
    const dot = mountDot("status-dot");
    const beat = dot.getAnimations()[0];
    expect(beat, "an unsettled dot must carry its own beat").toBeDefined();
    beat!.pause();

    beat!.currentTime = 0; // rest -> the element's own opacity
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(1, 2);

    beat!.currentTime = 1200; // half of --dot-beat-dur -> the declared peak
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
    expect(dot.getAnimations(), `a ${state} dot must not beat`).toEqual([]);
    expect(Number(getComputedStyle(dot).opacity)).toBeCloseTo(1, 3);
  });
});
