import { describe, it, expect, beforeEach, afterEach } from "vitest";

import { initPointerTier, currentTier, seedTier } from "./pointer-tier.js";
import { cachePointerTier } from "./device-view.js";

/** A real PointerEvent carrying a `pointerType`, which is the only field the
 *  module reads. happy-dom's PointerEvent honours the init dict, so this is the
 *  same object shape the browser delivers rather than a stub of one. */
function pointer(type: string, name = "pointerdown"): void {
  globalThis.dispatchEvent(new PointerEvent(name, { bubbles: true, pointerType: type }));
}

describe("the pointer tier", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-pointer");
  });

  afterEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-pointer");
  });

  it("seeds fine when nothing is cached and no touch is reported", () => {
    // happy-dom reports no maxTouchPoints and matchMedia answers false, which is
    // the mouse-desktop case.
    expect(seedTier()).toBe("fine");
  });

  it("prefers the cached observation over the capability guess", () => {
    // The whole reason the value is persisted: a returning device paints its own
    // control sizes on the first frame instead of rendering compact and jumping.
    cachePointerTier("coarse");
    expect(seedTier()).toBe("coarse");
  });

  it("applies the seed to the document element on init", () => {
    initPointerTier();
    expect(currentTier()).toBe("fine");
    expect(document.documentElement.dataset["pointer"]).toBe("fine");
  });

  it("switches to coarse on a touch and back to fine on a mouse move", () => {
    initPointerTier();

    pointer("touch");
    expect(currentTier()).toBe("coarse");

    // `pointermove` and not just `pointerdown`: a hybrid device has to return to
    // the compact tier when the person picks the mouse back up, and a down-only
    // listener could only manage that on their next click.
    pointer("mouse", "pointermove");
    expect(currentTier()).toBe("fine");
  });

  it("treats a pen as coarse", () => {
    // A stylus on a touchscreen has no hover and wants finger-sized affordances,
    // whatever its pixel precision.
    initPointerTier();
    pointer("pen");
    expect(currentTier()).toBe("coarse");
  });

  it("persists the observed tier so the next load paints it", () => {
    initPointerTier();
    pointer("touch");

    const raw = localStorage.getItem("vibekit.ui-state");
    expect(raw, "the tier must reach the device blob").not.toBeNull();
    expect(JSON.parse(raw ?? "{}")).toMatchObject({ pointer: "coarse" });
  });

  it("keeps the theme in the blob it shares", () => {
    // `device-view.ts` is the ONE owner of this key precisely because every write
    // is a read-modify-write of one JSON object, so a writer that forgets to merge
    // drops a sibling field. The theme is the field with a second reader (the
    // inline pre-paint snippet), so losing it costs a wrong-theme flash.
    localStorage.setItem("vibekit.ui-state", JSON.stringify({ theme: "dark" }));
    initPointerTier();
    pointer("touch");

    expect(JSON.parse(localStorage.getItem("vibekit.ui-state") ?? "{}")).toMatchObject({
      theme: "dark",
      pointer: "coarse",
    });
  });
});
