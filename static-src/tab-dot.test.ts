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

import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";
import cssContrastScript from "../scripts/css-contrast.py?raw";
import chatSrc from "./chat.ts?raw";
import { ruleContaining, loadCSS } from "./__test-helpers__/css-rules.js";
import {
  tabStatusFor,
  setSessions,
  setThinking,
  setTurnDone,
  clearTurnDone,
  outcomeLatch,
  relatchTurnVerdict,
  get,
} from "./store.js";
import type {
  ChatHeader,
  PermissionNeededPayload,
  RunInputNeededPayload,
  Session,
  TabKind,
} from "./types.js";
import type { TurnOutcome } from "./wire/types.gen.js";
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
vi.mock("./run-store.js", () => ({
  // The tab factory's name read. Inert here; a Browser-Mode mock is linked as
  // real ESM, so a name any module in the graph reaches has to exist on it.
  runLabelOf: vi.fn(() => ""),
}));
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
      // Present-but-inert so real-ESM linking succeeds: store-load's deep-link
      // confirmation reads it, and that module is in this graph. No case here
      // resolves an unknown chat id.
      apiGetTypedOrError: vi.fn(),
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
    // Present because the mock must carry every name anything in this test's
    // import graph reaches — Browser Mode links ESM for real, so a missing
    // export fails the whole file at link time rather than at the call.
    // `decision-dock.ts` (reached via the ask readers) imports it.
    forceReflow: vi.fn(() => 0),
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

  it("claims exactly what the failed latch's one producer supports", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const row = await openChat();
    setTabStatus(chatTabID, "failed");
    // REWRITTEN, and it used to pin the OPPOSITE. This case asserted
    // "last operation failed" and that the name did NOT contain "turn", on the
    // grounds that the latch was set for every `error` frame naming the chat,
    // `switch_failed` and `bridge_start_failed` among them. That breadth is gone:
    // the error handler stopped touching turn state when `endsTurn` was removed
    // (handlers/turn.ts), so `setTurnFailed` has one live producer, `turn_ended`
    // with outcome `failed` or `refused`, and its two other callers re-derive the
    // same turn verdict. The phrase is the only channel a screen-reader user has
    // here, so it must claim neither more NOR less than that.
    expect(nameFromContents(row)).toBe("Fix the parser, turn failed");
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
// 3. The phrase names the tab's own SUBJECT.
//
// The disc is `aria-hidden`, so the phrase is the only channel a screen-reader
// user has for the state — and THREE producers write these states about three
// different subjects: `tabStatusFor` about a turn, `runStatusFor` about a
// workflow run (run-dots.ts), `subagentStatusFor` about a delegate
// (subagent-dots.ts). So the two OUTCOME states have to name the subject of the
// row they are painted on, while the five that say nothing about a subject stay
// one wording for every kind. Widening a phrase back toward "last operation
// failed" to cover all three would spend the narrowness the chat case's own
// producer measurement bought, so the outcome phrases are pinned to a subject
// rather than to a state alone.
// ---------------------------------------------------------------------------

describe("the announced phrase names the subject of the tab it is on", () => {
  /** Every kind's ref except the composite one. A singleton's identity IS its
   *  kind, so its ref is empty. */
  const PLAIN_REF: Readonly<Record<Exclude<TabKind, "subagent">, string>> = {
    chat: "c1",
    editor: "/a.ts",
    run: "wf-1",
    settings: "",
    git: "",
    files: "",
    history: "",
    docs: "",
  };

  const NEUTRAL: readonly (readonly [TabDotStatus, string])[] = [
    ["idle", "idle"],
    ["working", "working"],
    ["waiting", "waiting for you"],
    ["input", "needs a decision"],
    ["dirty", "unsaved changes"],
  ];

  const EVERY_STATE: readonly TabDotStatus[] = [
    "idle",
    "working",
    "waiting",
    "input",
    "failed",
    "done",
    "dirty",
  ];

  beforeEach(async () => {
    await resetProjection();
  });

  /** Open a tab of `kind` and answer with its id and its rendered row. */
  async function openKind(kind: TabKind): Promise<{ id: string; row: HTMLElement }> {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    let id: string;
    if (kind === "subagent") {
      // A delegate's tab exists only as a SUB-TAB under the chat that dispatched
      // it, and the composite ref is spelled by the module that owns the format.
      const parent = await openSubject("chat", "c1");
      const ref = subagentRef("c1", "task-1");
      await openTab({ kind, ref, parent, owns: false });
      id = tabIdFor(kind, ref);
    } else {
      id = await openSubject(kind, PLAIN_REF[kind]);
    }
    await paint();
    const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
    if (row === null) {
      throw new Error(`the ${kind} row did not render`);
    }
    return { id, row };
  }

  function srOf(row: HTMLElement): HTMLElement {
    const sr = row.querySelector<HTMLElement>(".tab-status-sr");
    if (sr === null) {
      throw new Error("no announced-word element");
    }
    return sr;
  }

  function dotOf(row: HTMLElement): HTMLElement {
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    if (dot === null) {
      throw new Error("no dot element");
    }
    return dot;
  }

  /** The announced word without the `, ` that composes it onto the tab's name. */
  function phraseOn(row: HTMLElement): string {
    return (srOf(row).textContent ?? "").replace(/^, /u, "");
  }

  it("names the workflow run on a run tab", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { id, row } = await openKind("run");

    // The writer is `run-dots.ts`, and this row announced "turn failed" about a
    // workflow run for as long as that module has existed. The noun is the one
    // the rest of the app already puts in front of a reader (run-bar.ts's
    // fallback name, run-exec-source.ts's label, the run card's aria-label).
    setTabStatus(id, "failed");
    expect(phraseOn(row)).toBe("workflow run failed");
    setTabStatus(id, "done");
    expect(phraseOn(row)).toBe("workflow run finished");
  });

  it("names the subagent on a subagent sub-tab", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { id, row } = await openKind("subagent");

    // `subagent`, not `delegate`: the phrase is announced immediately after the
    // tab's own label, and the LABEL vocabulary is Subagent (roles.ts's fallback
    // name, the pipeline container's title, the route segment).
    setTabStatus(id, "failed");
    expect(phraseOn(row)).toBe("subagent failed");
    setTabStatus(id, "done");
    expect(phraseOn(row)).toBe("subagent finished");
  });

  it("still names the turn on a chat row", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { id, row } = await openKind("chat");

    // The regression guard for the kind that was already right: `tabStatusFor`'s
    // subject genuinely is a turn (its latch has one live producer, `turn_ended`
    // with a broken outcome), so kind-awareness must not move this wording.
    setTabStatus(id, "failed");
    expect(phraseOn(row)).toBe("turn failed");
    setTabStatus(id, "done");
    expect(phraseOn(row)).toBe("turn finished");
  });

  it("gives the five non-outcome states one wording on every kind", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const chat = await openKind("chat");
    const run = await openKind("run");
    const sub = await openKind("subagent");

    // These five say nothing about WHAT is idle or waiting, so a per-kind
    // wording would be five duplicated tables for no information — and a kind
    // that drifted in one of them would announce a state the CSS paints
    // identically on every other row.
    for (const [state, phrase] of NEUTRAL) {
      for (const t of [chat, run, sub]) {
        setTabStatus(t.id, state);
        expect(phraseOn(t.row)).toBe(phrase);
      }
    }
  });

  it("gives every kind a subject-bearing outcome phrase", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { TAB_ICONS } = await import("./tab-view.js");
    // Enumerated from the exhaustive table rather than listed here, so a tenth
    // kind added to the Go const block is covered the day it is generated.
    const kinds = Object.keys(TAB_ICONS) as TabKind[];
    const finishedBy = new Map<TabKind, string>();

    for (const kind of kinds) {
      const { id, row } = await openKind(kind);
      for (const [state, verb] of [
        ["failed", "failed"],
        ["done", "finished"],
      ] as const) {
        setTabStatus(id, state);
        const phrase = phraseOn(row);
        // A subject, then the verb. `endsWith` is what rules out the bare verb,
        // which is the subject-less phrase this change exists to remove; the
        // `undefined` read is the runtime half of the lookup's totality, because
        // a table missing a kind type-checks under a cast and only shows up here.
        expect(phrase.endsWith(` ${verb}`)).toBe(true);
        expect(phrase).not.toContain("undefined");
      }
      // The `done` write above is the last one, so this is each kind's FINISHED
      // phrase.
      finishedBy.set(kind, phraseOn(row));
    }

    // The three kinds with a live producer say three different things, which is
    // the whole property one state-keyed table could not have. Both outcomes are
    // composed from one `DOT_SUBJECT` entry, so three distinct finished phrases
    // is three distinct failed ones.
    const producers = ["chat", "run", "subagent"] as const;
    expect(new Set(producers.map((k) => finishedBy.get(k))).size).toBe(3);
  });

  it("paints the tooltip and the announced word from one string", async () => {
    const { setTabStatus } = await import("./tabs.js");
    const { TAB_ICONS } = await import("./tab-view.js");

    // The invariant kind-awareness had to survive: a sighted reader's tooltip and
    // a screen-reader user's word come from ONE resolver call, so they cannot
    // drift per kind. Asserted over every kind and every state, because a second
    // lookup is exactly the shape a per-kind phrase invites.
    for (const kind of Object.keys(TAB_ICONS) as TabKind[]) {
      const { id, row } = await openKind(kind);
      for (const state of EVERY_STATE) {
        setTabStatus(id, state);
        const announced = srOf(row).textContent ?? "";
        expect(announced.startsWith(", ")).toBe(true);
        expect(dotOf(row).title).toBe(announced.slice(2));
      }
    }
  });
});

