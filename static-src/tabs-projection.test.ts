// ---------------------------------------------------------------------------
// The tab projection END TO END: a gesture, the mutation it dispatches, the frame
// the server answers with, and the row that appears.
//
// `tabs-sync.test.ts` pins the version rules against a Set and
// `actions/tabs-actions.test.ts` pins the four mutations against a transport spy.
// Neither can see what this file is for: the JOIN. The rules are only correct if
// the thing they drive is a real projection holding real rows, and the mutations
// are only correct if the caller that awaits them gets a row it can activate.
//
// THE INTERLEAVING IS THE SUBJECT of the first block, and it is the one property
// no unit test reaches. An open has two answers on two channels — the command's
// response carries the id, the `tabs_changed` frame carries the render — and they
// race. Both orders are real traffic:
//
//   - RESPONSE-FIRST is the common case. The continuation must not run yet: a
//     caller that activated on the response would activate a tab whose row does
//     not exist, which is a silent no-op and a blank view.
//   - EVENT-FIRST happens whenever the frame beats the POST's own round trip, and
//     it also covers the IDEMPOTENT open, where the response says `created: false`
//     and NO frame is coming at all. A caller waiting only for the frame there
//     waits forever.
//
// One mechanism answers both (`whenOpen`), so both are driven for every subject
// kind a door opens: a chat, an editor, a singleton, and both run forms — an
// owned run and a review, which share one `(kind, ref)` and differ only in `owns`.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

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
  ICON_PIN_FILLED: "",
  ICON_ALERT: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
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
}));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./store.js", () => ({ get: vi.fn(() => undefined) }));
vi.mock("./run-store.js", () => ({ peekRunState: vi.fn(() => undefined) }));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
// A singleton needs no injected opener: the factory reaches its loader through a
// LAZY import, which is what keeps the five singleton pages out of a cycle with the
// store. So a singleton's activation is observed HERE rather than in `shown`.
vi.mock("./settings-tabs.js", () => ({
  loadSettingsTabData: vi.fn(),
  forceSettingsTab: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({ apiGetTyped: m.tabListRead() })),
);

import {
  openTab,
  closeTab,
  hasTab,
  tabIdFor,
  getActiveTabId,
  setTabPinned,
  _resetForTest,
} from "./tabs.js";
import { setReorderCallback } from "./tabs-drag.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, tabsVersion, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import {
  bindTabsSync,
  fakeSubject,
  settleTabs,
  tabServer,
} from "./__test-helpers__/tabs-server.js";
import { loadSettingsTabData } from "./settings-tabs.js";
import type { OpenTabArgs } from "./tabs.js";
import type { TabKind } from "./types.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

/** The drag's commit callback, captured at module load: tabs.ts registers it once
 *  and a drop is the only production path into `reorder_tabs`. */
const commitDrop = vi.mocked(setReorderCallback).mock.calls[0]?.[0];

/** Which openers ran, so an activation is observable at the seam production uses:
 *  a tab's `onShow` is the FACTORY's, wired by the composition root. */
interface Shown {
  kind: TabKind;
  ref: string;
  /** Whether the projection held the row at the moment the hook fired. */
  rowPresent: boolean;
}

let shown: Shown[];

function registerOpeners(): void {
  shown = [];
  const record = (kind: TabKind, ref: string): void => {
    shown.push({ kind, ref, rowPresent: hasTab(kind, ref) });
  };
  registerTabOpeners({
    chat: {
      show: (ref) => {
        record("chat", ref);
      },
      close: vi.fn(),
      dot: () => "",
    },
    editor: {
      show: (ref) => {
        record("editor", ref);
      },
      close: vi.fn(),
    },
    run: {
      show: (ref) => {
        record("run", ref);
      },
      cancel: vi.fn(),
    },
  });
}

/** Assert the tab's activation hook ran, AND that it ran against a row the
 *  projection already holds — which is the property the open's continuation buys
 *  and the one a response-continuation could not give.
 *
 *  Two channels because the factory has two: three kinds take an injected opener,
 *  and the five singletons reach their loader through a lazy import. */
