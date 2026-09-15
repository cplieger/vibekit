// ---------------------------------------------------------------------------
// The cross-language pin for the file-search reply.
//
// filebrowse.FileSearchResult, FileMatch and FileMatchKind are wiregen-registered;
// Go's TestFileSearchWireContract writes the fixture from the real handler over a
// seeded tree, and this decodes it through the generated decodeFileSearchResult —
// the decoder files-search.ts runs on every live reply — so the encoder cannot
// drift from what the file browser reads.
//
// Node placement because the fixture is a disk read.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeFileSearchResult } from "./wire/decoders.gen.js";

const FIXTURE_PATH = "../internal/filebrowse/testdata/file_search.json";

interface FileSearchFixture {
  query: string;
  result: unknown;
}

function loadFixture(): FileSearchFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as FileSearchFixture;
}

describe("the file-search reply shared with the Go implementation", () => {
  const fx = loadFixture();

  it("decodes through the generated decoder", () => {
    expect(() => decodeFileSearchResult(fx.result)).not.toThrow();
  });

  it("carries every row kind, names ahead of content, and a directory ahead of its child", () => {
    const r = decodeFileSearchResult(fx.result);
    expect(r.matches.slice(0, 2)).toEqual([
      { path: "/workspace/needle-dir", excerpt: "", kind: "dir", line: 0 },
      { path: "/workspace/notes-needle.md", excerpt: "", kind: "name", line: 0 },
    ]);
    expect(r.matches.slice(2).every((m) => m.kind === "content" && m.line >= 1)).toBe(true);
  });

  it("counts the lines the per-file cap cut while reporting the scan as whole", () => {
    const r = decodeFileSearchResult(fx.result);
    const manyRows = r.matches.filter((m) => m.path === "/workspace/many.txt");
    expect(manyRows).toHaveLength(20);
    expect(r.matched).toBe(24);
    expect(r.matched).toBeGreaterThan(r.matches.length);
    expect(r.scanned).toBe(4);
    expect(r.truncated).toBe(false);
  });

  it("refuses a row whose kind the bundle has no arm for", () => {
    const r = fx.result as { matches: Record<string, unknown>[] };
    const forged = { ...r, matches: [{ ...r.matches[0], kind: "sigil" }] };
    expect(() => decodeFileSearchResult(forged)).toThrow();
  });

  it("refuses a reply with the tally absent, so a missing count cannot read as zero", () => {
    const r = fx.result as Record<string, unknown>;
    const { matched: _matched, ...withoutMatched } = r;
    expect(() => decodeFileSearchResult(withoutMatched)).toThrow();
  });
});
