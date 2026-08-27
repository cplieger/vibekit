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
import {
  stashableCount,
  describeStatus,
  changedPathCount,
  distinctPaths,
  partiallyStagedPaths,
} from "./git-types.js";
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

// --- The status vocabulary ------------------------------------------------
//
// This table mirrors the server's (internal/git/parse.go statusLabels), and it
// was missing two of git's letters. Both are the letters whose meaning is least
// guessable from the character, which is exactly when a reader needs the word.

describe("describeStatus", () => {
  it("names every letter git status --porcelain=v1 can emit", () => {
    const want: Record<string, string> = {
      M: "Modified",
      T: "Typechange",
      A: "Added",
      D: "Deleted",
      R: "Renamed",
      C: "Copied",
      U: "Unmerged",
      "?": "Untracked",
    };
    for (const [letter, word] of Object.entries(want)) {
      expect(describeStatus(letter), `letter ${letter}`).toBe(word);
    }
  });

  it("names a copy, which used to come back as the bare letter", () => {
    expect(describeStatus("C")).not.toBe("C");
  });

  it("names a typechange, which used to come back as the bare letter", () => {
    // ` T`/`T ` is what git reports for a regular file swapped with a symlink.
    expect(describeStatus("T")).not.toBe("T");
  });

  it("falls back to the input for a letter it does not know", () => {
    expect(describeStatus("Z")).toBe("Z");
  });
});

// --- Counting files rather than index sides -------------------------------
//
// One path yields TWO entries when it is staged and then edited again
// (parse.go appendStatusEntries), so an entry count and a file count differ on
// exactly that input. A count a person reads has to be the second: the old
// repo-level "Discard all (N)" used the first and offered to discard "2
// uncommitted changes" for one file.

describe("changedPathCount", () => {
  it("is zero on a clean tree", () => {
    expect(changedPathCount([])).toBe(0);
  });

  it("counts a path once however many sides of the index it sits on", () => {
    expect(changedPathCount([entry("M", true), entry("M", false)])).toBe(2);
    const same: GitFileEntry[] = [
      { path: "p.ts", status: "M", staged: true, display: "Modified" },
      { path: "p.ts", status: "M", staged: false, display: "Modified" },
    ];
    expect(changedPathCount(same)).toBe(1);
  });

  it("differs from the entry count exactly on a partially-staged path", () => {
    const files: GitFileEntry[] = [
      { path: "p.ts", status: "M", staged: true, display: "Modified" },
      { path: "p.ts", status: "M", staged: false, display: "Modified" },
      { path: "q.ts", status: "M", staged: false, display: "Modified" },
    ];
    expect(files).toHaveLength(3);
    expect(changedPathCount(files)).toBe(2);
  });
});

describe("distinctPaths", () => {
  it("keeps first-seen order and drops repeats", () => {
    const files: GitFileEntry[] = [
      { path: "z.ts", status: "M", staged: true, display: "Modified" },
      { path: "a.ts", status: "M", staged: false, display: "Modified" },
      { path: "z.ts", status: "M", staged: false, display: "Modified" },
    ];
    expect(distinctPaths(files)).toEqual(["z.ts", "a.ts"]);
  });

  it("is empty for no entries", () => {
    expect(distinctPaths([])).toEqual([]);
  });
});

describe("partiallyStagedPaths", () => {
  it("finds the path present on both sides of the index", () => {
    const files: GitFileEntry[] = [
      { path: "p.ts", status: "M", staged: true, display: "Modified" },
      { path: "p.ts", status: "M", staged: false, display: "Modified" },
      { path: "q.ts", status: "M", staged: false, display: "Modified" },
      { path: "s.ts", status: "A", staged: true, display: "Added" },
    ];
    expect([...partiallyStagedPaths(files)]).toEqual(["p.ts"]);
  });

  it("is empty when no path repeats", () => {
    expect(partiallyStagedPaths([entry("M", true), entry("M", false)]).size).toBe(0);
  });

  it("is empty on a clean tree", () => {
    expect(partiallyStagedPaths([]).size).toBe(0);
  });
});
