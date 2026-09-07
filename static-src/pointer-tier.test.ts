// ---------------------------------------------------------------------------
// The tier is decided ONCE per load, and most of this file exists to pin that
// negative: an input event may record what the device is being driven by, and it
// may not move `data-pointer`.
//
// The two cases that used to assert the opposite — a touch switching to coarse, a
// following mouse move switching back — are inverted below rather than deleted,
// because they are the exact behaviour a future reader is most likely to restore
// by "simplifying" the observer into the applier again.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import { initPointerTier, currentTier, resolveTier, setPointerMode } from "./pointer-tier.js";
import {
  cachePointerTier,
  cachedPointerTier,
  coarseEverSeen,
  markCoarseSeen,
  pointerModeChoice,
  setPointerModeChoice,
} from "./device-view.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

/** A real PointerEvent carrying a `pointerType`, which is the only field the
 *  module reads. These run in real Chromium (the browser project in
 *  vitest.config.ts), so this is the object the platform delivers rather than a
 *  stub of one. */
function pointer(type: string, name = "pointerdown"): void {
  globalThis.dispatchEvent(new PointerEvent(name, { bubbles: true, pointerType: type }));
}

function blob(): Record<string, unknown> {
  const raw = localStorage.getItem(LS_UI_STATE_KEY);
  return raw === null ? {} : (JSON.parse(raw) as Record<string, unknown>);
}

function writeBlob(patch: Record<string, unknown>): void {
  localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify({ ...blob(), ...patch }));
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
});

describe("resolveTier", () => {
  it("answers fine when nothing is stored and no touch is reported", () => {
    // Headless Chromium reports no maxTouchPoints and `any-pointer: coarse` false,
    // which is the mouse-desktop case.
    expect(resolveTier()).toBe("fine");
  });

  it("prefers the previous load's observation over the capability guess", () => {
    // The middle rung: a real observation from this device beats a query that is
    // wrong on exactly the hardware the enlarged tier exists for.
    cachePointerTier("coarse");
    expect(resolveTier()).toBe("coarse");
  });

  it("prefers the stated choice over both lower rungs, in either direction", () => {
    // The whole point of the ladder: a reader on a hybrid device can ask for a
    // tier and keep it, whatever the device has been observed doing since.
    setPointerModeChoice("coarse");
    cachePointerTier("fine");
    expect(resolveTier()).toBe("coarse");

    setPointerModeChoice("fine");
    cachePointerTier("coarse");
    expect(resolveTier()).toBe("fine");
  });
});

describe("initPointerTier", () => {
  it("applies the resolution to the document element", () => {
    initPointerTier();
    expect(currentTier()).toBe("fine");
    expect(document.documentElement.dataset["pointer"]).toBe("fine");
  });

  it("does not move the tier on a touch, nor on a following mouse move", () => {
    initPointerTier();

    pointer("touch");
    expect(currentTier(), "a touch records; it does not re-lay-out the app").toBe("fine");

    pointer("mouse", "pointermove");
    expect(currentTier()).toBe("fine");
  });

  it("holds a stored coarse choice against a mouse event", () => {
    // The reported case: a machine with a mouse attached, a reader who asked for
    // the enlarged tier. Before the freeze the first mouse move took it away.
    setPointerModeChoice("coarse");
    initPointerTier();
    expect(currentTier()).toBe("coarse");

    pointer("mouse", "pointermove");
    expect(currentTier()).toBe("coarse");
    pointer("mouse");
    expect(currentTier()).toBe("coarse");
  });

  it("records a pen as coarse without changing the tier", () => {
    // A stylus on a touchscreen has no hover and wants finger-sized affordances,
    // whatever its pixel precision — so it is worth RECORDING as coarse, which is
    // a different claim from re-laying-out the page under the reader.
    initPointerTier();
    pointer("pen");

    expect(currentTier()).toBe("fine");
    expect(cachedPointerTier()).toBe("coarse");
  });

  it("writes the observation while data-pointer stays where it was", () => {
    initPointerTier();
    pointer("touch");

    expect(blob(), "the observation must reach the device blob").toMatchObject({
      pointer: "coarse",
    });
    expect(currentTier()).toBe("fine");
  });

  it("writes nothing on a repeat observation of the tier it already recorded", () => {
    initPointerTier();
    pointer("touch");

    // Take the field away by hand: a second write would put it back, and an
    // in-memory latch is the difference between one compare and a
    // read-modify-write per pointermove.
    const kept = blob();
    delete kept["pointer"];
    localStorage.setItem(LS_UI_STATE_KEY, JSON.stringify(kept));

    pointer("touch");
    pointer("touch", "pointermove");
    expect("pointer" in blob()).toBe(false);
  });

  it("keeps the theme in the blob it shares", () => {
    // `device-view.ts` is the ONE owner of this key precisely because every write
    // is a read-modify-write of one JSON object, so a writer that forgets to merge
    // drops a sibling field. The theme is the field with a second reader (the
    // inline pre-paint snippet), so losing it costs a wrong-theme flash.
    writeBlob({ theme: "dark" });
    initPointerTier();
    pointer("touch");

    expect(blob()).toMatchObject({ theme: "dark", pointer: "coarse" });
  });
});

