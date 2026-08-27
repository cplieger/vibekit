// D67b: agent scope shadowing, modelled rather than deduped away.
//
// A workspace agent and a catalog agent (bundled, or the user's global
// ~/.kiro/agents) can share an id. The merge used to filter the WORKSPACE entry
// out, so the surviving row was the global one — while the comment three lines
// above claimed the workspace definition was what a session would load. The
// picker did not merely hide one of the two, it showed the wrong one.
import { describe, it, expect } from "vitest";
import {
  isCustomSource,
  mergeCatalogAndWorkspace,
  scopeLabel,
  WORKSPACE_AGENT_DESC,
} from "./roles.js";
import type { SessionMode } from "./types.js";

const catalog: readonly SessionMode[] = [
  { id: "vibe", name: "Default", description: "General", source: "bundled" },
  { id: "reviewer", name: "reviewer", description: "The global one", source: "global" },
];

describe("mergeCatalogAndWorkspace", () => {
  it("offers the workspace definition, not the catalog one, on a collision", () => {
    const merged = mergeCatalogAndWorkspace(catalog, ["reviewer"]);
    const rows = merged.filter((p) => p.mode.id === "reviewer");
    expect(rows).toHaveLength(1);
    // KAS's last-write-wins: the workspace definition is what a session loads,
    // so it is the one offered.
    expect(rows[0]?.mode.source).toBe("workspace");
    expect(rows[0]?.mode.description).toBe(WORKSPACE_AGENT_DESC);
  });

  it("marks the shadowing entry with what it shadows", () => {
    const merged = mergeCatalogAndWorkspace(catalog, ["reviewer"]);
    const row = merged.find((p) => p.mode.id === "reviewer");
    expect(row?.shadowed).toBe("global");
  });

  it("marks a workspace agent shadowing a BUNDLED mode too", () => {
    const merged = mergeCatalogAndWorkspace(catalog, ["vibe"]);
    const row = merged.find((p) => p.mode.id === "vibe");
    expect(row?.mode.source).toBe("workspace");
    expect(row?.shadowed).toBe("bundled");
  });

  it("leaves a non-colliding workspace agent unmarked", () => {
    const merged = mergeCatalogAndWorkspace(catalog, ["only-here"]);
    const row = merged.find((p) => p.mode.id === "only-here");
    expect(row?.mode.source).toBe("workspace");
    expect(row?.shadowed).toBeUndefined();
  });

  it("keeps every non-shadowed catalog entry", () => {
    const merged = mergeCatalogAndWorkspace(catalog, ["only-here"]);
    expect(merged.map((p) => p.mode.id)).toEqual(["vibe", "reviewer", "only-here"]);
  });

  it("emits exactly one row per id", () => {
    // Two rows carrying the same id would offer a choice session/set_mode cannot
    // express: both would send the same mode id.
    const merged = mergeCatalogAndWorkspace(catalog, ["reviewer", "vibe", "extra"]);
    const ids = merged.map((p) => p.mode.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(ids).toContain("extra");
  });

  it("treats a catalog entry with no source as bundled when shadowed", () => {
    // BUILTIN_MODES tags its own source, but the pre-fetch fallback path and any
    // future catalog entry may not, and "shadows undefined" would be a worse
    // answer than naming the default group.
    const merged = mergeCatalogAndWorkspace([{ id: "x", name: "X" }], ["x"]);
    expect(merged[0]?.shadowed).toBe("bundled");
  });

  it("passes an empty workspace list through untouched", () => {
    const merged = mergeCatalogAndWorkspace(catalog, []);
    expect(merged).toHaveLength(catalog.length);
    expect(merged.every((p) => p.shadowed === undefined)).toBe(true);
  });
});

describe("scopeLabel", () => {
  it("names the two custom scopes", () => {
    expect(scopeLabel("workspace")).toBe("workspace");
    expect(scopeLabel("global")).toBe("global");
  });

  it("says nothing for a bundled or unset source", () => {
    // Every row in the top group is bundled, so labelling each one restates the
    // divider above it.
    expect(scopeLabel("bundled")).toBe("");
    expect(scopeLabel(undefined)).toBe("");
  });
});

describe("isCustomSource", () => {
  it("puts the two custom scopes under the divider", () => {
    expect(isCustomSource("workspace")).toBe(true);
    expect(isCustomSource("global")).toBe(true);
  });

  it("keeps bundled and unset in the top group", () => {
    // BUILTIN_MODES tags itself bundled, but the pre-fetch fallback path and any
    // catalog entry that omits the field must not land under "Custom agents".
    expect(isCustomSource("bundled")).toBe(false);
    expect(isCustomSource(undefined)).toBe(false);
    expect(isCustomSource("")).toBe(false);
  });

  it("treats a source value it has never seen as custom", () => {
    // The whole reason this tests what IS bundled rather than enumerating what is
    // custom. A vocabulary that grows upstream must not put an unknown scope in
    // the group a reader trusts to be Kiro's own; the divider is the safe side.
    expect(isCustomSource("organization")).toBe(true);
  });
});
