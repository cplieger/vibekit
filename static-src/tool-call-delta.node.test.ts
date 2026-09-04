// The client half of the `tool_call_update` contract, read from the fixture the
// Go BUILDER is driven against
// (internal/translate/streaming_tools_roundtrip_test.go). The server decides what
// a frame carries and this decides what a frame means, in two languages — so a
// case added to one side's own table only would let the two folds disagree on
// exactly that transition, and a reader would see a card missing a diff or
// stuck on a stale status with nothing red anywhere.
//
// `.node` because it reads the filesystem. The path is relative to this file.
import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { foldToolCallDelta } from "./store.js";
import type { ToolCall } from "./types.js";
import type { ToolCallUpdatePayload } from "./wire/types.gen.js";

interface DeltaCase {
  name: string;
  before: ToolCall;
  delta: ToolCallUpdatePayload;
  after: ToolCall;
}

const FIXTURE_PATH = "../internal/translate/testdata/tool_call_delta.json";

describe("the tool_call_update fold, against the contract shared with the server", () => {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  const fx = JSON.parse(raw) as { cases: DeltaCase[] };

  it("carries cases (an empty table would pass forever)", () => {
    expect(fx.cases.length).toBeGreaterThan(0);
  });

  it.each(fx.cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    // `toEqual` rather than `toStrictEqual`: the Go side omits an empty slice and
    // a zero scalar, so the fixture's `after` states only the fields that are
    // set, and an absent optional property must compare equal to an absent one.
    expect(foldToolCallDelta(c.before, c.delta)).toEqual(c.after);
  });

  it("leaves the held value untouched, because a card's signal dedups by identity", () => {
    const before: ToolCall = {
      id: "c1",
      title: "Running",
      kind: "execute",
      status: "in_progress",
      output: "head",
      ts: 1,
    };
    const snapshot = structuredClone(before);
    foldToolCallDelta(before, {
      message_id: "m1",
      tool_call_id: "c1",
      output_delta: "tail",
    });
    expect(before).toEqual(snapshot);
  });
});
