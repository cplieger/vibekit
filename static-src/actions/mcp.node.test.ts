// ---------------------------------------------------------------------------
// The cross-language pin for the registry search's two bodies.
//
// mcp.RegistrySearchResult, its entry family and mcp.RegistrySearchFailure are
// wiregen-registered; Go's TestRegistrySearchWireContract writes the fixture from
// a real normalisation of the captured upstream reply, and this decodes both
// bodies through the generated decoders `searchRegistry` runs on every live
// answer — so the encoder cannot drift from what the search panel reads.
//
// Node placement because the fixture is a disk read.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeRegistrySearchFailure, decodeRegistrySearchResult } from "../wire/decoders.gen.js";

const FIXTURE_PATH = "../../internal/mcp/testdata/registry_search_reply.json";

interface RegistryFixture {
  search: unknown;
  failure: unknown;
}

function loadFixture(): RegistryFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as RegistryFixture;
}

describe("the registry search reply shared with the Go implementation", () => {
  const fx = loadFixture();

  it("decodes the 200 body through the generated decoder", () => {
    expect(() => decodeRegistrySearchResult(fx.search)).not.toThrow();
  });

  it("carries the cut as a fact beside the list, and every row as installable", () => {
    const r = decodeRegistrySearchResult(fx.search);
    // Normalised at a limit of five from a six-row upstream page: the sixth row
    // is the sentinel that sets `truncated` and is dropped.
    expect(r.servers).toHaveLength(5);
    expect(r.truncated).toBe(true);
    expect(r.filtered).toBe(0);
    // The install-capability filter keeps only what this container can run.
    for (const s of r.servers) {
      expect(s.packages?.map((p) => p.registry_type)).toEqual(["npm"]);
    }
  });

  it("refuses a reply that omits either count", () => {
    // Both are REQUIRED on the wire (no omitempty), so a client cannot read an
    // absent cut or an absent drop count as zero.
    const r = fx.search as Record<string, unknown>;
    const { truncated: _t, ...noTruncated } = r;
    const { filtered: _f, ...noFiltered } = r;
    expect(() => decodeRegistrySearchResult(noTruncated)).toThrow(/truncated/);
    expect(() => decodeRegistrySearchResult(noFiltered)).toThrow(/filtered/);
  });

  it("decodes the 502 body with its class and upstream's interval", () => {
    expect(decodeRegistrySearchFailure(fx.failure)).toEqual({
      error: "registry unavailable",
      reason: "rate_limited",
      retry_after: 30,
    });
  });

  it("refuses a failure whose reason the client has no arm for", () => {
    // The enum is registered, so the decoder is strict: the panel branches on the
    // reason to say "wait" or "narrow the query", and a class it cannot map must
    // not reach that branch as no classification.
    const forged = { ...(fx.failure as Record<string, unknown>), reason: "throttled" };
    expect(() => decodeRegistrySearchFailure(forged)).toThrow(/reason/);
  });
});
