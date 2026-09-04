// The per-device localStorage keys, and the sweep a sign-out runs over them.
//
// Three records outlived a sign-out until this landed: the UI-state document, the
// turn folds and the dismissed banners. The snapshot's own record was already
// dropped on both doors, so the surface the perf work added did not add to the
// pile — the pre-existing three were one question with one answer.

import { describe, it, expect, afterEach } from "vitest";
import * as lsKeys from "./ls-keys.js";
import {
  clearDeviceKeys,
  LS_DISMISSED_BANNERS_KEY,
  LS_TURN_FOLDS_KEY,
  LS_UI_STATE_KEY,
} from "./ls-keys.js";

/** Every key this module DECLARES, read off its own exports.
 *
 *  Enumerated rather than listed, which is what makes the case below more than a
 *  copy of the implementation: a fourth `LS_*` key added above and left out of the
 *  sweep turns it red, which is the whole reason the sweep lives in this file. */
function declaredKeys(): string[] {
  const out: string[] = [];
  for (const [name, value] of Object.entries(lsKeys)) {
    if (name.startsWith("LS_") && typeof value === "string") {
      out.push(value);
    }
  }
  return out;
}

afterEach(() => {
  localStorage.clear();
});

describe("clearDeviceKeys", () => {
  it("drops every key this module declares", () => {
    const keys = declaredKeys();
    // The three the sweep exists for, so a rename that breaks the enumeration
    // above cannot leave this case asserting over an empty list.
    expect(keys).toEqual(
      expect.arrayContaining([LS_UI_STATE_KEY, LS_TURN_FOLDS_KEY, LS_DISMISSED_BANNERS_KEY]),
    );
    for (const key of keys) {
      localStorage.setItem(key, '{"held":true}');
    }

    clearDeviceKeys();

    for (const key of keys) {
      expect(localStorage.getItem(key), key).toBeNull();
    }
  });

  it("leaves a key it does not own alone", () => {
    // The theme-init snippet inlined in index.html reads LS_UI_STATE_KEY and
    // nothing else, but this origin is shared with whatever else is stored here.
    localStorage.setItem("vibekit.something-else", "keep me");
    localStorage.setItem(LS_UI_STATE_KEY, "{}");

    clearDeviceKeys();

    expect(localStorage.getItem("vibekit.something-else")).toBe("keep me");
  });

  it("does not throw where storage is denied", () => {
    // A sign-out must complete in a browser that refuses localStorage — Safari in
    // a private window with the quota exhausted is the reachable shape.
    const denied = {
      removeItem: () => {
        throw new DOMException("denied", "SecurityError");
      },
    };
    const original = Reflect.getOwnPropertyDescriptor(globalThis, "localStorage");
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: denied });
    try {
      expect(() => {
        clearDeviceKeys();
      }).not.toThrow();
    } finally {
      if (original !== undefined) {
        Object.defineProperty(globalThis, "localStorage", original);
      }
    }
  });
});
