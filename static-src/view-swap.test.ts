// view-swap.test.ts — the synchronous-swap + one-slot WAAPI entry-fade
// contract (design §A4): the caller's DOM update is final before any animation
// frame, a new swap cancels the previous handle, and the animation — never the
// swap — is skipped under reduced motion, a hidden document, and pre-boot.
//
// Real WAAPI throughout: Browser Mode runs this in Chromium, so getAnimations()
// and elementFromPoint() report the engine's own animation and hit-testing
// state rather than a mock's.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { swapViews, markBootDone, DUR_ENTER_MS, EASE_ENTER, _resetForTest } from "./view-swap.js";
import { loadCSS } from "./__test-helpers__/css-rules.js";

/** A visible block with a real box, appended to the body. */
function view(): HTMLElement {
  const el = document.createElement("div");
  el.style.width = "200px";
  el.style.height = "100px";
  document.body.appendChild(el);
  return el;
}

beforeEach(() => {
  _resetForTest();
});

afterEach(() => {
  // A 250ms entry fade outlives its test; stray animations would leak into the
  // next test's getAnimations() counts.
  for (const a of document.getAnimations()) {
    a.cancel();
  }
  _resetForTest();
  document.body.replaceChildren();
});

describe("swapViews", () => {
  it("applies the swap synchronously — DOM state is final before any animation frame", () => {
    const outgoing = view();
    const incoming = view();
    incoming.style.display = "none";

    swapViews(() => {
      outgoing.style.display = "none";
      incoming.style.display = "block";
      return incoming;
    });

    // Asserted in the same task, before any rAF or microtask can run.
    expect(incoming.style.display).toBe("block");
    expect(outgoing.style.display).toBe("none");
  });

  it("animates the incoming view with the entry duration, easing, and opacity keyframes", () => {
    markBootDone();
    const incoming = view();

    swapViews(() => incoming);

    const anim = incoming.getAnimations()[0];
    expect(anim).toBeDefined();
    const effect = anim!.effect as KeyframeEffect;
    expect(effect.getTiming().duration).toBe(DUR_ENTER_MS);
    expect(effect.getTiming().easing).toBe(EASE_ENTER);
    expect(effect.getKeyframes().map((k) => k["opacity"])).toEqual(["0", "1"]);
  });

  it("a second swap cancels the first swap's animation handle", async () => {
    markBootDone();
    const first = view();
    const second = view();

    swapViews(() => first);
    const firstAnim = first.getAnimations()[0];
    expect(firstAnim).toBeDefined();
    expect(firstAnim!.playState).toBe("running");

    swapViews(() => second);

    // Deterministic replacement: the old handle is cancelled (idle), only the
    // new view animates, and nothing stacked.
    expect(firstAnim!.playState).toBe("idle");
    expect(first.getAnimations()).toHaveLength(0);
    expect(second.getAnimations()).toHaveLength(1);

    // Fire-and-forget: the cancel rejects the first animation's `finished`
    // promise; a tick lets an unswallowed rejection surface, which Browser
    // Mode reports as an unhandled error and fails this test.
    await new Promise((r) => setTimeout(r, 0));
  });

  it("a swap with no incoming element runs the update without animating anything", () => {
    markBootDone();
    let ran = false;

    swapViews(() => {
      ran = true;
    });

    expect(ran).toBe(true);
    expect(document.getAnimations()).toHaveLength(0);
  });

  it("a no-element swap still cancels the previous animation — the new gesture wins now", () => {
    markBootDone();
    const previous = view();
    swapViews(() => previous);
    const anim = previous.getAnimations()[0];
    expect(anim).toBeDefined();

    // The files loadWithTransition shape: the gesture's DOM lands async, so the
    // callback returns nothing — but the gesture must still replace the running
    // entry fade immediately.
    swapViews(() => undefined);

    expect(anim!.playState).toBe("idle");
  });

  it("skips the animation under prefers-reduced-motion; the swap still runs", () => {
    markBootDone();
    const incoming = view();
    incoming.style.display = "none";
    // swapViews re-reads matchMedia per call, so a global stub is the seam.
    vi.stubGlobal("matchMedia", ((query: string) => ({
      matches: true,
      media: query,
    })) as typeof window.matchMedia);

    swapViews(() => {
      incoming.style.display = "block";
      return incoming;
    });

    expect(incoming.style.display).toBe("block");
    expect(incoming.getAnimations()).toHaveLength(0);
  });

  it("skips the animation when the document is hidden; the swap still runs", () => {
    markBootDone();
    const incoming = view();
    // document.hidden is false under headless Chromium and its accessor lives
    // on Document.prototype, so an own-property shadow is the seam; deleting
    // it restores the real accessor.
    Object.defineProperty(document, "hidden", { value: true, configurable: true });
    try {
      swapViews(() => incoming);
      expect(incoming.getAnimations()).toHaveLength(0);
    } finally {
      Reflect.deleteProperty(document, "hidden");
    }
    expect(document.hidden).toBe(false);
  });

  it("skips the animation before boot and animates after markBootDone", () => {
    const early = view();
    swapViews(() => early);
    expect(early.getAnimations()).toHaveLength(0);

    markBootDone();
    const late = view();
    swapViews(() => late);
    expect(late.getAnimations()).toHaveLength(1);
  });

  it("a mid-animation click lands on chrome AND on the incoming view's controls", () => {
    markBootDone();
    // Chrome: a control that is not part of either view.
    const chrome = document.createElement("button");
    chrome.textContent = "chrome";
    chrome.style.position = "fixed";
    chrome.style.bottom = "4px";
    chrome.style.right = "4px";
    document.body.appendChild(chrome);
    let chromeClicks = 0;
    chrome.addEventListener("click", () => {
      chromeClicks++;
    });

    const outgoing = view();
    const incoming = view();
    incoming.style.display = "none";
    const control = document.createElement("button");
    control.textContent = "inside";
    incoming.appendChild(control);
    let controlClicks = 0;
    control.addEventListener("click", () => {
      controlClicks++;
    });

    swapViews(() => {
      outgoing.style.display = "none";
      incoming.style.display = "block";
      return incoming;
    });
    expect(incoming.getAnimations()[0]?.playState).toBe("running");

    // elementFromPoint IS the engine's hit test: under a document view
    // transition the snapshot pseudo-layer would win these lookups (captured
    // content is non-hittable by spec §4.2); under the WAAPI opacity fade the
    // live elements must win them while the fade plays.
    const c = control.getBoundingClientRect();
    const hitControl = document.elementFromPoint(c.x + c.width / 2, c.y + c.height / 2);
    expect(hitControl).toBe(control);
    hitControl?.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    const h = chrome.getBoundingClientRect();
    const hitChrome = document.elementFromPoint(h.x + h.width / 2, h.y + h.height / 2);
    expect(hitChrome).toBe(chrome);
    hitChrome?.dispatchEvent(new MouseEvent("click", { bubbles: true }));

    expect(controlClicks).toBe(1);
    expect(chromeClicks).toBe(1);
    // The timeline cannot advance inside one task, so the fade was live for
    // both hit tests.
    expect(incoming.getAnimations()[0]?.playState).toBe("running");
  });

  it("DUR_ENTER_MS and EASE_ENTER mirror the CSS motion tokens", () => {
    const tokens = loadCSS("01-tokens.css");
    expect(tokens).toContain(`--dur-enter: ${String(DUR_ENTER_MS / 1000)}s;`);
    expect(tokens).toContain(`--ease-enter: ${EASE_ENTER};`);
  });
});
