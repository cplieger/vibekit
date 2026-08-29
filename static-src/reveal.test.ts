// ---------------------------------------------------------------------------
// reveal.ts — the constant-latency reveal cursor.
//
// The subject is TIME, so the clock is injected and every test drives it frame
// by frame. `schedule`/`cancel` stand in for requestAnimationFrame; a frame runs
// only if the controller asked for one, which is what makes "the loop stopped"
// an assertion rather than a wait.
// ---------------------------------------------------------------------------

import { describe, expect, it } from "vitest";
import fc from "fast-check";
import { createReveal } from "./reveal.js";

const FRAME_MS = 1000 / 60;

interface Harness {
  append(delta: string): void;
  setText(full: string): void;
  finishNow(): void;
  readonly idle: boolean;
  /** Run one frame. Returns false when none was pending. */
  frame(ms?: number): boolean;
  /** Run up to `n` frames, stopping early once the loop stops. */
  frames(n: number, ms?: number): void;
  readonly running: boolean;
  readonly writes: readonly string[];
  readonly idles: number;
  /** Everything written out so far, concatenated. */
  text(): string;
}

function harness(initial = ""): Harness {
  const writes: string[] = [];
  let idles = 0;
  let pending: ((t: number) => void) | null = null;
  let now = 5000; // any positive origin; rAF timestamps are never 0

  const r = createReveal(initial, {
    onWrite: (d) => writes.push(d),
    onIdle: () => {
      idles += 1;
    },
    schedule: (fn) => {
      pending = fn;
      return 1;
    },
    cancel: () => {
      pending = null;
    },
  });

  const frame = (ms: number = FRAME_MS): boolean => {
    const fn = pending;
    if (fn === null) {
      return false;
    }
    pending = null;
    now += ms;
    fn(now);
    return true;
  };

  return {
    append: (delta) => {
      r.append(delta);
    },
    setText: (full) => {
      r.setText(full);
    },
    finishNow: () => {
      r.finishNow();
    },
    get idle() {
      return r.idle;
    },
    frame,
    frames(n, ms) {
      for (let i = 0; i < n; i++) {
        if (!frame(ms)) {
          return;
        }
      }
    },
    get running() {
      return pending !== null;
    },
    writes,
    get idles() {
      return idles;
    },
    text: () => writes.join(""),
  };
}

