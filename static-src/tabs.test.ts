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
  ICON_TAB_AGENT: "",
  // roles.ts is in this graph now (tab-materialize.ts derives a delegate tab's
  // label from it), and Browser Mode links for real rather than reading
  // properties off a namespace object, so every name it imports has to be here.
  ICON_TAB_PLAN: "",
  ICON_TAB_SPEC: "",
  ICON_TAB_QUICK_SPEC: "",
  ICON_TAB_BUG: "",
  ICON_TAB_AUTONOMOUS: "",
  ICON_SUBAGENT_INTROSPECT: "",
  ICON_SUBAGENT_GATHERER: "",
  ICON_SUBAGENT_TASK: "",
  ICON_SUBAGENT_CREATOR: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
  ICON_TAB_SUBTAB: "",
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
        // keyboard-navigation tests. promptInput likewise: the last-tab close
        // moves focus to the composer, and the empty-state rollback reads the
        // typed text out of it. Every other getter stays a throwaway.
        if (prop === "tabList") {
          let tl = document.getElementById("tab-list");
          if (tl === null) {
            tl = document.createElement("div");
            tl.id = "tab-list";
            document.body.appendChild(tl);
          }
          return tl;
        }
        if (prop === "sidebar") {
          let sidebar = document.getElementById("sidebar");
          if (sidebar === null) {
            sidebar = document.createElement("aside");
            sidebar.id = "sidebar";
            document.body.appendChild(sidebar);
          }
          return sidebar;
        }
        if (prop === "promptInput") {
          let input = document.getElementById("prompt-input");
          if (input === null) {
            input = document.createElement("textarea");
            input.id = "prompt-input";
            document.body.appendChild(input);
          }
          return input;
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
// The two leaf stores the factory reads for a DISPLAY NAME, plus the three
// pointer writers the optimistic close moves (getActiveId / setActive /
// getSessions) — and everything else actions/chat.js links against, which is in
// the graph now that tabs.ts reaches composer-state for the rollback's draft
// restore. The COMPLETE helper, for the reason its own drift guard states: a
// partial factory fails the whole file at link time the day store.ts grows an
// export.
vi.mock("./store.js", () =>
  import("./__test-helpers__/store-mock.js").then((m) => ({ ...m.storeMock })),
);
vi.mock("./run-store.js", () => ({ peekRunState: vi.fn(() => undefined) }));
// The composer half of the close gesture: retargeting and the failed-send
// restore are per-chat state this suite does not stage, so both are inert.
vi.mock("./composer-state.js", () => ({
  retargetComposer: vi.fn(),
  restoreFailedSend: vi.fn(),
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
  seedComposerState: vi.fn(),
  adoptRemoteComposerState: vi.fn(),
  noteComposerText: vi.fn(),
  initComposerState: vi.fn(),
  _resetComposerStateForTest: vi.fn(),
}));
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
  setTabTooltip,
  hasTab,
  tabIdFor,
  tabIdForRoute,
  getActiveTabId,
  getActiveTabKind,
  tabSetVersion,
  activeChatRef,
  setOnEmpty,
  setTabPinned,
  showFilesView,
  toggleFilesView,
  _resetForTest,
} from "./tabs.js";
import { closeTabCommand } from "./actions/tabs.js";
import { restoreFailedSend, retargetComposer } from "./composer-state.js";
import { info as toastInfo, error as toastErrorFn } from "./toast.js";
import { attachDrag, setReorderCallback } from "./tabs-drag.js";
import { $ } from "./dom.js";
import type { OpenTabOutcome } from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import type { TabOpeners } from "./tab-materialize.js";
import {
  ingestTabsChanged,
  listTabs,
  beginRemove,
  removeCommitted,
  opTimedOut,
  _resetTabsSyncForTest,
} from "./tabs-sync.js";
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
  subagentShow: Mock<TabOpeners["subagent"]["show"]>;
}

let openers: Openers;

function registerOpeners(): void {
  openers = {
    chatShow: vi.fn<TabOpeners["chat"]["show"]>(),
    chatClose: vi.fn<TabOpeners["chat"]["close"]>(),
    editorShow: vi.fn<TabOpeners["editor"]["show"]>(),
    editorClose: vi.fn<TabOpeners["editor"]["close"]>(),
    runShow: vi.fn<TabOpeners["run"]["show"]>(),
    subagentShow: vi.fn<TabOpeners["subagent"]["show"]>(),
  };
  registerTabOpeners({
    chat: { show: openers.chatShow, close: openers.chatClose, dot: () => "" },
    editor: { show: openers.editorShow, close: openers.editorClose },
    run: { show: openers.runShow },
    subagent: { show: openers.subagentShow },
  });
}

// --- Openers the cases below drive ---

