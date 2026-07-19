// Table-driven tests for conflict.ts — parseConflicts and resolveHunk.
import { describe, it, expect } from "vitest";
import { parseConflicts, resolveHunk, type ConflictFile, type Resolution } from "./conflict.js";

describe("parseConflicts", () => {
  const cases: {
    name: string;
    input: string;
    expectedHunkCount: number;
    check?: (f: ConflictFile) => void;
  }[] = [
    {
      name: "no markers returns empty hunks",
      input: "line1\nline2\nline3\n",
      expectedHunkCount: 0,
    },
    {
      name: "single hunk with labels",
      input: [
        "before",
        "<<<<<<< HEAD",
        "ours line",
        "=======",
        "theirs line",
        ">>>>>>> feature",
        "after",
        "",
      ].join("\n"),
      expectedHunkCount: 1,
      check(f) {
        expect(f.hunks[0]!.ourLabel).toBe("HEAD");
        expect(f.hunks[0]!.theirLabel).toBe("feature");
        expect(f.hunks[0]!.oursLines).toEqual(["ours line"]);
        expect(f.hunks[0]!.theirsLines).toEqual(["theirs line"]);
        expect(f.hunks[0]!.startLine).toBe(1);
        expect(f.hunks[0]!.endLine).toBe(5);
      },
    },
    {
      name: "multiple hunks",
      input: [
        "<<<<<<< HEAD",
        "a",
        "=======",
        "b",
        ">>>>>>> br",
        "mid",
        "<<<<<<< HEAD",
        "c",
        "=======",
        "d",
        ">>>>>>> br2",
        "",
      ].join("\n"),
      expectedHunkCount: 2,
      check(f) {
        expect(f.hunks[0]!.oursLines).toEqual(["a"]);
        expect(f.hunks[1]!.oursLines).toEqual(["c"]);
        expect(f.hunks[1]!.theirLabel).toBe("br2");
      },
    },
    {
      name: "diff3 base section excluded from ours",
      input: [
        "<<<<<<< HEAD",
        "ours",
        "||||||| base",
        "base content",
        "=======",
        "theirs",
        ">>>>>>> feat",
        "",
      ].join("\n"),
      expectedHunkCount: 1,
      check(f) {
        // The base section (ancestor content) belongs to NO side: ours
        // stops at the ||||||| marker so a resolution can never splice
        // the marker line or ancestor lines into the file.
        expect(f.hunks[0]!.oursLines).toEqual(["ours"]);
        expect(f.hunks[0]!.theirsLines).toEqual(["theirs"]);
      },
    },
    {
      name: "diff3 labeled base marker excluded, multi-line base",
      input: [
        "<<<<<<< HEAD",
        "mine 1",
        "mine 2",
        "||||||| merged common ancestors",
        "old 1",
        "old 2",
        "=======",
        "theirs 1",
        ">>>>>>> feature/x",
        "",
      ].join("\n"),
      expectedHunkCount: 1,
      check(f) {
        expect(f.hunks[0]!.oursLines).toEqual(["mine 1", "mine 2"]);
        expect(f.hunks[0]!.theirsLines).toEqual(["theirs 1"]);
      },
    },
    {
      name: "malformed markers (missing separator) skipped",
      input: "<<<<<<< HEAD\nours\n>>>>>>> feat\n",
      expectedHunkCount: 0,
    },
    {
      name: "malformed markers (missing end) skipped",
      input: "<<<<<<< HEAD\nours\n=======\ntheirs\n",
      expectedHunkCount: 0,
    },
    {
      name: "empty ours and theirs",
      input: "<<<<<<< HEAD\n=======\n>>>>>>> feat\n",
      expectedHunkCount: 1,
      check(f) {
        expect(f.hunks[0]!.oursLines).toEqual([]);
        expect(f.hunks[0]!.theirsLines).toEqual([]);
      },
    },
    {
      name: "no label after markers",
      input: "<<<<<<< \nours\n=======\ntheirs\n>>>>>>>\n",
      expectedHunkCount: 1,
      check(f) {
        expect(f.hunks[0]!.ourLabel).toBe("");
        expect(f.hunks[0]!.theirLabel).toBe("");
      },
    },
    {
      name: "trailing newline detection",
      input: "hello\n",
      expectedHunkCount: 0,
      check(f) {
        expect(f.trailingNewline).toBe(true);
        expect(f.lines).toEqual(["hello"]);
      },
    },
    {
      name: "no trailing newline",
      input: "hello",
      expectedHunkCount: 0,
      check(f) {
        expect(f.trailingNewline).toBe(false);
        expect(f.lines).toEqual(["hello"]);
      },
    },
    {
      name: "empty string",
      input: "",
      expectedHunkCount: 0,
      check(f) {
        expect(f.lines).toEqual([]);
        expect(f.trailingNewline).toBe(false);
      },
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const result = parseConflicts(c.input);
      expect(result.hunks).toHaveLength(c.expectedHunkCount);
      c.check?.(result);
    });
  }
});

