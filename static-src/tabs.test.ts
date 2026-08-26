// ---------------------------------------------------------------------------
// The tab strip as a PROJECTION: the store, the DOM it paints, and the
// interaction wired onto each row.
//
// NOTHING HERE OPENS A TAB BY CALLING A MUTATOR. The tab set is server-owned, so
// every case dispatches a mutation against the fake collection in
// `__test-helpers__/tabs-server.ts` and the `tabs_changed` frame that follows is
// what paints. That is why every open and every close is awaited: the promise
// resolves once the row is IN the projection, which is exactly the contract ~30
// production call sites depend on.
//
// IDS ARE OPAQUE AND SERVER-MINTED, so no case composes one. A row is addressed
// by `tabIdFor(kind, ref)` after it exists, which is also the only lookup
// production has, so a test cannot reach a row by a route the app cannot.
//
// The interleaving is the harness's `mode`, and both are exercised: these cases
// mostly run "event-first" because it is the cheapest seeding path, while
// `tabs-projection.test.ts` is where the ORDERING itself is the subject.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { Mock } from "vitest";
import { effect } from "@cplieger/reactive";

// Mock dependencies that tabs.ts imports at module level.
vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_CLOSE: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_FILES: "",
  ICON_TAB_RUN: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
  ICON_ALERT: "",
  ICON_PIN_FILLED: "",
}));
// The active tab is this SCREEN's, so it is written per field to its own module
// rather than folded into any shared document. Stubbed here so the cases below
// can read it back without touching localStorage.
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        // tabList must be a stable, document-attached element so the real
        // renderDOM appends focusable role=tab nodes we can drive in the
        // keyboard-navigation tests. Every other getter stays a throwaway.
        if (prop === "tabList") {
          let tl = document.getElementById("tab-list");
          if (tl === null) {
            tl = document.createElement("div");
            tl.id = "tab-list";
            document.body.appendChild(tl);
          }
          return tl;
        }
        return document.createElement("div");
      },
    },
  ),
}));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
// The two leaf stores the factory reads for a DISPLAY NAME. Mocked to their empty
// answer, so every label below is either the factory's derived default or the
// override an opener passed — and never a chat the suite had to stage.
vi.mock("./store.js", () => ({ get: vi.fn(() => undefined) }));
vi.mock("./run-store.js", () => ({ peekRunState: vi.fn(() => undefined) }));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
// The fake collection: `send` answers the four tab commands off it, `newOpID`
// mints the correlation id, and `apiGetTyped` is the boot read.
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({ apiGetTyped: m.tabListRead() })),
);

import {
  openTab,
  openEditorView,
  openRunTab,
  closeTab,
  activateTab,
  renameTab,
  hasTab,
  tabIdFor,
  getActiveTabId,
  getActiveTabKind,
  setOnEmpty,
  setTabPinned,
  showFilesView,
  toggleFilesView,
  _resetForTest,
} from "./tabs.js";
import { attachDrag, setReorderCallback } from "./tabs-drag.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import type { TabOpeners } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer, settleTabs } from "./__test-helpers__/tabs-server.js";
import type { TabKind } from "./types.js";

// The harness takes the sync layer's two entry points by hand: its own module
// cannot import them, because the `api-client.js` mock factory imports the
// harness and that module is what tabs-sync reads the collection through.
bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

// The store's own reorderTabs, captured HERE because tabs.ts registers it once
// at module load and every suite below clears mocks in its beforeEach. Driving it
// is exactly what a completed drag does.
const commitDrop = vi.mocked(setReorderCallback).mock.calls[0]?.[0];

// --- The injected half of the factory ---
//
// `materializeTab` refuses to build a spec with no openers registered, so every
// case gets these. They are also the only teardown channel left: a spec's
// `onClose` is the FACTORY's now, not a caller's, so a test that wants to watch a
// teardown watches the opener it delegates to.

interface Openers {
  chatShow: Mock<TabOpeners["chat"]["show"]>;
  chatClose: Mock<TabOpeners["chat"]["close"]>;
  editorShow: Mock<TabOpeners["editor"]["show"]>;
  editorClose: Mock<TabOpeners["editor"]["close"]>;
  runShow: Mock<TabOpeners["run"]["show"]>;
  runCancel: Mock<TabOpeners["run"]["cancel"]>;
}

let openers: Openers;

function registerOpeners(): void {
  openers = {
    chatShow: vi.fn<TabOpeners["chat"]["show"]>(),
    chatClose: vi.fn<TabOpeners["chat"]["close"]>(),
    editorShow: vi.fn<TabOpeners["editor"]["show"]>(),
    editorClose: vi.fn<TabOpeners["editor"]["close"]>(),
    runShow: vi.fn<TabOpeners["run"]["show"]>(),
    runCancel: vi.fn<TabOpeners["run"]["cancel"]>(),
  };
  registerTabOpeners({
    chat: { show: openers.chatShow, close: openers.chatClose, dot: () => "" },
    editor: { show: openers.editorShow, close: openers.editorClose },
    run: { show: openers.runShow, cancel: openers.runCancel },
  });
}

// --- Openers the cases below drive ---

/** Open a chat tab for `ref`. Awaited, because the row lands with the frame. */
function openChat(ref: string, opts: { activate?: boolean; parent?: string } = {}): Promise<void> {
  return openTab({ kind: "chat", ref, ...opts });
}

