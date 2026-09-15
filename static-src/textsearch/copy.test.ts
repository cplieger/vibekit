// The empty-answer vocabulary and the copy over it: classify decides the ORDER
// once, so a surface only maps its own inputs onto EmptyFacts as data, and the
// three renderers spell each state and each count exactly once for every
// surface. Pure string functions, no DOM, so this runs unchanged in the default
// project.

import { describe, it, expect } from "vitest";
import {
  classify,
  cursorCount,
  emptyNote,
  scanNote,
  type EmptyFacts,
  type EmptyState,
  type Nouns,
  type Tally,
} from "./copy.js";

const FILES: Nouns = {
  match: { one: "match", many: "matches" },
  scanned: { one: "file", many: "files" },
};

const CHATS: Nouns = {
  match: { one: "conversation", many: "conversations" },
  scanned: { one: "conversation", many: "conversations" },
};

/** A complete scan that found nothing: the baseline every other case perturbs. */
const NOTHING: EmptyFacts = { matched: 0, shown: 0, truncated: false };

describe("classify", () => {
  it("answers none only when the scan finished and found nothing", () => {
    expect(classify(NOTHING)).toEqual({ kind: "none" });
    expect(classify({ ...NOTHING, scanned: 173 })).toEqual({ kind: "none" });
  });

  it("answers partial when not everything was read, carrying the count when there is one", () => {
    expect(classify({ ...NOTHING, truncated: true })).toEqual({ kind: "partial" });
    expect(classify({ ...NOTHING, truncated: true, scanned: 173 })).toEqual({
      kind: "partial",
      scanned: 173,
    });
  });

  it("answers withheld when more matched than is shown, counting what is not shown", () => {
    expect(classify({ ...NOTHING, matched: 3 })).toEqual({ kind: "withheld", matched: 3 });
    expect(classify({ ...NOTHING, matched: 1, where: "Agents" })).toEqual({
      kind: "withheld",
      matched: 1,
      where: "Agents",
    });
    expect(classify({ ...NOTHING, matched: 5, shown: 2 })).toEqual({
      kind: "withheld",
      matched: 3,
    });
  });

  it("does not call an answer withheld when everything that matched is shown", () => {
    expect(classify({ ...NOTHING, matched: 4, shown: 4 })).toEqual({ kind: "none" });
  });

  it("answers failed with the retry interval when the source gave one", () => {
    expect(classify({ ...NOTHING, failed: {} })).toEqual({ kind: "failed" });
    expect(classify({ ...NOTHING, failed: { retryAfterS: 37 } })).toEqual({
      kind: "failed",
      retryAfterS: 37,
    });
  });

  it("answers unreadable and tooShort from their own facts", () => {
    expect(classify({ ...NOTHING, unreadable: true })).toEqual({ kind: "unreadable" });
    expect(classify({ ...NOTHING, unreadable: false })).toEqual({ kind: "none" });
    expect(classify({ ...NOTHING, tooShort: 2 })).toEqual({ kind: "tooShort", min: 2 });
  });

  it("orders failed > unreadable > tooShort > withheld > partial > none", () => {
    // The inputs are not exclusive (a registry reply can be truncated with every
    // row filtered; the editor can hold an error beside a short query), so the
    // order is decided here and nowhere else. Each row drops the previous winner
    // and expects the next one.
    const everything: EmptyFacts = {
      failed: { retryAfterS: 1 },
      unreadable: true,
      tooShort: 2,
      matched: 3,
      shown: 0,
      where: "Agents",
      scanned: 4,
      truncated: true,
    };
    const { failed: _f, ...noFailed } = everything;
    const { unreadable: _u, ...noUnreadable } = noFailed;
    const { tooShort: _t, ...noTooShort } = noUnreadable;
    expect(classify(everything).kind).toBe("failed");
    expect(classify(noFailed).kind).toBe("unreadable");
    expect(classify(noTooShort).kind).toBe("withheld");
    expect(classify({ ...noTooShort, matched: 0 }).kind).toBe("partial");
    expect(classify({ ...noTooShort, matched: 0, truncated: false }).kind).toBe("none");
    expect(classify(noUnreadable).kind).toBe("tooShort");
  });
});

