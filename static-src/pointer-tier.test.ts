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
  document.documentElement.removeAttribute("data-touched");
});

afterEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.removeAttribute("data-touched");
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

describe("the third tier state, data-touched", () => {
  /** The attribute's presence on <html>. It is a bare attribute (no value), so
   *  presence is the whole signal — 01-tokens.css matches `:root[data-touched]`. */
  function touched(): boolean {
    return document.documentElement.hasAttribute("data-touched");
  }

  it("is stamped at init from a stored coarse observation", () => {
    // The backfill path: a device whose last observed pointer was coarse has been
    // touched here, so the flag lands on the same evidence the sticky flag takes.
    cachePointerTier("coarse");
    initPointerTier();

    expect(touched()).toBe(true);
  });

  it("is stamped on a device being driven by its mouse now", () => {
    // The case it exists for, and the only one no single tier can serve: a hybrid
    // that has been touched before and is on its mouse this load. The tier stays
    // `fine`, so every control keeps the dense layout the pointer asks for, and
    // only --hit-floor moves — a finger still lands a 44px target.
    markCoarseSeen();
    cachePointerTier("fine");
    initPointerTier();

    expect(currentTier(), "the tier itself is untouched by this flag").toBe("fine");
    expect(touched()).toBe(true);
  });

  it("is NOT stamped by the capability guess alone", () => {
    // Same evidence rule the sticky flag follows: `any-pointer: coarse` says a
    // coarse pointer is AVAILABLE, which the Windows-touch-laptop and iPadOS
    // reports in the module header make useless. Raising the floor on a guess
    // would enlarge every target on a desktop that merely has a touchscreen
    // attached.
    vi.stubGlobal("matchMedia", (query: string) => ({ media: query, matches: true }));
    initPointerTier();

    expect(currentTier(), "the guess still decides the tier").toBe("coarse");
    expect(touched()).toBe(false);
  });

  it("is NOT stamped mid-session by a touch after a fine init", () => {
    // The freeze, from this attribute's side. `initPointerTier` is the only writer
    // and it reads a flag written on a PREVIOUS load, so a touch now records for
    // the next load and re-lays out nothing under the reader. The companion
    // negative for `data-pointer` is above ("does not move the tier on a touch").
    initPointerTier();
    expect(touched()).toBe(false);

    pointer("touch");
    expect(coarseEverSeen(), "the touch is still RECORDED for the next load").toBe(true);
    expect(touched(), "and the layout does not move for it").toBe(false);

    document.documentElement.removeAttribute("data-pointer");
    initPointerTier();
    expect(touched(), "the next load is where it lands").toBe(true);
  });

  it("is absent when a fine choice is stored", () => {
    // A stated preference is rung 1 of `resolveTier` and outranks every
    // observation, so a reader who touched once and then PINNED fine asked for the
    // dense layout — a 44px floor would overturn it.
    markCoarseSeen();
    setPointerModeChoice("fine");
    initPointerTier();

    expect(currentTier()).toBe("fine");
    expect(touched()).toBe(false);
  });

  it("is absent when a coarse choice is stored", () => {
    // Not a special case for the same reason, but for a cheaper one: the
    // `[data-pointer="coarse"]` arm declares the identical --hit-floor, so the
    // flag would be a second writer of a value already in force.
    markCoarseSeen();
    setPointerModeChoice("coarse");
    initPointerTier();

    expect(currentTier()).toBe("coarse");
    expect(touched()).toBe(false);
  });

  it("is cleared when the reader pins a tier and re-loads", () => {
    // The flag is written on EVERY init rather than only set, so a choice made
    // during one session takes the floor back on the next load. Without the clear
    // it would be a one-way latch nothing could undo.
    markCoarseSeen();
    initPointerTier();
    expect(touched()).toBe(true);

    setPointerModeChoice("fine");
    initPointerTier();
    expect(touched()).toBe(false);
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