/** The projection's id for a chat, read back rather than composed. */
function chatID(ref: string): string {
  return tabIdFor("chat", ref);
}

/** Open several chats in order, and answer with their minted ids by ref. */
async function openChats(...refs: string[]): Promise<Record<string, string>> {
  const ids: Record<string, string> = {};
  for (const ref of refs) {
    await openChat(ref);
    ids[ref] = chatID(ref);
  }
  return ids;
}

/** renderDOM is RAF-deferred, so a DOM assertion has to wait for it. */
function paint(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

function rows(): HTMLElement[] {
  const list = document.getElementById("tab-list");
  return [...(list?.querySelectorAll<HTMLElement>("[data-tab-id]") ?? [])];
}

async function rowIDs(): Promise<string[]> {
  await paint();
  return rows().map((e) => e.dataset["tabId"] ?? "");
}

/** The rendered strip as REFS rather than opaque ids, which is what makes an
 *  order assertion readable. A row whose subject the projection has dropped
 *  answers with its raw id so a stray one is visible rather than blank. */
async function rowRefs(): Promise<string[]> {
  const ids = await rowIDs();
  const byID = new Map(tabServer.subjects().map((s) => [s.id, s.ref === "" ? s.kind : s.ref]));
  return ids.map((id) => byID.get(id) ?? id);
}

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerOpeners();
  resetActionFramework();
  _resetForTest();
  // Provide minimal DOM for renderDOM subscriber (tab-list element).
  document.body.innerHTML = '<div id="tab-list"></div>';
});

afterEach(() => {
  _resetTabsSyncForTest();
});

