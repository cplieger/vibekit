// ---------------------------------------------------------------------------
// The cross-language pin for the in-chat search reply.
//
// chat.SearchResult and chat.Hit are wiregen-registered, so the TypeScript types
// and decoders are GENERATED from the Go structs; what this pins is the ENCODER.
// Go's TestSearchWireContract writes the fixture from a real scan, and every reply
// in it is decoded here through the generated decodeSearchResult: a field the
// server renames, drops or re-types fails the decode, and a value outside a
// registered enum fails it too.
//
// Node placement because the fixture is a disk read; the decoder module is pure,
// so nothing browser-shaped loads.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeSearchResult } from "./wire/decoders.gen.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";

const FIXTURE_PATH = "../internal/chat/testdata/search_hits.json";
const GO_SEARCH_PATH = "../internal/chat/search.go";
const CLIENT_FIND_PATH = "./find-in-chat.ts";

interface SearchFixture {
  queries: {
    name: string;
    query: string;
    case_sensitive: boolean;
    result: unknown;
  }[];
}

function loadFixture(): SearchFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as SearchFixture;
}

/** One declared constant, read out of a source file as TEXT.
 *
 *  Text rather than an import on both sides: the Go value is unexported, and the
 *  client's lives in a DOM module this node project cannot load. Which is also why
 *  the pair needs a test at all — there is no wire field carrying the radius and no
 *  codegen on either side, so nothing else holds the two numbers together. */
function declaredNumber(rel: string, pattern: RegExp): number {
  const src = readFileSync(new URL(rel, import.meta.url), "utf8");
  const m = pattern.exec(src);
  expect(m?.[1], `${rel} declares nothing matching ${pattern.source}`).toBeDefined();
  return Number(m?.[1]);
}

describe("the in-chat search reply shared with the Go implementation", () => {
  const fx = loadFixture();
  // Decoded per test rather than at describe scope, so a drifted fixture fails
  // the named case below instead of aborting collection.
  const decodeAll = (): SearchResult[] => fx.queries.map((q) => decodeSearchResult(q.result));
  const allHits = (): Hit[] => decodeAll().flatMap((r) => r.matches);

  it("carries queries and hits (an empty fixture would pass forever)", () => {
    expect(fx.queries.length).toBeGreaterThan(0);
    for (const r of decodeAll()) {
      expect(r.matches.length).toBeGreaterThan(0);
    }
  });

  it.each(fx.queries.map((q) => [q.name, q.result] as const))(
    "decodes %s through the generated decoder",
    (_name, raw) => {
      expect(() => decodeSearchResult(raw)).not.toThrow();
    },
  );

  it("carries the tally beside the hits: every message read, every occurrence counted", () => {
    for (const r of decodeAll()) {
      // The fixture's message set is four messages, all read whatever matched.
      expect(r.scanned).toBe(4);
      // Nothing in the fixture reaches the cap, so the count IS the list.
      expect(r.matched).toBe(r.matches.length);
      expect(r.truncated).toBe(false);
    }
  });

  it("refuses a hit whose segment kind the client has no arm for", () => {
    // The enum is registered, so the decoder is strict: an unknown kind fails the
    // whole reply rather than reaching the navigation as a span it cannot resolve.
    const r = fx.queries[0]?.result as { matches: Record<string, unknown>[] };
    const forged = { ...r, matches: [{ ...r.matches[0], segment_kind: "footnote" }] };
    expect(() => decodeSearchResult(forged)).toThrow(/segment_kind/);
  });

  it("keeps the message-kind contract: offset 0, zero length, no block, no subtask", () => {
    const messages = allHits().filter((h) => h.segment_kind === "message");
    expect(messages.length).toBeGreaterThan(0);
    for (const h of messages) {
      expect(h.offset).toBe(0);
      expect(h.segment_len).toBe(0);
      expect(h.block_index).toBeUndefined();
      expect(h.agent_subtask_id).toBeUndefined();
    }
  });

  it("keeps offsets RUNE-counted: a multibyte word before the match must not skew it", () => {
    // The fixture's legacy message reads "… The naïve loop calls retry twice."
    // — the second occurrence sits behind "naïve", whose ï is two UTF-8 bytes,
    // so a server regression to byte offsets would regenerate this as 57.
    //
    // Scoped to that message's CONTENT segment: the same message also carries an
    // attachment, whose own segment is a different span with its own offsets.
    const legacy = allHits().filter((h) => h.message_id === "u1" && h.segment_kind === "content");
    expect(legacy.map((h) => h.offset)).toEqual([15, 56]);
    // And segment-relative rather than message-relative: the tool output is
    // block 2 of a longer message, yet its match indexes the OUTPUT alone
    // ("func retry…" → 5).
    const output = allHits().find((h) => h.segment_kind === "tool_output");
    expect(output?.offset).toBe(5);
    expect(output?.segment_len).toBe(37);
  });

  it("addresses blocks: tool title and output share an index, the delegate names its subtask", () => {
    const hits = allHits();
    const title = hits.find((h) => h.segment_kind === "tool_title");
    const output = hits.find((h) => h.segment_kind === "tool_output");
    expect(title?.block_index).toBe(2);
    expect(output?.block_index).toBe(2);
    const delegate = hits.find((h) => h.agent_subtask_id !== undefined);
    expect(delegate?.agent_subtask_id).toBe("sub-1");
    expect(delegate?.block_index).toBe(3);
    expect(delegate?.segment_kind).toBe("content");
    // Legacy blockless hits stay unaddressed — the optionality is load-bearing,
    // not decorative.
    expect(hits.some((h) => h.block_index === undefined)).toBe(true);
  });
});

describe("the excerpt radius the ranker compares against", () => {
  it("is the same number on both sides", () => {
    // The server slices `searchExcerptRadius` runes either side of a hit into its
    // excerpt; the client slices the same amount of RENDERED text around a candidate
    // mark before scoring the two against each other (`contextAround`). A wider
    // window on one side feeds tokens the other never saw into a Dice coefficient
    // with a similarity FLOOR, so the two numbers drifting apart does not break a
    // build — it quietly moves which occurrence a hit lands on, and pushes a thin
    // match below the floor into the "not in rendered text" notice.
    const go = declaredNumber(GO_SEARCH_PATH, /^const searchExcerptRadius = (\d+)$/m);
    const client = declaredNumber(CLIENT_FIND_PATH, /^const EXCERPT_RADIUS = (\d+);$/m);
    expect(client, "find-in-chat.ts EXCERPT_RADIUS must equal chat.searchExcerptRadius").toBe(go);
  });
});
