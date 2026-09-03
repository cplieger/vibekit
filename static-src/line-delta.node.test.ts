// The cross-language line-delta contract, read from the fixture the Go
// implementation reads (internal/buffer/linediff_test.go). The turn footer's
// numbers come from Go and the delegate footer's from diff.ts, and both render
// the same component — so the two must agree on every case or one footer lies
// about the same file. Add a case to the fixture, never to one side's table.
//
// `.node` because it reads the filesystem. The path is relative to this file.
import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { lineDelta } from "./diff.js";

interface DeltaCase {
  name: string;
  old: string;
  new: string;
  added: number;
  removed: number;
}

const FIXTURE_PATH = "../internal/buffer/testdata/line_delta.json";

describe("the line-delta contract shared with the Go implementation", () => {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const fx = JSON.parse(raw) as { cases: DeltaCase[] };

  it("carries cases (an empty table would pass forever)", () => {
    expect(fx.cases.length).toBeGreaterThan(0);
  });

  it.each(fx.cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    expect(lineDelta(c.old, c.new)).toEqual({ added: c.added, removed: c.removed });
  });
});
