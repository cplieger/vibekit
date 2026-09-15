// ---------------------------------------------------------------------------
// The cross-language pin for the configuration browser's inventory reply.
//
// server.KiroDoc and server.KiroDocsResponse are wiregen-registered; Go's
// TestKiroDocsWireContract writes the fixture from a real scan over one `.kiro`
// tree, and this decodes it through the generated decodeKiroDocsResponse — the
// decoder `docs.load` runs on every live reply — so the encoder cannot drift from
// what the page renders.
//
// Node placement because the fixture is a disk read.
// ---------------------------------------------------------------------------

import { readFileSync } from "node:fs";
import { describe, it, expect } from "vitest";
import { decodeKiroDocsResponse } from "./wire/decoders.gen.js";

const FIXTURE_PATH = "../internal/server/testdata/kiro_docs.json";

interface DocsFixture {
  result: unknown;
}

function loadFixture(): DocsFixture {
  const raw = readFileSync(new URL(FIXTURE_PATH, import.meta.url), "utf8");
  return JSON.parse(raw) as DocsFixture;
}

describe("the kiro-docs reply shared with the Go implementation", () => {
  const fx = loadFixture();

  it("decodes through the generated decoder", () => {
    expect(() => decodeKiroDocsResponse(fx.result)).not.toThrow();
  });

  it("carries every category the page has a tab for, over a scan that was not cut", () => {
    const r = decodeKiroDocsResponse(fx.result);
    expect(r.truncated).toBe(false);
    const categories = new Set(r.docs.map((d) => d.category));
    expect([...categories].sort()).toEqual(["agent", "hook", "skill", "spec", "steering"]);
  });

  it("shapes each row by its category, the way the tabs read them", () => {
    const r = decodeKiroDocsResponse(fx.result);
    const byPath = (p: string) => r.docs.find((d) => d.path.endsWith(p));
    // A steering row leads with its inclusion; a fileMatch one names its pattern.
    expect(byPath("steering/matched.md")).toMatchObject({
      inclusion: "fileMatch",
      file_match: "internal/**/*.go",
    });
    // An agent row carries its model and tools and no inclusion.
    const agent = byPath("agents/twin.md");
    expect(agent).toMatchObject({ model: "claude-opus-5", tools: ["read", "write"] });
    expect(agent?.inclusion).toBeUndefined();
    // A spec row is labelled by its H1 under its feature group.
    expect(byPath("specs/search/design.md")).toMatchObject({
      name: "Search design",
      group: "search",
    });
    // One hook FILE expands to one row per hook, each with its trigger and action.
    const hooks = r.docs.filter((d) => d.category === "hook");
    expect(hooks.map((h) => [h.name, h.trigger, h.action])).toEqual([
      ["First", "PostFileSave", "echo one"],
      ["Second", "SessionStart", "do a thing"],
    ]);
  });

  it("carries no hook_scope: that field is the client's extension, never the wire's", () => {
    // docs.ts widens the generated type with `hook_scope` for the scope join it
    // computes itself; a server that started sending one would be a second writer.
    const rows = (fx.result as { docs: Record<string, unknown>[] }).docs;
    for (const row of rows) {
      expect(Object.keys(row)).not.toContain("hook_scope");
    }
  });

  it("refuses a reply that omits truncated", () => {
    // REQUIRED on the wire (no omitempty), so a short list cannot read as the
    // whole inventory through an absent marker.
    const { truncated: _t, ...noTruncated } = fx.result as Record<string, unknown>;
    expect(() => decodeKiroDocsResponse(noTruncated)).toThrow(/truncated/);
  });
});
