// ---------------------------------------------------------------------------
// AN INTENT TO OPEN NEVER CLOSES A TAB, from two directions.
//
// `openTab({kind})` is idempotent by subject: it activates an open tab and opens
// a closed one, which is what a route means. A `toggle*View` helper CLOSES an
// already-active tab, so it belongs to a user affordance that toggles — a sidebar
// button, a keyboard shortcut — and to nothing that expresses navigation.
//
// The steering rule said that of `applyRoute` alone, and three call sites drifted
// anyway. So this file holds the two mechanisms that make it a property of the
// codebase: a SOURCE GUARD over the modules that own navigation, and a
// BEHAVIOURAL case that drives the real tab projection twice.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import routeApplySrc from "./route-apply.ts?raw";
import navigateSrc from "./navigate.ts?raw";
import pushMessageSrc from "./handlers/push-message.ts?raw";
import notificationOpenSrc from "./notification-open.ts?raw";
import tabMaterializeSrc from "./tab-materialize.ts?raw";
import settingsHighlightSrc from "./settings-highlight.ts?raw";
import recipesSrc from "./recipes.ts?raw";
import docsSrc from "./docs.ts?raw";
import historySrc from "./history.ts?raw";
import settingsSrc from "./settings.ts?raw";
import filesSrc from "./files.ts?raw";
import appSrc from "./app.ts?raw";

// ---------------------------------------------------------------------------
// The source guard
// ---------------------------------------------------------------------------

// PINNED LITERALLY, because the pattern IS the assertion. The trailing `\(` is
// load-bearing in both directions: it makes the guard read CALLS rather than
// mentions, so `tab-materialize.ts`'s prose sentence naming `toggleSettingsView`
// and the corrected comments in `history.ts` do not trip it, and it is what makes
// a re-added `show*`-that-toggles wrapper trip it.
const TOGGLE_CALL = /\btoggle[A-Za-z]*View\s*\(/;

/** The navigation-owning modules, one row and one reason each. */
const POPULATION: readonly { readonly name: string; readonly src: string; readonly why: string }[] =
  [
    {
      name: "route-apply.ts",
      src: routeApplySrc,
      why: "the router: a toggle here destroys the tab its own URL names",
    },
    {
      name: "navigate.ts",
      src: navigateSrc,
      why: "every in-app door into a view, including the turn footer's Review changes",
    },
    {
      name: "handlers/push-message.ts",
      src: pushMessageSrc,
      why: "a notification click is an intent to open, arriving from off-screen",
    },
    {
      name: "notification-open.ts",
      src: notificationOpenSrc,
      why: "the page seam a notification's route is spent through",
    },
    {
      name: "tab-materialize.ts",
      src: tabMaterializeSrc,
      why: "the factory materializes a subject, so a toggle would destroy the tab it describes",
    },
    {
      name: "settings-highlight.ts",
      src: settingsHighlightSrc,
      why: "a link to a named setting, which used to dismiss the panel it pointed at",
    },
    {
      name: "recipes.ts",
      src: recipesSrc,
      why: "a link out of a modal to a named panel",
    },
    {
      name: "docs.ts",
      src: docsSrc,
      why: "a PAGE module owns its content and its activation hooks, never a door that can close its own tab",
    },
    {
      name: "history.ts",
      src: historySrc,
      why: "the same, and the other place the trap lived: a show* wrapper that toggled",
    },
  ];

describe("the source guard", () => {
  // app.ts is deliberately NOT in the population: the applyRoute extraction is
  // what removed it, and what remains there is the two sidebar buttons, which are
  // the affordance the rule protects.
  it.each(POPULATION)("$name reaches no toggle*View call ($why)", ({ src }) => {
    expect.assertions(1);
    expect(src).not.toMatch(TOGGLE_CALL);
  });

  // The NEGATIVE CONTROL: without it the guard passes just as well when the
  // pattern has stopped matching anything at all.
  it.each([
    { name: "settings.ts", src: settingsSrc },
    { name: "files.ts", src: filesSrc },
    { name: "app.ts", src: appSrc },
  ])("$name still contains a toggle call, so the pattern matches", ({ src }) => {
    expect.assertions(1);
    expect(src).toMatch(TOGGLE_CALL);
  });
});

// ---------------------------------------------------------------------------
// An open intent is idempotent
// ---------------------------------------------------------------------------
//
// Against the REAL tab projection and the fake collection in
// `__test-helpers__/tabs-server.ts` — the shape `tabs.test.ts` uses, where every
// open is a dispatched mutation and the `tabs_changed` frame that follows is what
// paints, so every open is awaited.

const mocks = vi.hoisted(() => ({
  requestPRFocus: vi.fn<(identity: string) => void>(),
  forceGitTab: vi.fn(),
}));

vi.mock("./router.js", () => ({ pushRoute: vi.fn(), replaceRoute: vi.fn() }));
// route-apply's own graph, reduced to the arms this file does not drive. Each is a
// no-op rather than absent, because Browser Mode links ESM for real: a name any
// module in the graph imports has to exist on the factory.
vi.mock("./deep-link.js", () => ({
  admitLocation: vi.fn(() => "opens"),
  settleDeepLinkedChat: vi.fn(),
}));
vi.mock("./chat.js", () => ({ switchSession: vi.fn() }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./files.js", () => ({ pointFilesTab: vi.fn() }));
vi.mock("./settings-tabs.js", () => ({ forceSettingsTab: vi.fn() }));
vi.mock("./settings-highlight.js", () => ({ flushURLHighlight: vi.fn() }));
vi.mock("./git-tabs.js", () => ({ forceGitTab: mocks.forceGitTab }));
// The PRs tab is reached through a dynamic import, so mocking it is what makes the
// call OBSERVABLE without pulling the git view into this fixture.
vi.mock("./git-prs-tab.js", () => ({ requestPRFocus: mocks.requestPRFocus }));
vi.mock("./icons.js", () => ({
  ICON_CLOSE: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_FILES: "",
  ICON_TAB_RUN: "",
  ICON_TAB_AGENT: "",
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
  byId: (id: string) => {
    let el = document.getElementById(id);
    if (el === null) {
      el = document.createElement("span");
      el.id = id;
      document.body.appendChild(el);
    }
    return el;
  },
}));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
  DRAG_THRESHOLD_PX: 5,
}));
vi.mock("./store.js", () =>
  import("./__test-helpers__/store-mock.js").then((m) => ({ ...m.storeMock })),
);
vi.mock("./run-store.js", () => ({ runLabelOf: vi.fn(() => "") }));
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
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiGetTyped: m.tabListRead(),
    apiGet: vi.fn(() => Promise.resolve(null)),
  })),
);

