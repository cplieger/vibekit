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
    const row = _renderRowForTest({
      category: "steering",
      name: "actions",
      path: "workspace/.kiro/steering/actions.md",
    });
    expect(row.getAttribute("role")).toBe("button");
    expect(row.getAttribute("tabindex")).toBe("0");
    expect(row.getAttribute("aria-label")).toBe("Open actions");
  });
});