describe("openTab", () => {
  it("opens a new tab and activates it", async () => {
    expect.assertions(2);
    await openChat("a");
    expect(hasTab("chat", "a")).toBe(true);
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  it("opens one tab per distinct subject", async () => {
    expect.assertions(4);
    await openChats("a", "b", "c");
    for (const ref of ["a", "b", "c"]) {
      expect(hasTab("chat", ref)).toBe(true);
    }
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // The server's `(kind, ref)` uniqueness is what makes a second open idempotent,
  // and `created: false` is the only signal it gives: that mutation commits
  // nothing, so NO frame follows it. A caller waiting only for the frame would
  // wait forever, which is why openTab resolves from the response in that case.
  it("re-activates rather than opening a second tab for one subject", async () => {
    expect.assertions(3);
    await openChats("a", "b");
    await openChat("a");
    expect(getActiveTabId()).toBe(chatID("a"));
    expect(tabServer.sentOfType("open_tab")).toHaveLength(3);
    expect(await rowRefs()).toEqual(["a", "b"]);
  });

  // `activate: false` is what the automatic offers pass: the strip is the
  // reader's, so a tab a progress frame opened must not steal the screen.
  it("leaves the active tab alone when told not to activate", async () => {
    expect.assertions(2);
    await openChat("a");
    await openChat("b", { activate: false });
    expect(hasTab("chat", "b")).toBe(true);
    expect(getActiveTabId()).toBe(chatID("a"));
  });
});

// Editor tabs are MULTI-INSTANCE, and the SUBJECT is what makes them so: the
// server keys uniqueness on (kind, ref), and an editor tab's ref is its path. The
// `editor:<path>` id convention is gone with it — ids are opaque, so nothing
// composes or parses one.
describe("openEditorView (multi-instance by path)", () => {
  it("opens one tab per distinct path", async () => {
    expect.assertions(5);
    for (const p of ["src/a.ts", "docs/b.md", "out/shot.png"]) {
      await openEditorView(p);
    }
    for (const p of ["src/a.ts", "docs/b.md", "out/shot.png"]) {
      expect(hasTab("editor", p)).toBe(true);
    }
    expect(getActiveTabId()).toBe(tabIdFor("editor", "out/shot.png"));
    expect(await rowRefs()).toEqual(["src/a.ts", "docs/b.md", "out/shot.png"]);
  });

  // Two pills for one file is the same file: re-activate, never a second tab.
  it("re-activates rather than duplicating when the same path opens twice", async () => {
    expect.assertions(2);
    await openEditorView("src/a.ts");
    await openEditorView("docs/b.md");
    await openEditorView("src/a.ts");
    expect(getActiveTabId()).toBe(tabIdFor("editor", "src/a.ts"));
    expect(await rowRefs()).toEqual(["src/a.ts", "docs/b.md"]);
  });

  // The path travels in the SUBJECT's ref and in the route, never parsed back out
  // of the id.
  it("names the tab by basename while the subject keeps the whole path", async () => {
    expect.assertions(3);
    await openEditorView("a/b/c.ts");
    await paint();
    const id = tabIdFor("editor", "a/b/c.ts");
    const tab = document.querySelector(`[data-tab-id="${id}"]`);
    expect(tab?.textContent).toContain("c.ts");
    expect(tab?.textContent).not.toContain("a/b");
    expect(tabServer.idFor("editor", "a/b/c.ts")).toBe(id);
  });
});

describe("closeTab", () => {
  // The successor is the FIRST tab, not the neighbour, and that is the rule the
  // refactor states in one place: an active view naming no open tab falls back to
  // the first tab, checked on the boot read and on every applied removal. One rule
  // for two paths, rather than a neighbour walk here and a first-tab fallback
  // there that can disagree about which tab the reader lands on.
  it.each([
    {
      desc: "closing the active tab falls back to the FIRST tab",
      setup: ["a", "b", "c"],
      activate: "b",
      close: "b",
      expectActive: "a",
      expectHas: ["a", "c"],
      expectGone: ["b"],
    },
    {
      desc: "falls back to the first tab even when the closed one was last",
      setup: ["a", "b", "c"],
      activate: "c",
      close: "c",
      expectActive: "a",
      expectHas: ["a", "b"],
      expectGone: ["c"],
    },
    {
      desc: "closing an inactive tab preserves active",
      setup: ["a", "b", "c"],
      activate: "c",
      close: "a",
      expectActive: "c",
      expectHas: ["b", "c"],
      expectGone: ["a"],
    },
    {
      desc: "closing the only tab results in empty state",
      setup: ["a"],
      activate: "a",
      close: "a",
      expectActive: "",
      expectHas: [],
      expectGone: ["a"],
    },
  ])("$desc", async ({ setup, activate, close, expectActive, expectHas, expectGone }) => {
    expect.assertions(1 + expectHas.length + expectGone.length);
    await openChats(...setup);
    activateTab(chatID(activate));
    await closeTab(chatID(close));
    expect(getActiveTabId()).toBe(expectActive === "" ? "" : chatID(expectActive));
    for (const ref of expectHas) {
      expect(hasTab("chat", ref)).toBe(true);
    }
    for (const ref of expectGone) {
      expect(hasTab("chat", ref)).toBe(false);
    }
  });

  // Closing an id the collection does not hold is NOT an error: two devices can
  // close one tab, so the answer is an empty `closed` list and no frame.
  it("closing an id nothing holds leaves the strip alone", async () => {
    expect.assertions(3);
    await openChats("a", "b");
    await closeTab("tb_missing");
    expect(getActiveTabId()).toBe(chatID("b"));
    expect(hasTab("chat", "a")).toBe(true);
    expect(hasTab("chat", "b")).toBe(true);
  });

  // The projection REMOVES a tab before tearing it down, and these are the
  // properties that depend on it. A teardown that closes its own tab must find it
  // already gone; notifying first made every such callback an infinite loop. The
  // editor's teardown did exactly that (closeEditorFile ended in closeTab), so an
  // editor tab could not be closed by ×, middle-click or Delete — the click
  // recursed until the stack died.
  it("a teardown that closes its own tab does not recurse", async () => {
    expect.assertions(3);
    let closes = 0;
    openers.editorClose.mockImplementation((path: string) => {
      closes++;
      void closeTab(tabIdFor("editor", path));
    });
    await openEditorView("self.ts");
    await closeTab(tabIdFor("editor", "self.ts"));
    expect(closes).toBe(1);
    expect(hasTab("editor", "self.ts")).toBe(false);
    expect(getActiveTabId()).toBe("");
  });

  it("a teardown observes the tab as already gone", async () => {
    expect.assertions(1);
    let presentDuringTeardown = true;
    openers.editorClose.mockImplementation(() => {
      presentDuringTeardown = hasTab("editor", "t.ts");
    });
    await openEditorView("t.ts");
    await closeTab(tabIdFor("editor", "t.ts"));
    expect(presentDuringTeardown).toBe(false);
  });

  it("removes the named tab and only that tab when a cascade precedes the splice", async () => {
    expect.assertions(4);
    await openChat("before");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    await openChat("after");
    await closeTab(chatID("p"));
    expect(hasTab("chat", "p")).toBe(false);
    expect(hasTab("chat", "c")).toBe(false);
    expect(hasTab("chat", "before")).toBe(true);
    expect(hasTab("chat", "after")).toBe(true);
  });

  // A refused close leaves the strip EXACTLY as it was, because nothing renders
  // optimistically. That is the whole reason the projection paints from the frame
  // rather than from the gesture.
  it("leaves the tab in place when the mutation fails", async () => {
    expect.assertions(3);
    await openChats("a", "b");
    tabServer.failNext("close_tab");
    await closeTab(chatID("b"));
    expect(hasTab("chat", "b")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "b"]);
    expect(openers.chatClose).not.toHaveBeenCalled();
  });
});

// Closing the LAST tab is the one close whose store state is indistinguishable
// from the pre-boot state, and the DOM subscriber used to skip it on exactly that
// test. So the closed row kept its slot in the strip, un-animated, until the NEXT
// render — which is the one the empty-state respawn triggers 500ms later. Both
// animations then played at once and the new row was inserted in front of a row
// that still occupied the strip, so it appeared beside its predecessor and moved
// when the predecessor finally collapsed.
describe("closing the last tab", () => {
  it("starts the closed row's exit on close, not on the respawn", async () => {
    expect.assertions(2);
    await openChat("only");
    await paint();
    await closeTab(chatID("only"));
    await paint();
    expect(rows()).toHaveLength(1);
    expect(rows()[0]?.classList.contains("exiting")).toBe(true);
  });

  // The whole point of rendering the empty state: the respawn must find an empty
  // strip. No app stylesheet is loaded, so no animation runs and the removal is
  // driven by hand here — the browser's animationend does it, and the exit is
  // 0.18s against the 500ms empty-state delay.
  it("leaves the strip empty before the empty-state callback fires", async () => {
    expect.assertions(3);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChat("only");
    await paint();
    await closeTab(chatID("only"));
    await paint();
    rows()[0]?.dispatchEvent(new Event("animationend"));
    expect(rows()).toHaveLength(0);
    expect(onEmpty).not.toHaveBeenCalled();
    // Re-opening cancels the pending respawn, so no timer outlives the test.
    await openChat("respawned");
    expect(onEmpty).not.toHaveBeenCalled();
  });

  // A close driven by ANOTHER DEVICE must not mint a chat here. Nobody asked for
  // one, and it would propagate back as an addition every other device has to
  // absorb — which is the shape of the loop that minted a chat every 1.5s on the
  // live instance. The provenance is `op_id` correlation: a frame carrying an op
  // this device minted is local, and a frame carrying none is not.
  it("does not respawn when the LAST tab was closed remotely", async () => {
    expect.assertions(2);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChat("only");
    tabServer.closeRemotely(chatID("only"));
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // The local close still respawns: an empty strip is a dead end the reader did
    // not ask for when they closed the tab themselves.
    await openChat("mine");
    await closeTab(chatID("mine"));
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).toHaveBeenCalledTimes(1);
  });
});

describe("activateTab", () => {
  it.each([
    { desc: "activates existing tab", target: "a", expectActive: "a" },
    { desc: "activating already-active tab is a no-op", target: "b", expectActive: "b" },
  ])("$desc", async ({ target, expectActive }) => {
    expect.assertions(1);
    await openChats("a", "b");
    activateTab(chatID(target));
    expect(getActiveTabId()).toBe(chatID(expectActive));
  });

  it("activating a tab nothing holds is a no-op", async () => {
    expect.assertions(1);
    await openChats("a", "b");
    activateTab("tb_missing");
    expect(getActiveTabId()).toBe(chatID("b"));
  });
});

// getActiveTabKind is READ INSIDE AN EFFECT (the toolbar's find affordance in
// app.ts), so it has to subscribe to the tab set or that effect never re-runs on
// a switch. It used to be a plain array scan, which left the affordance leaning
// entirely on the BUS_TAB_CHANGED fallback — and that event is deduped on the
// active tab ID, so it is silent for a sub-tab switch inside one tab. The
// measured symptom was the /git/sources filter button staying visible and inert.
describe("getActiveTabKind is reactive", () => {
  it("re-runs an effect that reads it when the active tab changes", async () => {
    expect.assertions(3);
    await openChat("a");
    await openTab({ kind: "settings" });

    const seen: (TabKind | null)[] = [];
    const stop = effect(() => {
      seen.push(getActiveTabKind());
    });
    expect(seen).toEqual(["settings"]); // the settings tab is active after opening

    activateTab(chatID("a"));
    expect(seen.at(-1)).toBe("chat");

    activateTab(tabIdFor("settings"));
    expect(seen.at(-1)).toBe("settings");
    stop();
  });

  it("re-runs when the active tab is CLOSED and another takes over", async () => {
    expect.assertions(1);
    await openChat("a");
    await openTab({ kind: "settings" });

    const seen: (TabKind | null)[] = [];
    const stop = effect(() => {
      seen.push(getActiveTabKind());
    });

    await closeTab(tabIdFor("settings"));
    expect(seen.at(-1)).toBe("chat");
    stop();
  });
});

// A name is the one field of a spec a caller can override, and it is recorded
// against the SUBJECT rather than the row: it arrives at the DISPATCH site, before
// the server has minted an id, so keying it on (kind, ref) is what lets the row be
// BUILT with the right label instead of snapping to it a frame later.
describe("renameTab and the name a row renders", () => {
  it("renames an existing tab", async () => {
    expect.assertions(2);
    await openChat("a");
    renameTab(chatID("a"), "Renamed");
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Renamed");
    expect(hasTab("chat", "a")).toBe(true);
  });

  it("renaming a tab nothing holds is a no-op", async () => {
    expect.assertions(1);
    await openChat("a");
    expect(() => {
      renameTab("tb_missing", "Whatever");
    }).not.toThrow();
  });

  // The override is applied when the row is BUILT, not after: a caller's name
  // reaches `nameOverrides` before the dispatch, so the first paint carries it.
  it("renders a caller's name from the first frame, never the factory placeholder", async () => {
    expect.assertions(1);
    await openRunTab("wf-1", "Nightly sweep");
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Nightly sweep");
  });

  // A re-list rebuilds every row from the factory, so an override that lived only
  // on the row would be lost by any gap or 409.
  it("survives a re-list, which rebuilds every row", async () => {
    expect.assertions(1);
    await openRunTab("wf-1", "Nightly sweep");
    // A version two past local: the sync layer stops applying and re-lists.
    tabServer.emitRaw({ version: tabServer.version() + 2 });
    await settleTabs();
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Nightly sweep");
  });
});

// hasTab is keyed by `(kind, ref)` rather than by id, and that re-key is the
// point: ids are opaque, so a consumer holding a chat id or a path can no longer
// construct one.
describe("hasTab", () => {
  it("returns false for an empty projection", () => {
    expect.assertions(1);
    expect(hasTab("chat", "a")).toBe(false);
  });

  it("returns true after open, false after close", async () => {
    expect.assertions(2);
    await openChat("a");
    expect(hasTab("chat", "a")).toBe(true);
    await closeTab(chatID("a"));
    expect(hasTab("chat", "a")).toBe(false);
  });

  it("answers per SUBJECT, so one kind's ref does not satisfy another's", async () => {
    expect.assertions(3);
    await openEditorView("src/a.ts");
    expect(hasTab("editor", "src/a.ts")).toBe(true);
    expect(hasTab("chat", "src/a.ts")).toBe(false);
    // A singleton's ref is empty, which is the one kind whose identity is its
    // kind.
    expect(hasTab("settings")).toBe(false);
  });
});

describe("keyboard navigation (real tabs.ts handler via rendered tab nodes)", () => {
  async function renderTabs(...refs: string[]): Promise<HTMLElement[]> {
    await openChats(...refs);
    await paint();
    const list = document.getElementById("tab-list");
    if (list === null) {
      throw new Error("tab-list missing");
    }
    return [...list.querySelectorAll<HTMLElement>('[role="tab"]')];
  }

  it("ArrowRight moves focus to the next tab and wraps past the last", async () => {
    expect.assertions(3);
    const nodes = await renderTabs("a", "b", "c");
    expect(nodes).toHaveLength(3);

    nodes[0]?.focus();
    nodes[0]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(nodes[1]);

    nodes[2]?.focus();
    nodes[2]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(document.activeElement).toBe(nodes[0]);
  });

  it("ArrowLeft moves focus to the previous tab and wraps before the first", async () => {
    expect.assertions(2);
    const nodes = await renderTabs("a", "b", "c");

    nodes[2]?.focus();
    nodes[2]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(nodes[1]);

    nodes[0]?.focus();
    nodes[0]?.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
  });

  it("Home and End jump to the first and last tab", async () => {
    expect.assertions(2);
    const nodes = await renderTabs("a", "b", "c");

    nodes[1]?.focus();
    nodes[1]?.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(document.activeElement).toBe(nodes[0]);

    nodes[1]?.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
  });

  it("Enter activates the focused tab", async () => {
    expect.assertions(1);
    const nodes = await renderTabs("a", "b", "c");
    // c is active (last opened); Enter on the first tab's node activates it.
    nodes[0]?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // A close is a ROUND TRIP now, so focus has to move before the dispatch: waiting
  // for the removal to land would leave focus on <body> for the whole flight, and
  // the row the keyboard user is standing on is the element being removed.
  it("Delete moves focus to a surviving sibling before the close lands", async () => {
    expect.assertions(2);
    const nodes = await renderTabs("a", "b", "c");
    nodes[1]?.focus();
    tabServer.setMode("manual");
    nodes[1]?.dispatchEvent(new KeyboardEvent("keydown", { key: "Delete", bubbles: true }));
    expect(document.activeElement).toBe(nodes[2]);
    // The strip is untouched until the frame arrives, which is the honest surface
    // for a close that might be refused.
    expect(await rowIDs()).toHaveLength(3);
    tabServer.setMode("event-first");
    tabServer.flushFrames();
    await settleTabs();
  });
});

describe("setTabDirty (editor unsaved indicator)", () => {
  it("shows a steady dirty dot when dirty and clears it when clean", async () => {
    expect.assertions(4);
    const { setTabDirty } = await import("./tabs.js");
    await openEditorView("/a.ts");
    await paint();
    const id = tabIdFor("editor", "/a.ts");
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    const dot = row?.querySelector<HTMLElement>(".tab-status-dot");
    const sr = row?.querySelector<HTMLElement>(".tab-status-sr");
    if (dot === null || dot === undefined || sr === null || sr === undefined) {
      throw new Error("dot missing");
    }

    setTabDirty(id, true);
    expect(dot.dataset["status"]).toBe("dirty");
    // The announced word rides the same write, so an editor tab is not the one
    // surface where the dot is colour-only.
    expect(sr.textContent).toBe(", unsaved changes");

    setTabDirty(id, false);
    // The attribute is REMOVED rather than emptied: `[data-status]` alone is the
    // CSS reveal condition, so an empty value would leave a clean file's tab
    // showing an idle-styled dot.
    expect(dot.hasAttribute("data-status")).toBe(false);
    expect(sr.textContent).toBe("");
  });

  it("no-ops when the tab is not mounted", async () => {
    expect.assertions(1);
    const { setTabDirty } = await import("./tabs.js");
    expect(() => {
      setTabDirty("tb_missing", true);
    }).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Sub-tabs.
//
// A sub-tab is a SUBJECT fact (`TabSubject.Parent`), set at open and never
// reassigned — which is what makes a parent cycle unrepresentable and why there
// is no reparent command. The strip reads it to lay itself out, so nothing here
// mentions a chat, a run or a tangent.
// ---------------------------------------------------------------------------

describe("sub-tabs", () => {
  /** Open a chat nested under the tab already open for `parentRef`. */
  function openChild(ref: string, parentRef: string): Promise<void> {
    return openTab({ kind: "chat", ref, parent: chatID(parentRef) });
  }

  it("sorts a child immediately after its parent, not at the end", async () => {
    expect.assertions(1);
    await openChats("p", "other");
    await openChild("c", "p");
    expect(await rowRefs()).toEqual(["p", "c", "other"]);
  });

  it("keeps several children in creation order under one parent", async () => {
    expect.assertions(1);
    await openChats("p", "other");
    await openChild("c1", "p");
    await openChild("c2", "p");
    expect(await rowRefs()).toEqual(["p", "c1", "c2", "other"]);
  });

  it("indents a child and leaves a top-level tab flat", async () => {
    expect.assertions(2);
    await openChat("p");
    await openChild("c", "p");
    await paint();
    const child = rows().find((r) => r.dataset["tabId"] === chatID("c"));
    const parent = rows().find((r) => r.dataset["tabId"] === chatID("p"));
    expect(child?.classList.contains("tab-child")).toBe(true);
    expect(parent?.classList.contains("tab-child")).toBe(false);
  });

  // A tab nobody can see is worse than a tab in the wrong place, which is the
  // server's rule for an unknown parent and the strip's for a parent it does not
  // hold.
  it("falls back to top-level when the parent is not open", async () => {
    expect.assertions(2);
    await openTab({ kind: "chat", ref: "orphan", parent: "tb_missing" });
    expect(hasTab("chat", "orphan")).toBe(true);
    expect(await rowRefs()).toEqual(["orphan"]);
  });

  // A parent and its children go as ONE mutation, so the removal arrives as one
  // frame naming every id.
  it("closes children with their parent, in one frame", async () => {
    expect.assertions(4);
    await openChat("p");
    await openChild("c1", "p");
    await openChild("c2", "p");
    const before = tabServer.version();
    await closeTab(chatID("p"));
    expect(hasTab("chat", "p")).toBe(false);
    expect(hasTab("chat", "c1")).toBe(false);
    expect(hasTab("chat", "c2")).toBe(false);
    expect(tabServer.version()).toBe(before + 1);
  });

  // Children first, so a child's teardown never runs against a parent that has
  // already gone.
  it("tears down children before the parent", async () => {
    expect.assertions(1);
    const order: string[] = [];
    openers.chatClose.mockImplementation((ref: string) => {
      order.push(ref);
    });
    await openChat("p");
    await openChild("c", "p");
    await closeTab(chatID("p"));
    expect(order).toEqual(["c", "p"]);
  });

  it("closing a child leaves the parent alone", async () => {
    expect.assertions(2);
    await openChat("p");
    await openChild("c", "p");
    await closeTab(chatID("c"));
    expect(hasTab("chat", "p")).toBe(true);
    expect(hasTab("chat", "c")).toBe(false);
  });

  // `owns: false` is the VIEW case: dismissing a view must not kill the work it
  // was watching. It is a subject fact rather than a property of the kind — a
  // launcher-owned run and a run REVIEW share (kind, ref) and differ only here.
  it("does not tear down a tab that owns nothing", async () => {
    expect.assertions(2);
    await openRunTab("wf-1", "A run", { owns: false });
    await closeTab(tabIdFor("run", "wf-1"));
    expect(hasTab("run", "wf-1")).toBe(false);
    expect(openers.runCancel).not.toHaveBeenCalled();
  });

  it("tears down an owning tab", async () => {
    expect.assertions(2);
    await openRunTab("wf-1", "A run", { owns: true });
    await closeTab(tabIdFor("run", "wf-1"));
    expect(hasTab("run", "wf-1")).toBe(false);
    expect(openers.runCancel).toHaveBeenCalledTimes(1);
  });

  it("still removes an owns:false child when its parent closes", async () => {
    expect.assertions(2);
    await openChat("p");
    await openRunTab("wf-1", "A view", { parent: chatID("p"), owns: false });
    await closeTab(chatID("p"));
    expect(hasTab("run", "wf-1")).toBe(false);
    expect(openers.runCancel).not.toHaveBeenCalled();
  });

  // A cascade reaches every DESCENDANT, not just the direct children, and an
  // owns:false one among them still skips its teardown through its ownership
  // check.
  it("tears down every owning descendant of a closed parent", async () => {
    expect.assertions(5);
    const order: string[] = [];
    openers.chatClose.mockImplementation((ref: string) => {
      order.push(ref);
    });
    await openChat("p");
    await openChild("c", "p");
    await openChild("gc", "c");
    await openRunTab("wf-view", "A view", { parent: chatID("p"), owns: false });
    await closeTab(chatID("p"));
    expect(order).toEqual(["gc", "c", "p"]);
    expect(openers.runCancel).not.toHaveBeenCalled();
    for (const ref of ["p", "c", "gc"]) {
      expect(hasTab("chat", ref)).toBe(false);
    }
  });

  // A reorder names TOP-LEVEL ids and the projection expands them, so a drop on a
  // strip holding a sub-tab is a valid exact set rather than a 409.
  it("expands a drop's top-level order to the exact set the server demands", async () => {
    expect.assertions(2);
    await openChat("p");
    await openChild("c", "p");
    await openChat("other");
    commitDrop?.([chatID("other"), chatID("p")]);
    await settleTabs();
    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(1);
    expect(await rowRefs()).toEqual(["other", "p", "c"]);
  });

  it("is not independently draggable", async () => {
    expect.assertions(1);
    await openChat("p");
    await openChild("c", "p");
    await paint();
    // attachDrag runs once per DRAGGABLE row; the child must not get a handle,
    // because its position is its parent's rather than its own.
    expect(vi.mocked(attachDrag)).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// Pinned tabs.
//
// `TabSubject.Pinned` is stored server-side; the pinned-ahead-of-unpinned
// PARTITION is the client's RENDERING rule over the order the collection was
// given, which is what makes an unpin leave the tab exactly where it was.
//
// The partition lives in the ARRAY rather than in the render, because two
// mechanisms read DOM order back as the truth: a drop reads the new order out of
// the strip and the keyboard arrows walk the rendered children.
// ---------------------------------------------------------------------------

describe("pinned tabs", () => {
  it("moves a pinned tab ahead of every unpinned one", async () => {
    expect.assertions(1);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("c"), true);
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
  });

  it("does not reshuffle the pinned run when another tab joins it", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("c"), true);
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
    await setTabPinned(chatID("a"), true);
    // The partition is stable, so pinning `a` only guarantees it sits above the
    // unpinned tabs — it does not overtake a pin that was already ahead of it.
    // Reordering WITHIN the run is what drag is for.
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
  });

  it("leaves a tab where it is when the pin is removed", async () => {
    expect.assertions(2);
    await openChats("a", "b");
    await setTabPinned(chatID("b"), true);
    expect(await rowRefs()).toEqual(["b", "a"]);
    await setTabPinned(chatID("b"), false);
    expect(await rowRefs()).toEqual(["b", "a"]);
  });

  it("opens a new unpinned tab after the pinned run", async () => {
    expect.assertions(1);
    await openChat("a");
    await setTabPinned(chatID("a"), true);
    await openChat("b");
    expect(await rowRefs()).toEqual(["a", "b"]);
  });

  // The partition is what enforces this: a drop commits through `reorder_tabs`,
  // whose frame re-partitions, so the illegal position snaps back rather than
  // being refused mid-drag inside the drag subsystem's index arithmetic.
  it("cannot be dragged below an unpinned tab", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    expect(commitDrop).toBeTypeOf("function");
    commitDrop?.([chatID("b"), chatID("c"), chatID("a")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
  });

  it("still honours a drag that reorders within the pinned run", async () => {
    expect.assertions(1);
    await openChats("a", "b", "c");
    await setTabPinned(chatID("a"), true);
    await setTabPinned(chatID("b"), true);
    commitDrop?.([chatID("b"), chatID("a"), chatID("c")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["b", "a", "c"]);
  });

  it("carries a parent's children with it", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openChat("other");
    await setTabPinned(chatID("p"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "other"]);
    await setTabPinned(chatID("other"), true);
    // `other` is pinned second, so it lands after the whole `p` group rather than
    // between a parent and its child.
    expect(await rowRefs()).toEqual(["p", "kid", "other"]);
  });

  // Nested side chats are reachable: a tangent parents a new conversation on the
  // ACTIVE chat, which may itself be one. A grouping that only recognised a DIRECT
  // child made the grandchild an orphan top-level group, so pinning any other tab
  // could sort it away from the tab its own parent names.
  it("carries a whole descendant tree, not just direct children", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openTab({ kind: "chat", ref: "grandkid", parent: chatID("kid") });
    await openChat("other");
    await setTabPinned(chatID("p"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "grandkid", "other"]);
    // The second pin is what exposed it: an orphaned grandchild group sat BETWEEN
    // two pinned groups, so the partition hoisted `other` over it and the
    // grandchild ended up behind a stranger.
    await setTabPinned(chatID("other"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "grandkid", "other"]);
  });

  // Same assumption, second site: a drop names top-level ids only, so the walk
  // that reproduces the strip has to descend.
  it("keeps a nested descendant behind its parent through a reorder", async () => {
    expect.assertions(1);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openTab({ kind: "chat", ref: "grandkid", parent: chatID("kid") });
    await openChat("other");
    commitDrop?.([chatID("p"), chatID("other")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["p", "kid", "grandkid", "other"]);
  });

  // A sub-tab's position is its parent's, the same rule that denies it a drag
  // handle — and the refusal is LOCAL, before it costs a round trip.
  it("refuses to pin a sub-tab without asking the server", async () => {
    expect.assertions(2);
    await openChat("p");
    await openTab({ kind: "chat", ref: "kid", parent: chatID("p") });
    await openChat("other");
    await setTabPinned(chatID("kid"), true);
    expect(await rowRefs()).toEqual(["p", "kid", "other"]);
    expect(tabServer.sentOfType("pin_tab")).toHaveLength(0);
  });

  // A repeat of the pin already in force sends nothing either: the subject already
  // says so, and the server would commit nothing and emit nothing.
  it("sends nothing when the pin is already what was asked for", async () => {
    expect.assertions(2);
    await openChat("a");
    await setTabPinned(chatID("a"), true);
    expect(tabServer.sentOfType("pin_tab")).toHaveLength(1);
    await setTabPinned(chatID("a"), true);
    expect(tabServer.sentOfType("pin_tab")).toHaveLength(1);
  });

  it("marks the pinned row for the pin glyph", async () => {
    expect.assertions(3);
    await openChat("a");
    await setTabPinned(chatID("a"), true);
    await paint();
    const id = chatID("a");
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    expect(row?.classList.contains("tab-pinned")).toBe(true);
    // The glyph itself is decorative; the announced state is the .sr-only word.
    expect(row?.querySelector(".tab-pin .sr-only")?.textContent).toBe("Pinned");
    await setTabPinned(id, false);
    await paint();
    expect(
      document
        .querySelector<HTMLElement>(`[data-tab-id="${id}"]`)
        ?.classList.contains("tab-pinned"),
    ).toBe(false);
  });

  // A pin arrives as a `changed` upsert, so the row is REUSED: the subject is
  // replaced wholesale while the spec, the name override and the dot stay. Nothing
  // may re-materialize, or a pin would re-run a tab's activation wiring.
  it("keeps the row's local half across a pin", async () => {
    expect.assertions(2);
    await openChat("a");
    renameTab(chatID("a"), "Renamed");
    await setTabPinned(chatID("a"), true);
    await paint();
    expect(rows()[0]?.querySelector(".tab-name")?.textContent).toBe("Renamed");
    expect(openers.chatShow).toHaveBeenCalledTimes(1);
  });

  // A pin applied on ANOTHER device arrives as the same `changed` frame, so the
  // partition has to move the row with no local gesture behind it.
  it("re-partitions on a pin this device did not make", async () => {
    expect.assertions(1);
    await openChats("a", "b");
    const bID = chatID("b");
    const subject = tabServer.subjects().find((s) => s.id === bID);
    if (subject === undefined) {
      throw new Error("b not in the collection");
    }
    tabServer.emitRaw({
      changed: { ...subject, pinned: true },
      version: tabServer.version() + 1,
    });
    await settleTabs();
    expect(await rowRefs()).toEqual(["b", "a"]);
  });
});

describe("showFilesView vs toggleFilesView", () => {
  // "Toggle" and "go to" are different verbs, and the files view only had the
  // toggle. So a caller whose intent was "the browser must be visible for what I
  // am about to render into it" CLOSED it whenever it already was — which is what
  // find-in-files did from the browser's own search button. The search bar then
  // opened over a departed view and the browser came back in search mode on its
  // next open, read by the user as a search state leaking between tabs.

  it("shows the browser when it is not open", async () => {
    expect.assertions(2);
    await openChat("c-1");
    await showFilesView();
    expect(hasTab("files")).toBe(true);
    expect(getActiveTabId()).toBe(tabIdFor("files"));
  });

  it("activates the browser when it is open but not active", async () => {
    expect.assertions(2);
    await showFilesView();
    await openChat("c-1");
    expect(getActiveTabId()).toBe(chatID("c-1"));
    await showFilesView();
    expect(getActiveTabId()).toBe(tabIdFor("files"));
  });

  it("is a NO-OP when the browser is already active, where the toggle closes", async () => {
    expect.assertions(4);
    await showFilesView();
    expect(getActiveTabId()).toBe(tabIdFor("files"));

    await showFilesView();
    expect(hasTab("files"), "show must never close the tab it is asked to show").toBe(true);
    expect(getActiveTabId()).toBe(tabIdFor("files"));

    // The contrast is the point: the toolbar button still wants a toggle.
    await toggleFilesView();
    expect(hasTab("files")).toBe(false);
  });

  // A show that closed the tab ran the files view's teardown (the factory's
  // `onClose`, which drops the listing and the search bar) against a search the
  // caller was in the middle of opening.
  it("dispatches nothing at all on a second show", async () => {
    expect.assertions(2);
    await showFilesView();
    const before = tabServer.sent().length;
    await showFilesView();
    expect(tabServer.sent()).toHaveLength(before);
    expect(tabServer.sentOfType("close_tab")).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// A tab this device did not close.
//
// The provenance question has exactly one mechanism, and it is `op_id`: a frame
// carrying an op this device minted is its own echo, and a frame carrying none is
// another device's. What that decides is ONE thing — whether a removal's teardown
// re-dispatches. Every LOCAL cleanup runs in both cases, or this device keeps a
// store row, a dock card and composer state for a tab that is gone.
// ---------------------------------------------------------------------------

describe("close provenance", () => {
  it("tells the teardown the close was local when this device dispatched it", async () => {
    expect.assertions(1);
    await openChat("a");
    await closeTab(chatID("a"));
    expect(openers.chatClose).toHaveBeenCalledWith("a", { remote: false });
  });

  it("tells the teardown the close came from another device", async () => {
    expect.assertions(2);
    await openChat("a");
    tabServer.closeRemotely(chatID("a"));
    await settleTabs();
    // The teardown STILL RUNS — that is the point. Only the dispatch inside it is
    // suppressed, which is chat.ts's decision to make rather than this module's.
    expect(openers.chatClose).toHaveBeenCalledWith("a", { remote: true });
    expect(hasTab("chat", "a")).toBe(false);
  });

  it("marks a cascaded child remote too, because the other device already cascaded", async () => {
    expect.assertions(1);
    const seen: { ref: string; remote: boolean }[] = [];
    openers.chatClose.mockImplementation((ref: string, opts: { remote: boolean }) => {
      seen.push({ ref, remote: opts.remote });
    });
    await openChat("parent");
    await openTab({ kind: "chat", ref: "child", parent: chatID("parent") });
    tabServer.closeRemotely(chatID("parent"));
    await settleTabs();
    expect(seen).toEqual([
      { ref: "child", remote: true },
      { ref: "parent", remote: true },
    ]);
  });
});