import { openGitView, tabIdFor, getActiveTabRoute, _resetForTest } from "./tabs.js";
import { applyRoute } from "./route-apply.js";
import {
  registerNotificationOpener,
  _resetNotificationOpenerForTest,
} from "./notification-open.js";
import { routePushMessage } from "./handlers/push-message.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), dot: () => "" },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  document.body.innerHTML = '<div id="tab-list"></div>';
});

afterEach(() => {
  _resetTabsSyncForTest();
});

describe("an open intent is idempotent", () => {
  // The exact shape of the reported defect: today's `openChangeSet()` twice closes
  // the git view, and `setGitTab` then no-ops because `tabIdFor("git")` is "".
  it("leaves the git tab open and on prs after two openGitView calls", async () => {
    expect.assertions(3);
    await openGitView("prs");
    expect(tabIdFor("git")).not.toBe("");

    await openGitView("prs");
    expect(tabIdFor("git")).not.toBe("");
    expect(getActiveTabRoute()).toEqual({ kind: "git", tab: "prs" });
  });
});

// ---------------------------------------------------------------------------
// The same property one layer up: at the NOTIFICATION layer, where the reported
// defect lived. A tab-layer case cannot reach it, because the route has to travel
// through the subject vocabulary and the router's git branch to get there.
// ---------------------------------------------------------------------------

/** The projection is a round trip: the open is dispatched and the `tabs_changed`
 *  frame that follows is what paints, and the PRs tab is reached through a dynamic
 *  import on top of that. So every assertion polls. */
async function settled(check: () => boolean, what: string): Promise<void> {
  for (let i = 0; i < 50; i++) {
    if (check()) {
      break;
    }
    await new Promise((r) => {
      setTimeout(r, 1);
    });
  }
  expect(check(), what).toBe(true);
}

describe("a PR notification's destination", () => {
  const identity = "github:github.com:cplieger/vibekit#42";

  function click(): void {
    routePushMessage({
      type: "push",
      reason: "clicked",
      chatId: "",
      subject: `pr:${identity}`,
      title: "Vibekit",
      body: "",
    });
  }

  beforeEach(() => {
    mocks.requestPRFocus.mockClear();
    _resetNotificationOpenerForTest();
    // The REAL route-apply, through the real page seam: what a notification click
    // spends is a Route, and the two halves must agree about what that means.
    registerNotificationOpener((route) => {
      void applyRoute(route);
    });
  });

  afterEach(() => {
    _resetNotificationOpenerForTest();
  });

  it("opens the git tab on prs and focuses the pull request, on every click", async () => {
    expect.assertions(7);
    click();
    // Polled on the focus request rather than on the tab, because it is the LAST
    // step of the branch: the open has landed, the sub-tab is corrected and the
    // chunk has loaded by the time it fires.
    await settled(() => mocks.requestPRFocus.mock.calls.length === 1, "the first focus request");
    expect(mocks.requestPRFocus).toHaveBeenCalledWith(identity);
    expect(tabIdFor("git")).not.toBe("");
    expect(getActiveTabRoute()).toEqual({ kind: "git", tab: "prs" });

    // The SECOND click is the whole point: an intent to open is idempotent, so the
    // tab is still open and still on prs, and the request is made again.
    click();
    await settled(() => mocks.requestPRFocus.mock.calls.length === 2, "the second focus request");
    expect(tabIdFor("git")).not.toBe("");
    expect(getActiveTabRoute()).toEqual({ kind: "git", tab: "prs" });
  });
});