async function expectActivated(kind: TabKind, ref: string): Promise<void> {
  if (kind === "chat" || kind === "editor" || kind === "run") {
    expect(shown.at(-1)).toEqual({ kind, ref, rowPresent: true });
    return;
  }
  // The lazy import resolves on a microtask, so the loader lands one turn later.
  await settleTabs();
  expect(vi.mocked(loadSettingsTabData)).toHaveBeenCalledWith("general");
}

function paint(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** The strip as REFS, which is what makes an order assertion readable when the
 *  ids are opaque. A singleton's ref is empty, so it reads as its kind. */
async function rowRefs(): Promise<string[]> {
  await paint();
  const byID = new Map(tabServer.subjects().map((s) => [s.id, s.ref === "" ? s.kind : s.ref]));
  const list = document.getElementById("tab-list");
  return [...(list?.querySelectorAll<HTMLElement>("[data-tab-id]") ?? [])].map(
    (n) => byID.get(n.dataset["tabId"] ?? "") ?? n.dataset["tabId"] ?? "",
  );
}

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerOpeners();
  resetActionFramework();
  _resetForTest();
  document.body.innerHTML = '<div id="tab-list"></div>';
});

afterEach(() => {
  _resetTabsSyncForTest();
});

// ---------------------------------------------------------------------------
// 1. Both interleavings, every subject kind a door opens.
// ---------------------------------------------------------------------------

/** The five subjects, and the two run forms are here for a reason rather than for
 *  coverage: a launcher-OWNED run and a run REVIEW share `(kind, ref)` and differ
 *  only in `owns`, which is the one subject field a door decides. */
const SUBJECTS: readonly { desc: string; args: OpenTabArgs; kind: TabKind; ref: string }[] = [
  { desc: "a chat", args: { kind: "chat", ref: "c-1" }, kind: "chat", ref: "c-1" },
  {
    desc: "an editor tab",
    args: { kind: "editor", ref: "/workspace/main.go" },
    kind: "editor",
    ref: "/workspace/main.go",
  },
  { desc: "a singleton", args: { kind: "settings" }, kind: "settings", ref: "" },
  {
    desc: "an owned run",
    args: { kind: "run", ref: "wf-1", owns: true },
    kind: "run",
    ref: "wf-1",
  },
  {
    desc: "a run review",
    args: { kind: "run", ref: "wf-1", owns: false },
    kind: "run",
    ref: "wf-1",
  },
];

describe("event-first: the frame beats the response", () => {
  it.each(SUBJECTS)("$desc resolves with its row already there", async ({ args, kind, ref }) => {
    expect.assertions(5);
    tabServer.setMode("event-first");
    await openTab(args);

    // Nothing was left to deliver, which is the whole difference from the other
    // order: `whenOpen` found the row and resolved on the spot.
    expect(tabServer.pendingCount()).toBe(0);
    expect(hasTab(kind, ref)).toBe(true);
    expect(getActiveTabId()).toBe(tabIdFor(kind, ref));
    expect(await rowRefs()).toEqual([ref === "" ? kind : ref]);
    await expectActivated(kind, ref);
  });

  it("carries the door's `owns` onto the subject, so both run forms are one wire shape", async () => {
    expect.assertions(2);
    tabServer.setMode("event-first");
    await openTab({ kind: "run", ref: "wf-owned", owns: true });
    await openTab({ kind: "run", ref: "wf-review", owns: false });
    const owned = tabServer.subjects().find((s) => s.ref === "wf-owned");
    const review = tabServer.subjects().find((s) => s.ref === "wf-review");
    expect(owned?.owns).toBe(true);
    expect(review?.owns).toBe(false);
  });
});

describe("response-first: the response beats the frame", () => {
  it.each(SUBJECTS)(
    "$desc does not resolve until the frame has painted it",
    async ({ args, kind, ref }) => {
      expect.assertions(6);
      // MANUAL, so the frame lands exactly when this case says it does. The
      // response resolves as soon as it is asked for.
      tabServer.setMode("manual");
      let resolved = false;
      const open = openTab(args).then(() => {
        resolved = true;
      });

      // Every microtask has run and the command has answered, so the id is known
      // here — and the row still is not. A continuation that ran now would
      // activate a tab the projection does not hold.
      await settleTabs();
      expect(tabServer.sentOfType("open_tab")).toHaveLength(1);
      expect(hasTab(kind, ref)).toBe(false);
      expect(resolved).toBe(false);

      tabServer.flushFrames();
      await open;
      expect(hasTab(kind, ref)).toBe(true);
      expect(getActiveTabId()).toBe(tabIdFor(kind, ref));
      await expectActivated(kind, ref);
    },
  );

  // The dropped-frame bound (`whenOpen` RESOLVES on expiry rather than rejecting,
  // because every continuation is an activation and activating a row the projection
  // does not hold is already a no-op) is `tabs-sync.test.ts`'s case: driving it here
  // would mean waiting out the production 10s.
});

