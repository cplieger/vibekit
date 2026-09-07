//
// The attention system's BINDING and its trigger rules: the three sinks against a
// real document, the visible-rows test against real geometry, and initAttention
// wired to the real tab store.
//
// The decisions are attention-no-dom.node.test.ts's subject, under `node` with no DOM at all.
// What is here is everything that can only be wrong against a browser:
//
//  1. THE TITLE BASE. The retired `setBadge` asserted its own copy of
//     static/index.html's <title> over whatever that file declared, and nothing
//     pinned the two literals together. The sink composes prefix + a base
//     captured from the served document instead, which is only correct if it
//     never reads the current title back — that value already carries a prefix.
//  2. EVERY ICON LINK. Which link a browser picks differs (Chrome prefers the
//     SVG), so mutating one element is unreliable; and every variant must be
//     computed from the ORIGINAL href, or repeated swaps compound into a 404.
//  3. THE VISIBLE-ROWS RULE. `#tab-list` scrolls and the mobile sidebar is a
//     drawer parked at translateX(-100%). checkVisibility() answers neither
//     question, so the geometry is the rule and this is where it is measured.
//  4. THE RECOMPUTE FUNNEL. `setTabStatus` deliberately does not `emit()`, so a
//     funnel on the tab store's one signal covers the tab SET and misses every
//     status change; one on the dot alone misses a chat closing. Both legs are
//     driven here through the real store.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
// EVERY glyph, generated from the module rather than hand-listed: the projection's
// graph reaches several more icon consumers now, and real-ESM linking needs each
// name to be a real property — a Proxy answers a property read and still fails to
// link. No case here asserts on a glyph.
vi.mock("./icons.js", () => ({
  ICON_EDIT: "",
  ICON_EDIT_14: "",
  ICON_CLOSE: "",
  ICON_TRASH: "",
  ICON_TRASH_14: "",
  ICON_PLUS: "",
  ICON_PLUS_16: "",
  ICON_PIN: "",
  ICON_PIN_FILLED: "",
  ICON_COPY: "",
  ICON_COPY_MD: "",
  ICON_SOURCE: "",
  ICON_LINK: "",
  ICON_EXTERNAL: "",
  ICON_DOWNLOAD: "",
  ICON_REPO: "",
  ICON_EXPORT: "",
  ICON_DIFF: "",
  ICON_X: "",
  ICON_PLAY: "",
  ICON_CHEVRON_DOWN: "",
  ICON_CHEVRON_UP: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
  ICON_CANCEL: "",
  ICON_MODEL: "",
  ICON_MODEL_UI: "",
  ICON_ALERT: "",
  ICON_GIT_UP_ARROW: "",
  ICON_GIT_DOWN_ARROW: "",
  ICON_REFRESH: "",
  ICON_FILTER: "",
  ICON_REPO_EMPTY: "",
  ICON_PR_EMPTY: "",
  ICON_GLOBE: "",
  ICON_WARN_12: "",
  ICON_SCALE_12: "",
  ICON_SAVE_OK: "",
  ICON_SAVE_FAIL: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_PLAN: "",
  ICON_TAB_SPEC: "",
  ICON_TAB_AGENT: "",
  ICON_SUBAGENT_INTROSPECT: "",
  ICON_SUBAGENT_GATHERER: "",
  ICON_SUBAGENT_TASK: "",
  ICON_SUBAGENT_CREATOR: "",
  ICON_REFUSAL: "",
  ICON_TAB_QUICK_SPEC: "",
  ICON_TAB_BUG: "",
  ICON_TAB_AUTONOMOUS: "",
  ICON_TAB_SUBTAB: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_FILES: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
  ICON_TAB_RUN: "",
}));
// The tab set is SERVER-owned, so a row lands on the strip through a real round
// trip against the fake collection rather than through a store mutator.
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({ apiGetTyped: m.tabListRead() })),
);
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
// The two leaf stores the tab factory reads for a display NAME. Neither matters
// here: what this suite reads off a row is its DOT.
vi.mock("./run-store.js", () => ({
  // The tab factory's name read. Inert here; a Browser-Mode mock is linked as
  // real ESM, so a name any module in the graph reaches has to exist on it.
  runLabelOf: vi.fn(() => ""),
}));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
// The sidebar and the tab list must be the SAME document-attached elements the
// production code looks up, because the rule under test is geometric. Every other
// getter stays a throwaway, as in tabs.test.ts.
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        if (prop === "sidebar" || prop === "tabList") {
          const id = prop === "sidebar" ? "sidebar" : "tab-list";
          let node = document.getElementById(id);
          if (node === null) {
            node = document.createElement("div");
            node.id = id;
            document.body.appendChild(node);
          }
          return node;
        }
        return document.createElement("div");
      },
    },
  ),
  // `byId` is reached through this graph by page-title.ts. ESM links for real, so
  // a name any module in the graph imports must exist on the mock or the whole
  // FILE fails at link time, naming the export rather than the test. Inlined
  // rather than shared because a `vi.mock` factory is hoisted above every
  // top-level import, so it cannot reach a helper module. Resolve-or-create,
  // because the real `byId` throws and a suite mocking `dom.js` stages only what
  // its own subject needs.
  byId: (id: string): HTMLElement => {
    let el = document.getElementById(id);
    if (el === null) {
      el = document.createElement("span");
      el.id = id;
      document.body.appendChild(el);
    }
    return el;
  },
}));