// ---------------------------------------------------------------------------
// 4. Reduced motion.
// ---------------------------------------------------------------------------

describe("prefers-reduced-motion stops the dot's animation", () => {
  const tabs = loadCSS("12-tabs.css");
  const dot = '.tab-status-dot[data-status="working"]';

  it("declares no animation of its own, on the disc or on its overlay", () => {
    // The beat is an inherited value read off the document clock (03-base.css),
    // not an animation created on this element. That is the synchronisation: an
    // animation would be created when `data-status` becomes `working`, so N
    // working chats would hold N arbitrary phases.
    expect(/animation:/.test(ruleContaining(tabs, dot, "top").body)).toBe(false);
    const glow = ruleContaining(tabs, `${dot}::before`, "top");
    expect(/animation:/.test(glow.body)).toBe(false);
    expect(glow.body).toContain("var(--vk-beat)");
  });

  it("removes the beat overlay entirely under reduced motion", () => {
    // `content: none` rather than a reset of its opacity. Stopping the clock
    // leaves `--vk-beat` at its registered initial 0, so the overlay would be
    // invisible — but that is a property of the initial value, and a future
    // non-zero resting amplitude would paint a permanent bright ring over the
    // donut. Deleting the pseudo makes it independent of the clock's rest state.
    const reduced = ruleContaining(tabs, `${dot}::before`, "prefers-reduced-motion");
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
// 5. Selection does not change status ink.
// ---------------------------------------------------------------------------

describe("the active row keeps the same dot color", () => {
  it("never re-points --dot-color from an active-tab selector", () => {
    // Selection belongs to the row. A status belongs to the chat, so selecting
    // the row must not turn normal green into a darker green (user ruling,
    // 2026-08-31). Contrast adjustments belong to the row fill, not the state
    // indicator's identity.
    const sel = loadCSS("70-selection.css");
    expect(sel).not.toMatch(/\.tab\.active\s+\.tab-status-dot/u);
  });

  it("keeps every state resolving through the one custom property", () => {
    // The disc, both hollow borders, the hard ring's 30% mix and the
    // reduced-motion donut all read --dot-color. The selected row inherits the
    // exact same value because it has no override.
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
// 6. The finished-turn latch.
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
// 5b. EVERY outcome the wire can send reaches the dot, and none reaches it as
// "nothing is happening here".
//
// This is the section symptom 2 was reported against. The latch's mapping was
// hand-written over the outcome values and `interrupted` fell into its default
// arm, so a turn stopped by a network error — which the transcript divider, the
// collapsed face, the footer glyph and the fold rule all treat as a failure —
// latched nothing, `tabStatusFor` fell through every rung to `idle`, and
// 12-tabs.css painted `idle` as a TRANSPARENT disc with a 1.5px ring. The user
// saw a chat with a clear inline error message and an empty circle beside it.
//
// So the table below is the taxonomy, not a sample: every outcome, the latch it
// sets, the dot state that follows, and — for the two that matter — the SHAPE the
// stylesheet paints, which is the observable that was actually wrong.
// ---------------------------------------------------------------------------

describe("every turn outcome reaches the tab dot", () => {
  const tabs = loadCSS("12-tabs.css");

  /** outcome -> the dot state a chat shows once that turn has ended.
   *
   *  Derived from `severityOf` in production; spelled out here on purpose, because
   *  a test that re-derived it through the same function it is checking would pass
   *  for any mapping at all. */
  const cases: [TurnOutcome, TabDotStatus][] = [
    // BROKEN. All three are failures and all three must say so.
    ["failed", "failed"],
    ["refused", "failed"],
    ["interrupted", "failed"],
    // CLEAN.
    ["completed", "done"],
    // STOPPED. Neither is a failure — a cancel is what the user asked for, and an
    // unmeasured stop reason says nothing about whether the work succeeded — but
    // neither may be `idle` either, because the hollow ring means the chat has NOT
    // INITIATED (user ruling, 2026-09-04) and both of these ran a turn. `done` is
    // the transport's "a turn finished here", which is what both of them are.
    ["cancelled", "done"],
    ["unknown", "done"],
  ];

  // The whole point of the table above, stated as its own assertion so it cannot
  // be weakened one row at a time: no ENDED turn paints the hollow ring.
  it("never paints the hollow ring for a turn that ended, whatever became of it", () => {
    for (const [outcome, want] of cases) {
      expect(want, `${outcome} must not fall to idle`).not.toBe("idle");
    }
  });

  beforeEach(() => {
    setSessions([]);
  });

  for (const [outcome, want] of cases) {
    it(`shows ${want} for a turn that ended ${outcome}`, () => {
      setSessions([session({ id: "c1" })]);
      // Through `relatchTurnVerdict`, which is the door a reload and a transport gap
      // both come through: the latches are client memory, so the persisted outcome is
      // all there is to re-derive them from.
      setSessions([
        session({
          id: "c1",
          messages: [{ id: "m1", role: "assistant", ts: 1, turn_outcome: outcome } as never],
        }),
      ]);
      relatchTurnVerdict("c1");
      expect(tabStatusFor(get("c1"))).toBe(want);
    });
  }

  it("grades every outcome the same way the dot above painted it", () => {
    // The table drove `relatchTurnVerdict`; this pins the shared table underneath
    // it, so a change to `outcomeLatch` that the reload path happens to survive
    // still fails here. That the LIVE turn_ended handler agrees with both is a
    // separate assertion, in handlers/turn.test.ts — it needs that module's own
    // mock set, and it is the pairing that used to disagree.
    for (const [outcome, want] of cases) {
      expect(outcomeLatch(outcome), `outcomeLatch(${outcome})`).toBe(
        want === "failed" ? "failed" : want === "done" ? "done" : "",
      );
    }
  });

  it("latches an interrupted turn as a failure, which is the reported defect", () => {
    // Kept as its own case rather than left to the table: this single mapping is
    // the whole of symptom 2, and a table row is easy to edit without noticing.
    expect(outcomeLatch("interrupted")).toBe("failed");
  });

  it("latches a discarded and an unreadable turn as DONE, which two later fixes rest on", () => {
    // Named for the same reason as the row above, because two changes now depend on
    // these exact two mappings and a table row is easy to edit without noticing.
    //
    // `cancelled` is what a model switch that threw a live turn away now concludes:
    // it must not paint red for a switch the reader asked for, and must not paint the
    // hollow ring, which means the chat has not initiated. `unknown` is what a turn
    // NOTHING closed now reads as, so it reaches ordinary turns rather than only
    // displaced fragments — a red dot there would invent a failure the wire never
    // reported.
    expect(outcomeLatch("cancelled")).toBe("done");
    expect(outcomeLatch("unknown")).toBe("done");
  });

  it("paints a failure as the red lozenge and never as the idle ring", () => {
    // The observable the user described. `failed` is a filled, rotated square with
    // a small radius; `idle` is a transparent disc with a hairline ring. A source
    // fact rather than a computed one, for the reason this file's header gives: the
    // test page links no app stylesheet, so there is no cascade to measure.
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    expect(failed.body).toMatch(/transform:\s*rotate\(45deg\)/u);
    expect(failed.body).toMatch(/background:\s*var\(--dot-color\)/u);
    expect(failed.body).not.toMatch(/background:\s*transparent/u);

    const idle = ruleContaining(tabs, '.tab-status-dot[data-status="idle"]', "top");
    expect(idle.body).toMatch(/background:\s*transparent/u);
    expect(idle.body).toMatch(/border:/u);
    // The two must not be one shape, or the fix above would be unobservable.
    expect(failed.body.trim()).not.toBe(idle.body.trim());
  });
});

// ---------------------------------------------------------------------------
// 7. The two client-only latches survive a list refetch.
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
    // first time gets the floor rather than a neighbour's verdict — and with the
    // header silent on the outcome, the floor is still the right answer.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", turn_failed: true })]);
    mockApiGetTyped.mockResolvedValue({ chats: [header("c1"), header("c2")] });

    await loadList();
    expect(tabStatusFor(get("c2"))).toBe("idle");
  });

  // -------------------------------------------------------------------------
  // The other half, and the one the user reported: what the client does NOT
  // know, the server does. `last_turn_outcome` rides the header, so a chat this
  // client has never seen live gets a real verdict instead of the hollow ring.
  //
  // Every case below goes through the REAL store and the REAL loadList with no
  // pre-existing session, which is what a full page reload, a brand-new browser
  // session and a reconnect after hours all look like from here.
  // -------------------------------------------------------------------------

  const outcomeHeader = (id: string, outcome: string): ChatHeader =>
    ({ ...header(id), last_turn_outcome: outcome }) as unknown as ChatHeader;

  it("paints DONE for a finished turn on a chat it has never seen live", async () => {
    // The reported bug, asserted at the surface the user sees: "if a turn is done
    // (green dot in tab) after i close the window and come back, when i come back
    // it will be an empty circle". The empty circle is `idle`'s hollow ring.
    const { loadList } = await import("./store-load.js");
    setSessions([]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c-done", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c-done"))).toBe("done");
  });

  it("paints FAILED for a turn that failed or was refused", async () => {
    const { loadList } = await import("./store-load.js");
    setSessions([]);
    mockApiGetTyped.mockResolvedValue({
      chats: [outcomeHeader("c-fail", "failed"), outcomeHeader("c-refuse", "refused")],
    });

    await loadList();
    expect(tabStatusFor(get("c-fail"))).toBe("failed");
    expect(tabStatusFor(get("c-refuse"))).toBe("failed");
  });

  it("stays IDLE only for the LEGACY record, never for a turn that was stopped", async () => {
    // The two halves of the ruling, side by side on one fetch. A cancelled turn
    // RAN, so the hollow ring — which means the chat has not initiated — would be
    // wrong for it. A record written before the outcome existed carries nothing to
    // read, and that is the case the ruling exempts: no state to pull.
    const { loadList } = await import("./store-load.js");
    setSessions([]);
    mockApiGetTyped.mockResolvedValue({
      chats: [outcomeHeader("c-cancel", "cancelled"), header("c-legacy")],
    });

    await loadList();
    expect(tabStatusFor(get("c-cancel"))).toBe("done");
    expect(tabStatusFor(get("c-legacy"))).toBe("idle");
  });

  it("keeps a local failure when the header reports the turn before it completed", async () => {
    // The carry-over still outranks the seed: a latch set by a live `turn_ended`
    // on this page is newer than anything a header read can carry.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", turn_failed: true })]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c1", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c1"))).toBe("failed");
  });

  it("keeps a mid-turn reload WORKING rather than seeding the previous turn's verdict", async () => {
    // A live turn invalidates every prior verdict: the header's outcome describes
    // the turn BEFORE this one, so reporting it would call a working chat
    // finished — the inverse of the reported bug.
    //
    // TWO independent mechanisms defend this, which is why the case survives a
    // red check on either one alone: `latchFieldsFor` refuses to seed while
    // `thinking` is set (pinned on its own in store.test.ts), and `tabStatusFor`
    // ranks `working` above `done` regardless. What this case pins is the
    // end-to-end guarantee the reader actually gets.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", thinking: true })]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c1", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c1"))).toBe("working");
  });

  // A mid-turn reconnect races two independent doors: `loadList`'s header fetch
  // (which seeds from the header) and the SSE connect replay's one `turn_state`
  // per busy chat (which sets `thinking`). Nothing orders them, so BOTH orders
  // have to converge on `working` — otherwise a chat whose turn is still running
  // paints a settled dot until the turn ends.
  //
  // They converge for two independent reasons, and each of these cases isolates
  // one: the seed refuses to run while `thinking` is set, and `setThinking(id,
  // true)` clears both latches, so a seed that already landed is dropped.

  it("converges on working when the replay sets thinking BEFORE the header seed", async () => {
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1" })]);
    setThinking("c1", true); // the replay's turn_state, first
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c1", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c1"))).toBe("working");
    // The seed did not land at all, so nothing is left to leak when the turn ends.
    expect(get("c1")?.turn_done).toBeUndefined();
  });

  it("converges on working when the header seed lands BEFORE the replay", async () => {
    const { loadList } = await import("./store-load.js");
    setSessions([]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c1", "completed")] });

    await loadList();
    // The seed legitimately landed: nothing had said this chat was busy yet.
    expect(get("c1")?.turn_done).toBe(true);

    setThinking("c1", true); // the replay's turn_state, second
    expect(tabStatusFor(get("c1"))).toBe("working");
    // And the stale verdict is GONE rather than merely outranked, so the dot
    // cannot snap back to `done` when this turn ends on some other outcome.
    expect(get("c1")?.turn_done).toBeUndefined();
  });

  it("lets a waiting_on_user chat keep saying so over a seeded done", async () => {
    // `waiting` outranks `done` in tabStatusFor, and the connect replay re-emits
    // the retained waiting status — so the seed must not be able to bury the one
    // state whose whole meaning is that a person still owes an answer.
    const { loadList } = await import("./store-load.js");
    setSessions([session({ id: "c1", agent_status: "waiting_on_user" })]);
    mockApiGetTyped.mockResolvedValue({ chats: [outcomeHeader("c1", "completed")] });

    await loadList();
    expect(tabStatusFor(get("c1"))).toBe("waiting");
  });
});

