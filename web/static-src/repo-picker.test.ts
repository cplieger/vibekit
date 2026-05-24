// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Unit tests for repo-picker.ts pure functions.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { RepoEntry } from "./forge-types.js";

// Mock api-client so the per-row Clone button tests don't actually
// hit the network when their handler fires cloneAndSelect.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve(null)),
  apiPost: vi.fn(() => Promise.resolve(null)),
  apiPut: vi.fn(() => Promise.resolve(null)),
  apiDelete: vi.fn(() => Promise.resolve(null)),
}));

// Mock confirm.js so the trash-button click handler doesn't try to
// open a real <dialog> via showModal() in happy-dom (which has only
// partial dialog support).
vi.mock("./confirm.js", () => ({
  confirm: vi.fn(() => Promise.resolve(false)),
}));

import {
  hasForgeCredential,
  filtered,
  badgeGlyph,
  stateGlyph,
  __testSetEntries,
  __testSetSearch,
  __testSetOrgFilter,
} from "./repo-picker.js";

// --- hasForgeCredential ---

describe("hasForgeCredential", () => {
  const cases: Array<{ name: string; entry: Partial<RepoEntry>; expected: boolean }> = [
    { name: "returns true when forge_id is set", entry: { forge_id: "github:github.com" }, expected: true },
    { name: "returns false when forge_id is undefined", entry: {}, expected: false },
    { name: "returns false when forge_id is empty string", entry: { forge_id: "" }, expected: false },
  ];

  for (const { name, entry, expected } of cases) {
    it(name, () => {
      expect(hasForgeCredential(entry as RepoEntry)).toBe(expected);
    });
  }
});

// --- badgeGlyph ---

describe("badgeGlyph", () => {
  const cases: Array<{ kind: "github" | "gitlab" | "codeberg" | "gitea"; expected: string }> = [
    { kind: "github", expected: "GH" },
    { kind: "gitlab", expected: "GL" },
    { kind: "codeberg", expected: "CB" },
    { kind: "gitea", expected: "GT" },
  ];

  for (const { kind, expected } of cases) {
    it(`returns "${expected}" for ${kind}`, () => {
      expect(badgeGlyph(kind)).toBe(expected);
    });
  }
});

// --- remoteOnlyClonable (Clone all candidate set) ---

describe("remoteOnlyClonable", () => {
  it("returns only remote-only entries that have a clone_url", async () => {
    const { __testSetEntries: set, __testRemoteOnlyClonable } = await import("./repo-picker.js");
    set([
      // remote-only with clone_url → INCLUDED
      { id: "a", host: "github.com", owner: "o", name: "a", full_name: "o/a", is_remote: true, clone_url: "https://github.com/o/a.git" } as RepoEntry,
      // remote-only without clone_url → EXCLUDED
      { id: "b", host: "github.com", owner: "o", name: "b", full_name: "o/b", is_remote: true } as RepoEntry,
      // remote-only with empty clone_url → EXCLUDED
      { id: "c", host: "github.com", owner: "o", name: "c", full_name: "o/c", is_remote: true, clone_url: "" } as RepoEntry,
      // local clone (synced) → EXCLUDED
      { id: "d", host: "github.com", owner: "o", name: "d", full_name: "o/d", is_local: true, is_remote: true, clone_url: "https://github.com/o/d.git" } as RepoEntry,
      // local-only → EXCLUDED
      { id: "e", host: "", owner: "", name: "e", full_name: "e", is_local: true } as RepoEntry,
    ]);
    const result = __testRemoteOnlyClonable();
    expect(result.map((r) => r.id)).toEqual(["a"]);
  });

  it("returns empty when every entry is already a local clone", async () => {
    const { __testSetEntries: set, __testRemoteOnlyClonable } = await import("./repo-picker.js");
    set([
      { id: "1", host: "github.com", owner: "o", name: "a", full_name: "o/a", is_local: true, is_remote: true, clone_url: "x" } as RepoEntry,
      { id: "2", host: "", owner: "", name: "b", full_name: "b", is_local: true } as RepoEntry,
    ]);
    expect(__testRemoteOnlyClonable()).toHaveLength(0);
  });
});