import {
  browserAttentionEnv,
  createAttention,
  initAttention,
  pageVisible,
  rowsInView,
  CUE_SEEN_KEY,
} from "./attention.js";
import { setActive } from "./store.js";
import {
  openTab,
  openEditorView,
  closeTab,
  activateTab,
  setTabStatus,
  setTabDirty,
  tabIdFor,
  _resetForTest,
} from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";
import type { TabDotStatus } from "./tab-view.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

function rect(top: number, height: number, left = 0, width = 260): DOMRect {
  return {
    top,
    height,
    bottom: top + height,
    left,
    width,
    right: left + width,
    x: left,
    y: top,
    toJSON: () => ({}),
  } as DOMRect;
}

function place(el: Element, r: DOMRect): void {
  el.getBoundingClientRect = (): DOMRect => r;
}

/** A sidebar and a tab list with a real clip box, plus one row per id. */
function buildSidebar(rows: readonly { id: string; top: number; height: number }[]): {
  sidebar: HTMLElement;
  tabList: HTMLElement;
} {
  const sidebar = document.createElement("div");
  sidebar.id = "sidebar";
  const tabList = document.createElement("div");
  tabList.id = "tab-list";
  sidebar.appendChild(tabList);
  document.body.appendChild(sidebar);
  // The persistent desktop sidebar: on screen, 260px wide, full height.
  place(sidebar, rect(0, 700));
  // The scrolling list occupies 100..600.
  place(tabList, rect(100, 500));
  for (const row of rows) {
    const el = document.createElement("div");
    el.dataset["tabId"] = row.id;
    place(el, rect(row.top, row.height));
    tabList.appendChild(el);
  }
  return { sidebar, tabList };
}

function hidePage(hidden: boolean): void {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => (hidden ? "hidden" : "visible"),
  });
}

/** The dot the tab FACTORY seeds a chat row with, per chat ref.
 *
 *  This is the boot-restore path's channel: a spec's `dotStatus` is derived from
 *  current session state at materialization, so a restored chat holding a latch
 *  never calls `setTabStatus`. Registered rather than passed at a door, because a
 *  tab's behaviour cannot depend on who opened it. */
const seededDots = new Map<string, TabDotStatus>();

function registerOpeners(): void {
  registerTabOpeners({
    chat: {
      show: vi.fn(),
      close: vi.fn(),
      dot: (chatID: string) => seededDots.get(chatID) ?? "",
    },
    editor: { show: vi.fn(), close: vi.fn() },
    run: { show: vi.fn() },
    subagent: { show: vi.fn() },
  });
}

/** Open a chat tab and answer with its OPAQUE id: ids are server-minted, so a
 *  case addresses a row through the projection's own lookup. */
async function openChat(
  ref: string,
  opts: { activate?: boolean; parentRef?: string; owns?: boolean } = {},
): Promise<string> {
  await openTab({
    kind: "chat",
    ref,
    ...(opts.activate === undefined ? {} : { activate: opts.activate }),
    ...(opts.parentRef === undefined ? {} : { parent: tabIdFor("chat", opts.parentRef) }),
    ...(opts.owns === undefined ? {} : { owns: opts.owns }),
  });
  return tabIdFor("chat", ref);
}