// ---------------------------------------------------------------------------
// 8. An abandoned ask stops claiming the chat needs a decision.
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
// 9. A run's wait ending releases the LAUNCHING chat's dot.
//
// A chat-parented run's ask is filed under the LAUNCHING CHAT's queue key:
// handlers/run.ts takes the chat id off the SSE envelope, and for such a run that
// is the conversation that started it. So `input` lands on the PARENT tab's dot
// while a step is parked, which is the correct half of the reported behaviour.
//
// The run's own sub-tab reads a DIFFERENT predicate over the same map —
// `runPendingAsks` scans every queue for the run's own id — so the two surfaces
// can disagree about whether the wait is over, and that seam is where the reported
// failure lives: the sub-tab recovers and the parent stays amber for the life of
// the page. Every case here therefore asserts BOTH surfaces, because a parent
// stuck beside a recovered sub-tab is a different defect from nothing clearing at
// all.
//
// The state to come back to is `done`, not `idle`: the launching turn ended long
// before the run did (`run_workflow` returns as soon as the run is created), so a
// dot stuck on `input` cannot be mistaken for the hollow-ring floor.
// ---------------------------------------------------------------------------

describe("a run's wait ending releases the launching chat's dot", () => {
  const PARENT = "c-parent";
  const RUN = "wf-1";

  function runInput(over: Partial<RunInputNeededPayload> = {}): RunInputNeededPayload {
    return {
      workflow_id: RUN,
      ask_id: "ask-1",
      node_id: "review",
      step_session_id: "sess-1",
      agent_name: "reviewer",
      question: "Ship it?",
      asked_at: "2026-09-04T10:00:00Z",
      ...over,
    };
  }

  /** The step's question, filed exactly as `handlers/run.ts` files it for a
   *  chat-parented run: the LAUNCHING chat's id as the queue key, the run stamped
   *  on the decision. */
  function runAsk(over: Partial<RunInputNeededPayload> = {}) {
    const payload = runInput(over);
    return {
      kind: "run_input" as const,
      chatID: PARENT,
      runID: payload.workflow_id,
      askID: payload.ask_id,
      payload,
      submit: vi.fn(),
    };
  }

  /** A step's `_kiro/userInput` question, which reaches the client through the
   *  OTHER door (`handlers/turn.ts`) with the run stamped on it by the
   *  step-session registry. Same queue key, request-shaped identity. */
  function stepQuestion(requestID: number) {
    return {
      kind: "user_input" as const,
      chatID: PARENT,
      runID: RUN,
      requestID,
      payload: { question: "Which branch?", request_id: requestID, run_id: RUN, node_id: "review" },
      submit: vi.fn(),
    };
  }

  /** The run tab's own dock. Copied from `decision-dock.test.ts` rather than
   *  exporting `settle`: answering through a real click is what proves the entry
   *  the sub-tab's card is built from is the one sitting in the PARENT's queue. */
  async function mountRunHost(): Promise<HTMLElement> {
    const { mountRunDecisionDock } = await import("./decision-dock.js");
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, () => RUN);
    return el;
  }

  /** Answer the card on screen in the run's dock. `:scope >` skips an answered
   *  card still on screen for the length of its phase. */
  function answerInRunHost(hostEl: HTMLElement, text: string): void {
    const card = hostEl.querySelector<HTMLElement>(":scope > .dock-card");
    const box = card?.querySelector<HTMLTextAreaElement>(".run-input-text") ?? null;
    if (box !== null) {
      box.value = text;
    }
    [...(card?.querySelectorAll<HTMLButtonElement>("button") ?? [])]
      .find((b) => b.textContent === "Send answer")
      ?.click();
  }

  beforeEach(async () => {
    const dock = await import("./decision-dock.js");
    dock._resetForTest();
    document.body.replaceChildren();
    setSessions([session({ id: PARENT })]);
    // The launching turn ended when the run was created, so this is the state the
    // parent has to come back to.
    setTurnDone(PARENT);
  });

  it("clears the parent when the sub-tab answers the step's question", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks } = await import("./decision-dock.js");
    const runHost = await mountRunHost();
    pushDecision(runAsk());

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
    expect(runPendingAsks(RUN).count).toBe(1);

    answerInRunHost(runHost, "yes, ship it");

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("clears the parent when another surface settled the ask", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, collapseSettledRunInput } =
      await import("./decision-dock.js");
    pushDecision(runAsk());
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    // `run_input_settled`: a second device answered, so every surface still
    // showing the card has to retire it.
    collapseSettledRunInput(RUN, "ask-1", "user");

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("clears the parent when the run ENDS still parked on the ask", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, dropRunAsks } =
      await import("./decision-dock.js");
    pushDecision(runAsk());
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    // A terminal run cannot still be waiting on a person, and by then it has no
    // surface to be answered on either: the sub-tab is auto-closed on completion
    // and `dropTurnDecisions` exempts a run-scoped ask on purpose, so the launching
    // turn's own end cannot reach it. Nothing dropped it, so the parent's dot sat
    // on `input` for the life of the page.
    dropRunAsks(RUN);

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("clears a step's user-input question when the run ends too", async () => {
    const { pushDecision, hasPendingDecision, dropRunAsks } = await import("./decision-dock.js");
    // The sharpest instance of the same hole: this ask is REQUEST-shaped, so
    // `collapseSettledRunInput` cannot name it, and it carries a `runID`, so
    // `dropTurnDecisions` deliberately leaves it. Removal has to ask the adder's
    // own question — "this run's ask, wherever it is filed".
    pushDecision(stepQuestion(1));
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    dropRunAsks(RUN);

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("leaves another chat's own ask alone when a run ends", async () => {
    const { pushDecision, hasPendingDecision, dropRunAsks } = await import("./decision-dock.js");
    // The run-scoped sweep is keyed on the RUN, so a plain chat ask sharing the
    // launching chat's queue is not its business — that one still blocks a turn and
    // still has a card to answer it with.
    pushDecision(stepQuestion(1));
    pushDecision({ ...stepQuestion(2), runID: "" });

    dropRunAsks(RUN);

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
  });

  it("leaves a SIBLING run's ask alone when one of the two ends", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, dropRunAsks } =
      await import("./decision-dock.js");
    // Two runs launched from one chat share that chat's queue key, so the sweep
    // has to separate them by RUN rather than by queue. A survivor carrying a real
    // run id is what pins that: an over-broad match on a sibling run and a match
    // on every ask in the queue are different defects, and only this case can
    // fail on the first one.
    pushDecision(runAsk());
    pushDecision(runAsk({ workflow_id: "wf-2", ask_id: "ask-2" }));

    dropRunAsks(RUN);

    expect(runPendingAsks(RUN).count).toBe(0);
    expect(runPendingAsks("wf-2").count).toBe(1);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
  });

  // The reported symptom, in one assertion: the sub-tab reads as answered and the
  // PARENT stays amber. What holds it is a step's question whose `run_id` arrived
  // EMPTY — the registry had not seen its sub-session — which puts it outside every
  // run-scoped remover while still lighting the launching chat's dot.
  //
  // ITS WARRANT IS NOT A RED CHECK, and it must not be read as one: `dropRunAsks`
  // is keyed on `runID` and this ask has none, so no change to that predicate can
  // make the `"input"` assertion fail. What it guards is the OVER-BROAD fix —
  // widening the run sweep to take run-orphans indiscriminately, which is the same
  // change "leaves another chat's own ask alone when a run ends" below refuses from
  // the other side. The trigger that DOES clear it is red-checked in
  // `handlers/run.test.ts`, where the sweep's four gates live.
  it("holds the parent on input for a step question that carries NO run id", async () => {
    const { pushDecision, hasPendingDecision, runPendingAsks, dropTurnDecisions } =
      await import("./decision-dock.js");
    const runHost = await mountRunHost();
    // The orphan, and the run's own answerable ask, both under the launching chat.
    pushDecision({ ...stepQuestion(1), runID: "" });
    pushDecision(runAsk());
    expect(runPendingAsks(RUN).count).toBe(1);

    answerInRunHost(runHost, "yes, ship it");

    // Every run surface goes quiet — the sub-tab's dock, the run card's `needs
    // input`, the exec page's alert all read this count — while the parent's dot
    // is still held by an ask no run surface can see.
    expect(runPendingAsks(RUN).count).toBe(0);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");

    // The turn-scoped sweep is the one predicate that CAN name it: it keeps only
    // asks carrying a non-empty runID.
    dropTurnDecisions(PARENT);
    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("done");
  });

  it("refuses an empty run id rather than sweeping every chat's ask", async () => {
    const { pushDecision, hasPendingDecision, dropRunAsks } = await import("./decision-dock.js");
    // `runID` is empty on every ordinary chat ask and the id arrives off the wire,
    // so an empty argument would match the whole store rather than one run.
    pushDecision({ ...stepQuestion(1), runID: "" });

    dropRunAsks("");

    expect(tabStatusFor(get(PARENT), hasPendingDecision(PARENT))).toBe("input");
  });
});

