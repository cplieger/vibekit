// @vitest-environment happy-dom
// Property-based tests for conflicts.ts registry eviction invariants.
// Verifies size cap, freshness dedup, and oldest-ts eviction under
// random insert sequences.

import { describe, it, expect, beforeEach } from "vitest";
import fc from "fast-check";
import {
  remember,
  getConflict,
  clearConflicts,
  _resetRegistry,
  _registrySize,
  MAX_PER_CHAT,
  type Conflict,
} from "./conflicts.js";

/** Build a Conflict record from minimal fields. */
function mkConflict(path: string, ts: number): Conflict {
  return {
    path,
    other_chat: "other",
    expected_sha: "aaa",
    actual_sha: "bbb",
    tag: "",
    ts,
  };
}

/** Arbitrary for a small set of chatIDs (1-3 distinct values). */
const arbChatID = fc.constantFrom("chat-a", "chat-b", "chat-c");

/** Arbitrary for file paths (larger set to trigger eviction). */
const arbPath = fc.integer({ min: 0, max: 199 }).map((n) => `file-${n}.ts`);

/** Arbitrary for a single insert operation. */
const arbInsert = fc.record({
  chatID: arbChatID,
  path: arbPath,
  ts: fc.integer({ min: 1, max: 1_000_000 }),
});

describe("conflicts registry property-based tests", () => {
  beforeEach(() => {
    _resetRegistry();
  });

  it("size invariant: registry size never exceeds MAX_PER_CHAT", () => {
    fc.assert(
      fc.property(fc.array(arbInsert, { minLength: 1, maxLength: 500 }), (inserts) => {
        _resetRegistry();
        for (const ins of inserts) {
          remember(ins.chatID, mkConflict(ins.path, ins.ts));
        }
        // Check each chatID's registry size
        for (const chatID of ["chat-a", "chat-b", "chat-c"]) {
          expect(_registrySize(chatID)).toBeLessThanOrEqual(MAX_PER_CHAT);
        }
      }),
      { numRuns: 200 },
    );
  });

  it("freshness invariant: duplicate (chatID, path) retains higher ts", () => {
    fc.assert(
      fc.property(
        arbChatID,
        arbPath,
        fc.array(fc.integer({ min: 1, max: 1_000_000 }), { minLength: 2, maxLength: 50 }),
        (chatID, path, timestamps) => {
          _resetRegistry();
          for (const ts of timestamps) {
            remember(chatID, mkConflict(path, ts));
          }
          const stored = getConflict(chatID, path);
          expect(stored).not.toBeNull();
          // The stored entry must have the maximum timestamp
          const maxTs = Math.max(...timestamps);
          expect(stored!.ts).toBe(maxTs);
        },
      ),
      { numRuns: 200 },
    );
  });

  it("eviction correctness: evicted entry has the lowest ts among chat entries", () => {
    fc.assert(
      fc.property(
        arbChatID,
        // Generate exactly MAX_PER_CHAT + 10 distinct paths with unique timestamps
        fc.array(
          fc.record({
            pathIdx: fc.integer({ min: 0, max: 199 }),
            ts: fc.integer({ min: 1, max: 1_000_000 }),
          }),
          { minLength: MAX_PER_CHAT + 5, maxLength: MAX_PER_CHAT + 20 },
        ),
        (chatID, entries) => {
          _resetRegistry();

          // Track what we inserted: path → latest ts (mirroring freshness logic)
          const expected = new Map<string, number>();
          for (const e of entries) {
            const path = `file-${e.pathIdx}.ts`;
            remember(chatID, mkConflict(path, e.ts));
            const prev = expected.get(path);
            if (prev === undefined || e.ts > prev) {
              expected.set(path, e.ts);
            }
          }

          const size = _registrySize(chatID);
          expect(size).toBeLessThanOrEqual(MAX_PER_CHAT);

          // Every entry still in the registry must have ts >= the minimum
          // ts of any evicted entry. In other words: no entry with a higher
          // ts than a surviving entry was evicted.
          const survivingTimestamps: number[] = [];
          for (const [path] of expected) {
            const stored = getConflict(chatID, path);
            if (stored !== null) {
              survivingTimestamps.push(stored.ts);
            }
          }

          if (survivingTimestamps.length > 0 && size === MAX_PER_CHAT) {
            const minSurviving = Math.min(...survivingTimestamps);
            // Any entry NOT in the registry must have ts <= minSurviving
            // (it was evicted because it was older)
            for (const [path, latestTs] of expected) {
              const stored = getConflict(chatID, path);
              if (stored === null) {
                expect(latestTs).toBeLessThanOrEqual(minSurviving);
              }
            }
          }
        },
      ),
      { numRuns: 100 },
    );
  });

  it("single-entry chat: one insert never triggers eviction", () => {
    fc.assert(
      fc.property(arbChatID, arbPath, fc.integer({ min: 1, max: 1_000_000 }), (chatID, path, ts) => {
        _resetRegistry();
        remember(chatID, mkConflict(path, ts));
        expect(_registrySize(chatID)).toBe(1);
        expect(getConflict(chatID, path)).not.toBeNull();
        expect(getConflict(chatID, path)!.ts).toBe(ts);
      }),
      { numRuns: 100 },
    );
  });

  it("exactly-at-cap: MAX_PER_CHAT distinct paths stay without eviction", () => {
    fc.assert(
      fc.property(
        arbChatID,
        fc.array(fc.integer({ min: 1, max: 1_000_000 }), {
          minLength: MAX_PER_CHAT,
          maxLength: MAX_PER_CHAT,
        }),
        (chatID, timestamps) => {
          _resetRegistry();
          for (let i = 0; i < MAX_PER_CHAT; i++) {
            remember(chatID, mkConflict(`unique-${i}.ts`, timestamps[i]!));
          }
          expect(_registrySize(chatID)).toBe(MAX_PER_CHAT);
          // All entries survive
          for (let i = 0; i < MAX_PER_CHAT; i++) {
            expect(getConflict(chatID, `unique-${i}.ts`)).not.toBeNull();
          }
        },
      ),
      { numRuns: 50 },
    );
  });

  it("clearConflicts removes all entries for a chat", () => {
    fc.assert(
      fc.property(
        fc.array(arbInsert, { minLength: 1, maxLength: 100 }),
        arbChatID,
        (inserts, targetChat) => {
          _resetRegistry();
          for (const ins of inserts) {
            remember(ins.chatID, mkConflict(ins.path, ins.ts));
          }
          clearConflicts(targetChat);
          expect(_registrySize(targetChat)).toBe(0);
        },
      ),
      { numRuns: 100 },
    );
  });

  it("ts tie: equal timestamps do not overwrite (prior wins)", () => {
    fc.assert(
      fc.property(arbChatID, arbPath, fc.integer({ min: 1, max: 1_000_000 }), (chatID, path, ts) => {
        _resetRegistry();
        const first = mkConflict(path, ts);
        const second: Conflict = { ...mkConflict(path, ts), other_chat: "second" };
        remember(chatID, first);
        remember(chatID, second);
        // prior.ts >= c.ts means prior wins — first stays
        const stored = getConflict(chatID, path);
        expect(stored!.other_chat).toBe("other");
      }),
      { numRuns: 100 },
    );
  });
});
