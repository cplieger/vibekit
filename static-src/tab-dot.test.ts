//
// The tab activity dot: the state mapping, the accessible name, and the
// reduced-motion degradation.
//
// Ported from @cplieger/web-terminal-ui's `.wt-status-dot`, and the three things
// pinned here are the three that a port gets wrong silently:
//
//  1. THE MAPPING. Six chat states derive from four independent signals
//     (`thinking`, `agent_status`, the failure latch, the dock's queue), and two
//     of them COEXIST — a permission ask arrives mid-turn with `thinking` still
//     true. Precedence is therefore load-bearing rather than cosmetic: with
//     working first, every ask on a background chat is masked by the state that
//     needs nothing from anyone. That masking is a cost the app already pays for
//     elsewhere (the permission push notice cannot be silenced precisely because
//     "a background chat waiting on an approval renders identically to one that
//     is working"), so getting the order wrong would silently keep it.
//
//  2. THE ACCESSIBLE NAME. A 9px disc gives a screen-reader user nothing, and
//     this feature exists FOR tabs nobody is looking at. The announced word also
//     has to follow the tab name rather than precede it, which is a function of
//     where in the row the element sits — invisible to any type check.
//
//  3. REDUCED MOTION. 40-a11y.css zeroes every animation's duration and
//     iteration count globally, which RUNS each animation to completion rather
//     than suppressing it. Neither dot keyframe declares a fill-mode, so a
//     completed `vk-dot-wave` reverts its ::after to that rule's own
//     declarations — `opacity: 1`, no transform — leaving a solid opaque band
//     welded to the disc forever. `content: none` is what prevents that, and
//     nothing about the global rule makes it obvious.
//
// The CSS half asserts SOURCE facts because the test page loads no app
// stylesheet: nothing links `css/MANIFEST`, so `getComputedStyle` has no cascade
// to report on and cannot answer "which rule applies".

import { describe, it, expect, beforeEach, vi } from "vitest";
import cssContrastScript from "../scripts/css-contrast.py?raw";
import chatSrc from "./chat.ts?raw";
import { ruleContaining, loadCSS } from "./__test-helpers__/css-rules.js";
import {
  tabStatusFor,
  setSessions,
  setThinking,
  setTurnDone,
  clearTurnDone,
  get,
} from "./store.js";
import type { ChatHeader, PermissionNeededPayload, Session, TabKind } from "./types.js";
import type { TabDotStatus } from "./tab-view.js";

function session(over: Partial<Session> = {}): Session {
  return {
    id: "c1",
    name: "Fix the parser",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    usage: { context_pct: 0, context_size: 0, credits: 0, turns: 0, last_turn_ms: 0 },
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    ...over,
  } as Session;
}

// ---------------------------------------------------------------------------
// 1. Signal -> state.
// ---------------------------------------------------------------------------

describe("tabStatusFor maps each signal to its dot state", () => {
  it("idles a chat with no signal at all", () => {
    // The FLOOR for a real chat, not an absence: a chat tab always shows a dot,
    // which keeps the strip's leading column aligned and makes "nothing is
    // happening here" readable rather than inferred.
    expect(tabStatusFor(session())).toBe("idle");
  });

  it("returns nothing at all for a chat it has never heard of", () => {
    expect(tabStatusFor(undefined)).toBe("");
  });

  it("works while a turn is in flight", () => {
    expect(tabStatusFor(session({ thinking: true }))).toBe("working");
  });

  it("waits when the agent declared waiting_on_user", () => {
    expect(tabStatusFor(session({ agent_status: "waiting_on_user" }))).toBe("waiting");
  });

  it("finishes when the agent declared completed", () => {
    expect(tabStatusFor(session({ agent_status: "completed" }))).toBe("done");
  });

  it("fails when the failure latch is set", () => {
    expect(tabStatusFor(session({ turn_failed: true }))).toBe("failed");
  });

  it("needs a decision when the chat's dock holds an unanswered ask", () => {
    expect(tabStatusFor(session(), true)).toBe("input");
  });

  it("puts a pending ask AHEAD of the in-flight turn that raised it", () => {
    // The precedence that matters. These two genuinely coexist: KAS raises the
    // ask mid-turn and `thinking` stays true until the turn ends, so reporting
    // `working` here would hide every approval the app is blocked on.
    expect(tabStatusFor(session({ thinking: true }), true)).toBe("input");
  });

  it("puts a pending ask ahead of every settled verdict too", () => {
    // An ask outlives the turn that raised it, so a turn_ended or an error
    // landing first must not bury it.
    expect(tabStatusFor(session({ turn_failed: true }), true)).toBe("input");
    expect(tabStatusFor(session({ agent_status: "completed" }), true)).toBe("input");
  });

  it("reports a failure rather than an in-flight turn if both are somehow set", () => {
    // Unreachable today — the error handler clears thinking and a new turn clears
    // the latch — so this pins the ORDER, which is the thing a future writer of
    // either field could invalidate without noticing.
    expect(tabStatusFor(session({ thinking: true, turn_failed: true }))).toBe("failed");
  });

  it("ignores the agent statuses that are not their own dot state", () => {
    // `in_progress` and `idle` arrive on the same channel as waiting/completed.
    // in_progress duplicates `thinking`, which is the authoritative busy signal
    // (the server synthesizes it on connect); a chat is not made busy by the
    // agent SAYING so while the turn machinery reports otherwise.
    expect(tabStatusFor(session({ agent_status: "in_progress" }))).toBe("idle");
    expect(tabStatusFor(session({ agent_status: "idle" }))).toBe("idle");
  });
});

