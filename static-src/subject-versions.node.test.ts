// The version map's reporter: what an observed stamp forwards beyond the map, which
// under the worker host is how the host's map comes to hold every version a tab holds.

import { beforeEach, describe, expect, it } from "vitest";

import { observeStamp, _resetForTest, setObserveSink, versionMap } from "./subject-versions.js";

const EPOCH_A = "0123456789abcdef";
const EPOCH_B = "fedcba9876543210";

interface Forwarded {
  kind: string;
  ref: string;
  version: string;
  epoch: string;
}

let forwarded: Forwarded[];

beforeEach(() => {
  _resetForTest();
  forwarded = [];
  setObserveSink((subject, version, epoch) => {
    forwarded.push({ kind: subject.kind, ref: subject.ref, version, epoch });
  });
});

describe("the observe sink", () => {
  it("forwards a REST stamp with its own epoch, which also binds an unbound map", () => {
    observeStamp({ kind: "chats", ref: "", version: "2", epoch: EPOCH_A });
    expect(versionMap().epoch()).toBe(EPOCH_A);
    expect(forwarded).toEqual([{ kind: "chats", ref: "", version: "2", epoch: EPOCH_A }]);
  });

  it("forwards a frame stamp, which carries none, at the epoch the map is bound to", () => {
    versionMap().bind(EPOCH_A);
    observeStamp({ kind: "chat", ref: "c1", version: "4" });
    expect(forwarded).toEqual([{ kind: "chat", ref: "c1", version: "4", epoch: EPOCH_A }]);
  });

  it("forwards nothing the map refused: a foreign-epoch stamp records nothing", () => {
    versionMap().bind(EPOCH_A);
    observeStamp({ kind: "chats", ref: "", version: "9", epoch: EPOCH_B });
    expect(versionMap().snapshot().held).toEqual([]);
    expect(forwarded).toEqual([]);
  });

  it("forwards nothing from an unbound map, which has no epoch to present", () => {
    observeStamp({ kind: "chat", ref: "c1", version: "4" });
    expect(versionMap().snapshot().held).toHaveLength(1);
    expect(forwarded).toEqual([]);
  });

  it("a removed sink and a reset map report nothing", () => {
    versionMap().bind(EPOCH_A);
    setObserveSink(null);
    observeStamp({ kind: "chat", ref: "c1", version: "4" });
    expect(forwarded).toEqual([]);
  });
});
