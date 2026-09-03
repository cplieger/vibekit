// ---------------------------------------------------------------------------
// The cross-language pin for "which pauseReason means a step is waiting on a
// person".
//
// The rule exists twice on purpose and neither copy can go, because they answer
// different questions: the server's `needInputPause` decides whether to
// reconstruct an answerable ask for a run it found already parked (the registry
// is in memory, so a restart loses the question text), and this one decides what
// a reader is told when no ask reached this client at all. What was missing was
// anything holding the two together — a KAS wording change would have
// desynchronised them silently, in either direction.
//
// So both languages run the SAME table, `internal/agent/testdata/
// need_input_pauses.json`, on `internal/chat/testdata/turn_outcomes.json`'s
// pattern. Go's TestNeedInputPauseContract is the other half.
//
// Node placement because the fixture is a disk read. `run-store.ts` is a value
// import here rather than a type-only one, since the predicate is what is under
// test — that module's own imports (a signal, the api client, the generated
// decoders) touch no DOM at load.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { isNeedInputPause } from "./run-store.js";

const FIXTURE_PATH = "../internal/agent/testdata/need_input_pauses.json";

interface PauseFixture {
  cases: { name: string; reason: string; want: boolean }[];
}

describe("the need-input pause contract shared with the Go implementation", () => {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const fx = JSON.parse(raw) as PauseFixture;

  it("carries cases (an empty table would pass forever)", () => {
    expect(fx.cases.length).toBeGreaterThan(0);
  });

  it("carries both verdicts (a predicate returning a constant would pass otherwise)", () => {
    expect(fx.cases.some((c) => c.want)).toBe(true);
    expect(fx.cases.some((c) => !c.want)).toBe(true);
  });

  it.each(fx.cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    expect(isNeedInputPause(c.reason)).toBe(c.want);
  });

  // The client half takes `string | undefined` because a run that never paused
  // carries no reason at all; the Go half is called with a decoded field that is
  // always a string, so this case has no fixture row and is pinned here.
  it("an absent reason is not a question", () => {
    expect(isNeedInputPause(undefined)).toBe(false);
  });
});