/** MutationObserver delivers on a microtask; a transitionend is synchronous. */
async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

// ---------------------------------------------------------------------------
// 1. The browser sinks.
// ---------------------------------------------------------------------------

describe("the title sink", () => {
  beforeEach(() => {
    document.head.innerHTML = "";
    document.title = "Whatever The Server Served";
  });

  it("composes the count onto the title the document was SERVED with", () => {
    // Not a constant of its own. The literal this replaced was a second copy of
    // static/index.html's <title> with nothing keeping them equal, so editing
    // that file lost silently to the first count write.
    const env = browserAttentionEnv();
    env.titlePrefix("(2) ");
    expect(document.title).toBe("(2) Whatever The Server Served");
  });

  it("restores the exact served title at zero", () => {
    const env = browserAttentionEnv();
    env.titlePrefix("(2) ");
    env.titlePrefix("");
    expect(document.title).toBe("Whatever The Server Served");
  });

  it("cannot compound a prefix, however many times it is written", () => {
    // The bug avoided by holding the base rather than reading document.title
    // back: the current value already carries a prefix.
    const env = browserAttentionEnv();
    for (const n of ["(1) ", "(2) ", "(3) "]) {
      env.titlePrefix(n);
    }
    expect(document.title).toBe("(3) Whatever The Server Served");
  });

  it("is the floor: present even with no badge and no icon links", () => {
    vi.stubGlobal("navigator", {});
    const env = browserAttentionEnv();
    expect(env.setBadge).toBeUndefined();
    expect(env.setIcon).toBeUndefined();
    env.titlePrefix("(5) ");
    expect(document.title).toBe("(5) Whatever The Server Served");
  });
});

describe("the badge sink", () => {
  beforeEach(() => {
    document.head.innerHTML = "";
  });

  it("is absent when the platform has no Badging API", () => {
    // An absent capability is an absent sink, not an error: a browser without
    // the API is a normal state of the world.
    vi.stubGlobal("navigator", {});
    expect(browserAttentionEnv().setBadge).toBeUndefined();
  });

  it("always passes a NUMBER, never the spec's bare flag form", () => {
    // iOS renders nothing at all for setAppBadge() with no argument.
    const setAppBadge = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { setAppBadge });
    browserAttentionEnv().setBadge?.(4);
    expect(setAppBadge).toHaveBeenCalledWith(4);
  });

  it("clears through clearAppBadge where the platform has it", () => {
    const setAppBadge = vi.fn(() => Promise.resolve());
    const clearAppBadge = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { setAppBadge, clearAppBadge });
    browserAttentionEnv().setBadge?.(0);
    expect(clearAppBadge).toHaveBeenCalledTimes(1);
    expect(setAppBadge).not.toHaveBeenCalled();
  });

  it("clears with setAppBadge(0) where clearAppBadge is missing", () => {
    const setAppBadge = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { setAppBadge });
    browserAttentionEnv().setBadge?.(0);
    expect(setAppBadge).toHaveBeenCalledWith(0);
  });

  it("swallows a rejected badge promise", async () => {
    // An OS that will not paint a badge is not an error this page can act on,
    // and an unhandled rejection inside a status sweep surfaces as a page fault.
    const setAppBadge = vi.fn(() => Promise.reject(new Error("unsupported")));
    vi.stubGlobal("navigator", { setAppBadge });
    expect(() => browserAttentionEnv().setBadge?.(2)).not.toThrow();
    await settle();
    expect(setAppBadge).toHaveBeenCalledTimes(1);
  });

  it("swallows a synchronous throw from the badge call", () => {
    const setAppBadge = vi.fn(() => {
      throw new Error("nope");
    });
    vi.stubGlobal("navigator", { setAppBadge });
    expect(() => browserAttentionEnv().setBadge?.(2)).not.toThrow();
  });
});

