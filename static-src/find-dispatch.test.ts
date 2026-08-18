// @vitest-environment happy-dom
// Ctrl-F is scoped by the active TAB, and so is the toolbar button that opens
// it. What this pins is the routing and the three things that make it safe:
// exactly one destination per press (no extra meaning on the chord), a
// dispatcher that never consumes the event itself so each find keeps its own
// second-press fall-through to native find, and a DECLINE that falls through
// rather than swallowing the key.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Mock } from "vitest";
import type { TabKind } from "./tabs.js";
import type { PageFind } from "./find-registry.js";

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
  editorFindAvailable: () => editorClaims,
}));

const { handleFindKey, toggleFindForActiveTab, findAffordanceForActiveTab } =
  await import("./find-dispatch.js");
const { registerFind, _resetFindRegistry } = await import("./find-registry.js");

/** A page find that accepts. The three-function shape is the contract every
 *  destination answers now — the chord opens, the button toggles, and `focused`
 *  is what separates a second press (which belongs to the browser) from a first. */
function pageFindStub(over: Partial<PageFind> = {}): PageFind & { open: Mock; toggle: Mock } {
  return {
    open: vi.fn(() => true),
    toggle: vi.fn(),
    focused: () => false,
    kind: () => "filter",
    ...over,
  } as PageFind & { open: Mock; toggle: Mock };
}

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

  it("opens a registered page box on docs, history and git, consuming the press", () => {
    for (const kind of ["docs", "history", "git"] as TabKind[]) {
      _resetFindRegistry();
      chatFind.mockReset();
      const find = pageFindStub();
      registerFind(kind, find);
      activeKind = kind;
      const e = ctrlF();
      handleFindKey(e);
      expect(find.open).toHaveBeenCalledTimes(1);
      expect(e.defaultPrevented).toBe(true);
      expect(chatFind).not.toHaveBeenCalled();
    }
  });

  it("falls through when a page box DECLINES (the git Sources tab filters nothing)", () => {
    registerFind("git", pageFindStub({ open: () => false }));
    activeKind = "git";
    const e = ctrlF();
    handleFindKey(e);
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(false);
  });

  it("leaves a SECOND press to the browser, which is the escape hatch", () => {
    // The a11y justification for overriding the chord at all: a press from inside
    // the open box is not consumed, so native find still opens. Every other
    // destination keeps the same hatch.
    const find = pageFindStub({ focused: () => true });
    registerFind("docs", find);
    activeKind = "docs";
    const e = ctrlF();
    handleFindKey(e);
    expect(find.open).not.toHaveBeenCalled();
    expect(e.defaultPrevented).toBe(false);
    expect(chatFind).toHaveBeenCalledTimes(1);
  });

  it("falls through when a page never registered at all", () => {
    activeKind = "history";
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
  });

  it("does not reach a page box on a non-find chord", () => {
    const find = pageFindStub();
    registerFind("docs", find);
    activeKind = "docs";
    handleFindKey(new KeyboardEvent("keydown", { key: "g", ctrlKey: true, cancelable: true }));
    expect(find.open).not.toHaveBeenCalled();
  });

  it("routes a plan tab to the transcript, which is the view it shares", () => {
    activeKind = "plan";
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(filesFind).not.toHaveBeenCalled();
  });

  it("leaves the chord ALONE on a page with no search at all", () => {
    // Settings and a run view. They used to reach the transcript's handler through
    // the dispatcher's default branch, which declined because the chat view was
    // hidden — the right outcome by accident, and the reason a visible magnifier
    // sat there doing nothing. The table names them now, so nothing is offered and
    // the chord is the browser's.
    for (const kind of ["settings", "run"] as TabKind[]) {
      chatFind.mockReset();
      filesFind.mockReset();
      activeKind = kind;
      const e = ctrlF();
      handleFindKey(e);
      expect(chatFind).not.toHaveBeenCalled();
      expect(filesFind).not.toHaveBeenCalled();
      expect(e.defaultPrevented, "native find must still open").toBe(false);
    }
  });

  it("does nothing on the toolbar button there either, rather than half-acting", () => {
    for (const kind of ["settings", "run"] as TabKind[]) {
      chatToggle.mockReset();
      activeKind = kind;
      toggleFindForActiveTab();
      expect(chatToggle).not.toHaveBeenCalled();
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
    // The button TOGGLES where the chord OPENS: a second click closes the box,
    // while a second press hands the chord to the browser.
    const find = pageFindStub();
    registerFind("docs", find);
    activeKind = "docs";
    toggleFindForActiveTab();
    expect(find.toggle).toHaveBeenCalledTimes(1);
    expect(find.open).not.toHaveBeenCalled();
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

describe("findAffordanceForActiveTab", () => {
  // What the toolbar's magnifier paints, and whether it is painted at all. A
  // button that stays visible where nothing can answer it is the same dead door
  // the routing above was written to remove — it was just the other half of it.
  it("is available on a chat tab and on the files browser", () => {
    for (const kind of ["chat", "plan", "files"] as TabKind[]) {
      activeKind = kind;
      expect(findAffordanceForActiveTab().available).toBe(true);
    }
  });

  it("is UNAVAILABLE on a page with no search at all", () => {
    for (const kind of ["settings", "run"] as TabKind[]) {
      activeKind = kind;
      expect(findAffordanceForActiveTab().available).toBe(false);
    }
  });

  it("follows the editor's own answer about the surface", () => {
    activeKind = "editor";
    editorClaims = true;
    expect(findAffordanceForActiveTab().available).toBe(true);
    // A diff pane, an image or rendered markdown.
    editorClaims = false;
    expect(findAffordanceForActiveTab().available).toBe(false);
  });

  it("asks a page's own predicate, and defaults to available when it has none", () => {
    activeKind = "git";
    registerFind("git", pageFindStub({ available: () => false }));
    expect(findAffordanceForActiveTab().available).toBe(false);
    registerFind("git", pageFindStub());
    expect(findAffordanceForActiveTab().available).toBe(true);
  });

  it("is unavailable on a page that registered nothing", () => {
    activeKind = "docs";
    expect(findAffordanceForActiveTab().available).toBe(false);
  });

  it("calls the three built-in finds SEARCHES, because each reaches past the viewport", () => {
    // The transcript enumerates server-side over the whole conversation, the file
    // browser greps the tree, and the editor scans a buffer the viewport shows a
    // fraction of. None of the three is a filter over what is painted.
    for (const kind of ["chat", "plan", "files", "editor"] as TabKind[]) {
      activeKind = kind;
      expect(findAffordanceForActiveTab().kind, kind).toBe("search");
    }
  });

  it("takes a page's OWN word for which of the two it is", () => {
    activeKind = "history";
    registerFind("history", pageFindStub({ kind: () => "search" }));
    expect(findAffordanceForActiveTab().kind).toBe("search");
    registerFind("history", pageFindStub({ kind: () => "filter" }));
    expect(findAffordanceForActiveTab().kind).toBe("filter");
  });
});
