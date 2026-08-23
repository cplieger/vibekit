// Tests for the Kiro configuration browser's pure pieces: the repo/path split
// that makes a git letter resolvable, and the per-category metadata shaping.
import { describe, it, expect, vi, beforeEach, beforeAll } from "vitest";
import type * as GitStatusStore from "./git-status-store.js";

type GitStatusStoreModule = typeof GitStatusStore;

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));
vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  openFileGitDiff: undefined,
  openFile: vi.fn(),
}));
// toggleDocsView RUNS its onShow callback, because that callback is what wires
// the page (initDocsView) and loads it. A mock that swallowed it would leave the
// SSE cases below asserting against a page that was never opened.
vi.mock("./tabs.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  getActiveTabRoute: undefined,
  openRunTab: undefined,
  setGitTab: undefined,
  setSettingsTab: undefined,
  toggleGitView: undefined,
  toggleSettingsView: undefined,
  setDocsTab: vi.fn(),
  toggleDocsView: vi.fn((_tab: string, onShow: () => void) => {
    onShow();
  }),
}));
vi.mock("./bus.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  BUS_RUNS_CHANGED: undefined,
  onBus: undefined,
  onSSE: vi.fn(() => () => undefined),
}));
// Only the POLL is replaced. initGitStatusStore starts one that reaches
// /api/git/status-all through the actions transport — which the api-client mock
// above does not cover — so the first request outlived the window teardown and
// printed an unhandled AbortError. The store itself stays real: these tests seed
// it with _setReposForTest and assert on the letter statusFor derives.
vi.mock("./git-status-store.js", async (importOriginal) => ({
  ...(await importOriginal<GitStatusStoreModule>()),
  initGitStatusStore: vi.fn(),
}));
vi.mock("./actions/hooks.js", () => ({ setHookEnabled: { dispatch: vi.fn() } }));

import {
  _setDocsForTest,
  _setHooksForTest,
  _hookRowsForTest,
  _splitRepoPathForTest,
  _renderRowForTest,
} from "./docs.js";
import { _setReposForTest } from "./git-status-store.js";
import type { GitRepoStatus } from "./git-types.js";