describe("the icon sink", () => {
  beforeEach(() => {
    vi.stubGlobal("navigator", {});
  });

  it("rewrites EVERY icon link, not the first one a browser might pick", () => {
    document.head.innerHTML =
      '<link rel="icon" type="image/svg+xml" href="/favicon.svg">' +
      '<link rel="icon" sizes="32x32" href="/favicon-32x32.png">';
    browserAttentionEnv().setIcon?.("input");
    const hrefs = [...document.querySelectorAll("link")].map((l) => l.getAttribute("href"));
    expect(hrefs).toEqual(["/favicon-input.svg", "/favicon-input-32x32.png"]);
  });

  it("leaves apple-touch-icon alone, because the OS cached it at install", () => {
    document.head.innerHTML =
      '<link rel="icon" href="/favicon.svg">' +
      '<link rel="apple-touch-icon" href="/apple-touch-icon.png">';
    browserAttentionEnv().setIcon?.("alert");
    expect(document.querySelector('link[rel="apple-touch-icon"]')?.getAttribute("href")).toBe(
      "/apple-touch-icon.png",
    );
  });

  it("leaves it alone even when it is named like a favicon", () => {
    // `rel~="icon"` is the mechanism, and this is the shape that needs it: a
    // touch icon whose file happens to be a sized favicon is a real page, and
    // iconVariantHref would happily rewrite that name. The OS caches this icon
    // when the app is installed, so a swap cannot reach it and pointing it at a
    // variant only 404s the installed app's icon.
    document.head.innerHTML =
      '<link rel="icon" href="/favicon.svg">' +
      '<link rel="apple-touch-icon" sizes="180x180" href="/favicon-180x180.png">';
    browserAttentionEnv().setIcon?.("input");
    expect(document.querySelector('link[rel="apple-touch-icon"]')?.getAttribute("href")).toBe(
      "/favicon-180x180.png",
    );
    expect(document.querySelector('link[rel="icon"]')?.getAttribute("href")).toBe(
      "/favicon-input.svg",
    );
  });

  it("computes every variant from the ORIGINAL, so swaps cannot compound", () => {
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    const env = browserAttentionEnv();
    env.setIcon?.("input");
    env.setIcon?.("done");
    env.setIcon?.("alert");
    expect(document.querySelector("link")?.getAttribute("href")).toBe("/favicon-alert.svg");
  });

  it("restores the original href on null", () => {
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    const env = browserAttentionEnv();
    env.setIcon?.("done");
    env.setIcon?.(null);
    expect(document.querySelector("link")?.getAttribute("href")).toBe("/favicon.svg");
  });

  it("is absent when the page declares no icon link at all", () => {
    document.head.innerHTML = "";
    expect(browserAttentionEnv().setIcon).toBeUndefined();
  });

  it("drives the whole chain from a fold value", () => {
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    document.title = "Vibekit for Kiro";
    const surfaces = createAttention(browserAttentionEnv());
    surfaces.apply({ count: 2, worst: "waiting" });
    expect(document.title).toBe("(2) Vibekit for Kiro");
    // `waiting` shares the `input` asset: three variants ship, not four.
    expect(document.querySelector("link")?.getAttribute("href")).toBe("/favicon-input.svg");
  });
});

// ---------------------------------------------------------------------------
// 2. The visible-rows rule.
// ---------------------------------------------------------------------------