// ---------------------------------------------------------------------------
// 8b. The REAL dock repaints a REAL row's dot.
//
// The gap this closes is a seam between two suites, each of which fakes the half
// the other runs for real. Section 8 above drives the real dock and asserts through
// `tabStatusFor` DIRECTLY, so it proves the QUEUE empties and never that a row
// repaints. `chat-tab-strip.test.ts` drives the real row effect and the real
// reactive graph, but against a signal-backed FAKE dock and a mocked
// `setTabStatus`, so it proves the subscription TOPOLOGY and never that the real
// predicate participates in it or that a `data-status` attribute moves.
//
// So the one link neither suite covers is the real `hasPendingDecision` reading
// `queueVersion.value` rather than `.peek()` — a one-character change that would
// leave both suites green and every background chat's dot frozen. This drives the
// real dock, a real `effect`, the real `setTabStatus` and a real row built through
// the real projection, and reads the attribute the browser paints from.
//
// The effect body is chat.ts's `chatRowEffect` minus its name and tooltip writers,
// which is what keeps this case honest without importing chat.ts: doing that needs
// 13 module mocks, three of which (`bus`, `composer-state`, `roles`) are reached by
// the real `tabs.ts` and `tab-materialize.ts` this suite depends on — measured, it
// breaks 30 of the 80 tests here. The last case pins the replica against chat.ts's
// own source instead, which is this suite's existing idiom for a chat.ts fact.
// ---------------------------------------------------------------------------

