// ---------------------------------------------------------------------------
// The cross-language pin for the cross-chat search reply.
//
// chat.SearchAllResult and chat.Match are wiregen-registered; Go's
// TestSearchAllWireContract writes the fixture from a real SearchAll over a seeded
// store, and this decodes it through the generated decodeSearchAllResult — the
// decoder `searchChats` runs on every live reply — so the encoder cannot drift
// from what the History page reads.
//
// Node placement because the fixture is a disk read.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeSearchAllResult } from "../wire/decoders.gen.js";

const FIXTURE_PATH = "../../internal/chat/testdata/search_all.json";

interface SearchAllFixture {
  query: string;
  result: unknown;
}

function loadFixture(): SearchAllFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as SearchAllFixture;
}

describe("the cross-chat search reply shared with the Go implementation", () => {
  const fx = loadFixture();

  it("decodes through the generated decoder", () => {
    expect(() => decodeSearchAllResult(fx.result)).not.toThrow();
  });

  it("ranks the title-and-body match above the title-only one and counts every match", () => {
    const r = decodeSearchAllResult(fx.result);
    expect(r.matches.map((m) => m.id)).toEqual(["chat-001", "chat-003"]);
    expect(r.matched).toBe(r.matches.length);
    expect(r.scanned).toBe(3);
    expect(r.truncated).toBe(false);
  });

  it("carries a best hit only where the transcript holds a line to show", () => {
    const r = decodeSearchAllResult(fx.result);
    const [withLine, titleOnly] = r.matches;
    // The first occurrence in the chat, segment-relative, past no multibyte word.
    expect(withLine?.best).toMatchObject({
      message_id: "m1",
      segment_kind: "content",
      offset: 22,
      segment_len: 33,
    });
    expect(withLine?.hits).toBe(3);
    // A title-only match has no `best` at all: a zero hit would carry an empty
    // segment kind, which the registered enum refuses.
    expect(titleOnly?.best).toBeUndefined();
    expect(titleOnly?.hits).toBe(0);
  });

  it("refuses a title-only match spelled as a zero hit", () => {
    const r = fx.result as { matches: Record<string, unknown>[] };
    const zeroHit = {
      message_id: "",
      turn_message_id: "",
      excerpt: "",
      role: "",
      segment_kind: "",
      turn: 0,
      offset: 0,
      segment_len: 0,
    };
    const forged = { ...r, matches: [{ ...r.matches[1], best: zeroHit }] };
    expect(() => decodeSearchAllResult(forged)).toThrow();
  });
});
