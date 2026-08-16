// @vitest-environment happy-dom
// Tests for the Kiro configuration browser's pure pieces: the repo/path split
// that makes a git letter resolvable, and the per-category metadata shaping.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn() }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./tabs.js", () => ({ setDocsTab: vi.fn(), toggleDocsView: vi.fn() }));

import { _setDocsForTest, _splitRepoPathForTest, _renderRowForTest } from "./docs.js";
import { _setReposForTest } from "./git-status-store.js";
import type { GitRepoStatus } from "./git-types.js";

beforeEach(() => {
  _setReposForTest([]);
  _setDocsForTest([]);
});

describe("splitRepoPath", () => {
  it("treats the workspace-root .kiro as its own repo", () => {
    // The root .kiro IS a git repo (cplieger/.kiro), so the repo name is
    // ".kiro" and the path inside it drops that prefix.
    expect(_splitRepoPathForTest("workspace/.kiro/steering/actions.md")).toEqual({
      repo: ".kiro",
      rel: "steering/actions.md",
    });
  });

  it("treats a per-repo .kiro as part of that repo", () => {
    // Here the repo is the directory HOLDING .kiro, and .kiro is inside it.
    expect(_splitRepoPathForTest("workspace/myrepo/.kiro/steering/x.md")).toEqual({
      repo: "myrepo",
      rel: ".kiro/steering/x.md",
    });
  });

  it("handles an absolute-looking workdir prefix", () => {
    expect(_splitRepoPathForTest("home/cplieger/workspace/.kiro/agents/a.md")).toEqual({
      repo: ".kiro",
      rel: "agents/a.md",
    });
    expect(_splitRepoPathForTest("home/cplieger/workspace/pg-autodump/.kiro/specs/f/r.md")).toEqual(
      {
        repo: "pg-autodump",
        rel: ".kiro/specs/f/r.md",
      },
    );
  });

  it("yields an empty repo for a path with no .kiro segment", () => {
    expect(_splitRepoPathForTest("workspace/README.md")).toEqual({ repo: "", rel: "" });
  });
});

function repoStatus(name: string, path: string, status: string): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    has_dirty: true,
    stashes: 0,
    files: [{ path, status, staged: false, display: path }],
  };
}

describe("row rendering", () => {
  it("shows a steering doc's inclusion badge, with the pattern on hover", () => {
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
      description: "The actions framework",
      inclusion: "fileMatch",
      file_match: "actions/**",
    });
    expect(row.textContent).toContain("actions");
    expect(row.textContent).toContain("fileMatch");
    expect(row.textContent).toContain("The actions framework");
    // The class is lowercased; the visible label keeps the camelCase spelling.
    const badge = row.querySelector(".docs-badge-filematch");
    expect(badge?.getAttribute("data-tooltip")).toBe("actions/**");
  });

  it("shows an agent's model and tool count, with the tools on hover", () => {
    const row = _renderRowForTest({
      category: "agent",
      name: "trial-judge",
      path: "workspace/.kiro/agents/trial-judge.md",
      model: "claude-opus-5",
      tools: ["read", "write", "shell"],
    });
    expect(row.textContent).toContain("claude-opus-5");
    expect(row.textContent).toContain("3 tools");
    const chip = [...row.querySelectorAll(".docs-badge")].find((e) => e.textContent === "3 tools");
    expect(chip?.getAttribute("data-tooltip")).toBe("read, write, shell");
  });

  it("singularises a one-tool agent", () => {
    const row = _renderRowForTest({
      category: "agent",
      name: "solo",
      path: "workspace/.kiro/agents/solo.md",
      tools: ["read"],
    });
    expect(row.textContent).toContain("1 tool");
    expect(row.textContent).not.toContain("1 tools");
  });

  it("marks a skill that overrides the steering set", () => {
    const row = _renderRowForTest({
      category: "skill",
      name: "judgement",
      path: "workspace/.kiro/skills/judgement/SKILL.md",
      inclusion: "manual",
      steering_override: true,
    });
    expect(row.textContent).toContain("override");
  });

  it("shows a hook's trigger and its action as the subtitle", () => {
    const row = _renderRowForTest({
      category: "hook",
      name: "Knowledge Map regen",
      path: "workspace/.kiro/hooks/km.json",
      trigger: "PostFileSave",
      action: "python3 .kiro/scripts/generate-knowledge-map.py",
    });
    expect(row.textContent).toContain("PostFileSave");
    expect(row.textContent).toContain("generate-knowledge-map.py");
  });

  it("renders a spec row with no metadata badges (specs carry no front-matter)", () => {
    const row = _renderRowForTest({
      category: "spec",
      name: "Requirements — Something",
      path: "workspace/.kiro/specs/feature/requirements.md",
      group: "feature",
    });
    expect(row.textContent).toContain("Requirements — Something");
    expect(row.querySelectorAll(".docs-badge")).toHaveLength(0);
  });

  it("decorates a dirty document with its git letter", () => {
    _setReposForTest([repoStatus(".kiro", "steering/actions.md", "M")]);
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    const letter = row.querySelector(".docs-git-letter");
    expect(letter?.textContent).toBe("M");
    expect(letter?.getAttribute("aria-label")).toBe("Git status: Modified");
  });

  it("omits the git letter for a clean document", () => {
    _setReposForTest([repoStatus(".kiro", "steering/other.md", "M")]);
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    expect(row.querySelector(".docs-git-letter")).toBeNull();
  });

  it("is a keyboard-operable button", () => {
    // The three assertions are unchanged; they moved from the ROW onto the
    // activation SURFACE, because the row now also holds a delete button and a
    // button cannot contain one (D65's sibling-slot restructure).
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    const surface = row.querySelector<HTMLElement>(".docs-row-surface");
    expect(surface?.getAttribute("role")).toBe("button");
    expect(surface?.getAttribute("tabindex")).toBe("0");
    expect(surface?.getAttribute("aria-label")).toBe("Open actions");
  });
});