describe("the sticky coarse-seen flag", () => {
  it("stays unset through a mouse-only load", () => {
    initPointerTier();
    pointer("mouse");
    pointer("mouse", "pointermove");

    expect(coarseEverSeen()).toBe(false);
  });

  it("is set by the first coarse event, which calls onCoarseSeen exactly once", () => {
    const onCoarseSeen = vi.fn();
    initPointerTier({ onCoarseSeen });

    pointer("touch");
    expect(coarseEverSeen()).toBe(true);
    expect(onCoarseSeen).toHaveBeenCalledTimes(1);

    pointer("pen");
    pointer("touch", "pointermove");
    expect(onCoarseSeen).toHaveBeenCalledTimes(1);
  });

  it("is backfilled at init from a stored coarse choice", () => {
    // No migration code: a device that chose the enlarged tier before this flag
    // existed reveals the toggle on its next load.
    setPointerModeChoice("coarse");
    initPointerTier();

    expect(coarseEverSeen()).toBe(true);
  });

  it("is backfilled at init from a stored coarse observation", () => {
    // And the hole this closes: without the backfill the toggle would appear, then
    // vanish on the session's first mouse move once the detector overwrote
    // `pointer` with "fine".
    cachePointerTier("coarse");
    initPointerTier();
    expect(coarseEverSeen()).toBe(true);

    pointer("mouse", "pointermove");
    expect(cachedPointerTier()).toBe("fine");
    expect(coarseEverSeen()).toBe(true);
  });

  it("is NOT set by the capability guess alone", () => {
    // `any-pointer: coarse` says a coarse pointer is AVAILABLE, which is what the
    // Windows-touch-laptop and iPadOS reports in the module header make useless as
    // evidence. The flag answers whether this screen has actually been touched.
    vi.stubGlobal("matchMedia", (query: string) => ({ media: query, matches: true }));
    initPointerTier();

    expect(currentTier(), "the guess still decides the tier").toBe("coarse");
    expect(coarseEverSeen()).toBe(false);
  });

  it("writes nothing at init when the flag is already set", () => {
    // The backfill is a read-modify-write of the whole device blob, so running it
    // unconditionally costs one storage write per load on every touched device for a
    // flag that cannot change back. The observable is the WRITE, not the flag: with
    // the flag already set there is no state change to assert, so the spy goes on the
    // storage INSTANCE (the `Storage.prototype` form is the hollow one), and
    // `restoreMocks` in vitest.config.ts puts it back. Init performs no other write —
    // it applies the attribute, reads the two fields and seeds `recorded` — so
    // `not.toHaveBeenCalled()` is precise rather than approximate.
    setPointerModeChoice("coarse");
    markCoarseSeen();
    const setItem = vi.spyOn(localStorage, "setItem");

    initPointerTier();

    expect(setItem).not.toHaveBeenCalled();
    expect(coarseEverSeen()).toBe(true);
  });
});

describe("setPointerMode", () => {
  it("persists the choice and applies it in the same call", () => {
    initPointerTier();
    expect(currentTier()).toBe("fine");

    setPointerMode("coarse");
    expect(currentTier()).toBe("coarse");
    expect(pointerModeChoice()).toBe("coarse");
  });

  it("the choice survives a re-seed from storage", () => {
    initPointerTier();
    setPointerMode("coarse");

    document.documentElement.removeAttribute("data-pointer");
    initPointerTier();
    expect(currentTier()).toBe("coarse");
  });
});
