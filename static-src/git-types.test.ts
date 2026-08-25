// What `git stash push` can actually take, which is not what the tree holds.
//
// The Stash button used to render unconditionally — the only control in the git
// action bar with no state gate, against a documented convention in the same
// function that an action the repo state cannot service does not render at all.
// The gate needs the exact count, not `files.length`: the server runs
// `stash push` with no `-u`, so an untracked file is not stashed, while the
// status parse runs `-uall` and reports it. Gating on the wrong one puts the
// button back on a tree where git answers "No local changes to save".

import { describe, it, expect } from "vitest";
import { stashableCount } from "./git-types.js";
import type { GitFileEntry } from "./git-types.js";

function entry(status: string, staged = false): GitFileEntry {
  return { path: `f-${status}-${String(staged)}.ts`, status, staged, display: status };
}

describe("stashableCount", () => {
  it("is zero on a clean tree", () => {
    expect(stashableCount([])).toBe(0);
  });

  it("is zero when every change is an untracked file", () => {
    // The case `files.length` gets wrong. Both entries are real changes on disk
    // and neither is going into a stash.
    expect(stashableCount([entry("?"), entry("?")])).toBe(0);
  });

  it("counts tracked changes on both sides of the index", () => {
    // A path modified in the index AND in the worktree arrives as two entries
    // (parse.go appendStatusEntries), and stash takes both.
    expect(stashableCount([entry("M", true), entry("M", false)])).toBe(2);
  });

  it("counts every tracked status letter git status can emit", () => {
    const tracked = ["M", "A", "D", "R", "C", "U"].map((s) => entry(s));
    expect(stashableCount(tracked)).toBe(tracked.length);
  });

  it("counts the tracked changes and ignores the untracked ones beside them", () => {
    expect(stashableCount([entry("?"), entry("M"), entry("?"), entry("A")])).toBe(2);
  });
});