// ---------------------------------------------------------------------------
// 2. The idempotent open.
// ---------------------------------------------------------------------------

// `created: false` means the mutation committed NOTHING, so it bumped no version
// and emitted NO frame. It is the case a frame-only rule cannot serve, and the
// reason `openTab` resolves from the response when the row is already here.
describe("an idempotent open", () => {
  it("resolves and activates with no frame of its own", async () => {
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "c-1" });
    await openTab({ kind: "chat", ref: "c-2" });
    expect(getActiveTabId()).toBe(tabIdFor("chat", "c-2"));

    const versionBefore = tabsVersion();
    await openTab({ kind: "chat", ref: "c-1" });

    // Nothing committed: no frame, no version bump, no second row.
    expect(tabsVersion()).toBe(versionBefore);
    expect(tabServer.pendingCount()).toBe(0);
    expect(await rowRefs()).toEqual(["c-1", "c-2"]);
    // And it still ACTIVATED, which is what the gesture asked for.
    expect(getActiveTabId()).toBe(tabIdFor("chat", "c-1"));
  });

  // The one case where `created: false` arrives for a row that is NOT here yet: a
  // tab another mutation opened whose frame is still in flight (a `create_chat`
  // that opened its own chat tab server-side). The wait is therefore taken when
  // the row is missing whatever the flag says.
  it("waits for the frame when the row is missing despite created:false", async () => {
    expect.assertions(3);
    tabServer.setMode("manual");
    // A tab the server already holds, committed without this device asking.
    await tabServer.seed({ kind: "chat", ref: "c-elsewhere" });
    _resetTabsSyncForTest();
    _resetForTest();
    // The projection now holds nothing while the collection holds the tab, which is
    // exactly the shape of a frame still in flight.
    expect(hasTab("chat", "c-elsewhere")).toBe(false);

    let resolved = false;
    const open = openTab({ kind: "chat", ref: "c-elsewhere" }).then(() => {
      resolved = true;
    });
    await settleTabs();
    expect(resolved).toBe(false);

    // The frame that was in flight arrives.
    const subject = tabServer.subjects().find((s) => s.ref === "c-elsewhere");
    if (subject === undefined) {
      throw new Error("the collection lost the tab");
    }
    tabServer.emitRaw({ changed: subject, version: tabServer.version() });
    await open;
    expect(hasTab("chat", "c-elsewhere")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// 3. A snapshot that is behind us.
// ---------------------------------------------------------------------------

// A re-list can lose a race with a local open: the GET goes out, an open commits
// v+1, and the answer describes v. Adopting it would close a tab this device just
// opened — which is the 2026-08-25 defect (a tab closing because its id was absent
// from an incoming list) in new clothes. The guard is a comparison, and this is the
// case that shows what it protects: a ROW, not a Set entry.
describe("a stale GET /api/tabs", () => {
  it("does not take away a tab a committed open already gave us", async () => {
    expect.assertions(4);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "c-old" });
    const versionBefore = tabsVersion();

    // The snapshot the in-flight GET was already carrying: the collection as it was
    // BEFORE that open committed.
    tabServer.queueList({ tabs: [], version: versionBefore - 1 });
    await listTabs();

    expect(hasTab("chat", "c-old")).toBe(true);
    expect(await rowRefs()).toEqual(["c-old"]);
    // The watermark did not move backwards either, or the next real frame would
    // read as a duplicate.
    expect(tabsVersion()).toBe(versionBefore);
    expect(tabServer.listCalls()).toBe(1);
  });

  // The other side of the same comparison: a snapshot AT or ABOVE the local
  // version is authoritative, and a tab absent from it really is closed. That is a
  // different statement from a delta's `order`, which describes one mutation
  // rather than the whole set.
  it("adopts a snapshot at or above the local version, closing what it omits", async () => {
    expect.assertions(2);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "c-old" });
    tabServer.queueList({ tabs: [], version: tabsVersion() + 5 });
    await listTabs();
    expect(hasTab("chat", "c-old")).toBe(false);
    expect(await rowRefs()).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// 4. A skipped version.
// ---------------------------------------------------------------------------

// More than one past local means a frame was MISSED, so nothing after it can be
// trusted as a delta: its `order` may name tabs we never received and its
// `changed` may sit on top of a set we do not hold. The answer is the whole set.
describe("a skipped version", () => {
  it("re-lists instead of applying the frame", async () => {
    expect.assertions(4);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "c-1" });
    const listsBefore = tabServer.listCalls();

    // Another device opened a tab, and the frame for it never reached us. The
    // collection therefore holds two tabs at version+2 while we sit at version+1.
    await tabServer.seed({ kind: "chat", ref: "c-2" });
    _resetTabsSyncForTest();
    _resetForTest();
    await tabServer.seed();
    const local = tabsVersion();

    tabServer.emitRaw({
      changed: fakeSubject("tb_unknown", { ref: "c-3" }),
      version: local + 2,
    });
    await settleTabs();

    // The GET happened, and the frame's own `changed` was NOT applied: the row it
    // named is absent, because the snapshot is what the projection adopted.
    expect(tabServer.listCalls()).toBeGreaterThan(listsBefore);
    expect(hasTab("chat", "c-3")).toBe(false);
    expect(await rowRefs()).toEqual(["c-1", "c-2"]);
    expect(tabsVersion()).toBe(tabServer.version());
  });
});

// ---------------------------------------------------------------------------
// 5. `order` is a permutation, never a membership statement.
// ---------------------------------------------------------------------------

// THE DEFECT THIS WHOLE REFACTOR EXISTS TO FIX. Membership used to arrive as a
// whole-list document, so the client read "absent from the incoming list" as
// "closed elsewhere" — and closed tabs nobody closed, on the live instance, on
// 2026-08-25. Removal is now STATED per id in `removed_ids`, and an id an `order`
// does not name keeps its relative position and sorts LAST.
describe("a tabs_changed whose order omits a tab we hold", () => {
  it("keeps that tab, at the END of the strip, and never closes it", async () => {
    expect.assertions(4);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "a" });
    await openTab({ kind: "chat", ref: "b" });
    await openTab({ kind: "chat", ref: "c" });
    const closed: string[] = [];
    const { setOnTabClosed } = await import("./tabs.js");
    setOnTabClosed((id) => closed.push(id));

    // The order names two of the three. The third is not a closure and must not be
    // read as one.
    tabServer.emitRaw({
      order: [tabIdFor("chat", "c"), tabIdFor("chat", "a")],
      version: tabsVersion() + 1,
    });
    await settleTabs();

    expect(hasTab("chat", "b")).toBe(true);
    // LAST rather than first: an unnamed tab ahead of the strip would put a tab the
    // server has not told us about in front of the arrangement the reader made.
    expect(await rowRefs()).toEqual(["c", "a", "b"]);
    expect(closed).toEqual([]);
    expect(tabServer.sentOfType("close_tab")).toHaveLength(0);
  });

  it("closes only what removed_ids names, in the same frame as an order", async () => {
    expect.assertions(3);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "a" });
    await openTab({ kind: "chat", ref: "b" });
    await openTab({ kind: "chat", ref: "c" });

    // One committed mutation: `b` closed, and the surviving order stated. The three
    // parts are independent, and `removed_ids` is the ONLY statement of closure.
    tabServer.emitRaw({
      removed_ids: [tabIdFor("chat", "b")],
      order: [tabIdFor("chat", "c")],
      version: tabsVersion() + 1,
    });
    await settleTabs();

    expect(hasTab("chat", "b")).toBe(false);
    expect(hasTab("chat", "a")).toBe(true);
    expect(await rowRefs()).toEqual(["c", "a"]);
  });
});