describe("emptyNote", () => {
  it("says nothing more than No matches for a complete empty scan", () => {
    expect(emptyNote({ kind: "none" }, FILES)).toBe("No matches");
  });

  it("names the scan's reach for a partial when it has one, and says so without it", () => {
    expect(emptyNote({ kind: "partial", scanned: 173 }, CHATS)).toBe(
      "No matches in 173 conversations; not everything was searched",
    );
    expect(emptyNote({ kind: "partial", scanned: 1 }, FILES)).toBe(
      "No matches in 1 file; not everything was searched",
    );
    expect(emptyNote({ kind: "partial" }, FILES)).toBe("No matches; not everything was searched");
  });

  it("says where a withheld match is when it knows, and only that it is not here otherwise", () => {
    expect(emptyNote({ kind: "withheld", matched: 3 }, FILES)).toBe("3 matched, not shown here");
    expect(emptyNote({ kind: "withheld", matched: 1, where: "Agents" }, FILES)).toBe(
      "0 here, 1 on Agents",
    );
  });

  it("names the retry interval for a failure that carries one", () => {
    expect(emptyNote({ kind: "failed" }, FILES)).toBe("Could not search");
    expect(emptyNote({ kind: "failed", retryAfterS: 37 }, FILES)).toBe(
      "Could not search; try again in 37s",
    );
  });

  it("names the unread subject after the scanned unit", () => {
    expect(emptyNote({ kind: "unreadable" }, FILES)).toBe("File not read");
  });

  it("states the floor for a short query, in the singular at one", () => {
    expect(emptyNote({ kind: "tooShort", min: 2 }, FILES)).toBe("Type at least 2 characters");
    expect(emptyNote({ kind: "tooShort", min: 1 }, FILES)).toBe("Type at least 1 character");
  });

  it("renders every member of the union", () => {
    // The renderer is a Record over EmptyState["kind"], so a seventh member fails
    // the type check; this is the runtime half, that no member renders empty.
    const states: EmptyState[] = [
      { kind: "none" },
      { kind: "partial" },
      { kind: "withheld", matched: 1 },
      { kind: "failed" },
      { kind: "unreadable" },
      { kind: "tooShort", min: 2 },
    ];
    const sentences = states.map((s) => emptyNote(s, FILES));
    expect(new Set(sentences).size).toBe(states.length);
    for (const sentence of sentences) {
      expect(sentence).not.toBe("");
    }
  });
});

describe("cursorCount", () => {
  it("reads the cursor over the navigable list", () => {
    expect(cursorCount(1, 3)).toBe("1 of 3");
    expect(cursorCount(12, 12)).toBe("12 of 12");
  });

  it("adds the whole-chat count whenever it is passed, equal or not", () => {
    // `3 of 12 · 347 in chat` when the list is the marks or the stepped hits and
    // the server counted more. The function does not suppress an equal figure or
    // a smaller one: the DOM holding more marks than the server counted is a
    // discrepancy worth seeing, and whether an equal pair is worth two figures is
    // the caller's decision, made by passing the third argument or not.
    expect(cursorCount(3, 12, 347)).toBe("3 of 12 · 347 in chat");
    expect(cursorCount(1, 200, 347)).toBe("1 of 200 · 347 in chat");
    expect(cursorCount(3, 12, 12)).toBe("3 of 12 · 12 in chat");
    expect(cursorCount(3, 12, 8)).toBe("3 of 12 · 8 in chat");
  });
});

describe("scanNote", () => {
  const cut: Tally = { scanned: 1204, matched: 340, truncated: true };

  it("states the cut, the reach and the short read together", () => {
    expect(scanNote(cut, 12, FILES)).toBe(
      "12 of 340 matches shown; 1,204 files scanned, not everything was read",
    );
  });

  it("drops the cut clause when every match is shown and the read clause when everything was read", () => {
    expect(scanNote({ ...cut, truncated: false }, 340, FILES)).toBe(
      "340 matches; 1,204 files scanned",
    );
    expect(scanNote({ ...cut, truncated: false }, 12, FILES)).toBe(
      "12 of 340 matches shown; 1,204 files scanned",
    );
    expect(scanNote(cut, 340, FILES)).toBe(
      "340 matches; 1,204 files scanned, not everything was read",
    );
  });

  it("takes the singular at one, for either noun", () => {
    expect(scanNote({ scanned: 1, matched: 1, truncated: false }, 1, FILES)).toBe(
      "1 match; 1 file scanned",
    );
  });

  it("says the caller's nouns, so a chat scan never says files", () => {
    expect(scanNote({ scanned: 173, matched: 60, truncated: true }, 50, CHATS)).toBe(
      "50 of 60 conversations shown; 173 conversations scanned, not everything was read",
    );
  });
});
