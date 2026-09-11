// The freshness verdict is a pure function of two inputs — the kind's event
// coverage and its ledger record against the counter — so the honest truth table
// is small rather than one case per (kind, ledger) pair.
import { join } from "@cplieger/keyenc";
import { describe, it, expect, beforeEach } from "vitest";

import {
  _resetForTest,
  bumpSyncEpoch,
  forgetAllViews,
  forgetView,
  noteLoaded,
  subjectKey,
  syncEpoch,
  viewStale,
} from "./tab-freshness.js";
import type { TabKind } from "./types.js";

/** Every kind whose data has no complete event channel. */
const UNCOVERED: readonly TabKind[] = [
  "editor",
  "run",
  "settings",
  "git",
  "files",
  "history",
  "docs",
];

describe("viewStale", () => {
  beforeEach(() => {
    _resetForTest();
  });

  for (const kind of UNCOVERED) {
    it(`reads STALE for ${kind} even with a record at the current epoch`, () => {
      noteLoaded(kind, "ref", syncEpoch());
      expect(viewStale(kind, "ref")).toBe(true);
    });
  }

  it("reads STALE for a chat with no record", () => {
    expect(viewStale("chat", "c-1")).toBe(true);
  });

  it("reads FRESH for a chat whose record is at the current epoch", () => {
    noteLoaded("chat", "c-1", syncEpoch());
    expect(viewStale("chat", "c-1")).toBe(false);
  });

  it("reads STALE for a chat whose record is at an older epoch", () => {
    noteLoaded("chat", "c-1", syncEpoch());
    bumpSyncEpoch();
    expect(viewStale("chat", "c-1")).toBe(true);
  });

  // Pins the table's reason column rather than its face value: no loader writes a
  // `subagent:` key, so the entry can be `true` and the verdict still fetch.
  it("reads STALE for a subagent despite its covered entry", () => {
    expect(viewStale("subagent", "c-1/task-9")).toBe(true);
  });

  it("keeps one chat's record from answering for another", () => {
    noteLoaded("chat", "c-1", syncEpoch());
    expect(viewStale("chat", "c-2")).toBe(true);
  });
});

describe("the ledger's key", () => {
  beforeEach(() => {
    _resetForTest();
  });

  it("separates a ref holding the separator from the two-part join of its halves", () => {
    expect(subjectKey("editor", "a/b")).not.toBe(join("editor", "a", "b"));
  });

  it("gives two refs two records", () => {
    noteLoaded("chat", "a/b", syncEpoch());
    noteLoaded("chat", "a", syncEpoch());
    forgetView("chat", "a");
    expect(viewStale("chat", "a/b")).toBe(false);
    expect(viewStale("chat", "a")).toBe(true);
  });
});

describe("the epoch", () => {
  beforeEach(() => {
    _resetForTest();
  });

  it("only ever advances", () => {
    const first = syncEpoch();
    bumpSyncEpoch();
    const second = syncEpoch();
    bumpSyncEpoch();
    expect(second).toBeGreaterThan(first);
    expect(syncEpoch()).toBeGreaterThan(second);
  });
});

describe("forgetView", () => {
  beforeEach(() => {
    _resetForTest();
  });

  it("returns a key to the no-record state", () => {
    noteLoaded("chat", "c-1", syncEpoch());
    expect(viewStale("chat", "c-1")).toBe(false);
    forgetView("chat", "c-1");
    expect(viewStale("chat", "c-1")).toBe(true);
  });

  it("is a no-op for a key with no record", () => {
    forgetView("chat", "c-nothing");
    expect(viewStale("chat", "c-nothing")).toBe(true);
  });
});

// A page resume undermines every view rather than only the one on screen, and a record at
// the current epoch reads FRESH forever — so without this an in-app switch back to a
// background chat costs zero fetches and shows the pre-suspension window.
describe("forgetAllViews", () => {
  beforeEach(() => {
    _resetForTest();
  });

  it("makes every recorded view stale", () => {
    noteLoaded("chat", "c-1", syncEpoch());
    noteLoaded("chat", "c-2", syncEpoch());
    expect(viewStale("chat", "c-1")).toBe(false);

    forgetAllViews();

    expect(viewStale("chat", "c-1")).toBe(true);
    expect(viewStale("chat", "c-2")).toBe(true);
  });

  it("leaves the epoch alone, so a load already in flight still counts", () => {
    // The whole difference from `bumpSyncEpoch`: a resume dropped no frames, so an
    // answer that spanned the suspension describes the server's current state and
    // stranding it would buy a second fetch for nothing.
    const before = syncEpoch();
    const epochAtStart = syncEpoch();

    forgetAllViews();
    // The load that was in flight settles and stamps the epoch it went out under.
    noteLoaded("chat", "c-inflight", epochAtStart);

    expect(syncEpoch()).toBe(before);
    expect(viewStale("chat", "c-inflight")).toBe(false);
  });
});