// ---------------------------------------------------------------------------
// 6. Coalescing pulls two ways at once.
// ---------------------------------------------------------------------------

// A DOUBLE GESTURE IS ONE MUTATION: two taps on one door open one tab. A REPEATED
// GESTURE IS SEVERAL: pin -> unpin -> pin has to end pinned. Those pull in
// opposite directions, and both have shipped broken in this fleet — once as an
// argument-composite idempotency key replaying a cached success (`files.rename`),
// once as a `dedupe` default whose key included a unique id and collapsed nothing.
describe("a double gesture on one door", () => {
  it("collapses two opens 0ms apart into ONE round trip", async () => {
    expect.assertions(3);
    tabServer.setMode("event-first");
    // Held responses are the only window `dedupe` covers: the framework evicts its
    // slot in the result's `finally`, so a second dispatch after the first resolved
    // is a new gesture rather than a duplicate.
    tabServer.holdResponses();
    const first = openTab({ kind: "chat", ref: "c-1" });
    const second = openTab({ kind: "chat", ref: "c-1" });
    await settleTabs();
    tabServer.releaseResponses();
    await Promise.all([first, second]);

    expect(tabServer.sentOfType("open_tab")).toHaveLength(1);
    expect(await rowRefs()).toEqual(["c-1"]);
    expect(getActiveTabId()).toBe(tabIdFor("chat", "c-1"));
  });

  it("does NOT collapse two different subjects dispatched together", async () => {
    expect.assertions(2);
    tabServer.setMode("event-first");
    tabServer.holdResponses();
    const a = openTab({ kind: "chat", ref: "c-1" });
    const b = openTab({ kind: "chat", ref: "c-2" });
    await settleTabs();
    tabServer.releaseResponses();
    await Promise.all([a, b]);
    expect(tabServer.sentOfType("open_tab")).toHaveLength(2);
    expect(await rowRefs()).toEqual(["c-1", "c-2"]);
  });

  it("reaches the server again once the first open has resolved", async () => {
    expect.assertions(2);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "c-1" });
    await openTab({ kind: "chat", ref: "c-1" });
    // The second tap is a real dispatch. The server's own `(kind, ref)` uniqueness
    // is what makes it harmless, answering with the tab already open and committing
    // nothing — so a late tap needs no client-side guard at all.
    expect(tabServer.sentOfType("open_tab")).toHaveLength(2);
    expect(await rowRefs()).toEqual(["c-1"]);
  });

  it("collapses two closes of one tab, and the outcome is the same either way", async () => {
    expect.assertions(2);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "c-1" });
    const id = tabIdFor("chat", "c-1");
    tabServer.holdResponses();
    const a = closeTab(id);
    const b = closeTab(id);
    await settleTabs();
    tabServer.releaseResponses();
    await Promise.all([a, b]);
    expect(tabServer.sentOfType("close_tab")).toHaveLength(1);
    expect(hasTab("chat", "c-1")).toBe(false);
  });
});