describe("createReveal", () => {
  it("never writes the text it was constructed with", () => {
    // A mid-turn connect arrives holding the transcript so far, and a repaint
    // remounts a bubble around text already on screen. Neither is a token
    // arriving now, so neither is revealed — the caller paints it directly.
    const h = harness("everything that already happened");
    expect(h.writes).toHaveLength(0);
    expect(h.idle).toBe(true);
    expect(h.running).toBe(false);
  });

  it("reveals growth over many frames rather than in one write", () => {
    const h = harness();
    const body = "x".repeat(400);
    h.append(body);
    h.frames(4);
    // Something has landed, and it is nowhere near all of it.
    expect(h.text().length).toBeGreaterThan(0);
    expect(h.text().length).toBeLessThan(body.length);
    expect(h.writes.length).toBeGreaterThan(1);
  });

  it("delivers every character exactly once, in order", () => {
    // The correctness invariant the whole feature rests on: the parser
    // downstream is append-only, so a dropped, doubled or reordered slice is
    // unrecoverable. Driven through setText deliberately — the resync path,
    // where a caller re-publishes the full text over an initial prefix.
    const h = harness("head. ");
    const body = "head. " + "The quick brown fox jumps over the lazy dog. ".repeat(12);
    h.setText(body);
    h.frames(400);
    expect(h.text()).toBe(body.slice("head. ".length));
    expect(h.running).toBe(false);
    expect(h.idle).toBe(true);
  });

  it("reproduces the exact text over any chunk split and frame timing", () => {
    // The same exactly-once invariant, over the live path: whatever sizes the
    // chunks arrive in and however frames interleave with them, concatenating
    // the emitted slices is the concatenation of the appended deltas.
    fc.assert(
      fc.property(
        fc.array(fc.string(), { maxLength: 30 }),
        fc.array(fc.nat({ max: 4 }), { maxLength: 30 }),
        (chunks, frameCounts) => {
          const h = harness();
          chunks.forEach((chunk, i) => {
            h.append(chunk);
            h.frames(frameCounts[i] ?? 0);
          });
          h.finishNow();
          expect(h.text()).toBe(chunks.join(""));
          expect(h.idle).toBe(true);
        },
      ),
    );
  });

  it("ignores an empty delta rather than waking the loop", () => {
    // An idle cursor handed nothing must not schedule a frame: that frame has
    // nothing to write and its exit announces a spurious idle to the caller.
    const h = harness("settled");
    h.append("");
    expect(h.running).toBe(false);
    expect(h.writes).toHaveLength(0);
  });

  it("keeps flowing after the last growth, then stops itself", () => {
    // The standing backlog is what bridges an inter-burst gap: the reveal is
    // still behind the live edge when the bursts stop, so it must keep writing
    // rather than freeze and surge on the next one.
    const h = harness();
    h.append("y".repeat(300));
    h.frames(3);
    const atGap = h.text().length;
    expect(h.running).toBe(true);
    h.frames(6);
    expect(h.text().length).toBeGreaterThan(atGap);
    h.frames(400);
    expect(h.running).toBe(false);
    expect(h.idles).toBe(1);
  });

  it("holds a sliver back rather than spending a write on it", () => {
    // At the floor rate a per-frame write is one character, and every write is a
    // permanent node downstream. Only the final slice may be short.
    const h = harness();
    h.append("z".repeat(40)); // small backlog: the floor rate governs
    h.frames(400);
    const initialWrites = h.writes.slice(0, -1);
    for (const w of initialWrites) {
      expect(w.length).toBeGreaterThanOrEqual(3);
    }
    expect(h.text()).toBe("z".repeat(40));
  });

  it("caps an ordinary burst, so it cascades instead of dumping", () => {
    const h = harness();
    h.append("a".repeat(1000));
    h.frames(10);
    // 10 frames at the 600 chars/sec ceiling is at most ~100 characters, and the
    // slew makes the real figure lower.
    expect(h.text().length).toBeLessThan(200);
  });

  it("clears a large dump sub-linearly, not at the flat cap", () => {
    // A whole code block landing in one chunk. The flat 600 chars/sec ceiling
    // alone would trail for 33 seconds; the backlog/MAX_DRAIN_SECS ceiling takes
    // over and clears it in about 9. Both bounds are asserted, because the
    // interesting failure is in either direction: a regression that drops the
    // escape hatch makes this crawl, and one that drops the cap makes it a dump.
    const h = harness();
    const dump = "b".repeat(20_000);
    h.append(dump);
    h.frames(120); // 2s
    expect(h.text().length).toBeLessThan(dump.length);
    h.frames(700); // ~14s in total, against the ~9.1s measured
    expect(h.text()).toBe(dump);
  });

  it("holds its measured drain-from-cold curve", () => {
    // The table in MAX_DRAIN_SECS's comment, pinned. These are the figures a
    // constant change has to be judged against, so they belong in a test rather
    // than only in prose. Frames, at 60fps, for the whole text to land when all
    // of it arrives at once.
    const budget: [len: number, frames: number][] = [
      [40, 44],
      [200, 59],
      [1000, 131],
      [4000, 319],
      [20_000, 545],
    ];
    for (const [len, expected] of budget) {
      const h = harness();
      h.append("c".repeat(len));
      let frames = 0;
      while (h.running && frames < 2000) {
        h.frame();
        frames += 1;
      }
      expect(h.text()).toHaveLength(len);
      // Tolerance absorbs float drift across engines without absorbing a real
      // change to any of the five constants.
      expect(frames).toBeGreaterThan(expected - 4);
      expect(frames).toBeLessThan(expected + 4);
    }
  });

  it("absorbs a backgrounded tab instead of discharging on refocus", () => {
    // The first frame after a refocus reports a gap of whole seconds. Unclamped,
    // rate × dt would hand over the entire backlog in one write.
    const h = harness();
    h.append("c".repeat(5000));
    h.frames(20); // build up an applied rate
    const before = h.text().length;
    h.frame(30_000); // 30 seconds later
    expect(h.text().length - before).toBeLessThan(1000);
  });

  it("ignores a target that did not grow", () => {
    const h = harness("settled text");
    h.setText("settled text");
    h.setText("short");
    expect(h.running).toBe(false);
    expect(h.writes).toHaveLength(0);
  });

  it("finishNow writes the remainder and stops the loop", () => {
    const h = harness();
    h.append("d".repeat(500));
    h.frames(3);
    expect(h.text().length).toBeLessThan(500);
    h.finishNow();
    expect(h.text()).toBe("d".repeat(500));
    expect(h.running).toBe(false);
    expect(h.idle).toBe(true);
    expect(h.idles).toBe(1);
  });

  it("finishNow on a settled cursor still announces idle", () => {
    // The bubble finalizes from onIdle, so the one-write path and the
    // nothing-to-write path have to agree; otherwise a caret survives a
    // finishNow that happened to have no remainder.
    const h = harness("all of it");
    h.finishNow();
    expect(h.writes).toHaveLength(0);
    expect(h.idles).toBe(1);
  });

  it("accepts growth after it has gone idle", () => {
    // A block can be sealed and then grow again (a late chunk for a bubble whose
    // liveness was misjudged), so the cursor must restart rather than stay shut.
    const h = harness();
    h.append("first part. ");
    h.frames(400);
    expect(h.running).toBe(false);
    h.append("second part.");
    expect(h.running).toBe(true);
    h.frames(400);
    expect(h.text()).toBe("first part. second part.");
    expect(h.idles).toBe(2);
  });

  it("survives a frame pair with no elapsed time", () => {
    // Two ticks inside one millisecond give dt = 0. Nothing may advance, and
    // nothing may divide by it.
    const h = harness();
    h.append("e".repeat(100));
    h.frames(2);
    const before = h.text().length;
    h.frame(0);
    expect(h.text().length).toBe(before);
    h.frames(400);
    expect(h.text()).toBe("e".repeat(100));
  });
});