describe("resolveHunk", () => {
  const base = [
    "before",
    "<<<<<<< HEAD",
    "ours1",
    "ours2",
    "=======",
    "theirs1",
    ">>>>>>> feat",
    "after",
    "",
  ].join("\n");

  const cases: {
    name: string;
    input: string;
    hunkIndex: number;
    resolution: Resolution;
    expected: string;
  }[] = [
    {
      name: "resolve ours keeps our lines",
      input: base,
      hunkIndex: 0,
      resolution: "ours",
      expected: "before\nours1\nours2\nafter\n",
    },
    {
      name: "resolve theirs keeps their lines",
      input: base,
      hunkIndex: 0,
      resolution: "theirs",
      expected: "before\ntheirs1\nafter\n",
    },
    {
      name: "resolve both concatenates ours then theirs",
      input: base,
      hunkIndex: 0,
      resolution: "both",
      expected: "before\nours1\nours2\ntheirs1\nafter\n",
    },
    {
      name: "invalid hunk index returns file unchanged",
      input: base,
      hunkIndex: 99,
      resolution: "ours",
      expected: base,
    },
    {
      name: "resolve first of two hunks",
      input: [
        "<<<<<<< HEAD",
        "a",
        "=======",
        "b",
        ">>>>>>> br",
        "mid",
        "<<<<<<< HEAD",
        "c",
        "=======",
        "d",
        ">>>>>>> br2",
        "",
      ].join("\n"),
      hunkIndex: 0,
      resolution: "theirs",
      expected: "b\nmid\n<<<<<<< HEAD\nc\n=======\nd\n>>>>>>> br2\n",
    },
    {
      name: "resolve second of two hunks",
      input: [
        "<<<<<<< HEAD",
        "a",
        "=======",
        "b",
        ">>>>>>> br",
        "mid",
        "<<<<<<< HEAD",
        "c",
        "=======",
        "d",
        ">>>>>>> br2",
        "",
      ].join("\n"),
      hunkIndex: 1,
      resolution: "ours",
      expected: "<<<<<<< HEAD\na\n=======\nb\n>>>>>>> br\nmid\nc\n",
    },
    {
      name: "empty hunk resolved ours produces no extra lines",
      input: "top\n<<<<<<< HEAD\n=======\n>>>>>>> feat\nbottom\n",
      hunkIndex: 0,
      resolution: "ours",
      expected: "top\nbottom\n",
    },
    {
      name: "preserves no trailing newline",
      input: "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> feat",
      hunkIndex: 0,
      resolution: "theirs",
      expected: "theirs",
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const file = parseConflicts(c.input);
      const result = resolveHunk(file, c.hunkIndex, c.resolution);
      expect(result).toBe(c.expected);
    });
  }
});