describe("a repeated gesture that must NOT collapse", () => {
  it("executes pin -> unpin -> pin, all three, ending pinned", async () => {
    expect.assertions(3);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "c-1" });
    const id = tabIdFor("chat", "c-1");

    await setTabPinned(id, true);
    await setTabPinned(id, false);
    await setTabPinned(id, true);

    // Three mutations, three commits. A key that collapsed the third onto the first
    // would leave the collection at the SECOND, silently — which is the shape of
    // `files.rename`'s cached-success replay.
    expect(tabServer.sentOfType("pin_tab")).toHaveLength(3);
    expect(tabServer.subjects()[0]?.pinned).toBe(true);
    const pins = tabServer.sentOfType("pin_tab").map((c) => c.payload["pinned"]);
    expect(pins).toEqual([true, false, true]);
  });

  it("executes a drag A -> B -> A, all three, ending at A", async () => {
    expect.assertions(3);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "a" });
    await openTab({ kind: "chat", ref: "b" });
    const a = tabIdFor("chat", "a");
    const b = tabIdFor("chat", "b");
    expect(commitDrop).toBeTypeOf("function");

    commitDrop?.([a, b]);
    await settleTabs();
    commitDrop?.([b, a]);
    await settleTabs();
    commitDrop?.([a, b]);
    await settleTabs();

    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(3);
    expect(await rowRefs()).toEqual(["a", "b"]);
  });

  // The exact-set check IS the whole precondition, so a 409 means the set moved
  // under the drag: re-list, never re-send. The arrangement the gesture committed
  // describes a collection that no longer exists.
  it("answers a refused reorder with a re-list rather than a re-send", async () => {
    expect.assertions(4);
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "a" });
    await openTab({ kind: "chat", ref: "b" });
    const listsBefore = tabServer.listCalls();

    // ANOTHER DEVICE opened a tab and its frame has not reached us, so the
    // collection holds three tabs while the strip holds two. Every order this
    // device can compose is therefore a two-element set against a three-element
    // collection: the exact-set check refuses it, which is what "the set moved
    // under the drag" IS.
    tabServer.openElsewhere({ kind: "chat", ref: "c" });
    commitDrop?.([tabIdFor("chat", "b"), tabIdFor("chat", "a")]);
    await settleTabs();

    expect(tabServer.sentOfType("reorder_tabs")).toHaveLength(1);
    // ONE send, then a GET. Re-sending would refuse again, because the order still
    // describes a collection that no longer exists.
    expect(tabServer.listCalls()).toBe(listsBefore + 1);
    // The honest answer is the CURRENT set with the drag snapped back.
    expect(await rowRefs()).toEqual(["a", "b", "c"]);
    expect(tabsVersion()).toBe(tabServer.version());
  });
});

