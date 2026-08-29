// ---------------------------------------------------------------------------
// The cross-language pin for the SearchHit wire shape.
//
// chat.SearchHit is deliberately NOT wiregen-registered (the generated
// namespace's SearchHit name is taken by the tools type), so the mirror in
// chat-search.ts is hand-maintained. This runs against the same fixture Go's
// TestSearchHitWireContract produces from a REAL Search() run, so the two
// spellings cannot drift silently: a field the server renames or adds shows up
// here as an unknown or missing key, and a field the mirror renames breaks the
// typed construction below at typecheck (`npm run typecheck:tests`).
//
// Node placement because the fixture is a disk read; the module import is
// type-only, so nothing browser-shaped loads.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import type { SearchHit, SegmentKind } from "./chat-search.js";

const FIXTURE_PATH = "../internal/chat/testdata/search_hits.json";

interface HitsFixture {
  queries: {
    name: string;
    query: string;
    case_sensitive: boolean;
    hits: unknown[];
  }[];
}

/** Every member of the mirror's SegmentKind union. A `Record` over the union,
 *  so adding or renaming a kind in chat-search.ts fails typecheck here until
 *  this list (and the fixture) learn it. */
const KIND_LISTED: Record<SegmentKind, true> = {
  content: true,
  reasoning: true,
  tool_title: true,
  tool_output: true,
  message: true,
};
const SEGMENT_KINDS = Object.keys(KIND_LISTED);

/** Every field of the mirror, split by optionality (the Go side's omitempty).
 *  Keyed over `keyof SearchHit`, so the mirror cannot gain, lose, or rename a
 *  field without this table failing typecheck. */
const MIRROR_FIELDS: Record<keyof SearchHit, "required" | "optional"> = {
  block_index: "optional",
  message_id: "required",
  turn_message_id: "required",
  excerpt: "required",
  role: "required",
  segment_kind: "required",
  agent_subtask_id: "optional",
  turn: "required",
  offset: "required",
  segment_len: "required",
};

function loadFixture(): HitsFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as HitsFixture;
}

/** Decode one fixture hit through the mirror: every unknown key is drift from
 *  the Go side, every missing required key is drift from the mirror side, and
 *  the typed construction at the end is what ties the runtime checks to the
 *  mirror's actual spelling. */
function decodeHit(raw: unknown): SearchHit {
  expect(raw !== null && typeof raw === "object" && !Array.isArray(raw)).toBe(true);
  const o = raw as Record<string, unknown>;
  for (const key of Object.keys(o)) {
    expect(
      Object.keys(MIRROR_FIELDS),
      `wire field ${key} is unknown to the chat-search.ts mirror — update the mirror and this table together`,
    ).toContain(key);
  }
  for (const [key, need] of Object.entries(MIRROR_FIELDS)) {
    if (need === "required") {
      expect(o[key], `required mirror field ${key} missing from the fixture`).toBeDefined();
    }
  }
  expect(SEGMENT_KINDS).toContain(o["segment_kind"]);
  const hit: SearchHit = {
    message_id: o["message_id"] as string,
    turn_message_id: o["turn_message_id"] as string,
    excerpt: o["excerpt"] as string,
    role: o["role"] as string,
    segment_kind: o["segment_kind"] as SegmentKind,
    turn: o["turn"] as number,
    offset: o["offset"] as number,
    segment_len: o["segment_len"] as number,
  };
  if (o["block_index"] !== undefined) {
    expect(typeof o["block_index"]).toBe("number");
    hit.block_index = o["block_index"] as number;
  }
  if (o["agent_subtask_id"] !== undefined) {
    expect(typeof o["agent_subtask_id"]).toBe("string");
    hit.agent_subtask_id = o["agent_subtask_id"] as string;
  }
  for (const key of ["message_id", "turn_message_id", "excerpt", "role"] as const) {
    expect(typeof hit[key], key).toBe("string");
  }
  for (const key of ["turn", "offset", "segment_len"] as const) {
    expect(typeof hit[key], key).toBe("number");
  }
  return hit;
}

describe("the SearchHit wire contract shared with the Go implementation", () => {
  const fx = loadFixture();
  // Per test rather than at describe scope, so a drifted fixture fails the
  // named per-hit case below instead of aborting collection.
  const decodeAll = (): SearchHit[] => fx.queries.flatMap((q) => q.hits.map((h) => decodeHit(h)));

  it("carries queries and hits (an empty fixture would pass forever)", () => {
    expect(fx.queries.length).toBeGreaterThan(0);
    for (const q of fx.queries) {
      expect(q.hits.length, q.name).toBeGreaterThan(0);
    }
  });

  it.each(fx.queries.flatMap((q) => q.hits.map((h, i) => [`${q.name} #${String(i)}`, h] as const)))(
    "decodes %s through the mirror with no unknown fields",
    (_name, raw) => {
      decodeHit(raw);
    },
  );

  it("covers every segment kind, so a regeneration cannot silently drop one", () => {
    const seen = new Set(decodeAll().map((h) => h.segment_kind));
    expect([...seen].sort()).toEqual([...SEGMENT_KINDS].sort());
  });

  it("keeps the message-kind contract: offset 0, zero length, no block, no subtask", () => {
    const messages = decodeAll().filter((h) => h.segment_kind === "message");
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
    const legacy = decodeAll().filter((h) => h.message_id === "u1");
    expect(legacy.map((h) => h.offset)).toEqual([15, 56]);
    // And segment-relative rather than message-relative: the tool output is
    // block 2 of a longer message, yet its match indexes the OUTPUT alone
    // ("func retry…" → 5).
    const output = decodeAll().find((h) => h.segment_kind === "tool_output");
    expect(output?.offset).toBe(5);
    expect(output?.segment_len).toBe(37);
  });

  it("addresses blocks: tool title and output share an index, the delegate names its subtask", () => {
    const allHits = decodeAll();
    const title = allHits.find((h) => h.segment_kind === "tool_title");
    const output = allHits.find((h) => h.segment_kind === "tool_output");
    expect(title?.block_index).toBe(2);
    expect(output?.block_index).toBe(2);
    const delegate = allHits.find((h) => h.agent_subtask_id !== undefined);
    expect(delegate?.agent_subtask_id).toBe("sub-1");
    expect(delegate?.block_index).toBe(3);
    expect(delegate?.segment_kind).toBe("content");
    // Legacy blockless hits stay unaddressed — the mirror's optionality is
    // load-bearing, not decorative.
    expect(allHits.some((h) => h.block_index === undefined)).toBe(true);
  });
});
