import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { initBeatPhase, resetBeatPhaseForTest } from "./beat-phase.js";

/** Must match `--dot-beat-dur` (01-tokens.css). Declared here because the token
 *  stylesheet is not loaded in a unit test, and the module reads the token off the
 *  document rather than restating it — so this IS the input under test. */
const PERIOD = 2400;

/** The animation NAME is the contract with the stylesheet. That the CSS still spells
 *  it this way is pinned separately, by `tab-dot.test.ts` against `03-base.css`. */
const CSS = `
  @keyframes vk-dot-beat { 50% { opacity: var(--beat-peak) } }
  .beat-probe {
    opacity: 1;
    --beat-peak: 0.4;
    animation: vk-dot-beat ${String(PERIOD)}ms linear var(--beat-phase, 0ms) infinite;
  }`;

let style: HTMLStyleElement;
const made: HTMLElement[] = [];

/** Resolves once the module's document-level capture listener has had the event,
 *  which precedes the target phase — so the stamp is already written here. */
async function beatingDot(): Promise<HTMLElement> {
  const el = document.createElement("div");
  el.className = "beat-probe";
  document.body.append(el);
  made.push(el);
  await new Promise<void>((res) => {
    el.addEventListener("animationstart", () => res(), { once: true });
  });
  return el;
}

const phaseOf = (el: HTMLElement): string => el.style.getPropertyValue("--beat-phase");
const opacity = (el: HTMLElement): number => Number(getComputedStyle(el).opacity);

beforeEach(() => {
  style = document.createElement("style");
  style.textContent = CSS;
  document.head.append(style);
  document.documentElement.style.setProperty("--dot-beat-dur", `${String(PERIOD)}ms`);
  initBeatPhase();
});

afterEach(() => {
  resetBeatPhaseForTest();
  style.remove();
  document.documentElement.style.removeProperty("--dot-beat-dur");
  for (const el of made.splice(0)) {
    el.remove();
  }
});

describe("beat phase", () => {
  it("stamps a negative delay inside one period", async () => {
    const el = await beatingDot();
    const ms = Number.parseFloat(phaseOf(el));
    expect(phaseOf(el), "a beating dot must be stamped").not.toBe("");
    // Negative, because it rewinds the animation to the last grid boundary. A
    // positive delay would DEFER the beat instead of phase-shifting it.
    expect(ms).toBeLessThanOrEqual(0);
    expect(ms).toBeGreaterThan(-PERIOD);
  });

  it("puts dots that start at different times on one grid", async () => {
    // The whole mechanism. Two dots ~600ms apart is a quarter of the period, which
    // is well outside the tolerance below — an unstamped pair diverges visibly.
    const first = await beatingDot();
    await new Promise((r) => setTimeout(r, 600));
    const second = await beatingDot();

    // The user-visible property, read in one instant rather than compared as delays.
    expect(opacity(second)).toBeCloseTo(opacity(first), 2);
  });

  it("is what puts them there, not the shared duration", async () => {
    // The negative control, and it has to turn the MODULE off rather than force the
    // property: `stamp` overwrites whatever is there, so an inline `0ms` is aligned
    // like any other dot and the control silently passes. Two other shapes are
    // wrong too — comparing an aligned dot against a single unaligned one lands
    // wherever the wall clock sits (an aligned dot's local time is `now mod period`
    // whatever moment it started), and asserting on delays rather than paint tests
    // the arithmetic instead of the result. With the listener detached, two dots
    // 600ms apart are a quarter period apart, deterministically.
    resetBeatPhaseForTest();
    const first = await beatingDot();
    await new Promise((r) => setTimeout(r, 600));
    const second = await beatingDot();
    expect(phaseOf(first), "the control must be unstamped").toBe("");

    expect(Math.abs(opacity(second) - opacity(first))).toBeGreaterThan(0.05);
  });

  it("stamps an element once, so writing the delay cannot re-stamp it", async () => {
    // Setting `animation-delay` shifts a running animation's timeline, which can
    // fire `animationstart` again. A second stamp would re-anchor the dot to a later
    // moment — off the grid, which is the defect the WeakSet prevents.
    const el = await beatingDot();
    const first = phaseOf(el);

    el.classList.remove("beat-probe");
    void el.offsetWidth;
    await new Promise((r) => setTimeout(r, 300));
    el.classList.add("beat-probe");
    await new Promise<void>((res) => {
      el.addEventListener("animationstart", () => res(), { once: true });
    });

    expect(phaseOf(el)).toBe(first);
  });

  it("leaves the fallback alone when the period is unreadable", async () => {
    // A dot then beats correctly and merely out of step, which is the honest
    // degradation: stamping against a period of 0 would put every dot at 0ms.
    document.documentElement.style.removeProperty("--dot-beat-dur");
    const el = await beatingDot();
    expect(phaseOf(el)).toBe("");
  });

  it("ignores an animation that is not the beat", async () => {
    const other = document.createElement("style");
    other.textContent = `@keyframes not-the-beat { to { opacity: 0.5 } }
      .other-probe { animation: not-the-beat 100ms linear }`;
    document.head.append(other);
    const el = document.createElement("div");
    el.className = "other-probe";
    document.body.append(el);
    made.push(el);
    await new Promise<void>((res) => {
      el.addEventListener("animationstart", () => res(), { once: true });
    });
    other.remove();

    expect(phaseOf(el)).toBe("");
  });
});