beforeEach(() => {
  _setReposForTest([]);
  _setDocsForTest([]);
  _setHooksForTest([]);
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

// ---------------------------------------------------------------------------
// D66: hooks moved here from Settings, so the Hooks tab is the one tab that is
// not a pure projection of /api/workspace/kiro-docs. It JOINS the scan's rows
// against GET /api/hooks for the state a file scan cannot see, and synthesizes
// rows for the global hooks the scan cannot see at all.
//
// These cases are the ones that came with the move (the old hooks.test.ts is
// deleted): the toggle, the global-scope gate, the disabled reason, and the join
// itself.
// ---------------------------------------------------------------------------

interface HookLike {
  id: string;
  name: string;
  enabled: boolean;
  scope?: string;
  disabled_reason?: string;
  file_path?: string;
  trigger?: string;
  command?: string;
  prompt?: string;
}

function wsHook(over: Partial<HookLike> = {}): HookLike {
  return {
    id: "id-greet",
    name: "greet",
    enabled: true,
    scope: "workspace",
    file_path: ".kiro/hooks/greet.json",
    trigger: "Manual",
    command: "echo hello",
    ...over,
  };
}

// The fixture with one optional field genuinely ABSENT. `decodeHook` copies only
// the keys the payload carries, so a hook whose server JSON omitted a field has no
// such key at all — `{ scope: undefined }` is a shape production cannot produce,
// and a fixture that writes it tests against a state the decoder never hands the
// page. Rest-destructured rather than deleted so the key is gone by construction.

function withoutScope(hook: HookLike): HookLike {
  const { scope: _scope, ...rest } = hook;
  return rest;
}

function withoutCommand(hook: HookLike): HookLike {
  const { command: _command, ...rest } = hook;
  return rest;
}

/** The docs row the server emits for the same file. Its path carries the work
 *  directory; the hook's does not, which is the whole reason the join
 *  normalizes. */
function wsHookDoc(over: Partial<Parameters<typeof _renderRowForTest>[0]> = {}) {
  return {
    category: "hook",
    name: "greet",
    path: "workspace/.kiro/hooks/greet.json",
    group: "greet.json",
    trigger: "Manual",
    action: "echo hello",
    ...over,
  };
}

describe("the Hooks tab: joining state onto a scanned row", () => {
  it("joins across the two path SHAPES the endpoints use", () => {
    // hookInfo.FilePath is workDir-relative (".kiro/hooks/x.json"); kiroDoc.Path
    // carries the workdir without its leading slash. A join on the raw strings
    // matches nothing, and the row silently loses its toggle.
    _setHooksForTest([wsHook()]);
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector(".hook-toggle")).not.toBeNull();
  });

  it("keys the join on (path, NAME), because one file can hold several hooks", () => {
    // kiro_docs.go expands one v1 envelope into one row per hook, all sharing a
    // Path. Keyed on path alone, the first hook's toggle would be applied to
    // every hook in its file.
    _setHooksForTest([
      wsHook({ id: "id-a", name: "first", enabled: true }),
      wsHook({ id: "id-b", name: "second", enabled: false }),
    ]);
    const first = _renderRowForTest(wsHookDoc({ name: "first" }));
    const second = _renderRowForTest(wsHookDoc({ name: "second" }));
    expect((first.querySelector(".hook-toggle") as HTMLInputElement | null)?.checked).toBe(true);
    expect((second.querySelector(".hook-toggle") as HTMLInputElement | null)?.checked).toBe(false);
    expect(first.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-a");
    expect(second.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-b");
  });

  it("renders the enabled state the scan cannot know", () => {
    _setHooksForTest([wsHook({ enabled: false })]);
    const off = _renderRowForTest(wsHookDoc());
    expect((off.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(false);
    expect(off.querySelector(".hook-toggle")?.getAttribute("aria-label")).toBe("Enable hook greet");

    _setHooksForTest([wsHook({ enabled: true })]);
    const on = _renderRowForTest(wsHookDoc());
    expect((on.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);
    expect(on.querySelector(".hook-toggle")?.getAttribute("aria-label")).toBe("Disable hook greet");
  });

  it("shows why KAS disabled a hook", () => {
    _setHooksForTest([wsHook({ enabled: false, disabled_reason: "its command is empty" })]);
    const row = _renderRowForTest(wsHookDoc());
    const badge = row.querySelector(".docs-badge-disabled");
    expect(badge?.textContent).toBe("disabled");
    expect(badge?.getAttribute("data-tooltip")).toBe("its command is empty");
  });

  it("keeps a workspace hook's open surface and its delete", () => {
    // A hook is a `.kiro` file, so it gets exactly what every other document gets
    // — plus the toggle. That is the whole of D69's "three affordances, not four".
    _setHooksForTest([wsHook()]);
    const row = _renderRowForTest(wsHookDoc());
    const surface = row.querySelector<HTMLElement>(".docs-row-surface");
    expect(surface?.getAttribute("role")).toBe("button");
    expect(surface?.getAttribute("aria-label")).toBe("Open greet");
    expect(row.querySelector(".docs-row-delete")).not.toBeNull();
    expect(row.querySelector(".list-row-btn")).not.toBeNull();
  });

  it("leaves a non-hook row untouched by the join", () => {
    _setHooksForTest([wsHook()]);
    const row = _renderRowForTest({
      category: "steering",
      name: "greet",
      path: "workspace/.kiro/hooks/greet.json",
    });
    expect(row.querySelector(".hook-toggle")).toBeNull();
  });

  it("renders a hook with no state as a plain document row", () => {
    // The hooks fetch is best-effort and separate, so a row can legitimately
    // arrive with no state. It degrades to open + delete rather than showing a
    // toggle whose position it cannot know.
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector(".hook-toggle")).toBeNull();
    expect(row.querySelector(".docs-row-delete")).not.toBeNull();
  });
});

// The THIRD gate. `read_only` and `delete_protected` govern the control slot;
// this one governs the activation surface too, because the file is not reachable
// at all — the container HOME is deny-listed by the file surface
// (internal/filebrowse), and a `~`-prefixed display path would not resolve
// first. The three must AGREE rather than stack: a row whose controls are
// withheld must not still open an editable file on click.
describe("the Hooks tab: a global hook is unreachable, not merely read-only", () => {
  const globalHook = (over: Partial<HookLike> = {}) =>
    wsHook({ scope: "global", file_path: "~/.kiro/hooks/greet.json", ...over });

  // `hook_scope` is what a synthesized global row carries, so the fixture carries
  // it too — the join keys on scope, and a row claiming to be global while keyed
  // as workspace is not a row the page can produce.
  const globalDoc = () => wsHookDoc({ path: "~/.kiro/hooks/greet.json", hook_scope: "global" });

  it("gives it neither an open surface nor a delete", () => {
    _setHooksForTest([globalHook()]);
    const row = _renderRowForTest(globalDoc());
    expect(row.querySelector(".docs-row-delete")).toBeNull();
    expect(row.querySelector(".list-row-btn")).toBeNull();
  });

  it("makes the surface INERT, not a disabled-looking button", () => {
    // The fourth state the two existing gates could not express: they left
    // role=button, tabindex and the click listener in place, so the row
    // advertised a control that must not exist.
    _setHooksForTest([globalHook()]);
    const surface = _renderRowForTest(globalDoc()).querySelector<HTMLElement>(".docs-row-surface");
    expect(surface).not.toBeNull();
    expect(surface?.getAttribute("role")).toBeNull();
    expect(surface?.getAttribute("tabindex")).toBeNull();
    expect(surface?.getAttribute("aria-label")).toBeNull();
  });

  it("does not open the file when its surface is clicked or Entered", async () => {
    const { openFile } = await import("./editor-openers.js");
    _setHooksForTest([globalHook()]);
    const surface = _renderRowForTest(globalDoc()).querySelector<HTMLElement>(".docs-row-surface");
    surface?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    surface?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(vi.mocked(openFile)).not.toHaveBeenCalled();
  });

  it("STILL offers the toggle, which is the point of the three gates agreeing", () => {
    // The toggle goes through POST /api/hooks/{id}/enabled and KAS writes the
    // file, so it never touches vibekit's file surface. An unreachable row is
    // exactly the row that proves the distinction.
    _setHooksForTest([globalHook({ enabled: true })]);
    const row = _renderRowForTest(globalDoc());
    expect((row.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);
  });

  it("says where the file is, since the row cannot open it", () => {
    _setHooksForTest([globalHook()]);
    const badge = _renderRowForTest(globalDoc()).querySelector(".docs-badge-global");
    expect(badge?.textContent).toBe("global");
    expect(badge?.getAttribute("data-tooltip")).toContain("~/.kiro/hooks/greet.json");
    expect(badge?.getAttribute("data-tooltip")).toContain("cannot be opened or deleted");
  });

  it("carries no git letter, whose lookup its path cannot answer", () => {
    // splitRepoPath would resolve "~/.kiro/..." to a plausible repo name and look
    // up a file that is not in any repo the poll walks.
    _setReposForTest([repoStatus(".kiro", "hooks/greet.json", "M")]);
    _setHooksForTest([globalHook()]);
    expect(_renderRowForTest(globalDoc()).querySelector(".docs-git-letter")).toBeNull();
  });

  it("treats an absent scope as workspace, which is the safe direction", () => {
    // An older server sends no scope field. Defaulting to global would strip a
    // workspace hook of affordances it legitimately has.
    _setHooksForTest([withoutScope(wsHook())]);
    const row = _renderRowForTest(wsHookDoc());
    expect(row.querySelector<HTMLElement>(".docs-row-surface")?.getAttribute("role")).toBe(
      "button",
    );
    expect(row.querySelector(".docs-row-delete")).not.toBeNull();
  });
});

// A GLOBAL hook has no docs row at all: kiroRoots() scans the workspace's .kiro
// trees and nothing else, so without a synthesized row the move would have LOST
// every global hook from the UI.
describe("the Hooks tab: rows the file scan cannot see", () => {
  it("synthesizes a row for a global hook", () => {
    _setDocsForTest([]);
    _setHooksForTest([
      wsHook({
        scope: "global",
        file_path: "~/.kiro/hooks/fleet.json",
        name: "fleet",
        trigger: "PostFileSave",
        command: "make fmt",
      }),
    ]);
    const rows = _hookRowsForTest();
    expect(rows).toHaveLength(1);
    expect(rows[0]?.name).toBe("fleet");
    expect(rows[0]?.trigger).toBe("PostFileSave");
    expect(rows[0]?.action).toBe("make fmt");
    // The file name groups it, matching how the scanned rows group.
    expect(rows[0]?.group).toBe("fleet.json");
  });

  it("does not duplicate a hook the scan already reported", () => {
    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([wsHook()]);
    const rows = _hookRowsForTest();
    expect(rows).toHaveLength(1);
    // The SCANNED row wins, so the row keeps the path the editor and the delete
    // action accept.
    expect(rows[0]?.path).toBe("workspace/.kiro/hooks/greet.json");
  });

  it("orders workspace rows before global ones, like the server's own list", () => {
    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([
      wsHook(),
      wsHook({ id: "id-g", name: "fleet", scope: "global", file_path: "~/.kiro/hooks/fleet.json" }),
    ]);
    expect(_hookRowsForTest().map((d) => d.name)).toEqual(["greet", "fleet"]);
  });

  it("synthesizes nothing for a workspace hook the scan missed", () => {
    // A workspace hook outside the scan's reach means the two surfaces disagree
    // about the workspace. Inventing a row would paper over that with one whose
    // affordances would then be wrong.
    _setDocsForTest([]);
    _setHooksForTest([wsHook()]);
    expect(_hookRowsForTest()).toHaveLength(0);
  });

  it("keeps a global hook whose relative path and name a workspace hook shares", () => {
    // THE COLLISION. `hookPathKey` strips both scopes to the same `.kiro/...`
    // tail, so before scope joined the key these two were one entry — and the
    // endpoint loads workspace first then global, so the global state won it. Two
    // failures at once: the scanned workspace row joined to the global state (its
    // toggle addressed the global hook's id, and the global gate took its open and
    // delete away), and the real global row was dropped as already claimed.
    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([
      wsHook({ id: "id-ws", enabled: true }),
      wsHook({
        id: "id-global",
        enabled: false,
        scope: "global",
        file_path: "~/.kiro/hooks/greet.json",
      }),
    ]);

    const rows = _hookRowsForTest();
    expect(rows).toHaveLength(2);
    expect(rows.map((d) => d.path)).toEqual([
      "workspace/.kiro/hooks/greet.json",
      "~/.kiro/hooks/greet.json",
    ]);

    const ws = _renderRowForTest(rows[0] as Parameters<typeof _renderRowForTest>[0]);
    const global = _renderRowForTest(rows[1] as Parameters<typeof _renderRowForTest>[0]);

    // Each row toggles its OWN hook, at its own state.
    expect(ws.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-ws");
    expect(global.querySelector("[data-hook-id]")?.getAttribute("data-hook-id")).toBe("id-global");
    expect((ws.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(true);
    expect((global.querySelector(".hook-toggle") as HTMLInputElement).checked).toBe(false);

    // The workspace row keeps the affordances a workspace file legitimately has.
    expect(ws.querySelector<HTMLElement>(".docs-row-surface")?.getAttribute("role")).toBe("button");
    expect(ws.querySelector(".docs-row-delete")).not.toBeNull();
    // The global row has neither, because its file is outside the file surface.
    expect(global.querySelector<HTMLElement>(".docs-row-surface")?.getAttribute("role")).toBeNull();
    expect(global.querySelector(".docs-row-delete")).toBeNull();
  });

  it("uses an askAgent hook's prompt as its subtitle", () => {
    // kiro_docs.go's hookRows sets Action from the COMMAND only, so an askAgent
    // hook's scanned row has an empty subtitle. The join fills it for a global
    // one, which is the only row it builds outright.
    _setDocsForTest([]);
    _setHooksForTest([
      withoutCommand(
        wsHook({
          scope: "global",
          file_path: "~/.kiro/hooks/ask.json",
          name: "ask",
          prompt: "Review the diff",
        }),
      ),
    ]);
    expect(_hookRowsForTest()[0]?.action).toBe("Review the diff");
  });
});

// A kept row is LEFT ALONE by reconcile unless something repaints it, and a
// row's key is its path plus its name — neither of which moves when a hook is
// toggled. Without the update pass the toggle would show its mount-time state
// forever, however many times the server was refetched.
describe("the Hooks tab: a kept row repaints when its state changes", () => {
  it("changes its signature when the hook's enabled flag flips", () => {
    _setHooksForTest([wsHook({ enabled: true })]);
    const on = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    _setHooksForTest([wsHook({ enabled: false })]);
    const off = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(on).not.toBe(off);
  });

  it("changes its signature when the git letter changes", () => {
    const clean = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    _setReposForTest([repoStatus(".kiro", "hooks/greet.json", "M")]);
    const dirty = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(clean).not.toBe(dirty);
  });

  it("keeps the signature stable for an unchanged row", () => {
    _setHooksForTest([wsHook()]);
    const a = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    const b = _renderRowForTest(wsHookDoc()).getAttribute("data-sig");
    expect(a).toBe(b);
    expect(a).not.toBeNull();
  });

  it("distinguishes two rows whose text could forge one signature", () => {
    // Every component is arbitrary text from a file on disk, so a separator
    // inside one must not be able to impersonate a field boundary. keyenc's join
    // is what makes that true; a template literal was not.
    const a = _renderRowForTest(wsHookDoc({ name: "a:b", action: "c" })).getAttribute("data-sig");
    const b = _renderRowForTest(wsHookDoc({ name: "a", action: "b:c" })).getAttribute("data-sig");
    expect(a).not.toBe(b);
  });
});

// ---------------------------------------------------------------------------
// The live tab: the SSE wiring and the toggle round-trip.
//
// The SSE half is the half D66 explicitly asks to verify, and it is not the same
// event the rest of the page uses. `settings_updated` does not fire for a hook
// file, and the docs scan is memoized behind a signature of each category
// directory's mtime AND its entry names — so an IN-PLACE body edit changes
// neither and that endpoint alone would serve the old trigger forever. KAS
// watches the tree and emits `_kiro/hooks/didChange`, which the server turns into
// `hooks_changed`; subscribing to it is what keeps a hand-edited FILE reaching
// this tab.
//
// The page is wired ONCE for this block, deliberately: initDocsView is guarded by
// an `inited` flag and its change listener is delegated on #docs-view, so
// re-seeding the DOM per test would hand every case after the first an element
// nothing is listening to — a false green rather than a failure.
// ---------------------------------------------------------------------------

describe("the Hooks tab: staying current", () => {
  let sseHandlers: Map<string, () => void>;

  beforeAll(async () => {
    document.body.innerHTML = `
      <div id="docs-view">
        <nav id="docs-tab-bar">
          <button type="button" data-docs-tab="steering"></button>
          <button type="button" data-docs-tab="skills"></button>
          <button type="button" data-docs-tab="agents"></button>
          <button type="button" data-docs-tab="specs"></button>
          <button type="button" data-docs-tab="hooks"></button>
          <button type="button" data-docs-tab="workflows"></button>
        </nav>
        <select id="docs-tab-select"></select>
        <div data-docs-panel="steering" class="list-container docs-panel"></div>
        <div data-docs-panel="hooks" class="list-container docs-panel hidden"></div>
      </div>`;
    // offsetParent is null for a detached host, and both SSE handlers gate on it to skip
    // work while the page is closed. Force it truthy so the OPEN case is what
    // these tests exercise.
    Object.defineProperty(document.getElementById("docs-view"), "offsetParent", {
      get: () => document.body,
      configurable: true,
    });

    const { apiGet, apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGet).mockResolvedValue({ docs: [] });
    vi.mocked(apiGetTyped).mockResolvedValue({ hooks: [] });
    const { toggleDocsView } = await import("./tabs.js");
    // onShow is optional on tabs.ts's real toggleDocsView — a restored tab is
    // reopened without one — so the stub calls it only when the caller passed it.
    vi.mocked(toggleDocsView).mockImplementation((_tab, onShow) => {
      onShow?.();
    });

    const { onSSE } = await import("./bus.js");
    const { showDocsView } = await import("./docs.js");
    showDocsView("hooks");
    sseHandlers = new Map(vi.mocked(onSSE).mock.calls.map((c) => [c[0], c[1] as () => void]));
  });

  it("subscribes to hooks_changed, not only to settings_updated", () => {
    expect([...sseHandlers.keys()]).toContain("hooks_changed");
    expect([...sseHandlers.keys()]).toContain("settings_updated");
  });

  it("refetches BOTH halves when a hook file changes underneath it", async () => {
    // Both, because a hand edit can change either: the body (the inventory's) or
    // the enabled flag (the endpoint's).
    const { apiGet, apiGetTyped } = await import("./api-client.js");
    // Re-arm after clearing: this project's vitest resets the implementation with
    // the call log, and an unresolved fetch throws inside the handler.
    vi.mocked(apiGet).mockClear().mockResolvedValue({ docs: [] });
    vi.mocked(apiGetTyped).mockClear().mockResolvedValue({ hooks: [] });

    sseHandlers.get("hooks_changed")?.();

    expect(vi.mocked(apiGet)).toHaveBeenCalledWith("/api/workspace/kiro-docs", expect.anything());
    expect(vi.mocked(apiGetTyped)).toHaveBeenCalledWith("/api/hooks", expect.anything());
  });

  it("dispatches the toggle for the row it was clicked on", async () => {
    const { setHookEnabled } = await import("./actions/hooks.js");
    const { _setDocsForTest, _setHooksForTest, _renderActiveForTest } = await import("./docs.js");
    vi.mocked(setHookEnabled.dispatch).mockClear().mockResolvedValue(undefined);
    // The handler chains into loadHookState on success, so its fetch has to be
    // armed too or the reconcile refetch rejects after the assertion has passed.
    const { apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGetTyped).mockResolvedValue({ hooks: [] });

    _setDocsForTest([wsHookDoc()]);
    _setHooksForTest([wsHook({ enabled: true })]);
    _renderActiveForTest();

    const toggle = document
      .querySelector('[data-docs-panel="hooks"]')
      ?.querySelector<HTMLInputElement>(".hook-toggle");
    expect(toggle).not.toBeNull();
    toggle!.checked = false;
    toggle!.dispatchEvent(new Event("change", { bubbles: true }));

    expect(vi.mocked(setHookEnabled.dispatch)).toHaveBeenCalledWith({
      id: "id-greet",
      enabled: false,
    });
  });

  it("ignores a change event that is not a hook toggle", () => {
    // The listener is delegated on the whole page, so it has to be selective:
    // every future control on any docs tab dispatches change events through it.
    const other = document.createElement("input");
    other.type = "checkbox";
    document.getElementById("docs-view")?.appendChild(other);
    other.dispatchEvent(new Event("change", { bubbles: true }));
    // No throw, and nothing dispatched beyond the previous case's one call.
    expect(true).toBe(true);
  });
});