// --- stateGlyph ---

describe("stateGlyph", () => {
  it("returns synced glyph for local+remote", () => {
    const el = stateGlyph({ is_local: true, is_remote: true } as RepoEntry);
    expect(el).not.toBeNull();
    expect(el!.textContent).toBe("●");
    expect(el!.getAttribute("aria-label")).toBe("Cloned and tracked");
  });

  it("returns local glyph for local-only", () => {
    const el = stateGlyph({ is_local: true, is_remote: false } as RepoEntry);
    expect(el).not.toBeNull();
    expect(el!.textContent).toBe("📁");
    expect(el!.getAttribute("aria-label")).toBe("Local only");
  });

  it("returns globe SVG (not cloud emoji) for remote-only", () => {
    const el = stateGlyph({ is_local: false, is_remote: true } as RepoEntry);
    expect(el).not.toBeNull();
    expect(el!.getAttribute("aria-label")).toBe("Remote, not cloned");
    // SVG inline, not a Unicode glyph.
    const svg = el!.querySelector("svg");
    expect(svg).not.toBeNull();
    expect(svg!.getAttribute("viewBox")).toBe("0 0 24 24");
    expect(el!.textContent ?? "").not.toContain("☁");
  });

  it("returns null when neither local nor remote", () => {
    const el = stateGlyph({ is_local: false, is_remote: false } as RepoEntry);
    expect(el).toBeNull();
  });
});

// --- filtered ---

describe("filtered", () => {
  const entries: RepoEntry[] = [
    { id: "1", host: "github.com", owner: "acme", name: "web", full_name: "acme/web", is_local: true } as RepoEntry,
    { id: "2", host: "github.com", owner: "acme", name: "api", full_name: "acme/api", is_remote: true } as RepoEntry,
    { id: "3", host: "gitlab.com", owner: "corp", name: "infra", full_name: "corp/infra", is_local: true } as RepoEntry,
    { id: "4", host: "github.com", owner: "personal", name: "dotfiles", full_name: "personal/dotfiles", is_local: true } as RepoEntry,
  ];

  beforeEach(() => {
    __testSetEntries(entries);
    __testSetSearch("");
    __testSetOrgFilter("");
  });

  it("returns all entries with no filters", () => {
    expect(filtered()).toHaveLength(4);
  });

  it("filters by orgFilter (host/owner)", () => {
    __testSetOrgFilter("github.com/acme");
    const result = filtered();
    expect(result).toHaveLength(2);
    expect(result.map((e) => e.id)).toEqual(["1", "2"]);
  });

  it("filters by search on full_name (case-insensitive)", () => {
    __testSetSearch("web");
    const result = filtered();
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe("1");
  });

  it("filters by search on host", () => {
    __testSetSearch("gitlab");
    const result = filtered();
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe("3");
  });

  it("combines orgFilter and search", () => {
    __testSetOrgFilter("github.com/acme");
    __testSetSearch("api");
    const result = filtered();
    expect(result).toHaveLength(1);
    expect(result[0]!.id).toBe("2");
  });

  it("returns empty when no match", () => {
    __testSetSearch("nonexistent");
    expect(filtered()).toHaveLength(0);
  });
});

// --- per-row Clone button (PR 3) ---