// ---------------------------------------------------------------------------
// 2. The accessible name.
// ---------------------------------------------------------------------------

vi.mock("./router.js", () => ({
  pushRoute: vi.fn(),
  buildPath: vi.fn(() => "/"),
  parseRoute: vi.fn(),
}));
vi.mock("./tabs-drag.js", () => ({
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
// The tab set is SERVER-owned, so a row lands on the strip through a real round
// trip against the fake collection: `send` answers the four tab commands off it
// and emits the frames, `newOpID` mints the correlation id.
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
vi.mock("./run-store.js", () => ({ peekRunState: vi.fn(() => undefined) }));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));

// TWO readers of `apiGetTyped` in this graph and they answer different routes:
// store-load's chat read (stubbed at the boundary so the reconcile logic under
// test is the real one) and tabs-sync's `GET /api/tabs`, which the harness answers
// off the fake collection.
const { mockApiGetTyped } = vi.hoisted(() => ({ mockApiGetTyped: vi.fn() }));
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => {
    const listTabs = m.tabListRead();
    return {
      apiGetTyped: vi.fn((path: string, decode: unknown) =>
        path === "/api/tabs" ? listTabs(path) : mockApiGetTyped(path, decode),
      ),
    };
  }),
);
// `./actions/index.js` is deliberately NOT mocked. It is the actions framework's
// re-export, and `actions/tabs.ts` — the four tab mutations the projection
// dispatches — needs its whole surface, so a one-symbol stub no longer links. The
// harness resets the framework instead, which is what the action suites do.

// The dock's two leaves that reach for DOM it does not own, plus the toast,
// mocked exactly as decision-dock.test.ts mocks them.
vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  openFile: undefined,
  openFileGitDiff: vi.fn(),
}));
vi.mock("./actions/permissions.js", () => ({ editNativeRule: { dispatch: vi.fn() } }));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

/** The tab strip's real render target, so renderDOM runs for real. */
vi.mock("./dom.js", () => {
  const cache = new Map<string, HTMLElement>();
  return {
    $: new Proxy(
      {},
      {
        get(_t, prop: string) {
          if (prop === "tabList") {
            let list = cache.get("tabList");
            if (list === undefined || !list.isConnected) {
              list = document.getElementById("tab-list") ?? document.createElement("div");
              cache.set("tabList", list);
            }
            return list;
          }
          return document.createElement("div");
        },
      },
    ),
    byId: vi.fn(() => document.createElement("div")),
  };
});

async function paint(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(null)));
}

/** What a screen reader computes for the `role="tab"` row: its name from
 *  contents, in DOM order.
 *
 *  no accessible-name algorithm is consulted here and none of the
 *  app's CSS, so this is the traversal, and its two exclusions are the model
 *  rather than convenience:
 *
 *   - `aria-hidden="true"` — the dot itself. Excluded by the spec.
 *   - `.tab-pin` on a row without `.tab-pinned` — 12-tabs.css gives it
 *     `display: none`, which removes it from the accessibility tree. Every row
 *     carries the node so renderDOM toggles a class instead of adding and
 *     removing one, so a traversal that ignored the class would announce
 *     "Pinned" on every tab in the strip.
 *   - the close BUTTON — a focusable child with its own role and its own name,
 *     which AT presents as a separate node rather than folding into its
 *     container's.
 *
 *  The property under test is ORDER, and none of the three exclusions can
 *  affect it: the state word sits between the name and the pin. */
function nameFromContents(row: HTMLElement): string {
  const parts: string[] = [];
  for (const node of row.childNodes) {
    if (node.nodeType === Node.TEXT_NODE) {
      parts.push(node.textContent ?? "");
      continue;
    }
    const el = node as HTMLElement;
    if (el.getAttribute("aria-hidden") === "true") {
      continue;
    }
    if (el.tagName === "BUTTON") {
      continue;
    }
    if (el.classList.contains("tab-pin") && !row.classList.contains("tab-pinned")) {
      continue;
    }
    parts.push(el.getAttribute("aria-label") ?? el.textContent ?? "");
  }
  return parts
    .join(" ")
    .replace(/\s+([,.])/g, "$1")
    .replace(/\s+/g, " ")
    .trim();
}

// ---------------------------------------------------------------------------
// The projection harness the DOM sections below share.
//
// Every row on the strip arrives through a real `open_tab` round trip against the
// fake collection, so the ids are OPAQUE and server-minted: nothing here composes
// `c1` or `editor:a.ts`, and a row is addressed through `tabIdFor` after it
// exists. The tab's activation and teardown hooks are the FACTORY's, registered
// the way the composition root registers them, and the seeded dot rides that same
// registration rather than a field a caller sets.
// ---------------------------------------------------------------------------

const seededDots = new Map<string, TabDotStatus>();

async function resetProjection(): Promise<void> {
  const { _resetForTest } = await import("./tabs.js");
  const { registerTabOpeners, _resetTabOpenersForTest } = await import("./tab-materialize.js");
  const { _resetTabsSyncForTest, ingestTabsChanged, listTabs } = await import("./tabs-sync.js");
  const { resetActionFramework } = await import("./actions/__test-helpers__/action-test-setup.js");
  const { bindTabsSync, tabServer } = await import("./__test-helpers__/tabs-server.js");
  bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  seededDots.clear();
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
  resetActionFramework();
  _resetForTest();
  document.body.innerHTML = '<div id="tab-list"></div>';
}

