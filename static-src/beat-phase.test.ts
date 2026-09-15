import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { loadCSS } from "./__test-helpers__/css-rules.js";
import { initBeatPhase, resetBeatPhaseForTest } from "./beat-phase.js";

/** Must match `--dot-beat-dur` (01-tokens.css), which is also
 *  @cplieger/web-terminal-ui's `--dot-beat-dur` — the beat is shared across the two
 *  apps. Declared here because the token stylesheet is not loaded in a unit test,
 *  and the module reads the token off the document rather than restating it — so
 *  this IS the input under test. */
const PERIOD = 1200;

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

  it("re-stamps a beat that genuinely restarted, so a re-seated dot returns to the grid", async () => {
    // The production trigger is a re-seat: `tabs-drag.ts` re-inserts the dragged row on
    // every completed drop, and re-inserting an attached node destroys and recreates every
    // animation in it. This replaced a permanent stamped-once flag, under which such a dot
    // never got a second stamp and sat off the shared grid for the life of the page with
    // nothing able to put it back.
    const first = await beatingDot();
    await new Promise((r) => setTimeout(r, 600));
    const moved = await beatingDot();
    const before = phaseOf(moved);
    expect(before).not.toBe("");

    const parent = moved.parentElement;
    expect(parent).not.toBeNull();
    const restarted = new Promise<void>((res) => {
      moved.addEventListener("animationstart", () => res(), { once: true });
    });
    // Exactly what a drop does: re-insert a node that is already attached.
    parent?.appendChild(moved);
    await restarted;

    expect(phaseOf(moved), "a restarted beat must be re-stamped").not.toBe(before);
    // The user-visible property, read in one instant: back in step with its sibling.
    expect(opacity(moved)).toBeCloseTo(opacity(first), 2);
  });

  it("does not re-stamp on an echoed event, so the write cannot loop", async () => {
    // Measured in Chromium 151: writing the delay on a running beat fires NO
    // `animationstart` and leaves `startTime` untouched, so this echo does not occur here.
    // The guard is keyed on the animation INSTANCE rather than on that measurement,
    // because an engine that did re-fire would otherwise walk the dot off the grid one
    // event at a time — a permanent visual loop. Same animation, same `startTime`, so the
    // stamp must be refused.
    const el = await beatingDot();
    const first = phaseOf(el);
    await new Promise((r) => setTimeout(r, 200));

    el.dispatchEvent(
      new AnimationEvent("animationstart", { animationName: "vk-dot-beat", bubbles: true }),
    );

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

  it("uses the period the stylesheet actually declares", () => {
    // PERIOD above is a TRANSCRIPTION of `--dot-beat-dur`, and every other case in
    // this file feeds it to the module as the input under test — so a token retune
    // that misses it leaves the whole file green while asserting the phase grid of a
    // period nothing runs at. Its comment has warned about that since it was
    // written; this is the warning made mechanical, and it earned its keep on the
    // 2026-09 alignment with web-terminal-kiro, where the two numbers had to move
    // together.
    const declared = /--dot-beat-dur:\s*([\d.]+)(m?s)\s*;/.exec(loadCSS("01-tokens.css"));
    expect(declared, "01-tokens.css declares --dot-beat-dur").not.toBeNull();
    const ms = Number.parseFloat(declared![1]!) * (declared![2] === "ms" ? 1 : 1000);
    expect(ms, "PERIOD matches the declared token").toBe(PERIOD);
  });
});
