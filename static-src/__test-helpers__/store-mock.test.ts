// The helper's own drift guard, modelled on scroll-mock.test.ts beside it and
// existing for the same measured reason.
//
// `storeMock` is only useful while it is TOTAL. The hand-rolled factories this
// replaced listed store exports one by one, and when the optimistic mid-turn-steer
// work added five names to store.ts — recordSteerSent, forgetSteer, steerIDFor,
// dropConfirmedSteers, restoreSteers — every one of them failed the WHOLE FILE at
// link time: `SyntaxError: The requested module '/store.ts' does not provide an
// export named 'dropConfirmedSteers'`. Three files, and none of their tests ran.
//
// That failure is at least named. The worse shape is the one scroll-mock.test.ts
// documents: a missing export whose only symptom is "[vitest] There was an error
// when mocking a module", with no file, no export and no import chain, visible
// ONLY in a full-suite run. Which of the two a drift produces depends on where in
// the graph the importer sits, so neither is a thing to rely on noticing.
//
// The helper's header comment already asks a writer to add new exports here. That
// comment is not a mechanism; this file is.
import { describe, it, expect } from "vitest";

import { storeMock } from "./store-mock.js";
import * as store from "../store.js";
import type { Session } from "../types.js";

// Types are erased at runtime, so both sides compare the VALUE surface — exactly
// what an ESM link needs to resolve, which is what the mock stands in for. So
// `TabDotState` is correctly absent from both lists.
const real = Object.keys(store as Record<string, unknown>).sort();
const mocked = Object.keys(storeMock).sort();

describe("the store.js mock helper stays total", () => {
  it("names every value store.ts exports", () => {
    expect(real.length, "the real module exported nothing; the import is wrong").toBeGreaterThan(0);
    const missing = real.filter((k) => !mocked.includes(k));
    expect(missing, `store-mock.ts is missing: ${missing.join(", ")}`).toEqual([]);
  });

  it("names nothing store.ts does not export, so a rename cannot hide behind it", () => {
    const extra = mocked.filter((k) => !real.includes(k));
    expect(extra, `store-mock.ts has stale entries: ${extra.join(", ")}`).toEqual([]);
  });
});

// A helper spread by every mocked consumer cannot reach a factory's `importOriginal`,
// so its handful of pure entries are hand-written copies of production rules. Presence
// says nothing about them: a rule that gains a term leaves every copy asserting the
// old one with both suites green. This is the behaviour half of the guard.
describe("the mock's re-derived rules agree with the real ones", () => {
  function session(fields: Partial<Session>): Session {
    return { id: "c1", messages: [], ...fields } as unknown as Session;
  }

  it("derivedHasMore", () => {
    for (const [count, resident] of [
      [0, 0],
      [3, 3],
      [8, 3],
      [3, 8],
    ] as const) {
      expect(storeMock.derivedHasMore(count, resident)).toBe(store.derivedHasMore(count, resident));
    }
  });

  it("turnBaseOf", () => {
    for (const s of [
      session({}),
      session({ turn_offset: 0, turn_segment_closed: false }),
      session({ turn_offset: 12, turn_segment_closed: true }),
    ]) {
      expect(storeMock.turnBaseOf(s)).toEqual(store.turnBaseOf(s));
    }
  });

  it("turnLive", () => {
    for (const s of [
      session({ thinking: false }),
      session({ thinking: true }),
      session({ thinking: false, turn_open: true }),
      // The boot snapshot's row, which states neither input. Without it this guard
      // cannot see a drift in the third term.
      session({ thinking: false, provisional: true }),
    ]) {
      expect(storeMock.turnLive(s)).toBe(store.turnLive(s));
    }
  });

  it("steerIDFor", () => {
    for (const id of ["m-abc", "m-1-2"]) {
      expect(storeMock.steerIDFor(id)).toBe(store.steerIDFor(id));
    }
  });
});
