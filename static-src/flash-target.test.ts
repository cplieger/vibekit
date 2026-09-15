// ---------------------------------------------------------------------------
// Tests for flash-target.ts: the scroll-and-mark primitive two deep links share.
//
// TWO halves, and the second is the one that cannot be written any other way.
//
// The BEHAVIOURAL half moved here with the code from settings-highlight.test.ts:
// the mechanism has exactly two jobs — land on the target and mark it — and
// exactly one failure mode that matters, doing something loud when the target
// never resolves.
//
// The CSS half is the ONLY evidence for the move of `deep-link-flash` into
// css/30-utilities.css. `css-cascade-audit.py` structurally cannot see it: it
// pairs two rules only when their selectors share a class or id TOKEN, and
// `.deep-link-flash` and `.git-pr-row` are two classes on ONE element rather than
// two selectors over one class, so the pair is never emitted in either state and
// with or without `--loose`. What decides the question is what Chromium PAINTS,
// which is also a stronger reading — it resolves values a source audit could not
// see anyway.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach, beforeAll, afterAll } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import { cdp } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { flashTarget } from "./flash-target.js";

/** Drive the rAF retry loop the module uses to wait for a laid-out target. */
async function frames(n = 3): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
}

let scrolled: string[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  scrolled = [];
  document.body.innerHTML = "";
  Element.prototype.scrollIntoView = function (this: Element) {
    scrolled.push(this.id);
  };
});

function control(id: string): HTMLInputElement {
  const e = document.createElement("input");
  e.id = id;
  e.type = "checkbox";
  document.body.appendChild(e);
  return e;
}

/** The one argument the primitive takes: resolve this id, or answer null. */
function byId(id: string): () => HTMLElement | null {
  return () => document.getElementById(id);
}

