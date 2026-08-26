// ---------------------------------------------------------------------------
// Tests for the file browser's change DECORATION — the git letter on a row and
// the "changed by this chat" filter. Not the browser's navigation or CRUD.
//
// Each case pins a decision the decoration rests on:
//   - the filter DIMS, it never hides: a hidden row would make the listing lie
//     about what is on disk, and a folder whose only changed child was filtered
//     out would read as empty
//   - a repaint is in place, so a 15s poll cannot blow away the selection or the
//     scroll position of a listing the user is working in
//   - a directory row carries the worst status BENEATH it, or a change three
//     levels down is invisible until you walk into it
//   - only a file's letter is clickable; a directory rollup has no single diff
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

// Leaves that reach for DOM or state this module does not own. The store is NOT
// mocked: attribution reads the real `activeSession` computed, and a stubbed
// signal would test the stub instead of the wiring that ships.
vi.mock("./scroll.js", () => ({
  setUserScrolledUp: vi.fn(),
  scrollToBottom: vi.fn(),
  initScroll: vi.fn(),
}));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn(), openFileGitDiff: vi.fn() }));
// chat.ts transitively mounts the transcript view at import time (#messages).
vi.mock("./chat.js", () => ({ attachPathsToActiveChat: vi.fn() }));

import { toggleChatFilter, _repaintRowsForTest } from "./files.js";
import { openFileGitDiff } from "./editor-openers.js";
import { _setReposForTest } from "./git-status-store.js";
import { setWorkspaceRoot, _resetForTest as resetWorkspace } from "./workspace.js";
import { setSessions, setActive } from "./store.js";
import type { GitRepoStatus, GitFileEntry } from "./git-types.js";
import type { Session } from "./types.js";
import type { Message, FileChange } from "./wire/types.gen.js";

function repo(name: string, files: { path: string; status: string }[]): GitRepoStatus {
  return {
    repo: name,
    is_repo: true,
    branch: "main",
    remote: "origin",
    ahead: 0,
    behind: 0,
    has_dirty: files.length > 0,
    stashes: 0,
    files: files.map((f): GitFileEntry => ({
      path: f.path,
      status: f.status,
      staged: false,
      display: f.path,
    })),
  };
}