describe("the dock's own signal repaints the launching chat's row", () => {
  const PARENT = "c-parent";
  const RUN = "wf-1";

  function statusOf(tabID: string): string | null {
    return (
      document
        .querySelector(`[data-tab-id="${tabID}"] .tab-status-dot`)
        ?.getAttribute("data-status") ?? null
    );
  }

  /** The dot half of chat.ts's row effect: the dock read IS the subscription. */
  async function installRowEffect(chatID: string, tabID: string): Promise<() => void> {
    const { effect } = await import("@cplieger/reactive");
    const { hasPendingDecision } = await import("./decision-dock.js");
    const { setTabStatus } = await import("./tabs.js");
    return effect(() => {
      setTabStatus(tabID, tabStatusFor(get(chatID), hasPendingDecision(chatID)));
    });
  }

  it("moves data-status when an ORPHANED ask arrives and when it is swept", async () => {
    await resetProjection();
    const dock = await import("./decision-dock.js");
    dock._resetForTest();
    setSessions([session({ id: PARENT })]);
    // The launching turn ended when the run was created, so this is the state the
    // row starts in and the one it has to come back to.
    setTurnDone(PARENT);

    const tabID = await openSubject("chat", PARENT);
    const dispose = await installRowEffect(PARENT, tabID);
    await paint();
    expect(statusOf(tabID)).toBe("done");

    // A step's question whose run_id arrived EMPTY: filed under the launching
    // chat, invisible to every run-scoped surface, and this is the row it holds.
    dock.pushDecision({
      kind: "user_input",
      chatID: PARENT,
      runID: "",
      requestID: 1,
      payload: { question: "Which branch?", request_id: 1 },
      submit: vi.fn(),
    });
    await paint();
    expect(dock.runPendingAsks(RUN).count).toBe(0);
    expect(statusOf(tabID)).toBe("input");

    dock.dropTurnDecisions(PARENT);
    await paint();
    expect(statusOf(tabID)).toBe("done");
    dispose();
  });

  it("reads the same two inputs chat.ts's own row effect reads", () => {
    // The replica above stands in for `chatRowEffect`, so it is only worth what
    // production still doing the same thing is worth. Both reads are unconditional
    // and ahead of every early return in chat.ts, which is what subscribes a
    // BACKGROUND chat's row to a decision arriving on it.
    expect(chatSrc).toContain("hasPendingDecision(chatID)");
    expect(chatSrc).toContain("tabStatusFor(s, pendingAsk)");
    expect(chatSrc).toContain("setTabStatus(tabID,");
  });
});