describe("flashTarget", { timeout: testTimeoutFor(framesBudgetMs(25)) }, () => {
  it("scrolls the target into view and flashes a ring", async () => {
    const box = control("security-profile-list");
    flashTarget(byId("security-profile-list"));
    await frames(1);
    expect(scrolled).toEqual(["security-profile-list"]);
    expect(box.classList.contains("deep-link-flash")).toBe(true);
  });

  it("drops the flash class when the animation ends", async () => {
    const box = control("flag-tool-search");
    flashTarget(byId("flag-tool-search"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);
    box.dispatchEvent(new Event("animationend"));
    expect(box.classList.contains("deep-link-flash")).toBe(false);
  });

  // The quiet-degradation contract. A target that will never resolve must not
  // throw, must not scroll anything, and must not leave the retry loop running:
  // callers name targets that can be renamed out from under them, and a jump that
  // merely fails to find one element is a better outcome than an error.
  it("does nothing for a target that never resolves", async () => {
    control("real-control");
    expect(() => {
      flashTarget(byId("no-such-control"));
    }).not.toThrow();
    await frames(25);
    expect(scrolled).toEqual([]);
    expect(document.querySelectorAll(".deep-link-flash")).toHaveLength(0);
  });

  // The Tools / Permissions / Instructions panels populate from an async fetch and
  // a PR section arrives with its own paint, so a target can land several frames
  // after the request. The retry is what makes the link work in that window
  // instead of silently missing.
  it("waits for a target that does not exist yet", async () => {
    flashTarget(byId("late-control"));
    await frames(2);
    expect(scrolled).toEqual([]);

    control("late-control");
    await frames(2);
    expect(scrolled).toEqual(["late-control"]);
  });

  // The BUDGET, not just the retry: MAX_FRAMES is 20, so a target arriving after
  // it is not scrolled to. Without a bound an id that will never exist costs a
  // frame forever.
  it("stops looking once the frame budget is spent", async () => {
    flashTarget(byId("very-late-control"));
    await frames(25);
    control("very-late-control");
    await frames(3);
    expect(scrolled).toEqual([]);
  });

  // An element inside a `.hidden` panel or a collapsed disclosure has no box, and
  // scrollIntoView on it is a silent no-op — so "present in the DOM" is not enough
  // to jump to.
  it("waits for a present target that has no layout box", async () => {
    const box = document.createElement("input");
    box.id = "boxless-control";
    box.style.display = "none";
    document.body.appendChild(box);

    flashTarget(byId("boxless-control"));
    await frames(2);
    expect(scrolled).toEqual([]);

    box.style.display = "";
    await frames(2);
    expect(scrolled).toEqual(["boxless-control"]);
  });

  it("restarts the flash when the same target is asked for twice", async () => {
    const box = control("diagnostics-run");
    flashTarget(byId("diagnostics-run"));
    await frames(1);
    box.dispatchEvent(new Event("animationend"));
    expect(box.classList.contains("deep-link-flash")).toBe(false);
    flashTarget(byId("diagnostics-run"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);
  });
});

// The flash has two cleanup paths — an animationend listener and a 2.5s backstop
// timeout — and under `prefers-reduced-motion` the timeout is the ONLY one, since
// the animation is suppressed and animationend never fires. No animationend is
// dispatched below, so this is that path.
describe("the reduced-motion backstop", () => {
  beforeEach(() => {
    // setTimeout only: the retry loop runs on requestAnimationFrame, which
    // frames() awaits for real.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("does not let an older deadline clear a newer flash", async () => {
    const box = control("chat-retention-days");
    flashTarget(byId("chat-retention-days"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);

    // Re-flash while the first deadline is still counting down.
    vi.advanceTimersByTime(1000);
    flashTarget(byId("chat-retention-days"));
    await frames(1);
    expect(box.classList.contains("deep-link-flash")).toBe(true);

    // Past the FIRST flash's deadline and inside the second's: the older timer
    // used to strip the ring here, 1.5s early.
    vi.advanceTimersByTime(1600);
    expect(box.classList.contains("deep-link-flash")).toBe(true);

    // The newer deadline still clears it, so nothing is left flashing forever.
    vi.advanceTimersByTime(1000);
    expect(box.classList.contains("deep-link-flash")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The five computed reads. REQUIRED, per the paragraph at the top of this file.
// ---------------------------------------------------------------------------

/** 15% of the 1.6s keyframes: the stop where both animated properties reach the
 *  value the rule declares. Driven rather than slept for, so the read is
 *  deterministic — the outcome-mark.test.ts / tab-dot.test.ts pattern. */
const MID_MS = 0.15 * 1600;

const canvas = document.createElement("canvas");
canvas.width = 1;
canvas.height = 1;
const ctx = canvas.getContext("2d", { willReadFrequently: true });

/** The sRGB bytes Chromium paints for a computed colour string. Required because
 *  `getComputedStyle` hands back the authored `oklch()` / `color-mix()` form for a
 *  token-driven colour, which cannot be compared numerically. */
function bytes(colour: string): [number, number, number, number] {
  if (ctx === null) {
    throw new Error("no 2d context");
  }
  ctx.clearRect(0, 0, 1, 1);
  ctx.fillStyle = "#000000";
  ctx.fillStyle = colour;
  // A string the parser rejects leaves fillStyle at its previous value, so a
  // typo would silently measure the last colour instead of failing.
  expect(ctx.fillStyle, `Chromium parses ${colour}`).not.toBe("#000000");
  ctx.fillRect(0, 0, 1, 1);
  const d = ctx.getImageData(0, 0, 1, 1).data;
  return [d[0] ?? 0, d[1] ?? 0, d[2] ?? 0, d[3] ?? 0];
}

/** An interpolated colour lands within a byte of the value it interpolates to. */
function expectSameColour(got: string, want: string, what: string): void {
  const a = bytes(got);
  const b = bytes(want);
  for (let i = 0; i < 4; i++) {
    expect(Math.abs((a[i] ?? 0) - (b[i] ?? 0)), `${what}: ${got} vs ${want}`).toBeLessThanOrEqual(
      2,
    );
  }
}

/** A token's own resolved value, read off a probe rather than restated here. */
function probe(declaration: string, property: string): string {
  const p = document.createElement("div");
  p.setAttribute("style", declaration);
  document.body.appendChild(p);
  const value = getComputedStyle(p).getPropertyValue(property);
  p.remove();
  return value;
}

describe("the deep-link-flash rules as rendered", () => {
  let style: HTMLStyleElement;
  const host = document.createElement("div");

  beforeAll(() => {
    style = mountAppCSS();
    host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
    document.body.appendChild(host);
  });

  afterAll(() => {
    style.remove();
    host.remove();
    document.documentElement.removeAttribute("data-theme");
  });

  // The behavioural half above empties `document.body`, which DETACHES this host —
  // and a detached element computes an empty style rather than failing, so every
  // read below would silently compare "" against "". Re-attached rather than
  // recreated, so the reference the closures hold stays the mounted one.
  beforeEach(() => {
    if (!host.isConnected) {
      document.body.appendChild(host);
    }
  });

  afterEach(() => {
    host.replaceChildren();
    document.documentElement.removeAttribute("data-theme");
  });

  /** Dark is the unattributed default; light is the one keyed block. */
  function setTheme(theme: "dark" | "light"): void {
    if (theme === "dark") {
      document.documentElement.removeAttribute("data-theme");
      return;
    }
    document.documentElement.dataset["theme"] = theme;
  }

  /** A PR row as `renderPRRow` builds it. */
  function mountRow(): HTMLElement {
    const list = document.createElement("ul");
    list.className = "git-pr-list";
    const row = document.createElement("li");
    row.className = "git-pr-row";
    row.setAttribute("data-pr", "github:github.com:cplieger/vibekit#42");
    list.appendChild(row);
    host.replaceChildren(list);
    return row;
  }

  /** A bare settings control. A text field rather than a checkbox, because
   *  `02-reset.css` gives every checkbox `border-radius: var(--r-sm)` already, so a
   *  checkbox cannot tell the moved declaration from its own. */
  function mountControl(): HTMLElement {
    const ctl = document.createElement("input");
    ctl.type = "text";
    host.replaceChildren(ctl);
    return ctl;
  }

  /** Seek the element's flash to the keyframe stop and hand back its computed style. */
  function midAnimation(el: HTMLElement): CSSStyleDeclaration {
    el.classList.add("deep-link-flash");
    const [anim] = el.getAnimations();
    expect(anim, "the flash class starts an animation").not.toBeUndefined();
    anim?.pause();
    if (anim !== undefined) {
      anim.currentTime = MID_MS;
    }
    return getComputedStyle(el);
  }

  /** The two element families × the two themes. ONE element per case, deliberately:
   *  measured in this file, a second `classList.add` plus an immediate
   *  `getComputedStyle` in the SAME task reads the class rules as not applied to the
   *  second element — the first element reads correctly and swapping the pair swaps
   *  which one is wrong. So a loop over both families inside one case reports a
   *  false red for whichever it reads second, and it is also the shape that names
   *  two things in one failure. */
  const CASES = [
    { family: "a PR row", theme: "dark", mount: mountRow },
    { family: "a PR row", theme: "light", mount: mountRow },
    { family: "a settings control", theme: "dark", mount: mountControl },
    { family: "a settings control", theme: "light", mount: mountControl },
  ] as const;

  it("does not shrink a flashed PR row's corners", () => {
    const row = mountRow();
    const unflashed = getComputedStyle(row).borderTopLeftRadius;
    // The PREMISE: the row has a radius of its own, so there is something for the
    // utility to have overridden.
    expect(unflashed).toBe(probe("border-radius: var(--r)", "border-top-left-radius"));

    row.classList.add("deep-link-flash");
    expect(getComputedStyle(row).borderTopLeftRadius).toBe(unflashed);
  });

  it("gives a bare settings control the radius the declaration was written for", () => {
    const ctl = mountControl();
    // The other PREMISE: this control carries no radius of its own, so the utility
    // is the only thing that can supply one.
    expect(getComputedStyle(ctl).borderTopLeftRadius).toBe("0px");

    ctl.classList.add("deep-link-flash");
    expect(getComputedStyle(ctl).borderTopLeftRadius).toBe(
      probe("border-radius: var(--r-sm)", "border-top-left-radius"),
    );
  });

  it.each(CASES)("rings $family in the accent ($theme)", ({ theme, mount }) => {
    // The mid-animation reads mean nothing if the animation is suppressed.
    expect(matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(false);
    setTheme(theme);
    const accent = probe("color: var(--c-accent)", "color");
    const outline = midAnimation(mount()).outlineColor;
    expect(bytes(outline)[3], "the ring is not transparent").toBeGreaterThan(0);
    expectSameColour(outline, accent, "the ring");
  });

  it.each(CASES)("washes $family mid-flash ($theme)", ({ theme, mount }) => {
    setTheme(theme);
    const selected = probe("color: var(--c-selected-bg)", "color");
    // The animation origin outranks every normal author declaration, which is what
    // makes this true of the PR row too — it declares its own `background`.
    expectSameColour(midAnimation(mount()).backgroundColor, selected, "the wash");
  });

  it.each(CASES)(
    "leaves $family a steady ring and no animation under reduced motion ($theme)",
    async ({ theme, mount }) => {
      // The preference is EMULATED rather than stood in for by a rewritten sheet, so
      // the override is judged from its real position in MANIFEST order under the real
      // query. Chromium reports no-preference otherwise, and there is no way to move
      // that from the page itself.
      await cdp().send("Emulation.setEmulatedMedia", {
        features: [{ name: "prefers-reduced-motion", value: "reduce" }],
      });
      try {
        expect(matchMedia("(prefers-reduced-motion: reduce)").matches).toBe(true);
        setTheme(theme);
        const accent = probe("color: var(--c-accent)", "color");
        const el = mount();
        el.classList.add("deep-link-flash");
        const cs = getComputedStyle(el);
        expect(cs.animationName, "nothing animates").toBe("none");
        expectSameColour(cs.outlineColor, accent, "the steady ring");
      } finally {
        await cdp().send("Emulation.setEmulatedMedia", { features: [] });
      }
    },
  );
});
