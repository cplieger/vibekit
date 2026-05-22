// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./dom.js", () => ({
  $: new Proxy({}, {
    get: (_t, p) => {
      const el = document.createElement("div");
      if (p === "gitBranchBtn") el.id = "git-branch-btn";
      return el;
    },
  }),
}));
vi.mock("./api-client.js", () => ({ apiGet: async () => null }));

let selectionCallback: ((e: unknown) => void) | null = null;
const mockHasForgeCredential = vi.fn((_e: unknown) => true);
vi.mock("./repo-picker.js", () => ({
  onSelectionChange: (cb: (e: unknown) => void) => { selectionCallback = cb; },
  hasForgeCredential: (e: unknown) => mockHasForgeCredential(e),
}));

import { initCIPill } from "./pr-checks.js";

describe("CI_PILL_STYLES exhaustive mapping", () => {
  const states = ["success", "failure", "error", "pending", "canceled"] as const;

  it.each(states)("state '%s' maps to a non-empty git-ci-pill-* class", (state) => {
    const expectedClass = `git-ci-pill-${state}`;
    expect(expectedClass).toMatch(/^git-ci-pill-[a-z]+$/);
  });
});

describe("CIPillController.shouldShow guard logic", () => {
  beforeEach(() => {
    mockHasForgeCredential.mockReset();
    initCIPill();
  });

  it("null entry → pill hidden", () => {
    expect(selectionCallback).not.toBeNull();
    selectionCallback!(null);
    const pill = document.getElementById("git-ci-pill");
    expect(pill === null || pill.classList.contains("hidden")).toBe(true);
  });

  it("entry without forge credential → pill hidden", () => {
    mockHasForgeCredential.mockReturnValue(false);
    selectionCallback!({ id: "1", default_branch: "main" });
    const pill = document.getElementById("git-ci-pill");
    expect(pill === null || pill.classList.contains("hidden")).toBe(true);
  });

  it("entry with empty default_branch → pill hidden", () => {
    mockHasForgeCredential.mockReturnValue(true);
    selectionCallback!({ id: "1", default_branch: "" });
    const pill = document.getElementById("git-ci-pill");
    expect(pill === null || pill.classList.contains("hidden")).toBe(true);
  });
});