function session(paths: string[]): Session {
  const changed: Record<string, FileChange> = {};
  for (const p of paths) {
    changed[p] = { lines_added: 3, lines_removed: 1 };
  }
  const msg: Message = {
    id: "m1",
    role: "assistant",
    content: "done",
    ts: 0,
    changed_files: changed,
  };
  return {
    id: "c1",
    name: "c1",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 1,
    messages: [msg],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** One row, shaped exactly as entryRow builds it: the decoration reads only
 *  `data-path` / `data-is-dir` and inserts before `.fb-meta`. */
function row(path: string, isDir = false): HTMLElement {
  const r = document.createElement("div");
  r.className = "fb-row";
  r.dataset["path"] = path;
  r.dataset["isDir"] = String(isDir);
  r.dataset["name"] = path.slice(path.lastIndexOf("/") + 1);
  const meta = document.createElement("span");
  meta.className = "fb-meta";
  r.appendChild(meta);
  return r;
}

function list(): HTMLElement {
  return document.getElementById("fb-list") as HTMLElement;
}

function letters(): string[] {
  return [...list().querySelectorAll(".fb-git-letter")].map((n) => n.textContent ?? "");
}

function dimmed(): string[] {
  return [...list().querySelectorAll<HTMLElement>(".fb-row-unattributed")].map(
    (n) => n.dataset["path"] ?? "",
  );
}

/** The filter is module state; each case starts with it off. */
function filterOff(): void {
  while (toggleChatFilter()) {
    // toggling returns the new state — stop once it reads false
  }
}

beforeEach(() => {
  document.body.replaceChildren();
  const l = document.createElement("div");
  l.id = "fb-list";
  document.body.appendChild(l);
  resetWorkspace();
  // /api/git/status-all names each repo by a bare directory under the workspace,
  // so the absolute keys only exist once the handshake has stated the root.
  setWorkspaceRoot("/w");
  _setReposForTest([]);
  setSessions([session([])]);
  setActive("c1");
  filterOff();
  vi.mocked(openFileGitDiff).mockClear();
});

describe("git letter decoration", () => {
  it("puts the file's own letter on its row, before the meta column", () => {
    _setReposForTest([repo("r", [{ path: "a/b.go", status: "M" }])]);
    list().append(row("/w/r/a/b.go"));
    _repaintRowsForTest();
    expect(letters()).toEqual(["M"]);
    expect(list().firstElementChild?.children[0]?.className).toContain("fb-git-letter");
  });

  it("reuses the app's git-st-* colour vocabulary rather than a browser-local one", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row("/w/r/a.go"));
    _repaintRowsForTest();
    expect(list().querySelector(".fb-git-letter")?.className).toContain("git-st-m");
  });

  it("gives a directory the WORST letter beneath it", () => {
    _setReposForTest([
      repo("r", [
        { path: "a/untracked.go", status: "?" },
        { path: "a/conflict.go", status: "U" },
      ]),
    ]);
    list().append(row("/w/r/a", true));
    _repaintRowsForTest();
    expect(letters()).toEqual(["U"]);
  });

  it("leaves a clean row undecorated", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row("/w/r/clean.go"));
    _repaintRowsForTest();
    expect(letters()).toEqual([]);
  });

  it("opens the file's diff when its letter is clicked, without selecting the row", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    const r = row("/w/r/a.go");
    let rowClicks = 0;
    r.addEventListener("click", () => {
      rowClicks++;
    });
    list().append(r);
    _repaintRowsForTest();
    const badge = r.querySelector<HTMLElement>(".fb-git-letter");
    expect(badge?.classList.contains("fb-git-clickable")).toBe(true);
    badge?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(vi.mocked(openFileGitDiff)).toHaveBeenCalledWith("/w/r/a.go", "HEAD");
    // stopPropagation: clicking the badge must not also toggle the row.
    expect(rowClicks).toBe(0);
  });

  it("does not make a directory's rollup letter clickable — it has no one diff", () => {
    _setReposForTest([repo("r", [{ path: "a/b.go", status: "M" }])]);
    list().append(row("/w/r/a", true));
    _repaintRowsForTest();
    const badge = list().querySelector<HTMLElement>(".fb-git-letter");
    expect(badge?.classList.contains("fb-git-clickable")).toBe(false);
    badge?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(vi.mocked(openFileGitDiff)).not.toHaveBeenCalled();
  });

  it("repaints in place: the same row elements survive, so selection is untouched", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    const r = row("/w/r/a.go");
    r.classList.add("fb-row-selected");
    list().append(r);
    _repaintRowsForTest();
    expect(list().firstElementChild).toBe(r);
    expect(r.classList.contains("fb-row-selected")).toBe(true);
  });

  it("replaces the letter on the next poll rather than stacking a second one", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row("/w/r/a.go"));
    _repaintRowsForTest();
    _setReposForTest([repo("r", [{ path: "a.go", status: "D" }])]);
    _repaintRowsForTest();
    expect(letters()).toEqual(["D"]);
  });

  it("drops the letter when the tree goes clean", () => {
    _setReposForTest([repo("r", [{ path: "a.go", status: "M" }])]);
    list().append(row("/w/r/a.go"));
    _repaintRowsForTest();
    _setReposForTest([repo("r", [])]);
    _repaintRowsForTest();
    expect(letters()).toEqual([]);
  });
});

describe("changed-by-this-chat filter", () => {
  beforeEach(() => {
    setSessions([session(["a/mine.go"])]);
    setActive("c1");
    list().append(row("/w/r/a/mine.go"), row("/w/r/a/theirs.go"), row("/w/r/a", true));
  });

  it("decorates nothing while off", () => {
    _repaintRowsForTest();
    expect(dimmed()).toEqual([]);
  });

  it("DIMS the unattributed rows and hides none of them", () => {
    expect(toggleChatFilter()).toBe(true);
    expect(list().children.length).toBe(3);
    expect(dimmed()).toEqual(["/w/r/a/theirs.go"]);
  });

  it("keeps the ancestor folder of a changed file attributed", () => {
    toggleChatFilter();
    expect(dimmed()).not.toContain("/w/r/a");
  });

  it("clears every dim when toggled back off", () => {
    toggleChatFilter();
    expect(dimmed().length).toBe(1);
    expect(toggleChatFilter()).toBe(false);
    expect(dimmed()).toEqual([]);
  });

  it("re-derives attribution against the chat that is active NOW", () => {
    toggleChatFilter();
    expect(dimmed()).toEqual(["/w/r/a/theirs.go"]);
    setSessions([session(["a/theirs.go"])]);
    setActive("c1");
    _repaintRowsForTest();
    expect(dimmed()).toEqual(["/w/r/a/mine.go"]);
  });

  it("dims everything when the active chat changed nothing", () => {
    setSessions([session([])]);
    setActive("c1");
    toggleChatFilter();
    expect(dimmed().length).toBe(3);
  });

  it("still shows the git letter on a dimmed row — the filter is not a mask", () => {
    _setReposForTest([repo("r", [{ path: "a/theirs.go", status: "M" }])]);
    toggleChatFilter();
    expect(dimmed()).toEqual(["/w/r/a/theirs.go"]);
    expect(letters()).toEqual(["M", "M"]);
  });
});