/** Open a chat tab for `ref`. Awaited, because the row lands with the frame. */
function openChat(
  ref: string,
  opts: { activate?: boolean; parent?: string; owns?: boolean } = {},
): Promise<OpenTabOutcome> {
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
  // The successor is the most recently visited tab still open, and the FIRST tab
  // is only the fallback for an exhausted history — one rule, stated in one place,
  // checked on the boot read and on every applied removal. Position is not the
  // input: `openChats("a","b","c")` activates each in turn, so the history reads
  // [c,b,a], and activating `b` makes it [b,c,a]. Closing `b` prunes it and leaves
  // `c` at the head. In the second case activating `c` hits activateTab's
  // already-active return and changes nothing, so closing `c` leaves `b`.
  it.each([
    {
      desc: "closing the active tab activates the most recently visited open tab",
      setup: ["a", "b", "c"],
      activate: "b",
      close: "b",
      expectActive: "c",
      expectHas: ["a", "c"],
      expectGone: ["b"],
    },
    {
      desc: "activates the previously visited tab when the closed one was last",
      setup: ["a", "b", "c"],
      activate: "c",
      close: "c",
      expectActive: "b",
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

  // The empty-state respawn DEFERS while any remove is pending: the strip may be
  // empty only because a close is still in flight, and a respawned chat would
  // race whatever settles it. The settlement re-arms the timer — here the
  // client-behind semantic confirmation (`closed: []`), the arm a close that
  // another device won answers with.
  it("defers the respawn while a remove is pending, and re-arms when it settles", async () => {
    expect.assertions(3);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChats("held", "mine");

    // An optimistic close of "held" is in flight (the machine holds its op; the
    // gesture half that empties the strip belongs to the close task — what
    // matters here is the PENDING REMOVE, which is the deferral's whole input).
    const onConfirm = vi.fn();
    beginRemove("op-held", {
      id: chatID("held"),
      capturedTabIDs: [chatID("held")],
      onConfirm,
      rollback: vi.fn(),
    });
    // Another device's close of the same tab lands first: the row leaves the
    // strip, while OUR dispatch stays unanswered (a foreign frame settles no op).
    tabServer.closeRemotely(chatID("held"));
    await settleTabs();

    // The reader closes the last tab. The strip is empty, the respawn is due —
    // and deferred, because "held"'s remove is still pending.
    await closeTab(chatID("mine"));
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // The response finally arrives: closed [] — the other device's close won.
    // Semantic confirmation settles the op and re-arms the respawn.
    removeCommitted("op-held", [], tabServer.version());
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).toHaveBeenCalledTimes(1);
  });

  // Same deferral, VERIFYING arm: the close got no answer at all, so the op sits
  // in `verifying` and the respawn must keep waiting — network unavailability
  // must not mint a chat into a strip whose last close is unconfirmed. An
  // authoritative list settles it (absent → confirm) and the respawn proceeds.
  it("defers the respawn while a remove is verifying, until an authoritative list settles it", async () => {
    expect.assertions(2);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChats("held", "mine");
    beginRemove("op-held", {
      id: chatID("held"),
      capturedTabIDs: [chatID("held")],
      onConfirm: vi.fn(),
      rollback: vi.fn(),
    });
    tabServer.closeRemotely(chatID("held"));
    await settleTabs();
    await closeTab(chatID("mine"));

    // The dispatch times out: no restore, no retire — verifying.
    opTimedOut("op-held");
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // An authoritative snapshot (a gap re-list, a verify tick — one mechanism)
    // no longer holds the row: silent confirmation, and the respawn re-arms.
    await listTabs();
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).toHaveBeenCalledTimes(1);
  });

  // The deferral's other exit: the settlement RESTORED a row, so the strip is
  // not empty and there is nothing to respawn.
  it("drops the deferred respawn when the settlement restores the row", async () => {
    expect.assertions(4);
    const onEmpty = vi.fn();
    setOnEmpty(onEmpty);
    await openChats("held", "mine");
    const rollback = vi.fn();
    beginRemove("op-held", {
      id: chatID("held"),
      capturedTabIDs: [chatID("held")],
      onConfirm: vi.fn(),
      rollback,
    });
    // The row leaves the PROJECTION only: a foreign frame this device should not
    // have received (the server still holds the tab). The projection is now a
    // version ahead, so a same-version reopen elsewhere re-aligns them.
    tabServer.emitRaw({
      removed_ids: [chatID("held")],
      order: [chatID("mine")],
      version: tabServer.version() + 1,
    });
    tabServer.openElsewhere({ kind: "chat", ref: "other" });
    await settleTabs();
    await closeTab(chatID("mine"));
    opTimedOut("op-held");
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();

    // The authoritative list still holds "held": restore, not confirm. The strip
    // has rows again, so the deferred respawn is DROPPED rather than re-armed.
    await listTabs();
    expect(rollback).toHaveBeenCalledTimes(1);
    expect(hasTab("chat", "held")).toBe(true);
    await new Promise((r) => setTimeout(r, 700));
    expect(onEmpty).not.toHaveBeenCalled();
  });
});