// D65 / D67a: the delete affordance and the provenance gate that decides it.
describe("row affordances", () => {
  it("gives a writable row a delete button", () => {
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    const del = row.querySelector<HTMLButtonElement>(".docs-row-delete");
    expect(del).not.toBeNull();
    expect(del?.getAttribute("aria-label")).toBe("Delete actions");
  });

  it("gives an asserted read-only row neither an edit glyph nor a delete", () => {
    const row = _renderRowForTest({
      category: "steering",
      name: "locked",
      path: "workspace/.kiro/steering/locked.md",
      read_only: true,
    });
    expect(row.querySelector(".docs-row-delete")).toBeNull();
    expect(row.querySelector(".list-row-btn")).toBeNull();
  });

  it("makes no read-only claim, because the surface still opens the file", () => {
    // D67a is withdrawn. The badge that used to sit here said "read-only" while
    // the activation surface opened a file the editor could save — the row and
    // its own control contradicted each other. A row states what it can back up.
    const row = _renderRowForTest({
      category: "steering",
      name: "locked",
      path: "workspace/.kiro/steering/locked.md",
      read_only: true,
    });
    expect(row.textContent).not.toContain("read-only");
    expect(row.querySelector(".docs-badge-readonly")).toBeNull();
  });

  it("keeps a symlinked row's edit and withholds only its delete", () => {
    // The two questions are separate: editing through a link writes the target
    // (which is what following a link means), while deleting through it removes a
    // file listed under its own name elsewhere on the page.
    const row = _renderRowForTest({
      category: "steering",
      name: "alias",
      path: "workspace/.kiro/steering/alias.md",
      delete_protected: true,
    });
    expect(row.querySelector(".docs-row-delete")).toBeNull();
    expect(row.querySelector(".list-row-btn")).not.toBeNull();
    const badge = row.querySelector(".docs-badge-link");
    expect(badge?.textContent).toBe("link");
    expect(badge?.getAttribute("data-tooltip")).toContain("delete is disabled");
  });

  it("treats absent provenance fields as unrestricted", () => {
    // The direction matters as much as the value: a restriction is asserted by the
    // server, never inferred from a field failing to arrive.
    const row = _renderRowForTest({
      category: "hook",
      name: "h",
      path: "workspace/.kiro/hooks/h.json",
    });
    expect(row.querySelector(".docs-row-delete")).not.toBeNull();
    expect(row.querySelector(".list-row-btn")).not.toBeNull();
  });

  it("keeps the delete OUTSIDE the activation surface", () => {
    // Nesting an interactive control inside a role=button is invalid HTML and
    // gets flattened by assistive tech — the defect pill-expand.ts documents.
    // The two are siblings, which is what makes both reachable.
    const row = _renderRowForTest({
      category: "agent",
      name: "a",
      path: "workspace/.kiro/agents/a.md",
    });
    const surface = row.querySelector<HTMLElement>(".docs-row-surface");
    const del = row.querySelector<HTMLButtonElement>(".docs-row-delete");
    expect(surface?.getAttribute("role")).toBe("button");
    expect(del).not.toBeNull();
    expect(surface?.contains(del ?? null)).toBe(false);
    expect(surface?.querySelector("button")).toBeNull();
  });
});