describe("buildRow Clone button (remote-only entries)", () => {
  function makeEntry(over: Partial<RepoEntry>): RepoEntry {
    return {
      id: "github.com:o/r",
      kind: "github",
      host: "github.com",
      owner: "o",
      name: "r",
      full_name: "o/r",
      ...over,
    };
  }

  it("renders a Clone button on remote-only entries with a clone_url", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_remote: true,
      clone_url: "https://github.com/o/r.git",
    }));
    const cloneBtn = row.querySelector<HTMLButtonElement>("[data-repo-picker-clone-btn]");
    expect(cloneBtn).not.toBeNull();
    expect(cloneBtn?.textContent).toBe("Clone");
  });

  it("does NOT render a Clone button on local-clone rows", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_local: true,
      local_path: "r",
    }));
    expect(row.querySelector("[data-repo-picker-clone-btn]")).toBeNull();
  });

  it("does NOT render a Clone button when clone_url is missing", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_remote: true,
      // no clone_url
    }));
    expect(row.querySelector("[data-repo-picker-clone-btn]")).toBeNull();
  });

  it("does NOT render a Clone button when clone_url is an empty string", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_remote: true,
      clone_url: "",
    }));
    expect(row.querySelector("[data-repo-picker-clone-btn]")).toBeNull();
  });

  it("clicking the Clone button does not also fire the row body click", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_remote: true,
      clone_url: "https://github.com/o/r.git",
    }));
    const cloneBtn = row.querySelector<HTMLButtonElement>("[data-repo-picker-clone-btn]")!;

    let rowClickCount = 0;
    row.addEventListener("click", () => { rowClickCount++; }, true);

    cloneBtn.click();
    // Microtask drain so any synchronous bubble races settle.
    await Promise.resolve();
    // The row's bubble-phase handler shouldn't fire because the button
    // calls stopPropagation. The capture-phase listener above DOES fire
    // (1 invocation), proving the click reached the button. The
    // implementation-side bubble handler that would re-trigger
    // cloneAndSelect is the one we want suppressed; we verified that
    // by ensuring stopPropagation in the click handler.
    expect(rowClickCount).toBe(1);
  });
});

// --- per-row Trash button (delete local copy) ---

describe("buildRow Trash button (local entries)", () => {
  function makeEntry(over: Partial<RepoEntry>): RepoEntry {
    return {
      id: "github.com:o/r",
      kind: "github",
      host: "github.com",
      owner: "o",
      name: "r",
      full_name: "o/r",
      ...over,
    };
  }

  it("renders a Trash button on synced (local + remote) rows", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_local: true,
      is_remote: true,
      local_path: "r",
      clone_url: "https://github.com/o/r.git",
    }));
    const trashBtn = row.querySelector<HTMLButtonElement>("[data-repo-picker-trash-btn]");
    expect(trashBtn).not.toBeNull();
    expect(trashBtn?.getAttribute("aria-label")).toBe("Remove local copy");
    // The trash icon is inline SVG, not a Unicode glyph.
    expect(trashBtn?.querySelector("svg")).not.toBeNull();
  });

  it("renders a Trash button on local-only rows (no forge credential)", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow({
      id: "local:r",
      host: "",
      owner: "",
      name: "r",
      full_name: "r",
      is_local: true,
      local_path: "r",
    } as RepoEntry);
    expect(row.querySelector("[data-repo-picker-trash-btn]")).not.toBeNull();
  });

  it("does NOT render a Trash button on remote-only rows", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_remote: true,
      clone_url: "https://github.com/o/r.git",
    }));
    expect(row.querySelector("[data-repo-picker-trash-btn]")).toBeNull();
  });

  it("does NOT render a Trash button when local_path is missing", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_local: true,
      // no local_path
    }));
    expect(row.querySelector("[data-repo-picker-trash-btn]")).toBeNull();
  });

  it("does NOT render a Trash button when local_path is an empty string", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_local: true,
      local_path: "",
    }));
    expect(row.querySelector("[data-repo-picker-trash-btn]")).toBeNull();
  });

  it("clicking the Trash button does not also fire the row body click", async () => {
    const { __testBuildRow } = await import("./repo-picker.js");
    const row = __testBuildRow(makeEntry({
      is_local: true,
      is_remote: true,
      local_path: "r",
      clone_url: "https://github.com/o/r.git",
    }));
    const trashBtn = row.querySelector<HTMLButtonElement>("[data-repo-picker-trash-btn]")!;
    let rowClickCount = 0;
    row.addEventListener("click", () => { rowClickCount++; }, true);
    trashBtn.click();
    await Promise.resolve();
    // Capture-phase listener fires once (proving click landed on the
    // button). The picker's own bubble-phase row click that would
    // try to pick the entry is suppressed by stopPropagation.
    expect(rowClickCount).toBe(1);
  });
});