/** Open a tab of any kind and answer with its minted id. */
async function openSubject(
  kind: TabKind,
  ref = "",
  opts: { activate?: boolean } = {},
): Promise<string> {
  const { openTab, tabIdFor } = await import("./tabs.js");
  await openTab({
    kind,
    ref,
    ...(opts.activate === undefined ? {} : { activate: opts.activate }),
  });
  return tabIdFor(kind, ref);
}

describe("the tab's accessible name announces its state", () => {
  /** The chat tab's opaque id, for the cases that write to it after opening. */
  let chatTabID = "";

  beforeEach(async () => {
    await resetProjection();
    chatTabID = "";
  });

  async function openChat(): Promise<HTMLElement> {
    const { renameTab } = await import("./tabs.js");
    chatTabID = await openSubject("chat", "c1");
    // The store holds no row for this chat, so the factory's derived default is a
    // placeholder. The label a caller knows is the one field of a spec it may
    // override, which is exactly how the run and chat openers supply theirs.
    renameTab(chatTabID, "Fix the parser");
    await paint();
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${chatTabID}"]`);
    if (row === null) {
      throw new Error("tab did not render");
    }
    return row;
  }

  it("names the chat first and its state second", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();

    // Seeded at creation, so the row is never a frame narrower than its
    // neighbours and never announces a chat with no state.
    expect(nameFromContents(row)).toBe("Fix the parser, idle");

    setTabStatus(chatTabID, "working");
    expect(nameFromContents(row)).toBe("Fix the parser, working");
  });

  it("gives every state a distinct spoken phrase", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();
    const spoken = new Map<string, string>();
    for (const s of ["idle", "working", "waiting", "input", "failed", "done"] as const) {
      setTabStatus(chatTabID, s);
      spoken.set(s, nameFromContents(row));
    }
    // `waiting` and `input` share one VISUAL (a 9px disc has no channel left to
    // separate them — see css/12-tabs.css), so these phrases are the only place
    // the distinction survives. If they ever collapse to near-synonyms the
    // information is simply gone.
    expect(new Set(spoken.values()).size).toBe(6);
    expect(spoken.get("waiting")).toBe("Fix the parser, waiting for you");
    expect(spoken.get("input")).toBe("Fix the parser, needs a decision");
  });

  it("does not claim a TURN failed for a failure that had no turn in it", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();
    setTabStatus(chatTabID, "failed");
    // The latch behind this state is set for every `error` frame naming the chat,
    // and that deliberately includes `switch_failed` and `bridge_start_failed` —
    // failures with no turn in them. The breadth is right (a chat whose bridge
    // would not start has failed at something the reader needs to see), so the
    // PHRASE is what had to widen: it is the only channel a screen-reader user has
    // here, and it must not be more specific than its producer supports.
    expect(nameFromContents(row)).toBe("Fix the parser, last operation failed");
    expect(nameFromContents(row)).not.toContain("turn");
  });

  it("keeps the state word between the name and the pinned marker", async () => {
    const { setTabStatus, setTabPinned } = await import("./tabs.js");
    const row = await openChat();
    setTabPinned(chatTabID, true);
    await paint();
    setTabStatus(chatTabID, "input");
    // Both extra words compose onto the name, in the order they are read: what
    // this chat IS, then what it needs, then how it is filed. That ordering falls
    // out of DOM position, which is the only reason the announced word is a
    // sibling after `.tab-name` rather than a child of the leading dot.
    expect(nameFromContents(row)).toBe("Fix the parser, needs a decision Pinned");
  });

  it("hides the dot itself from the accessibility tree", async () => {
    const row = await openChat();
    // Colour and shape are one channel; the word beside it is the other. A dot
    // that is not aria-hidden would announce as an unlabelled element AND put
    // its state ahead of the name, since it leads the row.
    expect(row.querySelector(".tab-status-dot")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("leads a chat row with the dot and gives every other kind its glyph", async () => {
    const chat = await openChat();
    // A singleton's ref is EMPTY: its identity is its kind, so the `__files__`
    // sentinel id is gone with every other composed one.
    const filesID = await openSubject("files");
    await paint();
    const files = document.querySelector<HTMLElement>(`[data-tab-id="${filesID}"]`);

    // The replacement: a chat tab has no role glyph at all, and its leading
    // element is the dot. That position is what the CSS's `:first-child` slot
    // rule keys on, so it is a real contract rather than an ordering detail.
    expect(chat.querySelector(".tab-icon")).toBeNull();
    expect(chat.firstElementChild?.classList.contains("tab-status-dot")).toBe(true);

    // Every other kind is untouched: it keeps its glyph and the dot stays in the
    // trailing slot, which is where the editor's unsaved mark already lived.
    expect(files?.querySelector(".tab-icon")).not.toBeNull();
    expect(files?.firstElementChild?.classList.contains("tab-icon")).toBe(true);
    expect(files?.querySelector(".tab-status-dot")?.hasAttribute("data-status")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 3. Reduced motion.
// ---------------------------------------------------------------------------

describe("prefers-reduced-motion stops the dot's animation", () => {
  const tabs = loadCSS("12-tabs.css");
  const dot = '.tab-status-dot[data-status="working"]';

  it("animates only in the normal-motion rule", () => {
    const normal = ruleContaining(tabs, dot, "top");
    expect(/animation:/.test(normal.body)).toBe(false);
    // The motion lives on the two pseudos, which is what lets the disc stay a
    // static layer the GPU merely blends over.
    expect(/animation: vk-dot-glow/.test(ruleContaining(tabs, `${dot}::before`, "top").body)).toBe(
      true,
    );
    expect(/animation: vk-dot-wave/.test(ruleContaining(tabs, `${dot}::after`, "top").body)).toBe(
      true,
    );
  });

  it("removes both animated pseudos entirely under reduced motion", () => {
    // `content: none` rather than `animation: none`. The global rule in
    // 40-a11y.css runs each animation to completion instead of suppressing it,
    // and with no fill-mode the wave's ::after then reverts to `opacity: 1` with
    // no transform — a permanent opaque band around the disc. Deleting the
    // pseudos is the only thing that prevents it.
    const reduced = ruleContaining(tabs, `${dot}::before`, "prefers-reduced-motion");
    expect(reduced.selector).toContain(`${dot}::after`);
    expect(/content:\s*none/.test(reduced.body)).toBe(true);
  });

  it("replaces the lost motion with a shape, not just a colour", () => {
    // Motion is what separates `working` from every settled state, so with it
    // gone the disc has to differ by SHAPE or the state is carried by hue alone
    // (WCAG 1.4.1). It becomes a donut.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    expect(/radial-gradient\(closest-side, transparent/.test(reduced.body)).toBe(true);
    // The hole is a TRANSPARENT gradient stop, not a background-coloured inset
    // shadow: the same dot sits on five different row fills (resting, hovered,
    // selected, selected-hover, selected-press) in two themes, and an opaque
    // hole would be wrong on four of them.
    expect(/inset .*var\(--c-bg/.test(reduced.body)).toBe(false);
  });

  it("does not borrow the wants-you ring to make up for the motion", () => {
    // It used to. The ring means "this chat wants you", `working` wants nothing,
    // and with the waiting/input pair un-merged three of six states would have
    // carried one. Dropping it also leaves the donut one channel from `idle`
    // alone rather than from both ringed states.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    expect(/box-shadow/.test(reduced.body)).toBe(false);
  });

  it("keeps the donut's band tellable apart from a hollow dot's hairline", () => {
    // `idle` and `waiting` are hollow — a 1.5px edge — so at 9px the donut is the
    // OTHER ring of ink in the vocabulary and the two have to differ by weight,
    // not just by hue. A 45% hole leaves a 2.5px band around a 4px hole; the 55%
    // it started at left 2.0px, close enough to a hairline to read as one.
    // Widening past 45% closes the hole until the donut reads as a solid disc,
    // which is the collision on the other side, so the stop is bounded twice.
    const reduced = ruleContaining(tabs, dot, "prefers-reduced-motion");
    const stop = /transparent 0 (\d+)%, var\(--dot-color\) \1% 100%/.exec(reduced.body);
    expect(stop, "the donut must have one hole radius, used by both stops").not.toBeNull();
    const hole = Number(stop?.[1]);
    expect(hole).toBeGreaterThanOrEqual(35);
    expect(hole).toBeLessThanOrEqual(50);
  });

  it("keeps the global reduced-motion sweep that backs it up", () => {
    // The component rule above is the fix; this is the belt. If the global sweep
    // ever narrows to a selector list, an animation added to the dot later would
    // silently keep running.
    const a11y = loadCSS("40-a11y.css");
    const global = ruleContaining(a11y, "*", "prefers-reduced-motion");
    expect(global.selector).toContain("*::before");
    expect(/animation-iteration-count:\s*1/.test(global.body)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// 4. The selected row's ink.
// ---------------------------------------------------------------------------

describe("every dot state has an on-selected ink", () => {
  it("re-points --dot-color for all six states on the active row", () => {
    // The dot declares its own colour, so `--c-selected-fg` never reaches it and
    // the raw hues drop to 2.793:1 on the light selected fill. A state added to
    // 12-tabs.css without a line here would look right on every unselected row and
    // fail on the one row that is always selected — which is the failure mode this
    // whole file exists to catch.
    const sel = loadCSS("70-selection.css");
    const inks: [string, string][] = [
      ["idle", "--c-selected-muted-fg"],
      ["working", "--c-selected-dot-working-fg"],
      // The wants-you pair shares one ink here for the same reason it shares one
      // seed: the states separate on fill, not on hue.
      ["waiting", "--c-selected-dot-input-fg"],
      ["input", "--c-selected-dot-input-fg"],
      ["failed", "--c-selected-dot-failed-fg"],
      ["done", "--c-selected-dot-done-fg"],
      ["dirty", "--c-selected-accent-fg"],
    ];
    for (const [state, token] of inks) {
      const rule = ruleContaining(sel, `.tab.active .tab-status-dot[data-status="${state}"]`);
      expect(
        rule.body.includes(`--dot-color: var(${token})`),
        `${state} must take ${token} on a selected row; got: ${rule.body.trim()}`,
      ).toBe(true);
    }
  });

  it("keeps every state resolving through the one custom property", () => {
    // The disc, both hollow borders, the hard ring's 30% mix and the
    // reduced-motion donut all read --dot-color, which is what makes one line per
    // state in 70-selection.css sufficient. A state that hardcoded `background:
    // var(--c-red)` would take its selected ink in no layer at all.
    const tabs = loadCSS("12-tabs.css");
    for (const state of ["working", "input", "failed", "done", "dirty"]) {
      const rule = ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top");
      expect(
        /background: var\(--dot-color\)/.test(rule.body),
        `${state} must paint from --dot-color; got: ${rule.body.trim()}`,
      ).toBe(true);
    }
    // The two hollow states paint their ink into a border instead of a
    // background, so they read the same property from the other side.
    for (const state of ["idle", "waiting"]) {
      expect(
        /border: 1\.5px solid var\(--dot-color\)/.test(
          ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top").body,
        ),
        `${state} must draw its hollow edge from --dot-color`,
      ).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// 4b. The ink vocabulary, shared with web-terminal-kiro.
//
// The two apps sit in the same window (vibekit hosts a web-terminal panel), so a
// state that means one thing must not carry two colours between them. It did
// twice over. First `working` was vibekit's violet accent while the terminal's was
// blue. Then the fix aligned these tokens to @cplieger/web-terminal-ui's LIBRARY
// DEFAULTS — which web-terminal-kiro overrides on every member — so the two apps
// agreed with the package and still disagreed with each other. The values are now
// web-terminal-kiro's own, hue-exact in both themes with L and C sized per theme.
// ---------------------------------------------------------------------------

describe("the dot inks are web-terminal-kiro's status vocabulary", () => {
  const tabs = loadCSS("12-tabs.css");

  it("gives each state the token carrying the source system's ink", () => {
    // Tokens, not values: the source has one theme and vibekit has two, so the
    // token is where the per-theme sizing lives. See the --c-dot-* block in
    // 01-tokens.css for each value's provenance.
    const inks: [string, string][] = [
      ["working", "--c-dot-working"], // --status-working #c6a0ff
      ["waiting", "--c-dot-input"],
      ["input", "--c-dot-input"], // --status-input  oklch(78% 0.15 95deg)
      ["failed", "--c-dot-failed"], // --status-failed #dc2626, lightened to be visible
      ["done", "--c-dot-done"], // --status-done   oklch(78% 0.15 150deg)
    ];
    for (const [state, token] of inks) {
      const rule = ruleContaining(tabs, `.tab-status-dot[data-status="${state}"]`, "top");
      expect(
        rule.body.includes(`--dot-color: var(${token})`),
        `${state} must take ${token}; got: ${rule.body.trim()}`,
      ).toBe(true);
    }
  });

  it("keeps the editor's mark on the accent token, and working off it", () => {
    // `dirty` and `working` were BOTH literally --c-accent, which is how an editor
    // tab with unsaved changes and a chat mid-turn came to look identical. They
    // read different tokens now. The VALUES are close again — the source's working
    // violet is nearly this app's accent, deliberately — and what separates them is
    // MOTION, plus the structural fact that a tab is never both a chat and a file.
    const working = ruleContaining(tabs, '.tab-status-dot[data-status="working"]', "top");
    const dirty = ruleContaining(tabs, '.tab-status-dot[data-status="dirty"]', "top");
    expect(working.body).not.toContain("--c-accent");
    expect(dirty.body).toContain("--dot-color: var(--c-accent)");
  });

  it("un-merges waiting from input on fill, which is the only channel it has", () => {
    // The pair shares ONE ink on purpose: both mean "action required", which is the
    // single thing the source's --status-input says. So fill is what carries them
    // apart, and it is load-bearing rather than decorative — the alias that used to
    // exempt this pair from the non-colour-channel check is gone, so hue alone
    // would be a WCAG 1.4.1 failure. `input` is solid (a turn frozen mid-flight),
    // `waiting` is hollow (its turn is over).
    const waiting = ruleContaining(tabs, '.tab-status-dot[data-status="waiting"]', "top");
    const input = ruleContaining(tabs, '.tab-status-dot[data-status="input"]', "top");
    expect(waiting.body).toContain("background: transparent");
    expect(input.body).toContain("background: var(--dot-color)");
    // The ring is the wants-you marker and BOTH keep it: that is the half of the
    // treatment they still share, and it is why the phrases still have to differ.
    for (const rule of [waiting, input]) {
      expect(
        /box-shadow: 0 0 0 2px color-mix\(in srgb, var\(--dot-color\) 30%/.test(rule.body),
      ).toBe(true);
    }
  });

  it("gives the ring to nothing else, so it keeps meaning one thing", () => {
    // Every rule in the block, at every scope: exactly two carry a ring. The
    // reduced-motion `working` donut used to be a third, which put the wants-you
    // marker on the one state that wants nothing from the reader.
    // The prelude is capture group 1 and the pattern makes it mandatory, so the
    // `= ""` default never applies; it is there so a later edit to the pattern
    // shows up as an empty selector in the assertion below rather than a throw.
    const ringed = [...tabs.matchAll(/([^{}]*)\{([^{}]*box-shadow: 0 0 0 2px[^{}]*)\}/g)].map(
      ([, prelude = ""]) => prelude.trim().split("\n").pop()?.trim(),
    );
    expect(ringed.sort()).toEqual([
      '.tab-status-dot[data-status="input"]',
      '.tab-status-dot[data-status="waiting"]',
    ]);
  });

  it("keeps the tool's alias list empty, which is where the un-merge is proven", () => {
    // scripts/css-contrast.py fails when two chat states differ by hue alone, and
    // DOT_ALIASES is the list of pairs it has been told not to look at. The
    // wants-you pair was in it, and it now shares one INK, so an entry re-appearing
    // here would exempt from the check the exact pair whose only separator is fill —
    // letting a merge come back with the check still reporting PASS, which is the
    // one failure mode a mechanical gate has that a human reviewer does not.
    expect(cssContrastScript).toContain("DOT_ALIASES: list[tuple[str, str]] = []");
  });
});

// ---------------------------------------------------------------------------
// 5. The finished-turn latch.
//
// `agent_status === "completed"` is the higher-fidelity signal for "this turn is
// over" and it is NOT a guaranteed one: it only arrives when the model calls
// `update_session_information`. So a turn that ended without one fell to `idle`,
// and "this chat finished" — the headline promise of the whole strip — held only
// for the turns where the agent happened to say so. `turn_ended` always arrives,
// so a client-side latch is what makes the promise total.
// ---------------------------------------------------------------------------

describe("the finished-turn latch reports done without the agent's tool call", () => {
  beforeEach(() => {
    setSessions([]);
  });

  it("derives done from the latch alone", () => {
    expect(tabStatusFor(session({ turn_done: true }))).toBe("done");
  });

  it("agrees with the agent when the agent does declare completed", () => {
    // Both producers, one state. There is nothing to reconcile: `completed` is
    // preferred in the rule's order, and it maps to the same dot either way.
    expect(tabStatusFor(session({ agent_status: "completed", turn_done: true }))).toBe("done");
  });

  it("does not let the latch outrank a chat that wants something", () => {
    // A finished turn that left a question behind is a chat that wants you, not a
    // chat that is done. Both of these can now genuinely coexist with the latch,
    // which is why the order is asserted rather than assumed.
    expect(tabStatusFor(session({ agent_status: "waiting_on_user", turn_done: true }))).toBe(
      "waiting",
    );
    expect(tabStatusFor(session({ turn_done: true }), true)).toBe("input");
    expect(tabStatusFor(session({ turn_done: true, turn_failed: true }))).toBe("failed");
    expect(tabStatusFor(session({ turn_done: true, thinking: true }))).toBe("working");
  });

  it("is cleared by the next turn, in the same place every other verdict is", () => {
    setSessions([session({ id: "c1", turn_done: true })]);
    setThinking("c1", true);
    expect(get("c1")?.turn_done).toBeUndefined();
    // Not a one-off: the same write clears the failure latch and the agent's
    // declared status, because all three are latched-until-the-next-turn.
    setSessions([
      session({ id: "c2", turn_failed: true, agent_status: "completed", turn_done: true }),
    ]);
    setThinking("c2", true);
    expect(tabStatusFor(get("c2"))).toBe("working");
  });

  it("is NOT cleared by seeing it, so the dot can turn green while you watch", () => {
    // The one caller of clearTurnDone is the transport-gap reconciler. Opening the
    // chat used to clear it, back when the mark meant "finished while you were
    // away" — and the cost of that pair of rules (skip the latch for a watched
    // chat, clear it on activation) was the dot falling back to hollow `idle` at
    // the exact moment a turn completed in front of the reader. web-terminal-kiro
    // latches its own `done` in the engine, focus-blind and cleared only by the
    // next turn's progress state, and this is now the same rule.
    expect(chatSrc).not.toContain("clearTurnDone");
    // The function still exists and still works; nothing but a dropped stream is
    // entitled to call it.
    setSessions([session({ id: "c1", turn_done: true })]);
    clearTurnDone("c1");
    expect(tabStatusFor(get("c1"))).toBe("idle");
  });

  it("does not churn the session signal on a replayed turn_ended", () => {
    setSessions([session({ id: "c1" })]);
    setTurnDone("c1");
    const first = get("c1");
    setTurnDone("c1");
    expect(get("c1")).toBe(first);
  });
});

// ---------------------------------------------------------------------------
// 6. The two client-only latches survive a list refetch.
//
// `loadList` rebuilds each Session from a chat HEADER, and the server sends none
// of the client-only projections — so every field it does not explicitly carry
// over is silently reset. It runs on EVERY `connected`, reconnects included, so
// this was not an edge case: an ordinary network recovery repainted a failed tab
// as idle and dropped the agent's declared status with it.
// ---------------------------------------------------------------------------

describe("a reconnect does not erase what only the client knows", () => {
  const header = (id: string): ChatHeader =>
    ({
      id,
      name: id,
      message_count: 0,
      usage: { context_pct: 0, context_size: 0, credits: 0, turns: 0, last_turn_ms: 0 },
    }) as unknown as ChatHeader;

  beforeEach(() => {
    setSessions([]);
    mockApiGetTyped.mockReset();
  });

  it("keeps a failure latched across the refetch a reconnect triggers", async () => {
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", turn_failed: true })]);
    mockApiGetTyped.mockResolvedValue({ chats: [header("c1")] });

    expect(await loadList()).toBe(true);
    expect(tabStatusFor(get("c1"))).toBe("failed");
  });

  it("keeps the finished latch and the agent's declared status too", async () => {
    const { loadList } = await import("./store-load.js");
    setSessions([
      session({ id: "c1", turn_done: true }),
      session({ id: "c2", agent_status: "waiting_on_user", agent_status_text: "reading main.go" }),
    ]);
    mockApiGetTyped.mockResolvedValue({ chats: [header("c1"), header("c2")] });

    await loadList();
    expect(tabStatusFor(get("c1"))).toBe("done");
    expect(tabStatusFor(get("c2"))).toBe("waiting");
    expect(get("c2")?.agent_status_text).toBe("reading main.go");
  });

  it("carries nothing over for a chat it has not seen before", async () => {
    // The preservation is per-id off the EXISTING row, so a chat arriving for the
    // first time gets the floor rather than a neighbour's verdict.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", turn_failed: true })]);
    mockApiGetTyped.mockResolvedValue({ chats: [header("c1"), header("c2")] });

    await loadList();
    expect(tabStatusFor(get("c2"))).toBe("idle");
  });
});

// ---------------------------------------------------------------------------
// 7. An abandoned ask stops claiming the chat needs a decision.
//
// `input` outranks every other state, so a queue entry left behind after its
// request died marked the chat as blocked indefinitely. The queue lives in
// decision-dock.ts and had no production caller for its own drop function at all.
// ---------------------------------------------------------------------------

describe("an abandoned ask does not keep a chat in input", () => {
  const ask = (chatID: string, requestID: number, runID: string) => ({
    kind: "permission" as const,
    chatID,
    runID,
    requestID,
    payload: { request_id: requestID, title: "run a command", options: [] } as unknown as
      PermissionNeededPayload | PermissionNeededPayload,
    submit: vi.fn(),
  });

  beforeEach(async () => {
    const dock = await import("./decision-dock.js");
    dock._resetForTest();
    setSessions([session({ id: "c1" })]);
  });

  it("drops a turn's ask when the turn ends", async () => {
    const { pushDecision, hasPendingDecision, dropTurnDecisions } =
      await import("./decision-dock.js");
    pushDecision(ask("c1", 1, ""));
    expect(tabStatusFor(get("c1"), hasPendingDecision("c1"))).toBe("input");

    // What handlers/turn.ts now runs on turn_ended. Every ask BLOCKS its turn, so
    // a turn that has ended is not waiting on one: it was answered (already
    // spliced) or abandoned when the turn was cancelled, and cmdCancel clears the
    // server's own pending set — the card left here could never be answered.
    dropTurnDecisions("c1");
    expect(tabStatusFor(get("c1"), hasPendingDecision("c1"))).toBe("idle");
  });

  it("leaves a workflow run's ask alone when the launching turn ends", async () => {
    const { pushDecision, hasPendingDecision, dropTurnDecisions, dropDecisions } =
      await import("./decision-dock.js");
    // An agent-launched run is parented on the calling chat's session and its asks
    // are keyed under that chat's id, but it OUTLIVES the launching turn (a goal
    // run ends its turn immediately and then runs). Dropping these would strand
    // the run waiting for an answer no surface offers — the exact failure the
    // dock's queue was built to end.
    pushDecision(ask("c1", 2, "run-7"));
    dropTurnDecisions("c1");
    expect(hasPendingDecision("c1")).toBe(true);

    // The chat going away is different: close_chat and delete_chat cancel the
    // chat's runs server-side, and there is no surface left to answer on.
    dropDecisions("c1");
    expect(hasPendingDecision("c1")).toBe(false);
  });

  it("keeps a turn's ask on one chat when another chat's turn ends", async () => {
    const { pushDecision, hasPendingDecision, dropTurnDecisions } =
      await import("./decision-dock.js");
    pushDecision(ask("c1", 3, ""));
    dropTurnDecisions("c2");
    expect(hasPendingDecision("c1")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// 8. A row that is CREATED knows what it should show.
//
// The dot used to live only in the DOM and `setTabStatus` wrote only to the live
// node, so every path that built a row without a following state change showed
// the seeded `idle` whatever the chat was doing. Two such paths, both ordinary:
// the boot restore populates sessions BEFORE opening their tabs (so the store
// effect has already run by the time the rows exist, and nothing makes it run
// again), and `promoteTab` discards and rebuilds a row on purpose.
// ---------------------------------------------------------------------------

describe("a row built later paints the state its chat is in", () => {
  beforeEach(async () => {
    await resetProjection();
  });

  function statusOf(id: string): string | null {
    return (
      document
        .querySelector(`[data-tab-id="${id}"] .tab-status-dot`)
        ?.getAttribute("data-status") ?? null
    );
  }

  it("paints the seed the factory derived, not the idle floor", async () => {
    // What the factory derives from the chat this client already holds, through the
    // registered `dot` hook. This is the boot restore: the collection is adopted
    // before any dot write, so the row has to be BUILT with the right state.
    seededDots.set("c1", "working");
    const id = await openSubject("chat", "c1");
    await paint();
    expect(statusOf(id)).toBe("working");
  });

  it("paints a status written before its row existed", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const id = await openSubject("chat", "c1", { activate: false });
    // renderDOM is rAF-deferred, so this lands while the row does not exist —
    // which is the ordering every state change that arrives during boot has.
    setTabStatus(id, "failed");
    await paint();
    expect(statusOf(id)).toBe("failed");
  });

  it("keeps the dot through a rebuild, which a pin change forces", async () => {
    // `promoteTab` is gone with the reparent it performed: `TabSubject.Parent` is
    // set at open and never reassigned, which is what makes a parent cycle
    // unrepresentable. The rebuild it exercised is still reachable — dropping the
    // node and making the store emit is what any re-render after a lost row does —
    // so the property survives with the mechanism that remains.
    const { openTab, setTabStatus, setTabPinned, tabIdFor } = await import("./tabs.js");
    const parent = await openSubject("chat", "parent");
    await openTab({ kind: "chat", ref: "c2", parent });
    const child = tabIdFor("chat", "c2");
    await paint();
    setTabStatus(child, "input");
    expect(statusOf(child)).toBe("input");

    document.querySelector(`[data-tab-id="${child}"]`)?.remove();
    await setTabPinned(parent, true);
    await paint();
    expect(statusOf(child)).toBe("input");
  });

  it("still floors a chat row at idle and leaves other kinds blank", async () => {
    const chat = await openSubject("chat", "c3");
    const editor = await openSubject("editor", "a.ts");
    await paint();
    expect(statusOf(chat)).toBe("idle");
    // An editor tab with nothing unsaved has no state to show, and `[data-status]`
    // is the CSS reveal condition, so the attribute must be ABSENT rather than "".
    expect(statusOf(editor)).toBeNull();
  });

  it("carries the editor's dirty mark through a rebuild, in both directions", async () => {
    const { setTabDirty, setTabPinned } = await import("./tabs.js");
    const id = await openSubject("editor", "b.ts");
    await paint();

    /** Rebuild the row — drop the node, then make the store emit — so createTabEl
     *  runs again with no write behind it. A pin is a `changed` upsert, which is
     *  the cheapest real emit available. */
    async function rebuild(pinned: boolean): Promise<void> {
      document.querySelector(`[data-tab-id="${id}"]`)?.remove();
      await setTabPinned(id, pinned);
      await paint();
    }

    setTabDirty(id, true);
    expect(statusOf(id)).toBe("dirty");
    // An unsaved file must still read as unsaved after its row is rebuilt: the
    // editor's mark rides the same element and the same one attribute as a chat's
    // activity, so it needs the same spec record behind it.
    await rebuild(true);
    expect(statusOf(id)).toBe("dirty");

    setTabDirty(id, false);
    // And a SAVED file must not come back dirty, which is what recording "" as an
    // ABSENT field rather than an empty string buys.
    await rebuild(false);
    expect(statusOf(id)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 9. The dot is live state, so it must never be persisted.
//
// TabSpec feeds the persistence subscriber, and a dot restored from a previous
// process would be a claim about a turn that ended before the page loaded.
// ---------------------------------------------------------------------------

// The dot is LIVE state, and the projection is what makes that structural rather
// than a rule someone has to remember: a `TabSubject` has no dot field at all, so
// there is nothing for a dot write to travel on. `dotVersion` is the other half —
// a dot write does not `emit()`, so it queues no re-render.
describe("the dot is local and costs the projection nothing", () => {
  beforeEach(async () => {
    await resetProjection();
  });

  it("puts no dot on the wire, whatever the row is showing", async () => {
    const { setTabStatus, setTabPinned } = await import("./tabs.js");
    const { tabServer } = await import("./__test-helpers__/tabs-server.js");
    const id = await openSubject("chat", "c1");
    await setTabPinned(id, true);
    setTabStatus(id, "failed");
    await paint();

    // Every command this device sent, in full. A dot cannot reach the collection
    // because no payload has a field for it — which is stronger than a rule about
    // what a writer must not send.
    const payloads = JSON.stringify(tabServer.sent());
    expect(tabServer.sent().length).toBeGreaterThan(0);
    expect(payloads).not.toContain("failed");
    expect(payloads).not.toContain("dotStatus");
    // And the collection itself holds no trace of it.
    expect(JSON.stringify(tabServer.subjects())).not.toContain("failed");
  });

  it("sends nothing at all for a dot change", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { tabServer } = await import("./__test-helpers__/tabs-server.js");
    const id = await openSubject("chat", "c1");
    await paint();
    const before = tabServer.sent().length;
    setTabStatus(id, "working");
    setTabStatus(id, "done");
    await paint();
    // A status write is a direct DOM paint plus a row field, and it deliberately
    // does not emit: emitting would put a full re-render on every streaming state
    // change, and a mutation would put a round trip there.
    expect(tabServer.sent()).toHaveLength(before);
  });
});

// ---------------------------------------------------------------------------
// 10. Two source facts about the leading slot and the motion vocabulary.
// ---------------------------------------------------------------------------

describe("the leading dot slot is one width for every state", () => {
  const tabs = loadCSS("12-tabs.css");

  it("re-derives the diamond's margin from its own 8px width", () => {
    // The generic slot rule is computed for the 9px disc, so the 8px diamond
    // reserved 13px where every other state reserves 14px — and the chat's name
    // moved 1px sideways at the exact moment its status flipped to or from failed,
    // which is the one moment the reader is watching that row.
    const generic = ruleContaining(tabs, ".tab-status-dot:first-child", "top");
    expect(generic.body).toContain("calc((0.875rem - 9px) / 2)");

    const diamond = ruleContaining(
      tabs,
      '.tab-status-dot[data-status="failed"]:first-child',
      "top",
    );
    expect(diamond.body).toContain("calc((0.875rem - 8px) / 2)");
  });

  it("keeps the only geometry deviation at 8px, so the override stays paired", () => {
    // If `failed` ever returns to 9px this override becomes wrong rather than
    // merely redundant, so the two are asserted together.
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    expect(/width:\s*8px/.test(failed.body)).toBe(true);
  });
});

describe("the dot's motion uses the app's own easing vocabulary", () => {
  const tabs = loadCSS("12-tabs.css");
  const dot = '.tab-status-dot[data-status="working"]';

  it("beats on the standard curve rather than a bare ease-in-out", () => {
    const glow = ruleContaining(tabs, `${dot}::before`, "top");
    expect(glow.body).toContain("animation: vk-dot-glow 1.2s var(--ease-standard) infinite");
    expect(glow.body).not.toContain("ease-in-out");
  });

  it("leaves the wave linear, which is not an oversight", () => {
    // The glow is UI emphasis and takes the token; the wave's travel is continuous
    // motion, and an eased one made it invisible (the source records the same
    // finding). Asserted so a later sweep does not "fix" it into the token.
    const wave = ruleContaining(tabs, `${dot}::after`, "top");
    expect(wave.body).toContain("animation: vk-dot-wave 1.2s linear infinite");
  });

  it("resolves the token it names", () => {
    expect(loadCSS("01-tokens.css")).toContain("--ease-standard:");
  });
});