// ---------------------------------------------------------------------------
// 10. A row that is CREATED knows what it should show.
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
// 11. The dot is live state, so it must never be persisted.
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
// 12. Two source facts about the leading slot and the motion vocabulary.
// ---------------------------------------------------------------------------

describe("the leading dot slot is one width for every state", () => {
  const tabs = loadCSS("12-tabs.css");

  it("derives both slot margins from the dot tokens, never a literal", () => {
    // The generic slot rule is computed for the DISC, so the smaller diamond
    // would reserve less than every other state — and the chat's name would move
    // sideways at the exact moment its status flipped to or from failed, which is
    // the one moment the reader is watching that row. Each is derived from the
    // token it actually renders at, so changing --dot-size cannot desync them.
    const generic = ruleContaining(tabs, ".tab-status-dot:first-child", "top");
    expect(generic.body).toContain("calc((0.875rem - var(--dot-size)) / 2)");

    const diamond = ruleContaining(
      tabs,
      '.tab-status-dot[data-status="failed"]:first-child',
      "top",
    );
    expect(diamond.body).toContain("calc((0.875rem - var(--dot-size-sm)) / 2)");
  });

  it("keeps the diamond on the small token, so the override stays paired", () => {
    // A rotated square's DIAGONAL is its footprint, so the diamond takes the
    // smaller token to sit level with the disc beside it (6px on the diagonal is
    // 8.49px against an 8px disc). If it ever returns to --dot-size the margin
    // override above becomes wrong rather than merely redundant, so the two are
    // asserted together.
    const failed = ruleContaining(tabs, '.tab-status-dot[data-status="failed"]', "top");
    expect(/inline-size:\s*var\(--dot-size-sm\)/.test(failed.body)).toBe(true);
  });

  it("re-derives the SUB-TAB diamond's slot from both dot tokens too", () => {
    // The same correction one position over, and the position is why it needs its
    // own rule: on a sub-tab the nesting arrow holds the 14px glyph slot, so the
    // dot only has to match its own siblings and the leading rule's arithmetic is
    // the wrong one. Both tokens, never a literal, so changing --dot-size cannot
    // leave the diamond and the disc reserving different widths — which is what
    // would move a delegate's name at the exact moment its status flipped to or
    // from failed.
    const diamond = ruleContaining(
      tabs,
      '.tab.tab-child .tab-status-dot[data-status="failed"]',
      "top",
    );
    expect(diamond.body).toContain("calc((var(--dot-size) - var(--dot-size-sm)) / 2)");
  });
});