describe("activateTab", () => {
  it.each([
    { desc: "activates existing tab", target: "a", expectActive: "a" },
    { desc: "keeps an already-active tab active", target: "b", expectActive: "b" },
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

  it("closes the mobile drawer when the active tab is tapped", async () => {
    await openChats("a", "b");
    const sidebar = $.sidebar;
    sidebar.classList.add("open");

    activateTab(chatID("b"));

    expect(getActiveTabId()).toBe(chatID("b"));
    expect(sidebar.classList.contains("open")).toBe(false);
  });
});

// The history is in memory only and it is not observable directly, so every case
// here reads it through the one decision that consumes it: which tab a close hands
// the active view to. The four closeTab cases above pin the core rule; these pin
// the properties that rule has to hold ACROSS the other tab mechanisms.
describe("MRU activation history", () => {
  // A close removes a parent AND its whole subtree as one mutation, so the walk
  // has to skip every id that went rather than the one that was clicked. `first`
  // is what makes the case discriminating: the child is the most recent entry and
  // sits inside the removed set, and the tab it hands over to is neither the
  // removed child nor position 0.
  it("skips a whole closed subtree, not just the clicked tab", async () => {
    expect.assertions(2);
    await openChats("first", "a");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    // History: [c, p, a, first]. Re-activating the parent puts it at the head, so
    // the subtree holds the two most recent entries.
    activateTab(chatID("p"));

    await closeTab(chatID("p"));

    expect(getActiveTabId()).toBe(chatID("a"));
    expect(hasTab("chat", "c")).toBe(false);
  });

  // The first-tab rule is the FALLBACK now, not the rule, and it still has to
  // work: a cold boot restores a strip with no history, and `activate: false` opens
  // never claim recency for a tab the reader has not visited.
  it("falls back to the first tab when the history is exhausted", async () => {
    expect.assertions(2);
    await openChat("a");
    await openChat("b", { activate: false });
    await openChat("c", { activate: false });
    // History: [a] alone — neither automatic open activated anything.
    await closeTab(chatID("a"));

    expect(getActiveTabId()).toBe(chatID("b"));
    expect(await rowRefs()).toEqual(["b", "c"]);
  });

  // Two halves: a non-active close does not move the active tab, and the close
  // that follows it still picks by recency rather than by position. Four tabs,
  // because with three the second close leaves one survivor and both rules agree
  // on it — so the case would pass whatever the successor rule was.
  it("closing a non-active tab leaves active alone", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c", "d");
    activateTab(chatID("b")); // history [b, d, c, a]

    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));

    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // A reorder replaces `state.tabs` without changing set membership, so it must
  // not touch recency. The dropped order puts `b` first, so the assertion separates
  // "most recent" from "position 0" — under the old rule both closes landed there.
  it("is not affected by a reorder", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    activateTab(chatID("a")); // history [a, c, b]
    commitDrop?.([chatID("b"), chatID("a"), chatID("c")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["b", "a", "c"]);

    await closeTab(chatID("a"));
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // The `tabs_changed` door, which is a different production call site from the
  // gesture's: a frame carrying `removed_ids` applies the removal and then picks a
  // successor with `local: false`. The PRUNE itself is deliberately not asserted
  // anywhere — `mostRecentOpenTab` also filters on `hasRow` and server-minted ids
  // are never reused, so a stale entry is inert and no test could distinguish it.
  // What is observable is the pick, so that is what these two pin.
  it("hands over to the most recent survivor when another device closes the active tab", async () => {
    expect.assertions(2);
    await openChats("a", "b", "c");
    activateTab(chatID("b")); // history [b, c, a]

    tabServer.closeRemotely(chatID("b"));
    await settleTabs();

    expect(hasTab("chat", "b")).toBe(false);
    expect(getActiveTabId()).toBe(chatID("c"));
  });

  // `removed_ids` is a LIST, and a remote close of a parent is where it carries
  // more than one id. The child is the entry behind the head, so a walk that
  // skipped only the id it was handed would land on a row that is gone.
  it("skips every id a remote removal names, not just the first", async () => {
    expect.assertions(2);
    await openChats("first", "a");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    activateTab(chatID("p")); // history [p, c, a, first]

    tabServer.closeRemotely(chatID("p"));
    await settleTabs();

    expect(hasTab("chat", "c")).toBe(false);
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // The ONE path on which a missing prune-and-restore is observable: a refused
  // close puts the row back under its ORIGINAL id, so an entry dropped at gesture
  // time has to come back with it. Without the restore the history reads [a, c]
  // and the next close hands over to `c` — a tab visited longer ago than the one
  // whose close was refused.
  it("a refused close restores the closed tab's place in the history", async () => {
    expect.assertions(4);
    await openChats("a", "b", "c");
    activateTab(chatID("b"));
    activateTab(chatID("a")); // history [a, b, c]
    tabServer.failNext("close_tab");

    // Non-active, so the rollback restores the row without moving the active tab.
    await closeTab(chatID("b"));
    expect(hasTab("chat", "b")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
    expect(getActiveTabId()).toBe(chatID("a"));

    await closeTab(chatID("a"));
    expect(getActiveTabId()).toBe(chatID("b"));
  });

  // The OTHER path rollbackClose serves, and the one a gate on the rows THIS call
  // spliced cannot reach: a close whose dispatch got no answer is settled by an
  // authoritative list that still names the tab, and readList adopts that snapshot
  // BEFORE running the callback — so the row is already back and nothing is
  // spliced. History [act, x, y, first], so the hand-over target is neither the
  // entry behind it nor position 0.
  it("a verify-settled restore returns the closed tab's place in the history", async () => {
    expect.assertions(3);
    await openChats("first", "y", "x", "act"); // history [act, x, y, first]
    const doomedTab = chatID("x");

    // No answer at all: the response is held and the dispatch canceled, which is
    // the branch a real 5s timeout takes. Manual mode keeps the echo frame back,
    // or a matching op_id would confirm the op instead.
    tabServer.setMode("manual");
    tabServer.holdResponses();
    const closing = closeTab(doomedTab);
    closeTabCommand.cancel();
    tabServer.releaseResponses();
    await closing;
    expect(hasTab("chat", "x")).toBe(false);

    // The authoritative list still holds the row: the close never committed.
    const held = tabServer
      .subjects()
      .map((s) => ({ ...s }))
      .concat([{ id: doomedTab, kind: "chat", ref: "x", parent: "", pinned: false, owns: true }]);
    tabServer.queueList({ tabs: held, version: tabServer.version() + 1 });
    await listTabs();
    expect(hasTab("chat", "x")).toBe(true);

    // With the slot restored the active tab's close hands over to `x`; without it
    // the history reads [act, y, first] and `y` takes over instead.
    await closeTab(chatID("act"));
    expect(getActiveTabId()).toBe(chatID("x"));
  });

  // The captured slot is an ANCHOR, not an index, because the dispatch AWAIT sits
  // between the capture and the restore and every entry an index was measured
  // against can move across it. The reviewer's traced case verbatim: history
  // [b,a,c], `c` closed, `d` opened before the refusal lands. The anchor answers
  // [d,b,a,c]; an absolute index answered [d,b,c,a], ranking `c` ahead of a tab
  // visited more recently. `first` is never visited, so the expected answer is
  // neither the buggy one nor the position-0 fallback.
  it("restores a rank the await window moved, not the index it was captured at", async () => {
    expect.assertions(5);
    await openChat("first", { activate: false });
    await openChats("a", "b", "c"); // history [c, b, a]
    activateTab(chatID("a"));
    activateTab(chatID("b")); // history [b, a, c] — `c` is the tail, behind `a`
    tabServer.failNext("close_tab");
    tabServer.holdResponses();

    // Both dispatches in flight at once, the OPEN released first, so its
    // activation lands inside the close's window and the history reads [d, b, a].
    const opening = openChat("d");
    const closing = closeTab(chatID("c"));

    // That interleaving is what the case DISCRIMINATES on, so it is asserted, off
    // the projection's own reactive seam rather than after the awaits — by then
    // both have run and neither order is observable. Released the other way round
    // the rollback restores against [b, a] and the two designs agree, so the case
    // would keep passing while catching nothing.
    const seen: { active: string; back: boolean }[] = [];
    const stop = effect(() => {
      tabSetVersion();
      seen.push({ active: getActiveTabId(), back: hasTab("chat", "c") });
    });

    tabServer.releaseResponses();
    await opening;
    await closing;
    stop();
    // The frame the rollback landed on: `d` already active, so at the head.
    expect(seen.find((s) => s.back)).toEqual({ active: chatID("d"), back: true });
    expect(hasTab("chat", "c")).toBe(true);
    expect(getActiveTabId()).toBe(chatID("d"));

    // The rank, read the only way it is observable: `c` came back BEHIND `a`, so
    // closing `d` then `b` hands over to `a` rather than to the restored tab.
    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));
    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("a"));
  });

  // A parent-plus-subtree close removes entries that can be ADJACENT in the
  // history, and their order among themselves has to survive the round trip. Each
  // anchors on its immediate predecessor whether or not that one also went, so the
  // capture is ordered by SLOT and the restore replays it, which is what has `c`
  // back before `p` asks for it. The child is the more recent of the two here on
  // purpose: that is the orientation where slot order and the subtree's own
  // parent-first order disagree, so both halves of the rule are load-bearing.
  it("returns adjacent removed siblings in their original relative order", async () => {
    expect.assertions(5);
    await openChat("first", { activate: false });
    await openChat("a");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    await openChat("b"); // history [b, c, p, a] — the pair, adjacent, mid-history
    await openChat("d", { activate: false });
    tabServer.failNext("close_tab");
    tabServer.holdResponses();

    const closing = closeTab(chatID("p"));
    // The reader's gesture inside the window, synchronous: history [d, b, a].
    activateTab(chatID("d"));
    tabServer.releaseResponses();
    await closing;
    expect(hasTab("chat", "p")).toBe(true);
    expect(hasTab("chat", "c")).toBe(true);

    // [d, b, c, p, a]: the pair is back behind `b`, and `c` still outranks `p`.
    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));
    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("c"));
    await closeTab(chatID("c"));
    expect(getActiveTabId()).toBe(chatID("p"));
  });

  // The anchor is a survivor, so another device can close it inside the same
  // window. The entry ranked BEHIND it, so the tail is the defensible answer:
  // understating recency drops a preference, where the head would invert a real
  // one — and the head would make `c` the answer to the FIRST close below.
  it("falls back to the tail when the anchor itself was closed in the window", async () => {
    expect.assertions(4);
    await openChat("first", { activate: false });
    await openChats("a", "c", "s", "b"); // history [b, s, c, a]
    await openChat("d", { activate: false });
    tabServer.failNext("close_tab");
    tabServer.holdResponses();

    const closing = closeTab(chatID("c")); // anchored on `s`
    tabServer.closeRemotely(chatID("s")); // and `s` goes, mid-window
    activateTab(chatID("d")); // history [d, b, a]
    tabServer.releaseResponses();
    await closing;
    expect(hasTab("chat", "c")).toBe(true);
    expect(hasTab("chat", "s")).toBe(false);

    // [d, b, a, c], so `a` outranks the restored tab; the captured index put `c`
    // in front of it.
    await closeTab(chatID("d"));
    expect(getActiveTabId()).toBe(chatID("b"));
    await closeTab(chatID("b"));
    expect(getActiveTabId()).toBe(chatID("a"));
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

describe("setTabTooltip and the title a row carries", () => {
  it("sets and clears the row's title", async () => {
    expect.assertions(2);
    await openChat("a");
    await paint();
    setTabTooltip(chatID("a"), "Code · reading files");
    expect(rows()[0]?.title).toBe("Code · reading files");
    setTabTooltip(chatID("a"), "");
    expect(rows()[0]?.hasAttribute("title")).toBe(false);
  });

  // The tooltip is parked on the row like the dot: the per-row store effect
  // rewrites it only when that chat's own inputs churn, so a rebuild that
  // dropped it would leave the row tooltipless until then — hours, for an
  // idle chat.
  it("survives a re-list, which rebuilds every row", async () => {
    expect.assertions(1);
    await openChat("a");
    setTabTooltip(chatID("a"), "Code · reading files");
    // A version two past local: the sync layer stops applying and re-lists.
    tabServer.emitRaw({ version: tabServer.version() + 2 });
    await settleTabs();
    await paint();
    expect(rows()[0]?.title).toBe("Code · reading files");
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

// What a BACK or FORWARD press asks before it applies a route. A history entry
// names a location this browser WAS at, which is not the same thing as a location
// that still exists — so an entry answering "" is a closed tab, and app.ts
// redirects instead of opening one. Before the question existed, applying such an
// entry re-opened the tab: a reader pressing back watched a tab they had closed
// come back, and the server-owned collection broadcast it to every other device.
describe("tabIdForRoute", () => {
  it("resolves the route a tab carries to that tab's id", async () => {
    expect.assertions(1);
    await openChat("a");
    expect(tabIdForRoute({ kind: "chat", id: "a" })).toBe(chatID("a"));
  });

  it("answers '' once that tab is closed", async () => {
    expect.assertions(2);
    await openChats("a", "b");
    const id = chatID("a");
    await closeTab(id);
    expect(tabIdForRoute({ kind: "chat", id: "a" })).toBe("");
    // And the neighbour that took over is still resolvable, so the redirect has
    // somewhere honest to land.
    expect(tabIdForRoute({ kind: "chat", id: "b" })).toBe(chatID("b"));
  });

  // The route kind is `file` and the tab kind is `editor`: the one place the two
  // vocabularies differ, and the one this lookup exists to stop a caller
  // re-deriving.
  it("resolves /file/{path} to the editor tab holding that path", async () => {
    expect.assertions(2);
    await openEditorView("src/a.ts");
    expect(tabIdForRoute({ kind: "file", path: "src/a.ts" })).toBe(tabIdFor("editor", "src/a.ts"));
    expect(tabIdForRoute({ kind: "file", path: "src/b.ts" })).toBe("");
  });

  // A singleton's sub-position is not part of its identity, so a deep link into a
  // sub-tab of an OPEN singleton resolves to it. Answering "" here would redirect
  // every back press onto /settings/tools away from a Settings tab sitting right
  // there.
  it("ignores a singleton's sub-position", async () => {
    expect.assertions(2);
    expect(tabIdForRoute({ kind: "settings", tab: "tools" })).toBe("");
    await openTab({ kind: "settings" });
    expect(tabIdForRoute({ kind: "settings", tab: "tools" })).toBe(tabIdFor("settings"));
  });

  // "/" names no chat, so it resolves to nothing even with chats open. That is
  // what sends a back press onto "/" through the redirect, which canonicalizes it
  // to whatever is on screen — the same thing applyInitialRoute does on load.
  it("answers '' for the default route", async () => {
    expect.assertions(1);
    await openChat("a");
    expect(tabIdForRoute({ kind: "chat", id: "" })).toBe("");
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
  function openChild(ref: string, parentRef: string): Promise<OpenTabOutcome> {
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

  // The exit ANIMATION is chosen from what the departing row is, and the choice
  // has to be made when the row is already out of the projection — which is why
  // the parent id rides the element. The CSS behind the two classes lives in
  // 10-shell-app.css; what is pinned here is which one each case gets.
  describe("the exit animation", () => {
    // A sub-tab folds back UP into the row it hangs off, because that is where
    // its work came from and where the reader's attention should land. The
    // sideways swipe it used to share with a top-level tab also reset the indent
    // on its first frame, so the row jumped a full 1rem wider before it left.
    it("merges a child up into a parent that stays", async () => {
      expect.assertions(2);
      await openChat("p");
      await openChild("c", "p");
      const child = chatID("c");
      await paint();
      await closeTab(child);
      await paint();
      const row = rows().find((r) => r.dataset["tabId"] === child);
      expect(row?.classList.contains("exiting")).toBe(true);
      expect(row?.classList.contains("exiting-merge")).toBe(true);
    });

    // A child cannot merge into a row that is leaving too, so the whole subtree
    // takes the parent's own exit and the group reads as one block departing
    // rather than as N children folding into a vanishing target.
    it("sends a closed parent's children out with it, not into it", async () => {
      expect.assertions(6);
      await openChat("p");
      await openChild("c1", "p");
      await openChild("c2", "p");
      const ids = [chatID("p"), chatID("c1"), chatID("c2")];
      await paint();
      await closeTab(ids[0] ?? "");
      await paint();
      for (const id of ids) {
        const row = rows().find((r) => r.dataset["tabId"] === id);
        expect(row?.classList.contains("exiting")).toBe(true);
        expect(row?.classList.contains("exiting-merge")).toBe(false);
      }
    });

    // An ORPHAN carries a parent its strip does not hold, so it renders indented
    // with nothing above it to merge into. The survivor-set test answers that for
    // free: the parent is not open, so the row takes the sideways exit.
    it("swipes an orphan out rather than merging it into nothing", async () => {
      expect.assertions(2);
      await openTab({ kind: "chat", ref: "orphan", parent: "tb_missing" });
      const id = chatID("orphan");
      await paint();
      await closeTab(id);
      await paint();
      const row = rows().find((r) => r.dataset["tabId"] === id);
      expect(row?.classList.contains("exiting")).toBe(true);
      expect(row?.classList.contains("exiting-merge")).toBe(false);
    });
  });

  // `owns: false` is the VIEW case: dismissing a view must not kill the work it was
  // watching. Asserted on a CHAT, which is the kind that still has an ownership axis —
  // a side conversation owns its bridge while a tab watching another chat's work does
  // not. It used to be asserted on a run tab, and cannot be any more: a run tab is
  // always a view now (user decision, 2026-08), so it has no owning case to compare
  // against.
  it("does not tear down a tab that owns nothing", async () => {
    expect.assertions(2);
    await openChat("watch", { owns: false });
    await closeTab(chatID("watch"));
    expect(hasTab("chat", "watch")).toBe(false);
    expect(openers.chatClose).not.toHaveBeenCalled();
  });

  it("tears down an owning tab", async () => {
    expect.assertions(2);
    await openChat("mine");
    await closeTab(chatID("mine"));
    expect(hasTab("chat", "mine")).toBe(false);
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
  });

  it("still removes a view child when its parent closes", async () => {
    expect.assertions(1);
    await openChat("p");
    await openRunTab("wf-1", "A view", { parent: chatID("p") });
    await closeTab(chatID("p"));
    expect(hasTab("run", "wf-1")).toBe(false);
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
    await openRunTab("wf-view", "A view", { parent: chatID("p") });
    await closeTab(chatID("p"));
    expect(order).toEqual(["gc", "c", "p"]);
    // The run sub-tab went with the cascade and tore nothing down, which is what a
    // view child is: removed with its parent, never a teardown of its own.
    expect(hasTab("run", "wf-view")).toBe(false);
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
// another device's. What that decides is WHEN the client-local teardown runs — at
// the machine's confirmation for this device's own close, at the applied removal
// for another device's — never WHAT it does: the teardown is identical in both
// cases, because nothing local dispatches anything any more (the process teardown
// and the retention-off delete are both the server's close operation).
// ---------------------------------------------------------------------------

describe("close provenance", () => {
  it("runs the teardown exactly once for a close this device dispatched", async () => {
    expect.assertions(2);
    await openChat("a");
    await closeTab(chatID("a"));
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("a");
  });

  it("runs the same teardown for a close from another device", async () => {
    expect.assertions(2);
    await openChat("a");
    tabServer.closeRemotely(chatID("a"));
    await settleTabs();
    // The teardown STILL RUNS — that is the point: this device keeps no store
    // row, dock card or composer state for a tab that is gone, whoever closed it.
    expect(openers.chatClose).toHaveBeenCalledWith("a");
    expect(hasTab("chat", "a")).toBe(false);
  });

  it("tears down a remotely-cascaded child too, children first", async () => {
    expect.assertions(1);
    const seen: string[] = [];
    openers.chatClose.mockImplementation((ref: string) => {
      seen.push(ref);
    });
    await openChat("parent");
    await openTab({ kind: "chat", ref: "child", parent: chatID("parent") });
    tabServer.closeRemotely(chatID("parent"));
    await settleTabs();
    expect(seen).toEqual(["child", "parent"]);
  });
});

// ---------------------------------------------------------------------------
// Which mutations are allowed to swap the visible view
// ---------------------------------------------------------------------------

// The view/route effect re-runs on EVERY projection mutation, so a swap on a
// mutation that changes nothing about which view is visible is not merely
// wasted work — swapViews cancels and replays the entry fade, so a redundant
// swap re-animates the view the reader is already looking at.
//
// These cases count `class` mutations on the view elements, which is what the
// swap actually does and the only thing that separates a skip from an idempotent
// re-run. No view-swap mock is needed: swapViews applies the swap synchronously,
// and with boot not marked done it skips the animation.
describe("the visible view is only swapped when it actually changes", () => {
  let views: HTMLElement[];
  let mutations = 0;
  let mo: MutationObserver | undefined;

  /** Stage the two views showView resolves against, with chat already the shown
   *  one — the state a reader is in whenever a chat tab is active. */
  function stageViews(): void {
    for (const id of ["chat-view", "settings-view"]) {
      const v = document.createElement("div");
      v.id = id;
      v.setAttribute("data-tab-view", "");
      if (id !== "chat-view") {
        v.classList.add("hidden");
      }
      document.body.appendChild(v);
    }
    views = [...document.querySelectorAll<HTMLElement>("[data-tab-view]")];
  }

  function watch(): void {
    mutations = 0;
    mo = new MutationObserver((records) => {
      mutations += records.length;
    });
    for (const v of views) {
      mo.observe(v, { attributes: true, attributeFilter: ["class"] });
    }
  }

  /** MutationObserver delivers in a microtask, so let it flush before counting. */
  async function settle(): Promise<number> {
    await Promise.resolve();
    mo?.takeRecords().forEach(() => {
      mutations += 1;
    });
    return mutations;
  }

  beforeEach(() => {
    stageViews();
  });

  afterEach(() => {
    mo?.disconnect();
    mo = undefined;
  });

  it("does not touch the views when a non-active tab closes", async () => {
    await openChat("a");
    await openChat("b");
    // "b" is active and the chat view is already the shown one.
    watch();

    await closeTab(chatID("a"));

    // The strip lost a row, but which view is visible did not change, so there is
    // nothing to animate and nothing to swap.
    expect(await settle()).toBe(0);
    expect(document.getElementById("chat-view")?.classList.contains("hidden")).toBe(false);
  });

  it("does not touch the views when the active tab moves between two chats", async () => {
    await openChat("a");
    await openChat("b");
    watch();

    // Both rows resolve to the SAME view element, so the old swap captured two
    // identical snapshots and animated nothing while still costing a serialized
    // transition. Skipping it loses no animation: the content the reader sees
    // change is re-rendered by the chat view afterwards, outside any transition.
    activateTab(chatID("a"));

    expect(await settle()).toBe(0);
  });

  it("still swaps when the active tab needs a different view", async () => {
    await openChat("a");
    watch();

    await openTab({ kind: "settings", ref: "" });

    // A real navigation: settings has its own view element, so the swap has to
    // run — and this is the case the guard must not swallow.
    expect(await settle()).toBeGreaterThan(0);
    expect(document.getElementById("settings-view")?.classList.contains("hidden")).toBe(false);
    expect(document.getElementById("chat-view")?.classList.contains("hidden")).toBe(true);
  });

  it("repairs a view state that drifted, even with the active tab unchanged", async () => {
    await openChat("a");
    // Something outside this module left every view hidden. The guard reads the
    // DOM rather than remembering what it last showed, so the next mutation
    // notices and puts the right one back instead of trusting a cached answer.
    for (const v of views) {
      v.classList.add("hidden");
    }
    watch();

    await openChat("b");

    expect(document.getElementById("chat-view")?.classList.contains("hidden")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// The optimistic close: reversible gesture, deferred teardown (task 11).
//
// The gesture applies only what a rollback can undo — the subtree leaves the
// projection, activation falls back — while every destructive step (the spec
// onClose teardown, and server-side the record delete) waits for the machine's
// confirmation. These cases drive the REAL closeTab end to end over the fake
// collection: the machine's transition table is tabs-sync.test.ts's; what is
// pinned here is what the GESTURE does with each ending.
// ---------------------------------------------------------------------------

describe("optimistic close: the reversible gesture", () => {
  it("removes the subtree at the gesture and defers the teardown to the frame", async () => {
    expect.assertions(4);
    tabServer.setMode("manual");
    await openChats("a", "b");
    const doomed = chatID("b");

    const closing = closeTab(doomed);
    // The row left the strip synchronously with the gesture…
    expect(hasTab("chat", "b")).toBe(false);
    await closing;
    // …and the response alone (frames still held) runs NO teardown: the op is
    // confirmed-awaiting-frame, and client-local state must survive a rollback.
    expect(openers.chatClose).not.toHaveBeenCalled();

    tabServer.flushFrames();
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("b");
  });

  it("runs the teardown exactly once, frame-first", async () => {
    expect.assertions(2);
    tabServer.setMode("event-first");
    await openChats("a", "b");
    await closeTab(chatID("b"));
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("b");
  });

  it("runs the teardown exactly once, response-first", async () => {
    expect.assertions(2);
    tabServer.setMode("response-first");
    await openChats("a", "b");
    await closeTab(chatID("b"));
    await settleTabs();
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
    expect(openers.chatClose).toHaveBeenCalledWith("b");
  });

  it("dispatches close_tab and nothing else — delete_chat does not exist on this path", async () => {
    expect.assertions(2);
    await openChats("a");
    await closeTab(chatID("a"));
    await settleTabs();
    expect(tabServer.sentOfType("delete_chat")).toHaveLength(0);
    expect(tabServer.sentOfType("close_tab")).toHaveLength(1);
  });

  it("a definitive refusal restores the chat row in place, re-activates it, and toasts", async () => {
    expect.assertions(6);
    await openChats("a", "b", "c");
    activateTab(chatID("b"));
    vi.mocked(openers.chatShow).mockClear();
    tabServer.failNext("close_tab");

    await closeTab(chatID("b"));

    expect(hasTab("chat", "b")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
    expect(getActiveTabId()).toBe(chatID("b"));
    // The restored chat was RE-ACTIVATED (its view reloads from retained state)…
    expect(openers.chatShow).toHaveBeenCalledWith("b");
    // …and none of the chat's client state was torn down in between.
    expect(openers.chatClose).not.toHaveBeenCalled();
    expect(vi.mocked(toastErrorFn)).toHaveBeenCalledWith("Couldn't close that tab");
  });

  it("a definitive refusal leaves a dirty editor's state untouched", async () => {
    expect.assertions(3);
    await openChats("a");
    await openEditorView("dirty.ts");
    tabServer.failNext("close_tab");

    await closeTab(tabIdFor("editor", "dirty.ts"));

    // The editor teardown is what deletes its FileState (the unsaved buffer),
    // so its absence IS the dirty state surviving the refused close.
    expect(openers.editorClose).not.toHaveBeenCalled();
    expect(hasTab("editor", "dirty.ts")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "dirty.ts"]);
  });

  it("a reopen inside the window skips the chat-scoped teardown (row-scoped forget only)", async () => {
    expect.assertions(4);
    tabServer.setMode("manual");
    await openChats("a", "keep");

    await closeTab(chatID("a"));
    // Reopened from History before the close settled: the server serialized
    // close-then-open, so the reply mints a NEW tab id for the same chat.
    await openTab({ kind: "chat", ref: "a" });
    expect(hasTab("chat", "a")).toBe(true);

    tabServer.flushFrames();
    await settleTabs();
    // The close confirmed — but the subject is open again, so the chat-scoped
    // teardown is SKIPPED: the reopen re-established the chat's client state.
    expect(openers.chatClose).not.toHaveBeenCalled();
    expect(hasTab("chat", "a")).toBe(true);
    expect(tabServer.idFor("chat", "a")).toBe(tabIdFor("chat", "a"));
  });

  it("burst closes settle independently: one confirms while the other rolls back", async () => {
    expect.assertions(5);
    await openChats("a", "b", "c");
    tabServer.holdResponses();
    const first = closeTab(chatID("a"));
    tabServer.failNext("close_tab");
    const second = closeTab(chatID("b"));
    expect(hasTab("chat", "a")).toBe(false);
    expect(hasTab("chat", "b")).toBe(false);

    tabServer.releaseResponses();
    await Promise.all([first, second]);
    await settleTabs();

    expect(hasTab("chat", "a")).toBe(false);
    expect(hasTab("chat", "b")).toBe(true);
    expect(openers.chatClose).toHaveBeenCalledTimes(1);
  });

  it("children close with their parent and are restored with it, in place", async () => {
    expect.assertions(3);
    await openChat("before");
    await openChat("p");
    await openTab({ kind: "chat", ref: "c", parent: chatID("p") });
    await openChat("after");
    tabServer.failNext("close_tab");

    await closeTab(chatID("p"));

    expect(hasTab("chat", "p")).toBe(true);
    expect(hasTab("chat", "c")).toBe(true);
    expect(await rowRefs()).toEqual(["before", "p", "c", "after"]);
  });
});

describe("optimistic close: timeout, verifying, and authoritative settlement", () => {
  // The 5s deadline itself is definition-level (CLOSE_CONFIRM_MS on
  // closeTabCommand) and cannot be advanced by fake timers — AbortSignal.timeout
  // is not faked — so these cases end the dispatch the other no-answer way, a
  // cancellation, which takes the identical opTimedOut branch. The machine's own
  // transition table is pinned in tabs-sync.test.ts; this is the integration
  // through the real closeTab.

  /** A close whose dispatch ends with NO answer: response held, then the
   *  in-flight dispatch canceled. The op lands in `verifying`. */
  async function closeIntoVerifying(id: string): Promise<void> {
    tabServer.setMode("manual");
    tabServer.holdResponses();
    const closing = closeTab(id);
    closeTabCommand.cancel();
    tabServer.releaseResponses();
    await closing;
  }

  it("settles a verifying close as CONFIRMED when the authoritative list says absent", async () => {
    expect.assertions(4);
    await openChats("a", "keep");
    await closeIntoVerifying(chatID("a"));
    expect(hasTab("chat", "a")).toBe(false);
    expect(openers.chatClose).not.toHaveBeenCalled();

    // The server's live collection no longer holds the tab (the command
    // committed even though its answer was lost): absent → silent confirm.
    await listTabs();
    expect(openers.chatClose).toHaveBeenCalledWith("a");
    expect(vi.mocked(toastInfo)).not.toHaveBeenCalled();
  });

  it("settles a verifying close as RESTORED when the row is authoritatively present", async () => {
    expect.assertions(4);
    await openChats("a", "keep");
    const doomedTab = chatID("a");
    await closeIntoVerifying(doomedTab);

    // The authoritative list still holds the row: the close never committed.
    const held = tabServer
      .subjects()
      .map((s) => ({ ...s }))
      .concat([{ id: doomedTab, kind: "chat", ref: "a", parent: "", pinned: false, owns: true }]);
    tabServer.queueList({ tabs: held, version: tabServer.version() + 1 });
    await listTabs();

    expect(hasTab("chat", "a")).toBe(true);
    expect(openers.chatClose).not.toHaveBeenCalled();
    // Restored with a notice — this arm is the machine's, not a server error's,
    // so the ordinary failure toast stays silent.
    expect(vi.mocked(toastInfo)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(toastErrorFn)).not.toHaveBeenCalled();
  });
});

describe("optimistic close: the last tab and the empty-state surface", () => {
  /** The two views the empty surface resolves against, chat hidden behind the
   *  settings view — the state a reader is in when a singleton is active. */
  function stageViews(): { chatView: HTMLElement; settingsView: HTMLElement } {
    const chatView = document.createElement("div");
    chatView.id = "chat-view";
    chatView.setAttribute("data-tab-view", "");
    const settingsView = document.createElement("div");
    settingsView.id = "settings-view";
    settingsView.setAttribute("data-tab-view", "");
    document.body.append(chatView, settingsView);
    return { chatView, settingsView };
  }

  it("renders the empty chat surface when the strip empties, without disposing the closed view", async () => {
    expect.assertions(3);
    const { chatView, settingsView } = stageViews();
    await openTab({ kind: "settings" });
    expect(settingsView.classList.contains("hidden")).toBe(false);

    await closeTab(tabIdFor("settings"));
    await settleTabs();

    // The empty-state surface is the CHAT view (empty transcript + composer,
    // whose Send creates a fresh chat); the departed view is hidden, not torn
    // down — every dispose belongs to confirmed teardown.
    expect(chatView.classList.contains("hidden")).toBe(false);
    expect(settingsView.classList.contains("hidden")).toBe(true);
  });

  it("keeps create-vs-send on CREATE while the strip is empty", async () => {
    expect.assertions(3);
    await openChat("solo");
    expect(activeChatRef()).toBe("solo");
    tabServer.setMode("manual");
    const closing = closeTab(chatID("solo"));
    // The projection's active subject is what app.ts keys the decision on: with
    // the strip empty (close still pending), Send must create a fresh chat, not
    // send into the chat being closed — whose STORE row is still retained.
    expect(activeChatRef()).toBe("");
    await closing;
    expect(activeChatRef()).toBe("");
  });

  it("rollback of a last-tab close restores the view and the empty-state composer text", async () => {
    expect.assertions(5);
    await openChat("solo");
    const solo = chatID("solo");
    vi.mocked(openers.chatShow).mockClear();
    tabServer.failNext("close_tab");

    const closing = closeTab(solo);
    // Typed into the EMPTY-STATE composer while the close was in flight: with
    // no live chat this text is parked nowhere but the box. (Read through the
    // dom mock's getter, which mints the element on first access.)
    const input = $.promptInput;
    input.value = "half a thought";
    await closing;

    // The subtree is back, re-activated, and the typed text was filed as the
    // restored chat's draft through the failed-send primitive (which writes the
    // draft map and only ever an EMPTY box).
    expect(hasTab("chat", "solo")).toBe(true);
    expect(getActiveTabId()).toBe(solo);
    expect(openers.chatShow).toHaveBeenCalledWith("solo");
    expect(vi.mocked(restoreFailedSend)).toHaveBeenCalledWith("solo", "half a thought");
    expect(vi.mocked(retargetComposer)).not.toHaveBeenCalled();
  });

  it("keyboard close of the last tab moves focus to the composer", async () => {
    expect.assertions(1);
    await openChat("solo");
    await paint();
    const row = rows()[0];
    row?.focus();
    row?.dispatchEvent(new KeyboardEvent("keydown", { key: "Delete", bubbles: true }));
    const input = document.getElementById("prompt-input");
    expect(document.activeElement).toBe(input);
  });
});
