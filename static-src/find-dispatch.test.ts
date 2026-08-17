// @vitest-environment happy-dom
// Ctrl-F is scoped by the active TAB, and so is the toolbar button that opens
// it. What this pins is the routing and the three things that make it safe:
// exactly one destination per press (no extra meaning on the chord), a
// dispatcher that never consumes the event itself so each find keeps its own
// second-press fall-through to native find, and a DECLINE that falls through
// rather than swallowing the key.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { TabKind } from "./tabs.js";

const chatFind = vi.fn();
const chatToggle = vi.fn();
const filesFind = vi.fn();
const filesToggle = vi.fn();
const editorToggle = vi.fn();
/** What the editor's hotkey handler answers: true = it claimed the press. */
let editorClaims = true;
let activeKind: TabKind | null = "chat";

vi.mock("./tabs.js", () => ({ getActiveTabKind: () => activeKind }));
vi.mock("./find-in-chat.js", () => ({
  handleFindHotkey: (e: KeyboardEvent) => chatFind(e),
  toggleChatFind: () => chatToggle(),
}));
vi.mock("./files-search.js", () => ({
  handleFindInFilesHotkey: (e: KeyboardEvent) => filesFind(e),
  toggleFilesSearch: () => filesToggle(),
}));
vi.mock("./editor-find.js", () => ({
  handleEditorFindHotkey: (e: KeyboardEvent) => {
    if (editorClaims) {
      e.preventDefault();
    }
    return editorClaims;
  },
  toggleEditorFind: () => editorToggle(),
}));

const { handleFindKey, toggleFindForActiveTab } = await import("./find-dispatch.js");
const { registerFind, _resetFindRegistry } = await import("./find-registry.js");

function ctrlF(): KeyboardEvent {
  return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
}

beforeEach(() => {
  chatFind.mockReset();
  chatToggle.mockReset();
  filesFind.mockReset();
  filesToggle.mockReset();
  editorToggle.mockReset();
  editorClaims = true;
  activeKind = "chat";
  _resetFindRegistry();
});

describe("handleFindKey", () => {
  it("means find-in-files over a files tab", () => {
    activeKind = "files";
    handleFindKey(ctrlF());
    expect(filesFind).toHaveBeenCalledTimes(1);
    expect(chatFind).not.toHaveBeenCalled();
  });

  it("means find-in-FILE over an editor tab, not find-in-files", () => {
    // The tab's kind, not its route's: an editor tab routes as `file`, and
    // keying on that would be a second vocabulary for one question.
    //
    // This is the routing item 7 changed. Ctrl-F on a file tab used to open the
    // recursive FILES search, which activates the browser view — so the chord
    // every editor binds to "search this document" switched you away from the
    // document you were editing. It searches the open buffer now.
    activeKind = "editor";
    handleFindKey(ctrlF());
    expect(filesFind).not.toHaveBeenCalled();
    expect(chatFind).not.toHaveBeenCalled();
  });

  it("falls through to native find when the editor DECLINES the press", () => {
    // A diff pane, an image, or rendered markdown: no line geometry, so a
    // counted match could not be reached. The editor declines, the chat handler
    // declines in turn (the chat view is hidden), and the browser's own find —
    // which reads those three surfaces perfectly well — gets the key.
    activeKind = "editor";
    editorClaims = false;
    const e = ctrlF();
    handleFindKey(e);
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(false);
  });

  it("means find-in-chat over a chat tab", () => {
    activeKind = "chat";
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(filesFind).not.toHaveBeenCalled();
  });

  it("focuses a registered page box on docs and history, consuming the press", () => {
    for (const kind of ["docs", "history"] as TabKind[]) {
      _resetFindRegistry();
      chatFind.mockReset();
      const focus = vi.fn(() => true);
      registerFind(kind, focus);
      activeKind = kind;
      const e = ctrlF();
      handleFindKey(e);
      expect(focus).toHaveBeenCalledTimes(1);
      expect(e.defaultPrevented).toBe(true);
      expect(chatFind).not.toHaveBeenCalled();
    }
  });

  it("falls through when a page box DECLINES (Workflows has no inventory to filter)", () => {
    registerFind("docs", () => false);
    activeKind = "docs";
    const e = ctrlF();
    handleFindKey(e);
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(false);
  });

  it("falls through when a page never registered at all", () => {
    activeKind = "history";
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
  });

  it("does not reach a page box on a non-find chord", () => {
    const focus = vi.fn(() => true);
    registerFind("docs", focus);
    activeKind = "docs";
    handleFindKey(new KeyboardEvent("keydown", { key: "g", ctrlKey: true, cancelable: true }));
    expect(focus).not.toHaveBeenCalled();
  });

  it("leaves every other tab kind on the chat find, which guards its own context", () => {
    // find-in-chat already refuses unless the chat view is the active context,
    // so the default branch cannot open a search over settings or git.
    for (const kind of ["settings", "git", "run", "plan"] as TabKind[]) {
      chatFind.mockReset();
      filesFind.mockReset();
      activeKind = kind;
      handleFindKey(ctrlF());
      expect(chatFind).toHaveBeenCalledTimes(1);
      expect(filesFind).not.toHaveBeenCalled();
    }
  });

  it("routes with no tab open at all", () => {
    activeKind = null;
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
  });

  it("never consumes the press itself, so each find owns its escape hatch", () => {
    for (const kind of ["files", "chat"] as TabKind[]) {
      activeKind = kind;
      const e = ctrlF();
      handleFindKey(e);
      expect(e.defaultPrevented).toBe(false);
    }
  });

  it("hands the SAME event to the destination, not a copy", () => {
    activeKind = "files";
    const e = ctrlF();
    handleFindKey(e);
    expect(filesFind).toHaveBeenCalledWith(e);
  });
});

describe("toggleFindForActiveTab", () => {
  // The BUTTON's half of the routing, and the reason it exists: #find-btn used
  // to call find-in-chat's opener directly, so on a files or editor tab it hit
  // that module's context guard (the chat view is hidden there), returned, and
  // did nothing at all. A visible control that does nothing on two of the app's
  // views is the dead door item 7 names.
  it("toggles the destination that belongs to the active tab", () => {
    const cases: { kind: TabKind; fn: () => void }[] = [
      { kind: "chat", fn: chatToggle },
      { kind: "files", fn: filesToggle },
      { kind: "editor", fn: editorToggle },
    ];
    for (const c of cases) {
      chatToggle.mockReset();
      filesToggle.mockReset();
      editorToggle.mockReset();
      activeKind = c.kind;
      toggleFindForActiveTab();
      expect(c.fn, `${c.kind} button should reach its own find`).toHaveBeenCalledTimes(1);
    }
  });

  it("reaches the same registered box the hotkey does, so button and chord agree", () => {
    const focus = vi.fn(() => true);
    registerFind("docs", focus);
    activeKind = "docs";
    toggleFindForActiveTab();
    expect(focus).toHaveBeenCalledTimes(1);
    expect(chatToggle).not.toHaveBeenCalled();
  });

  it("is inert rather than wrong on a page whose box is not there", () => {
    activeKind = "history";
    expect(() => {
      toggleFindForActiveTab();
    }).not.toThrow();
    expect(chatToggle).not.toHaveBeenCalled();
  });
});