// ---------------------------------------------------------------------------
// 13. The same claim in REAL LAYOUT, for a subagent sub-tab.
//
// The source facts above say the two margins are derived from the right tokens.
// They cannot say what the browser does with them, and the one thing that has to
// be true is that a delegate's name does not move when its dot changes state:
// `subagent-dots.ts` writes `working` while the delegate runs and `failed` if it
// fails, so a row whose label shifted on that transition would be a row jumping
// at the one moment the reader is watching it.
//
// It was never asserted for a sub-tab at all. `.tab-nest + .tab-status-dot` sets
// `margin-inline-start: 0` at (0,2,0) while the diamond's correction is (0,4,0),
// so the two rules genuinely contest one property and the cascade decides — which
// is exactly the class of thing this app's own rule says to verify numerically
// rather than reason about.
// ---------------------------------------------------------------------------

describe("a subagent sub-tab's name holds still while its dot changes state", () => {
  let sheet: HTMLStyleElement;

  beforeAll(async () => {
    // The assembled bundle, because the answer here is the CASCADE's rather than
    // any one rule's. Removed in afterAll so the source-fact sections around this
    // one keep the stylesheet-free page their own comments describe.
    const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
    sheet = mountAppCSS();
  });

  afterAll(() => {
    sheet.remove();
  });

  beforeEach(async () => {
    await resetProjection();
  });

  /** The delegate's row, opened as a sub-tab of its launching chat exactly as
   *  `openSubagentTab` opens one. */
  async function subagentRow(): Promise<HTMLElement> {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const parent = await openSubject("chat", "c1");
    const ref = subagentRef("c1", "task-1");
    await openTab({ kind: "subagent", ref, parent, owns: false });
    await paint();
    const node = document.querySelector<HTMLElement>(
      `[data-tab-id="${tabIdFor("subagent", ref)}"]`,
    );
    if (node === null) {
      throw new Error("the subagent row did not render");
    }
    return node;
  }

  /** Where the label starts, measured from the row's own left edge so the strip's
   *  entry animation cancels out of the comparison. */
  function nameOffset(row: HTMLElement): number {
    const name = row.querySelector<HTMLElement>(".tab-name");
    if (name === null) {
      throw new Error("no name element");
    }
    return name.getBoundingClientRect().left - row.getBoundingClientRect().left;
  }

  it("puts the name at the same x for working and for failed", async () => {
    const { setTabStatus, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const row = await subagentRow();
    const id = tabIdFor("subagent", subagentRef("c1", "task-1"));

    setTabStatus(id, "working");
    const working = nameOffset(row);
    setTabStatus(id, "failed");
    const failed = nameOffset(row);
    setTabStatus(id, "done");
    const done = nameOffset(row);

    // Sub-pixel, because the diamond's own correction is a `calc` that can land on
    // a fraction. What must not happen is the ~2px step an uncorrected 6px mark
    // would produce in an 8px slot.
    expect(Math.abs(failed - working)).toBeLessThan(0.5);
    expect(Math.abs(done - working)).toBeLessThan(0.5);
  });

  it("puts the name at that same x before any state has been written", async () => {
    // The reserved slot doing its job, which is the other half of the promise: the
    // effect paints on a later tick than the row is built, so a row that reserved
    // nothing would render one width and then shift.
    const { setTabStatus, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const row = await subagentRow();
    const blank = nameOffset(row);

    setTabStatus(tabIdFor("subagent", subagentRef("c1", "task-1")), "working");
    expect(Math.abs(nameOffset(row) - blank)).toBeLessThan(0.5);
  });

  it("reserves the slot rather than collapsing it, so the dot has somewhere to land", async () => {
    // The GAP this change closes, stated as a measurement so the fix is provable
    // rather than described: with no state written the slot is `display: block`
    // plus `visibility: hidden`, so it occupies its 8px and the name sits past it.
    // A dot that never gets a state is what left that 8px (plus the row's own 8px
    // gap) permanently empty.
    const row = await subagentRow();
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    if (dot === null) {
      throw new Error("no dot element");
    }
    expect(dot.getAttribute("data-status")).toBeNull();
    expect(getComputedStyle(dot).display).toBe("block");
    expect(getComputedStyle(dot).visibility).toBe("hidden");
    expect(dot.getBoundingClientRect().width).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// 14. The gap CLOSED, end to end.
//
// Everything above tests one half: `subagent-dots.test.ts` proves the effect
// resolves the right state (with `tabs.js` mocked, so no DOM), and section 13
// proves the row's layout holds for each state (written by hand, so no effect).
// Neither can fail if the two halves are not JOINED — and the join is the
// deliverable, because the reported defect is a slot that is reserved and never
// filled.
//
// So this drives the real subscriber against the real projection and the real
// store, and asserts what a reader would see: a dot that is VISIBLE with the
// delegate's own state on it, in a row whose name did not move to make room.
// ---------------------------------------------------------------------------

describe("the real subscriber fills the reserved slot", () => {
  let sheet: HTMLStyleElement;
  let installed = false;

  beforeAll(async () => {
    const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
    sheet = mountAppCSS();
  });

  afterAll(() => {
    sheet.remove();
  });

  beforeEach(async () => {
    await resetProjection();
    const { setSessions } = await import("./store.js");
    setSessions([]);
    if (!installed) {
      // Installed once, like the composition root does it: the subscriber
      // registers a module-level effect and exposes no disposer, exactly as
      // `installRunDotSubscriber` does not.
      const { installSubagentDotSubscriber } = await import("./subagent-dots.js");
      installSubagentDotSubscriber();
      installed = true;
    }
  });

  /** A chat whose resident window holds one delegate's invocation, at `status`. */
  async function chatWithDelegate(status: string): Promise<void> {
    const { setSessions } = await import("./store.js");
    setSessions([
      {
        ...session({ id: "c1" }),
        messages: [
          {
            id: "m1",
            role: "assistant",
            ts: 1,
            content: "",
            blocks: [{ type: "tool_use", tool_call_id: "tc1", agent_subtask_id: "task-1" }],
            tool_calls: [
              {
                id: "tc1",
                title: "Sub-agent: introspect",
                kind: "other",
                status,
                agent_subtask_id: "task-1",
                ts: 1,
              },
            ],
          },
        ],
      } as unknown as Session,
    ]);
  }

  async function subagentRow(): Promise<HTMLElement> {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const { subagentRef } = await import("./tab-materialize.js");
    const parent = await openSubject("chat", "c1");
    const ref = subagentRef("c1", "task-1");
    await openTab({ kind: "subagent", ref, parent, owns: false });
    await paint();
    const node = document.querySelector<HTMLElement>(
      `[data-tab-id="${tabIdFor("subagent", ref)}"]`,
    );
    if (node === null) {
      throw new Error("the subagent row did not render");
    }
    return node;
  }

  function dotOf(row: HTMLElement): HTMLElement {
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    if (dot === null) {
      throw new Error("no dot element");
    }
    return dot;
  }

  it("paints a VISIBLE working dot on a running delegate's row", async () => {
    await chatWithDelegate("in_progress");
    const dot = dotOf(await subagentRow());

    // The whole deliverable in three reads: the attribute CSS keys off, the
    // computed visibility the reservation rule was hiding, and a real box.
    expect(dot.getAttribute("data-status")).toBe("working");
    expect(getComputedStyle(dot).visibility).toBe("visible");
    expect(dot.getBoundingClientRect().width).toBeGreaterThan(0);
  });

  it("paints a failed delegate's row red and announces it", async () => {
    await chatWithDelegate("failed");
    const row = await subagentRow();
    expect(dotOf(row).getAttribute("data-status")).toBe("failed");
    expect(getComputedStyle(dotOf(row)).visibility).toBe("visible");
    // The 9px mark is not the only channel: the screen-reader word rides its own
    // element after the name, which is what makes the state reach a reader who
    // cannot see the diamond.
    //
    // The phrase names the DELEGATE, and this case is where the wrong subject was
    // written down: it asserted ", turn failed" on a subagent's row, because the
    // phrase table was keyed on the state alone and a turn was the only producer
    // when it was written. A chat row still says "turn failed" (section 3).
    expect(row.querySelector(".tab-status-sr")?.textContent).toBe(", subagent failed");
    expect(nameFromContents(row)).toContain("subagent failed");
  });

  it("leaves the slot hidden when no invocation is resident", async () => {
    // The residual the design accepts: a boot-restored tab whose chat has not been
    // fetched. "" is honest, and the row keeps the invisible reserved slot rather
    // than claiming a state.
    const { setSessions } = await import("./store.js");
    setSessions([session({ id: "c1" })]);
    const dot = dotOf(await subagentRow());
    expect(dot.getAttribute("data-status")).toBeNull();
    expect(getComputedStyle(dot).visibility).toBe("hidden");
  });

  it("does not move the name when the state arrives", async () => {
    // The reservation earning its keep against the real writer rather than a hand
    // write: the effect paints on a later tick than the row is built, so this is
    // the ordering every real subagent tab has.
    const { upsertToolCall } = await import("./store.js");
    await chatWithDelegate("in_progress");
    const row = await subagentRow();
    const name = row.querySelector<HTMLElement>(".tab-name");
    if (name === null) {
      throw new Error("no name element");
    }
    const offsetOf = (): number =>
      name.getBoundingClientRect().left - row.getBoundingClientRect().left;
    const working = offsetOf();
    expect(dotOf(row).getAttribute("data-status")).toBe("working");

    // The real ingest path for a `tool_call_update`, which is what turns a running
    // delegate into a failed one — and the diamond into the disc's own footprint.
    upsertToolCall(
      "c1",
      "m1",
      {
        id: "tc1",
        title: "Sub-agent: introspect",
        kind: "other",
        status: "failed",
        agent_subtask_id: "task-1",
        ts: 1,
      },
      0,
    );
    await new Promise((r) => setTimeout(r, 0));

    expect(dotOf(row).getAttribute("data-status")).toBe("failed");
    expect(Math.abs(offsetOf() - working)).toBeLessThan(0.5);
  });
});

describe("the dot's motion uses the app's own easing vocabulary", () => {
  const tabs = loadCSS("12-tabs.css");
  const dot = '.tab-status-dot[data-status="working"]';

  it("beats on the standard curve, which now lives on the clock", () => {
    // The easing moved WITH the animation: the dot reads `--vk-beat` as a plain
    // 0..1 amplitude, so the shape is shared by every dot in the app instead of
    // being restated per keyframe set at four different durations.
    const base = loadCSS("03-base.css");
    const clock = ruleContaining(base, ":root", "top");
    expect(clock.body).toContain("animation: vk-beat var(--dot-beat-dur) var(--ease-standard)");
    expect(clock.body).not.toContain("ease-in-out");
  });

  it("scales the beat down rather than flashing to full opacity", () => {
    // Every dot beating in unison at full amplitude trades a noisy strip for a
    // throbbing one, which is not what "less visually present" asked for.
    const glow = ruleContaining(tabs, `${dot}::before`, "top");
    expect(glow.body).toMatch(/opacity:\s*calc\(var\(--vk-beat\) \* 0\.55\)/u);
  });

  it("resolves the token it names", () => {
    expect(loadCSS("01-tokens.css")).toContain("--ease-standard:");
  });
});