// ---------------------------------------------------------------------------
// 7. A refused mutation.
// ---------------------------------------------------------------------------

// NOTHING RENDERS OPTIMISTICALLY, which is what makes a refusal cheap: the strip
// is exactly as it was, rather than half-drawn. The action framework has already
// raised its toast, and there is no local state to roll back because none was
// written.
describe("a failed mutation leaves the strip unchanged", () => {
  beforeEach(async () => {
    tabServer.setMode("event-first");
    await openTab({ kind: "chat", ref: "a" });
    await openTab({ kind: "chat", ref: "b" });
    await setTabPinned(tabIdFor("chat", "a"), true);
  });

  it("a refused open adds no row and never rejects", async () => {
    expect.assertions(4);
    tabServer.failNext("open_tab");
    const before = await rowRefs();
    await expect(openTab({ kind: "chat", ref: "c" })).resolves.toBeUndefined();
    expect(hasTab("chat", "c")).toBe(false);
    expect(await rowRefs()).toEqual(before);
    expect(getActiveTabId()).toBe(tabIdFor("chat", "b"));
  });

  // At the product limit the refusal has a remedy, which is the difference between
  // a control that looks broken and one that is bounded.
  it("a 409 open is refused the same way, with no row and no throw", async () => {
    expect.assertions(2);
    tabServer.failNext("open_tab", 409, "too many open");
    await expect(openTab({ kind: "chat", ref: "c" })).resolves.toBeUndefined();
    expect(hasTab("chat", "c")).toBe(false);
  });

  it("a refused close leaves the tab and its position", async () => {
    expect.assertions(3);
    tabServer.failNext("close_tab");
    await closeTab(tabIdFor("chat", "b"));
    expect(hasTab("chat", "b")).toBe(true);
    expect(await rowRefs()).toEqual(["a", "b"]);
    expect(getActiveTabId()).toBe(tabIdFor("chat", "b"));
  });

  it("a refused pin leaves the subject's pin and the partition alone", async () => {
    expect.assertions(3);
    tabServer.failNext("pin_tab");
    await setTabPinned(tabIdFor("chat", "b"), true);
    expect(tabServer.subjects().find((s) => s.ref === "b")?.pinned).toBe(false);
    // The partition is a rendering rule over the stored order, so an uncommitted
    // pin cannot move a row.
    expect(await rowRefs()).toEqual(["a", "b"]);
    expect(tabServer.version()).toBe(3);
  });

  it("a refused reorder leaves the order, and does not re-list", async () => {
    expect.assertions(2);
    const listsBefore = tabServer.listCalls();
    tabServer.failNext("reorder_tabs", 500, "boom");
    commitDrop?.([tabIdFor("chat", "b"), tabIdFor("chat", "a")]);
    await settleTabs();
    expect(await rowRefs()).toEqual(["a", "b"]);
    // A 500 is not a 409: only the exact-set refusal means "you are behind".
    expect(tabServer.listCalls()).toBe(listsBefore);
  });
});