describe("rowsInView", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  it("reports every row fully inside the list's own box", () => {
    const { sidebar, tabList } = buildSidebar([
      { id: "a", top: 110, height: 40 },
      { id: "b", top: 160, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["a", "b"]);
  });

  it("KEEPS a scrolled-out row out of view", () => {
    // The case the whole rule exists for: `#tab-list` is overflow-y:auto, so the
    // forgotten background chat is precisely the one below the fold.
    const { sidebar, tabList } = buildSidebar([
      { id: "a", top: 110, height: 40 },
      { id: "b", top: 900, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["a"]);
  });

  it("excludes a row scrolled off the TOP as well as the bottom", () => {
    const { sidebar, tabList } = buildSidebar([
      { id: "above", top: 20, height: 40 },
      { id: "in", top: 200, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["in"]);
  });

  it("excludes a HALF-clipped row, so the count lingers rather than losing a cue", () => {
    // Fully inside, not partially. The asymmetry is deliberate: a lingering count
    // is dismissible, a blanked cue is not recoverable.
    const { sidebar, tabList } = buildSidebar([
      { id: "half", top: 580, height: 40 },
      { id: "whole", top: 300, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["whole"]);
  });

  it("reports nothing when the sidebar is a drawer parked off the viewport", () => {
    // translateX(-100%) on mobile. checkVisibility() calls such an element fully
    // visible, which is why the geometry is the test.
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    place(sidebar, rect(0, 700, -1024, 1024));
    expect(rowsInView(sidebar, tabList)).toEqual([]);
  });

  it("reports nothing when the sidebar is hidden by CSS", () => {
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    (sidebar as unknown as { checkVisibility: () => boolean }).checkVisibility = () => false;
    expect(rowsInView(sidebar, tabList)).toEqual([]);
  });

  it("reports nothing when the sidebar has no box at all", () => {
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    place(sidebar, rect(0, 0, 0, 0));
    expect(rowsInView(sidebar, tabList)).toEqual([]);
  });

  it("reports nothing when the sidebar has collapsed in either dimension", () => {
    // The shape a desktop collapse would most likely take, and checkVisibility()
    // calls a zero-width element visible because nothing about it is display:none.
    // Both placements are checked because a collapsed box AT the viewport edge is
    // already rejected by the intersection clauses — only one sitting INSIDE the
    // viewport isolates the extent test, and where a future collapse would put the
    // sidebar is not something this rule should have to assume.
    const { sidebar, tabList } = buildSidebar([{ id: "a", top: 110, height: 40 }]);
    for (const collapsed of [rect(0, 700, 0, 0), rect(0, 700, 100, 0), rect(100, 0, 0, 260)]) {
      place(sidebar, collapsed);
      expect(rowsInView(sidebar, tabList)).toEqual([]);
    }
  });

  it("skips a zero-height row, which is a collapsed one rather than a visible one", () => {
    const { sidebar, tabList } = buildSidebar([
      { id: "folded", top: 200, height: 0 },
      { id: "a", top: 250, height: 40 },
    ]);
    expect(rowsInView(sidebar, tabList)).toEqual(["a"]);
  });

  it("is transform-invariant, so it holds while the drawer animates in", () => {
    // The rows and the clip box sit in the same transformed subtree, so a
    // translate moves both identically and containment is preserved. That is what
    // lets the drawer's settled event ask this question mid-flight.
    const { sidebar, tabList } = buildSidebar([
      { id: "a", top: 110, height: 40 },
      { id: "b", top: 900, height: 40 },
    ]);
    place(tabList, rect(-400, 500));
    place(tabList.children[0] as Element, rect(-390, 40));
    place(tabList.children[1] as Element, rect(400, 40));
    expect(rowsInView(sidebar, tabList)).toEqual(["a"]);
  });
});

describe("pageVisible", () => {
  beforeEach(() => {
    hidePage(false);
    setActive("");
  });

  it("reads a prerendered or visible page as visible", () => {
    expect(pageVisible()).toBe(true);
  });

  it("reads a hidden page as hidden", () => {
    hidePage(true);
    expect(pageVisible()).toBe(false);
  });

  // An `isWatching(chatID)` sat here until 2026-08, giving the `turn_done` latch
  // and the acknowledgement pass one definition of "looking at it". The latch
  // stopped asking (a finished turn is `done` whoever is watching), which left the
  // pass as its only reader — and that derives the condition from its own injected
  // wiring, so the exported helper had no production caller left. The behaviour it
  // described is still pinned, by the two cases in the raise-rule block below:
  // a cue on the active visible chat raises nothing, and the same cue on a hidden
  // page raises the count.
});

// ---------------------------------------------------------------------------
// 3. initAttention against the real tab store.
// ---------------------------------------------------------------------------

describe("initAttention", () => {
  let dispose: (() => void) | null = null;

  /** Place one row per TAB id. Opaque ids, so every case reads them back from the
   *  projection rather than composing one. */
  function rows(entries: readonly { id: string; top: number; height: number }[]): void {
    const tabList = document.getElementById("tab-list");
    if (tabList === null) {
      throw new Error("no tab list");
    }
    tabList.innerHTML = "";
    for (const entry of entries) {
      const el = document.createElement("div");
      el.dataset["tabId"] = entry.id;
      place(el, rect(entry.top, entry.height));
      tabList.appendChild(el);
    }
  }

  function count(): number {
    const match = /^\((\d+)\) /.exec(document.title);
    return match === null ? 0 : Number(match[1]);
  }

  function iconVariant(): string {
    return document.querySelector("link")?.getAttribute("href") ?? "";
  }

  /** The acknowledgement map as an object, keyed by TAB id. */
  function seen(): Record<string, string> {
    return JSON.parse(localStorage.getItem(CUE_SEEN_KEY) ?? "{}") as Record<string, string>;
  }

  beforeEach(() => {
    tabServer.reset();
    _resetTabsSyncForTest();
    _resetTabOpenersForTest();
    seededDots.clear();
    registerOpeners();
    resetActionFramework();
    _resetForTest();
    setActive("");
    hidePage(false);
    localStorage.clear();
    document.body.innerHTML = "";
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">';
    document.title = "Vibekit for Kiro";
    vi.stubGlobal("navigator", {});
    // The persistent desktop sidebar, with the list clipping to 100..600. The
    // rows are placed per test; the store's own renderDOM is rAF-coalesced and
    // its markup is tabs.test.ts's subject, so the geometry is stated here.
    const sidebar = document.createElement("div");
    sidebar.id = "sidebar";
    const tabList = document.createElement("div");
    tabList.id = "tab-list";
    sidebar.appendChild(tabList);
    document.body.appendChild(sidebar);
    place(sidebar, rect(0, 700));
    place(tabList, rect(100, 500));
    dispose = initAttention();
  });

  afterEach(() => {
    dispose?.();
    dispose = null;
    _resetTabsSyncForTest();
  });

  it("starts silent, leaving the served title alone", () => {
    expect(document.title).toBe("Vibekit for Kiro");
    expect(iconVariant()).toBe("/favicon.svg");
  });

  it("raises a cue on a background chat", async () => {
    await openChat("a");
    const b = await openChat("b", { activate: false });
    setTabStatus(b, "done");
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-done.svg");
  });

  // "Watching" is a fact about the TAB, not about the chat store's active id: every
  // key in this wiring is a tab id, and a chat id is no longer one.
  it("raises nothing for the tab the reader is watching", async () => {
    const a = await openChat("a");
    setTabStatus(a, "done");
    expect(count()).toBe(0);
  });

  it("raises the active tab's cue while the page is hidden", async () => {
    const a = await openChat("a");
    hidePage(true);
    setTabStatus(a, "failed");
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-alert.svg");
  });

  it("counts each latched chat once and paints the most severe", async () => {
    const ids: string[] = [];
    for (const ref of ["a", "b", "c"]) {
      ids.push(await openChat(ref, { activate: false }));
    }
    setTabStatus(ids[0] ?? "", "done");
    setTabStatus(ids[1] ?? "", "failed");
    setTabStatus(ids[2] ?? "", "input");
    expect(count()).toBe(3);
    expect(iconVariant()).toBe("/favicon-input.svg");
  });

  it("raises a cue the SPEC carried in, with no dot write at all", async () => {
    // The boot-restore path. The factory seeds a chat row's dot from current
    // session state at materialization, so a restored chat holding a latch never
    // calls `setTabStatus` — the tab-set signal is the only thing that can wake the
    // fold for it. This is the leg a funnel hung off the dot alone would miss.
    seededDots.set("a", "failed");
    await openChat("a", { activate: false });
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-alert.svg");
  });

  it("counts a sub-tab chat, which owns its own bridge and its own cue", async () => {
    // A tangent carries a parent and the default `owns`, so it is its own chat
    // rather than a view over its parent's. Excluding sub-tabs would silence it.
    await openChat("parent", { activate: false });
    const tangent = await openChat("tangent", { activate: false, parentRef: "parent" });
    setTabStatus(tangent, "done");
    expect(count()).toBe(1);
  });

  it("does NOT count a view tab watching work another chat owns", async () => {
    // `owns: false` makes a tab a window onto a chat rather than the chat, so
    // counting it would count one chat twice whenever its own tab is open too.
    const a = await openChat("a", { activate: false });
    const view = await openChat("a-view", { activate: false, parentRef: "a", owns: false });
    setTabStatus(a, "done");
    setTabStatus(view, "done");
    expect(count()).toBe(1);
  });

  it("never counts an editor tab's unsaved mark", async () => {
    await openEditorView("/main.go");
    setTabDirty(tabIdFor("editor", "/main.go"), true);
    expect(count()).toBe(0);
  });

  it("clears a chat's cue when the reader switches to it", async () => {
    await openChat("a");
    const b = await openChat("b", { activate: false });
    setTabStatus(b, "done");
    expect(count()).toBe(1);

    activateTab(b);
    expect(count()).toBe(0);
  });

  it("acknowledges only the IN-VIEW rows on becoming visible", async () => {
    const a = await openChat("a", { activate: false });
    const b = await openChat("b", { activate: false });
    setTabStatus(a, "done");
    setTabStatus(b, "input");
    hidePage(true);
    expect(count()).toBe(2);

    // `b` is scrolled below the list's fold: it was never on screen.
    rows([
      { id: a, top: 110, height: 40 },
      { id: b, top: 900, height: 40 },
    ]);
    hidePage(false);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-input.svg");
  });

  it("acknowledges the rest once the reader scrolls to them", async () => {
    const a = await openChat("a", { activate: false });
    const b = await openChat("b", { activate: false });
    setTabStatus(a, "done");
    setTabStatus(b, "input");
    rows([
      { id: a, top: 110, height: 40 },
      { id: b, top: 900, height: 40 },
    ]);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(count()).toBe(1);

    rows([
      { id: a, top: -400, height: 40 },
      { id: b, top: 200, height: 40 },
    ]);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(count()).toBe(0);
  });

  it("acknowledges nothing while the page is hidden", async () => {
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    rows([{ id: a, top: 110, height: 40 }]);
    hidePage(true);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(count()).toBe(1);
  });

  it("acknowledges the in-view rows when the drawer's class changes", async () => {
    // The drawer opening is a class toggle, which is not an event, so it is
    // OBSERVED — that covers the menu button and the edge-swipe path in
    // platform.ts by construction rather than by a call at each site.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    rows([{ id: a, top: 110, height: 40 }]);
    expect(count()).toBe(1);

    const sidebar = document.getElementById("sidebar");
    sidebar?.classList.add("open");
    await settle();
    expect(count()).toBe(0);
  });

  it("acknowledges the in-view rows once the drawer has settled", async () => {
    // The mutation lands BEFORE the transform animates, so on a real phone the
    // drawer's box is still off-viewport at that instant and the geometric test
    // correctly declines. The settled event is what acknowledges then.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    rows([{ id: a, top: 110, height: 40 }]);
    const sidebar = document.getElementById("sidebar");
    if (sidebar === null) {
      throw new Error("no sidebar");
    }
    place(sidebar, rect(0, 700, -1024, 1024));
    sidebar.classList.add("open");
    expect(count()).toBe(1);

    place(sidebar, rect(0, 700));
    sidebar.dispatchEvent(new Event("transitionend", { bubbles: true }));
    expect(count()).toBe(0);
  });

  it("ignores a transition finishing on a tab row rather than the sidebar", async () => {
    // transitionend bubbles, and every row animates its own background on hover.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    rows([{ id: a, top: 110, height: 40 }]);
    const row = document.querySelector("[data-tab-id]");
    row?.dispatchEvent(new Event("transitionend", { bubbles: true }));
    expect(count()).toBe(1);
  });

  it("keeps a scrolled-out row's cue when the drawer opens", async () => {
    const a = await openChat("a", { activate: false });
    const b = await openChat("b", { activate: false });
    setTabStatus(a, "done");
    setTabStatus(b, "input");
    rows([
      { id: a, top: 110, height: 40 },
      { id: b, top: 900, height: 40 },
    ]);
    document.getElementById("sidebar")?.classList.add("open");
    await settle();
    expect(count()).toBe(1);
  });

  it("un-acknowledges a chat whose status moves off the cue", async () => {
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    rows([{ id: a, top: 110, height: 40 }]);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(count()).toBe(0);

    // A new turn starts, then finishes: the next cue is fresh news.
    setTabStatus(a, "working");
    expect(count()).toBe(0);
    setTabStatus(a, "done");
    expect(count()).toBe(1);
  });

  it("re-raises immediately when the cue changes to a different cue", async () => {
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    rows([{ id: a, top: 110, height: 40 }]);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(count()).toBe(0);

    setTabStatus(a, "failed");
    expect(count()).toBe(1);
    expect(iconVariant()).toBe("/favicon-alert.svg");
  });

  it("reaches zero when the last latched chat closes", async () => {
    // The funnel's other leg. `setTabStatus` does not `emit()`, so a recompute
    // hung off the dot alone would leave this count claiming a chat that is gone.
    const a = await openChat("a", { activate: false });
    const b = await openChat("b", { activate: false });
    setTabStatus(a, "done");
    setTabStatus(b, "input");
    expect(count()).toBe(2);

    await closeTab(a);
    expect(count()).toBe(1);
    await closeTab(b);
    expect(count()).toBe(0);
    expect(iconVariant()).toBe("/favicon.svg");
  });

  it("drops a closed chat's acknowledgement from storage", async () => {
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    rows([{ id: a, top: 110, height: 40 }]);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(seen()).toEqual({ [a]: "done" });

    await closeTab(a);
    expect(seen()).toEqual({});
  });

  it("persists an acknowledgement so a reconnect cannot re-raise it", async () => {
    // The latches behind every cue are rebuilt from server state — the connect
    // replay re-delivers turn_state per busy chat and re-pushes every unanswered
    // decision — so without persistence a dismissed count came back on a phone
    // simply returning to a backgrounded page.
    //
    // The KEY is the tab id, which survives a reload because ids are server-minted
    // and persisted with the collection: the same subject comes back under the same
    // id, which is what makes the acknowledgement addressable at all.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "input");
    rows([{ id: a, top: 110, height: 40 }]);
    document.dispatchEvent(new Event("visibilitychange"));
    expect(count()).toBe(0);

    dispose?.();
    _resetForTest();
    document.title = "Vibekit for Kiro";
    dispose = initAttention();
    // The collection is re-adopted through a real `GET /api/tabs`, which is what a
    // reload does — so the row comes back under the id the acknowledgement names.
    await tabServer.seed();
    setTabStatus(a, "input");
    expect(count()).toBe(0);
  });

  it("stops touching the surfaces once disposed", async () => {
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    expect(count()).toBe(1);
    dispose?.();
    dispose = null;
    setTabStatus(a, "failed");
    expect(iconVariant()).toBe("/favicon-done.svg");
  });

  it("hands the page's own icon back when the page goes away with a cue lit", async () => {
    // A browser remembers one icon per URL and shows it for the bookmark, the
    // history row and the new-tab tile. A tab closed on a lit cue would leave a
    // status variant standing in for this app until the next page load.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    expect(iconVariant()).toBe("/favicon-done.svg");

    window.dispatchEvent(new Event("pagehide"));

    expect(iconVariant()).toBe("/favicon.svg");
    expect(document.title).toBe("Vibekit for Kiro");
  });

  it("KEEPS the cue when the browser merely freezes a background tab", async () => {
    // The case the restore must not reach. A frozen tab is still in the strip
    // rendering its icon, so `freeze` is not a proxy for the page going away and
    // restoring there would blank the cue in exactly the case it exists for.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "input");

    document.dispatchEvent(new Event("freeze"));

    expect(iconVariant()).toBe("/favicon-input.svg");
    expect(count()).toBe(1);
  });

  it("repaints the cue when the page comes back from the bfcache", async () => {
    // pagehide fires on bfcache entry too, and that page can return. The sinks
    // are change-gated, so the fold has to be re-run rather than trusted.
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "failed");
    window.dispatchEvent(new Event("pagehide"));
    expect(iconVariant()).toBe("/favicon.svg");

    const restored = new Event("pageshow");
    Object.defineProperty(restored, "persisted", { value: true });
    window.dispatchEvent(restored);

    expect(iconVariant()).toBe("/favicon-alert.svg");
    expect(count()).toBe(1);
  });

  it("stops restoring once disposed", async () => {
    const a = await openChat("a", { activate: false });
    setTabStatus(a, "done");
    dispose?.();
    dispose = null;
    window.dispatchEvent(new Event("pagehide"));
    expect(iconVariant()).toBe("/favicon-done.svg");
  });
});
