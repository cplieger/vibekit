// @vitest-environment happy-dom
// Ctrl-F is scoped by the active TAB. What this pins is the routing and the two
// things that make it safe: exactly one destination per press (no third meaning
// on the chord), and a dispatcher that never consumes the event itself, so each
// find keeps its own second-press fall-through to the browser's native find.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { TabKind } from "./tabs.js";

const chatFind = vi.fn();
const filesFind = vi.fn();
let activeKind: TabKind | null = "chat";

vi.mock("./tabs.js", () => ({ getActiveTabKind: () => activeKind }));
vi.mock("./find-in-chat.js", () => ({ handleFindHotkey: (e: KeyboardEvent) => chatFind(e) }));
vi.mock("./files-search.js", () => ({
  handleFindInFilesHotkey: (e: KeyboardEvent) => filesFind(e),
}));

const { handleFindKey } = await import("./find-dispatch.js");

function ctrlF(): KeyboardEvent {
  return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
}

beforeEach(() => {
  chatFind.mockReset();
  filesFind.mockReset();
  activeKind = "chat";
});

describe("handleFindKey", () => {
  it("means find-in-files over a files tab", () => {
    activeKind = "files";
    handleFindKey(ctrlF());
    expect(filesFind).toHaveBeenCalledTimes(1);
    expect(chatFind).not.toHaveBeenCalled();
  });

  it("means find-in-files over an editor tab", () => {
    // The tab's kind, not its route's: an editor tab routes as `file`, and
    // keying on that would be a second vocabulary for one question.
    activeKind = "editor";
    handleFindKey(ctrlF());
    expect(filesFind).toHaveBeenCalledTimes(1);
    expect(chatFind).not.toHaveBeenCalled();
  });

  it("means find-in-chat over a chat tab", () => {
    activeKind = "chat";
    handleFindKey(ctrlF());
    expect(chatFind).toHaveBeenCalledTimes(1);
    expect(filesFind).not.toHaveBeenCalled();
  });

  it("leaves every other tab kind on the chat find, which guards its own context", () => {
    // find-in-chat already refuses unless the chat view is the active context,
    // so the default branch cannot open a search over settings or git.
    for (const kind of ["settings", "git", "history", "docs", "run", "plan"] as TabKind[]) {
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
    for (const kind of ["files", "editor", "chat"] as TabKind[]) {
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
