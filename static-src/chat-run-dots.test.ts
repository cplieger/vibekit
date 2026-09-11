// ---------------------------------------------------------------------------
// The workflow mark on a CHAT's tab row.
//
// Five properties carry the feature, and each one is the reason it exists rather
// than a detail of it:
//
//  1. THE FOLD. N live runs land on ONE mark, by the dot vocabulary's own
//     precedence minus the outcome states: input > waiting > working > nothing.
//     Get the order wrong and a run blocked on a decision is masked by a sibling
//     that needs nothing from anyone — the same masking `tabStatusFor`'s own
//     precedence exists to prevent, one element over.
//
//  2. PER-CHAT SCOPING, and it is the whole point of putting the mark on the strip.
//     `run-bar.ts` already renders the ACTIVE chat's live runs, so it can say runs
//     exist and cannot say WHICH chat. A mark that leaked across rows would answer
//     the bar's question instead of this one.
//
//  3. IT IS NOT GATED ON THE READER. A run launched from chat A must show on A's
//     row while the reader sits in chat B — that is the reported defect verbatim,
//     and the reason the producer reads a GLOBAL inventory rather than the active
//     session.
//
//  4. NO FOOTPRINT WITHOUT A RUN. The mark takes space only while it is painting
//     one, so a quiet row is charged nothing and the strip keeps one text origin
//     with no compensating indent anywhere. It used to reserve its box in every
//     state, which put 16px of empty space on every chat row for the mark's sake;
//     the title moving when a run starts is the accepted cost of taking that back.
//     Asserted against real layout with the shipped stylesheet mounted, because it
//     is a geometry claim and nothing about the markup implies it.
//
//  5. WITHDRAWAL ON SETTLE. The live inventory deletes a run's row at its terminal
//     status, so an OUTCOME is not available to paint. The mark has to disappear
//     rather than turn green, and a `done` leaking through would be a claim this
//     producer cannot support.
//
// The projection is REAL here — rows arrive through an `open_tab` round trip
// against the fake collection and `createTabEl` builds them — because three of the
// five properties are about the row's DOM rather than about the fold. The run store
// and the dock are the two mocks, so a case can drive an inventory and an ask
// directly; both are signal-backed, or the effect under test loses the dependency
// that makes it repaint.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { RunNode, RunState } from "./run-store.js";
import type { TabKind } from "./types.js";

const m = {
  /** workflow id -> its row, as the live inventory holds it: the chat that launched
   *  it, and whether this process holds a deadline for it. */
  live: new Map<string, { chat: string; executing: boolean }>(),
  states: new Map<string, RunState>(),
  /** One entry per unanswered ask, by the RUN it names. */
  asks: [] as string[],
};

const runsVersion = signal(0);
const queueVersion = signal(0);

/** Bump the mocked inventory/state the way a resolved fetch or a lifecycle frame
 *  does, so the effect re-runs. */
function runsChanged(): void {
  runsVersion.value = runsVersion.value + 1;
}

/** Bump the mocked dock queue the way a real push does. */
function dockChanged(): void {
  queueVersion.value = queueVersion.value + 1;
}

vi.mock("./run-store.js", () => ({
  // TRACKED like production's: the inventory's version is what repaints a row when
  // a run starts or settles with no tab mutation behind it. Whole ROWS, because the
  // fold reads `executing` as its floor while a run's cell is still absent.
  liveRunsForChat: vi.fn((chatID: string) => {
    void runsVersion.value;
    if (chatID === "") {
      return [];
    }
    return [...m.live.entries()]
      .filter(([, row]) => row.chat === chatID)
      .map(([id, row]) => ({ id, ...row }));
  }),
  // UNTRACKED, like production's: the demand predicate below is called from
  // `forgetRun`, which runs outside this module's effect.
  peekLiveRun: vi.fn((id: string) => m.live.get(id)),
  // The ids-only wrapper the store keeps for `run-bar.ts` and `chat-settled.ts`.
  // Inert here, and present because a browser-mode mock is linked as real ESM.
  liveRunIDsForChat: vi.fn((chatID: string) => {
    void runsVersion.value;
    return [...m.live.entries()].filter(([, row]) => row.chat === chatID).map(([id]) => id);
  }),
  // TRACKED too: the cell resolving is the moment the mark can paint at all.
  runState: vi.fn((id: string) => {
    void runsVersion.value;
    return m.states.get(id);
  }),
  // The REAL rule, not a stub answering false: the mark's `input` arm is decided by
  // it, so a stub would leave a run parked on a person reading as an ordinary wait.
  // BOTH arms, because a park inside a parallel branch reaches only the second.
  isNeedInputPark: (state: RunState | undefined): boolean => {
    if (state?.status !== "paused") {
      return false;
    }
    const reason = state.pauseReason;
    const byReason =
      reason === "Step requested user input via send_message." ||
      (reason !== undefined &&
        reason.startsWith("Step '") &&
        (reason.endsWith("' is waiting for user input.") ||
          reason.endsWith("' is waiting for the next user message.")));
    const parked = (function find(n: RunNode | undefined): boolean {
      if (n === undefined) {
        return false;
      }
      if (n.status === "paused" && n.completionSignal === "need_input") {
        return true;
      }
      return (n.children ?? []).some(find);
    })(state.root);
    return byReason || parked;
  },
  // The tab factory's own read, inert: a Browser-Mode mock is linked as real ESM,
  // so a name any module in the graph reaches has to exist on it.
  runLabelOf: vi.fn(() => ""),
}));

vi.mock("./decision-dock.js", () => ({
  // The RUN-scoped reader, joined by run id: a chat-parented run's ask is filed
  // under the LAUNCHING chat with the run stamped on the payload, so only a scan
  // over that field finds it.
  runPendingAsks: vi.fn((workflowID: string) => {
    void queueVersion.value;
    return {
      count: m.asks.filter((a) => a === workflowID).length,
      nodes: new Set<string>(),
      label: "",
    };
  }),
}));

// --- The projection harness, as tab-dot.test.ts assembles it -----------------

vi.mock("./router.js", () => ({
  pushRoute: vi.fn(),
  buildPath: vi.fn(() => "/"),
  parseRoute: vi.fn(),
}));
// Type-only, for the `importOriginal` below.
import type * as TabsDrag from "./tabs-drag.js";
// The three FUNCTIONS are stubbed and nothing else is: `DRAG_THRESHOLD_PX` is the
// strip's drag slop and `tabs.ts` reads it, so a partial factory would fail this
// whole file at link time.
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((mod) => mod.tabTransportMock()),
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
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));

const { mockApiGetTyped } = vi.hoisted(() => ({ mockApiGetTyped: vi.fn() }));
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((mod) => {
    const listTabs = mod.tabListRead();
    return {
      apiGetTyped: vi.fn((path: string, decode: unknown) =>
        path === "/api/tabs" ? listTabs(path) : mockApiGetTyped(path, decode),
      ),
      apiGetTypedOrError: vi.fn(),
    };
  }),
);
vi.mock("./toast.js", () =>
  import("./__test-helpers__/toast-mock.js").then((mod) => mod.toastMock()),
);

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
    forceReflow: vi.fn(() => 0),
  };
});

const { chatTabFoldsRun, installChatRunDotSubscriber } = await import("./chat-run-dots.js");

async function paint(): Promise<void> {
  await new Promise((r) => requestAnimationFrame(() => r(null)));
}

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
  registerTabOpeners({
    chat: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), dot: () => "" },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
  document.body.innerHTML = '<div id="tab-list"></div>';
}

/** Open a tab of any kind and answer with its minted id. */
async function openSubject(kind: TabKind, ref = "", activate = true): Promise<string> {
  const { openTab, tabIdFor } = await import("./tabs.js");
  await openTab({ kind, ref, activate });
  return tabIdFor(kind, ref);
}

function rowOf(id: string): HTMLElement {
  const row = document.querySelector<HTMLElement>(`[data-tab-id="${id}"]`);
  if (row === null) {
    throw new Error(`tab ${id} did not render`);
  }
  return row;
}

function markOf(id: string): HTMLElement {
  const mark = rowOf(id).querySelector<HTMLElement>(".tab-run-dot");
  if (mark === null) {
    throw new Error(`tab ${id} carries no workflow mark`);
  }
  return mark;
}

/** What the mark is painting: the state CSS keys off, or "" for withdrawn. */
function markState(id: string): string {
  return markOf(id).dataset["status"] ?? "";
}

/** A CSS length RESOLVED by the page, so a geometry expectation is derived from
 *  the token the rule names instead of restating its value. The tokens are authored
 *  in rem and one of them changes on a coarse pointer, so reading them off `:root`
 *  and converting here would put a second unit rule in the test. */
function probe(value: string): number {
  const p = document.createElement("div");
  p.style.inlineSize = value;
  document.body.append(p);
  const w = p.getBoundingClientRect().width;
  p.remove();
  return w;
}

/** The row's own flex gap, which is what every element in the leading cluster is
 *  separated by and therefore the unit the mark's footprint is charged in. */
function gap(): number {
  return probe("var(--sp-2)");
}

/** Wait out everything moving in a row, which is the precondition for comparing two
 *  geometry reads taken in different frames.
 *
 *  TWO animations live here, and missing either one costs 12px or 16px of nonsense.
 *  The mark's footprint is a TRANSITION (12-tabs.css), so a read in the tick of the
 *  state write lands mid-flight at the tucked value. And a freshly rendered row runs
 *  `vk-slide-in-x` (10-shell-app.css `.tab.entering`), a `translateX(-0.75rem)` over
 *  the whole ROW — so a baseline taken right after `paint()` is 12px left of where
 *  the row settles, and the shift it is subtracted from reads 28 instead of 16. The
 *  subtree covers both, which is why this takes the row rather than the mark.
 *
 *  Event-driven rather than a timeout, so it is not a load-sensitive assertion: the
 *  rect read forces the style recalc that CREATES the transitions, and `finished` is
 *  what says they are over. Infinite animations are excluded or the wait never
 *  returns; a cancelled one rejects, which is a settle for this purpose (a second
 *  state landed and the caller waits on its own settle). */
async function settle(id: string): Promise<void> {
  const row = rowOf(id);
  row.getBoundingClientRect();
  const finite = row.getAnimations({ subtree: true }).filter((a) => {
    const timing = a.effect?.getComputedTiming();
    return Number.isFinite(timing?.endTime ?? Number.POSITIVE_INFINITY);
  });
  await Promise.all(finite.map((a) => a.finished.catch(() => undefined)));
  await new Promise((r) => requestAnimationFrame(() => r(undefined)));
}

/** Where a row's title starts, which is the one number every alignment case here
 *  is about. */
function titleLeft(id: string): number {
  const name = rowOf(id).querySelector<HTMLElement>(".tab-name");
  if (name === null) {
    throw new Error(`tab ${id} has no name element`);
  }
  return name.getBoundingClientRect().left;
}

/** Record a run as live for a chat, with the status its own cell reports. */
function liveRun(runID: string, chatID: string, state: Partial<RunState> = {}): void {
  m.live.set(runID, { chat: chatID, executing: true });
  m.states.set(runID, { workflowId: runID, ...state } as RunState);
  runsChanged();
}

/** A run the inventory holds and nothing has been fetched for: the state a
 *  lifecycle frame or the boot rebuild leaves for the round trip before `inspect`
 *  answers. `executing` is the only thing the client has been told about it. */
function unfetchedRun(runID: string, chatID: string, executing: boolean): void {
  m.live.set(runID, { chat: chatID, executing });
  runsChanged();
}

/** A run reaching a terminal status: the inventory row goes, which is the only
 *  thing the client is told. */
function settleRun(runID: string): void {
  m.live.delete(runID);
  runsChanged();
}

let installed = false;

beforeEach(async () => {
  m.live.clear();
  m.states.clear();
  m.asks.length = 0;
  await resetProjection();
  if (!installed) {
    installChatRunDotSubscriber();
    installed = true;
  }
  // The install above ran once; every later case relies on the effect's own
  // dependencies to re-run it, which is exactly the production path.
  runsChanged();
});

// ---------------------------------------------------------------------------
// 1. The mark exists, on chat rows and nowhere else.
// ---------------------------------------------------------------------------

describe("the workflow mark rides a chat row's leading cluster", () => {
  it("sits immediately after the activity dot and before the name", async () => {
    const id = await openSubject("chat", "c1");
    await paint();
    const kids = [...rowOf(id).children].map((e) => e.className.split(" ")[0]);
    expect(kids.slice(0, 3)).toEqual(["tab-status-dot", "tab-run-dot", "tab-name"]);
  });

  it("hides itself from the accessibility tree and speaks through a phrase span", async () => {
    const id = await openSubject("chat", "c1");
    await paint();
    // Colour and shape are one channel; the word beside it is the other. Both
    // marks follow the name, so the row announces the title first.
    expect(markOf(id).getAttribute("aria-hidden")).toBe("true");
    const order = [...rowOf(id).children].map((e) => e.className.split(" ")[0]);
    expect(order.indexOf("tab-run-sr")).toBeGreaterThan(order.indexOf("tab-name"));
    expect(order.indexOf("tab-run-sr")).toBeGreaterThan(order.indexOf("tab-status-sr"));
  });

  it("is never given the grab-me-to-reorder class", async () => {
    const id = await openSubject("chat", "c1");
    await paint();
    // `.tab-icon` also means "grab me", which is why the nesting arrow declines it
    // too. A status mark is not a drag handle.
    expect(markOf(id).classList.contains("tab-icon")).toBe(false);
  });

  it("goes on a chat SUB-TAB too, which is its own chat with its own runs", async () => {
    const { openTab, tabIdFor } = await import("./tabs.js");
    const parent = await openSubject("chat", "c1");
    await openTab({ kind: "chat", ref: "c2", parent });
    const child = tabIdFor("chat", "c2");
    await paint();
    const kids = [...rowOf(child).children].map((e) => e.className.split(" ")[0]);
    expect(kids.slice(0, 4)).toEqual(["tab-nest", "tab-status-dot", "tab-run-dot", "tab-name"]);
  });

  it("goes on no other kind, because nothing else in the strip launches a run", async () => {
    const files = await openSubject("files");
    const run = await openSubject("run", "wf_1");
    await paint();
    expect(rowOf(files).querySelector(".tab-run-dot")).toBeNull();
    expect(rowOf(run).querySelector(".tab-run-dot")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 2. The fold.
// ---------------------------------------------------------------------------

describe("N live runs fold onto one mark", () => {
  let chat = "";

  beforeEach(async () => {
    chat = await openSubject("chat", "c1");
    await paint();
  });

  it("shows nothing at all for a chat with no live run", () => {
    expect(markState(chat)).toBe("");
  });

  it("works while a run is executing", () => {
    liveRun("wf_1", "c1", { status: "running" });
    expect(markState(chat)).toBe("working");
  });

  it("waits for a run that is parked with nothing moving", () => {
    liveRun("wf_1", "c1", { status: "paused" });
    expect(markState(chat)).toBe("waiting");
  });

  it("needs a decision when a run is parked on a person", () => {
    liveRun("wf_1", "c1", {
      status: "paused",
      pauseReason: "Step requested user input via send_message.",
    });
    expect(markState(chat)).toBe("input");
  });

  it("needs a decision when the dock holds an unanswered ask for the run", () => {
    liveRun("wf_1", "c1", { status: "running" });
    m.asks.push("wf_1");
    dockChanged();
    // The ask arrives with the run still `running`, exactly as KAS reports it, so
    // without this join a run blocked on a permission reads as making progress.
    expect(markState(chat)).toBe("input");
  });

  it("puts a parked run AHEAD of an executing sibling", () => {
    liveRun("wf_working", "c1", { status: "running" });
    liveRun("wf_parked", "c1", { status: "paused" });
    // A run that stopped is the one a reader can act on; the one still going needs
    // nothing from anyone, so reporting it would mask the other.
    expect(markState(chat)).toBe("waiting");
  });

  it("puts a run wanting a decision ahead of both", () => {
    liveRun("wf_working", "c1", { status: "running" });
    liveRun("wf_parked", "c1", { status: "paused" });
    liveRun("wf_asking", "c1", {
      status: "paused",
      pauseReason: "Step requested user input via send_message.",
    });
    expect(markState(chat)).toBe("input");
  });

  it("works from the inventory's own flag before the run's cell resolves", () => {
    // The row lands with the lifecycle frame; the `inspect` that fills the cell is a
    // round trip behind it. `executing` is what the client has been told in that
    // window, so the mark paints from it rather than waiting — which is what makes
    // the square appear from the first answer instead of the second.
    unfetchedRun("wf_unknown", "c1", true);
    expect(markState(chat)).toBe("working");
  });

  it("says nothing for an unfetched run this process holds no deadline for", () => {
    // `executing === false` is where the flag stops being an answer: a parked lease
    // and one read back from disk report it identically, so claiming a state would
    // be guessing. `run-bar.ts` withholds in the same case.
    unfetchedRun("wf_unknown", "c1", false);
    expect(markState(chat)).toBe("");
  });

  it("still reports a sibling whose state HAS arrived", () => {
    unfetchedRun("wf_unknown", "c1", false);
    liveRun("wf_known", "c1", { status: "running" });
    expect(markState(chat)).toBe("working");
  });

  it("takes the cell over the flag once the cell arrives", () => {
    // `executing` reports whether THIS PROCESS holds a deadline for the run, so it is
    // a FLOOR for the window before the cell lands and nothing afterwards: a run read
    // back from disk answers `false` while genuinely running, which is the state a
    // boot rebuild lands in. Gating the fold on the flag rather than only the floor
    // would withhold for exactly that run.
    m.live.set("wf_disk", { chat: "c1", executing: false });
    m.states.set("wf_disk", { workflowId: "wf_disk", status: "running" } as RunState);
    runsChanged();
    expect(markState(chat)).toBe("working");
  });

  it("lets an unanswered ask outrank the floor's withholding", () => {
    // The ask is joined by RUN id and short-circuits ahead of any status, so it
    // reaches the run whose cell has not arrived AND whose flag withholds — which is
    // the run a reader most needs marked, because nothing else on screen says it is
    // blocked on them.
    unfetchedRun("wf_unknown", "c1", false);
    m.asks.push("wf_unknown");
    dockChanged();
    expect(markState(chat)).toBe("input");
  });
});

// ---------------------------------------------------------------------------
// 3. Scoping, and the reader's own position.
// ---------------------------------------------------------------------------

describe("the mark is scoped to the chat that launched the run", () => {
  it("leaves another chat's row alone", async () => {
    const a = await openSubject("chat", "cA");
    const b = await openSubject("chat", "cB");
    await paint();
    liveRun("wf_1", "cA", { status: "running" });
    expect(markState(a)).toBe("working");
    expect(markState(b)).toBe("");
  });

  it("shows chat A's run on A's row while the reader sits in chat B", async () => {
    const a = await openSubject("chat", "cA");
    // Opening B activates it, so the reader is looking at B — the reported defect
    // verbatim: from another tab there was no way to see that A had a run going.
    const b = await openSubject("chat", "cB");
    await paint();
    const { getActiveTabId } = await import("./tabs.js");
    expect(getActiveTabId()).toBe(b);

    liveRun("wf_1", "cA", { status: "running" });
    expect(markState(a)).toBe("working");
  });

  it("marks a row for a run that started before that chat's tab was rendered", async () => {
    // The inventory is GLOBAL and rebuilt from GET /api/runs/live, so a run can
    // predate the row. The state is parked on the row and repainted by
    // createTabEl, which is what makes a boot restore correct.
    liveRun("wf_1", "cA", { status: "running" });
    const a = await openSubject("chat", "cA");
    await paint();
    expect(markState(a)).toBe("working");
  });
});

// ---------------------------------------------------------------------------
// 4. Withdrawal on settle.
// ---------------------------------------------------------------------------

describe("the mark withdraws when a run ends", () => {
  let chat = "";

  beforeEach(async () => {
    chat = await openSubject("chat", "c1");
    await paint();
  });

  it("goes away rather than turning green", () => {
    liveRun("wf_1", "c1", { status: "running" });
    expect(markState(chat)).toBe("working");
    // A terminal `run_finished` deletes the inventory row, so an OUTCOME is not
    // available to paint. `done` here would be a claim this producer cannot make.
    settleRun("wf_1");
    expect(markState(chat)).toBe("");
  });

  it("refuses a settled outcome even if one reaches the fold", () => {
    // The status arm exists in `runStatusFor`, so the refusal has to be at the
    // FOLD rather than left to an inventory that happens never to hold the row.
    m.live.set("wf_done", { chat: "c1", executing: true });
    m.states.set("wf_done", { workflowId: "wf_done", status: "completed" } as RunState);
    m.live.set("wf_failed", { chat: "c1", executing: true });
    m.states.set("wf_failed", { workflowId: "wf_failed", status: "failed" } as RunState);
    runsChanged();
    expect(markState(chat)).toBe("");
  });

  it("keeps a sibling that is still going", () => {
    liveRun("wf_1", "c1", { status: "running" });
    liveRun("wf_2", "c1", { status: "running" });
    settleRun("wf_1");
    expect(markState(chat)).toBe("working");
  });
});

// ---------------------------------------------------------------------------
// 5. The count and its breakdown.
// ---------------------------------------------------------------------------

describe("the fold's arithmetic survives in the phrase and the tooltip", () => {
  let chat = "";

  beforeEach(async () => {
    chat = await openSubject("chat", "c1");
    await paint();
  });

  function spoken(): string {
    return rowOf(chat).querySelector(".tab-run-sr")?.textContent ?? "";
  }

  it("paints the tooltip and the announced word from one string", () => {
    liveRun("wf_1", "c1", { status: "running" });
    const phrase = "1 workflow run, working";
    expect(markOf(chat).dataset["tooltip"]).toBe(phrase);
    expect(spoken()).toBe(`, ${phrase}`);
  });

  it("names the run rather than the turn, which is the dot's subject", () => {
    liveRun("wf_1", "c1", { status: "running" });
    expect(markOf(chat).dataset["tooltip"]).toContain("workflow run");
  });

  it("breaks three runs down so one wanting a decision does not read as one run", () => {
    liveRun("wf_a", "c1", { status: "running" });
    liveRun("wf_b", "c1", { status: "running" });
    liveRun("wf_c", "c1", {
      status: "paused",
      pauseReason: "Step requested user input via send_message.",
    });
    // The fold shows ONE mark, so this string is the only place the count and the
    // split survive. Without it "three runs, one wanting a decision" is
    // indistinguishable from "one run".
    expect(markOf(chat).dataset["tooltip"]).toBe("3 workflow runs, 1 needs a decision");
  });

  it("clears the phrase with the state, so nothing outlives its mark", () => {
    liveRun("wf_1", "c1", { status: "running" });
    settleRun("wf_1");
    expect(markOf(chat).hasAttribute("data-tooltip")).toBe(false);
    expect(spoken()).toBe("");
  });
});

// ---------------------------------------------------------------------------
// 6. The mark's footprint, against real layout.
//
// The mark takes space only while a run is live, so a row with no run is charged
// nothing for it. That replaced a reserved box (`visibility: hidden`) which bought
// a title that never moved and charged every chat row 16px of empty space to do
// it — reported as a large permanent gap between the activity dot and the title.
// The cost of withdrawing it is a title that moves when a run starts, so BOTH the
// zero footprint and the size of that move are measured here with the shipped
// stylesheet mounted: nothing about the markup implies either.
// ---------------------------------------------------------------------------

describe("the mark takes space only while a run is live", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  it("puts nothing between the activity dot and the title while no run is going", async () => {
    // THE DEFECT, measured: the whole distance from the dot's ink to the title is the
    // dot's own trailing half-slot plus the row's gap, and no third term. With the
    // mark's box reserved it was that plus a gap plus a mark — 27px against 12 on a
    // fine pointer — on every chat row in the strip, permanently.
    const chat = await openSubject("chat", "c1");
    await paint();
    await settle(chat);
    const row = rowOf(chat);
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    expect(dot).not.toBeNull();
    const dotToName = titleLeft(chat) - (dot?.getBoundingClientRect().right ?? 0);
    expect(dotToName).toBeCloseTo(
      (probe("var(--icon-ui)") - probe("var(--dot-size)")) / 2 + gap(),
      1,
    );
  });

  it("cancels its own footprint while it is tucked away", async () => {
    const chat = await openSubject("chat", "c1");
    await paint();
    const mark = markOf(chat);
    // The mark keeps its BOX so it has something to animate into layout from, and
    // pays for that box with a negative leading margin: one mark plus one gap, which
    // is the pair of terms the row's own `gap` charges on either side of it. Not
    // `visibility: hidden`, whose box is charged in full, and not `display: none`,
    // which cannot animate.
    expect(getComputedStyle(mark).opacity).toBe("0");
    expect(mark.getBoundingClientRect().width).toBeGreaterThan(0);
    expect(Number.parseFloat(getComputedStyle(mark).marginInlineStart)).toBeCloseTo(
      -(probe("var(--dot-size)") + gap()),
      1,
    );

    liveRun("wf_1", "c1", { status: "running" });
    await settle(chat);
    expect(getComputedStyle(mark).opacity).toBe("1");
    expect(Number.parseFloat(getComputedStyle(mark).marginInlineStart)).toBeCloseTo(0, 1);
  });

  it("opens the space BEFORE the mark fades into it, and closes it after", async () => {
    // The spawn's ORDER, which is the whole of what a reader sees: the title makes
    // room, then the mark arrives in the room it made. Read off the resolved cascade
    // rather than the source, because the two directions are two separate lists and
    // what matters is which property carries the delay in each.
    const chat = await openSubject("chat", "c1");
    await paint();
    const mark = markOf(chat);
    const list = (el: HTMLElement): Map<string, string> => {
      const cs = getComputedStyle(el);
      const props = cs.transitionProperty.split(", ");
      const delays = cs.transitionDelay.split(", ");
      return new Map(props.map((p, i) => [p, delays[i] ?? "0s"]));
    };
    // EXIT (the tuck's own list): the fade leads, the space closes behind it.
    const exit = list(mark);
    expect(exit.get("opacity")).toBe("0s");
    expect(Number.parseFloat(exit.get("margin-inline-start") ?? "0")).toBeGreaterThan(0);

    liveRun("wf_1", "c1", { status: "running" });
    // ENTRY (the state rule's list): the space leads, the fade follows it.
    const entry = list(mark);
    expect(entry.get("margin-inline-start")).toBe("0s");
    expect(Number.parseFloat(entry.get("opacity") ?? "0")).toBeGreaterThan(0);
  });

  it("moves the title by exactly one gap and one mark when a run starts, and back", async () => {
    // The accepted cost of withdrawing the reservation, pinned so it cannot grow: the
    // shift is the mark plus the one gap it is charged, and it is fully reversed on
    // settle. A reader can see what moved it, which the permanent gap never showed.
    const chat = await openSubject("chat", "c1");
    const { renameTab } = await import("./tabs.js");
    renameTab(chat, "Fix the parser");
    await paint();
    await settle(chat);
    const idle = titleLeft(chat);

    liveRun("wf_1", "c1", { status: "running" });
    expect(markState(chat)).toBe("working");
    await settle(chat);
    expect(titleLeft(chat) - idle).toBeCloseTo(gap() + probe("var(--dot-size)"), 1);

    settleRun("wf_1");
    expect(markState(chat)).toBe("");
    await settle(chat);
    expect(titleLeft(chat)).toBeCloseTo(idle, 1);
  });

  it("charges the cluster exactly one glyph slot, one gap and one mark while live", async () => {
    const chat = await openSubject("chat", "c1");
    await paint();
    liveRun("wf_1", "c1", { status: "running" });
    await settle(chat);
    const row = rowOf(chat);
    const dot = row.querySelector<HTMLElement>(".tab-status-dot");
    const mark = markOf(chat);
    // The arithmetic 12-tabs.css states, DERIVED rather than restated as numbers, so
    // a token retune moves the expectation instead of failing it. Probes resolve the
    // tokens; nothing here is a literal.
    const slot = probe("var(--icon-ui)");
    const size = probe("var(--dot-size)");
    // The mark is charged the row's own gap and nothing else, and the dot keeps the
    // trailing half of the glyph slot it holds: that is the whole of the pair's extra
    // chrome, so the cluster is one glyph slot plus one gap plus one mark.
    const dotToMark = mark.getBoundingClientRect().left - (dot?.getBoundingClientRect().right ?? 0);
    expect(dotToMark).toBeCloseTo((slot - size) / 2 + gap(), 1);
    // And the name follows the mark on that same gap.
    const markToName = titleLeft(chat) - mark.getBoundingClientRect().right;
    expect(markToName).toBeCloseTo(gap(), 1);
  });

  it("keeps two chat rows with no live run on ONE origin", async () => {
    const a = await openSubject("chat", "cA");
    const b = await openSubject("chat", "cB");
    await paint();
    // The common case is every row: a chat with no run in flight is what the strip
    // mostly holds, and those rows share an origin with each other and with every
    // other kind (section 7 below).
    await settle(a);
    await settle(b);
    expect(titleLeft(a)).toBe(titleLeft(b));
  });

  it("bounds the divergence a live run creates to that one shift", async () => {
    const a = await openSubject("chat", "cA");
    const b = await openSubject("chat", "cB");
    await paint();
    liveRun("wf_1", "cA", {
      status: "paused",
      pauseReason: "Step requested user input via send_message.",
    });
    // `input` is the state that also carries a halo, which is a box-shadow and so
    // takes no layout — the widest state must still cost exactly the mark and its
    // gap, or the divergence between a running row and a quiet one grows with state.
    expect(markState(a)).toBe("input");
    await settle(a);
    await settle(b);
    expect(titleLeft(a) - titleLeft(b)).toBeCloseTo(gap() + probe("var(--dot-size)"), 1);
  });

  it("moves a chat SUB-TAB's name by that same shift, and no other", async () => {
    // A sub-tab's cluster is a different rule from a top-level row's — the nesting
    // arrow holds the glyph slot and `.tab-nest + .tab-status-dot` zeroes the dot's
    // own margin — so the shift has to be measured on that shape too. The dot's
    // reservation for the two kinds that NEST is also a separate rule from the
    // mark's, and this is the row where the two meet.
    const { openTab, tabIdFor, renameTab } = await import("./tabs.js");
    const parent = await openSubject("chat", "c1");
    await openTab({ kind: "chat", ref: "c2", parent });
    const child = tabIdFor("chat", "c2");
    renameTab(child, "A tangent");
    await paint();
    await settle(child);
    const idle = titleLeft(child);

    liveRun("wf_1", "c2", { status: "running" });
    expect(markState(child)).toBe("working");
    await settle(child);
    expect(titleLeft(child) - idle).toBeCloseTo(gap() + probe("var(--dot-size)"), 1);

    settleRun("wf_1");
    expect(markState(child)).toBe("");
    await settle(child);
    expect(titleLeft(child)).toBeCloseTo(idle, 1);
  });
});

// ---------------------------------------------------------------------------
// 7. The strip's shared text origin, which this feature must not disturb.
//
// `.tab-status-dot:first-child` puts a chat row's leading dot in the KIND GLYPH'S
// slot for one reason: so a chat row's title lines up with a settings or files
// row's. That is now the whole mechanism — the mark costs a quiet row nothing, so
// the two compensating `.tab-name` indents that used to pay for its reserved box
// are gone. It is a claim about eight kinds this producer never writes to, which is
// why it is measured here rather than reasoned about.
// ---------------------------------------------------------------------------

describe("a mixed strip keeps one text origin", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  it("lines a chat row's title up with every other top-level kind's", async () => {
    const chat = await openSubject("chat", "c1");
    const kinds: TabKind[] = ["files", "git", "history", "settings", "docs"];
    const ids = [];
    for (const kind of kinds) {
      ids.push(await openSubject(kind));
    }
    await paint();
    const origin = titleLeft(chat);
    for (const [i, id] of ids.entries()) {
      expect(titleLeft(id), `a ${kinds[i]} row's title must share the chat row's origin`).toBe(
        origin,
      );
    }
    // And the shared origin is ONE glyph slot plus one gap, with no term for the
    // mark: the dot's slot IS the glyph's width, so the two shapes agree by
    // construction rather than through a corrective indent. Derived from the tokens,
    // so a retune moves it — and on a coarse pointer --icon-ui changes while a
    // literal would not, which is how the old 0.875rem hid a 4px misalignment.
    const row = rowOf(chat).getBoundingClientRect();
    const border = probe("1px");
    const pad = probe("var(--sp-3)");
    expect(origin - row.left).toBeCloseTo(border + pad + probe("var(--icon-ui)") + gap(), 1);
  });

  it("lines a chat SUB-TAB's title up with the other kinds that nest", async () => {
    // A run sub-tab and a subagent sub-tab are the two kinds that sit here and cannot
    // carry a mark, and their arrow is --icon-ui exactly like a chat sub-tab's — so
    // with the mark costing a quiet row nothing, all three shapes are arrow, gap,
    // reserved dot slot, gap, title, and they agree with no indent anywhere.
    const { openTab, tabIdFor } = await import("./tabs.js");
    const parent = await openSubject("chat", "c1");
    await openTab({ kind: "chat", ref: "c2", parent });
    await openTab({ kind: "run", ref: "wf_1", parent, owns: false });
    await openTab({ kind: "subagent", ref: "c1/task-1", parent, owns: false });
    await paint();
    const chatChild = titleLeft(tabIdFor("chat", "c2"));
    expect(titleLeft(tabIdFor("run", "wf_1"))).toBe(chatChild);
    expect(titleLeft(tabIdFor("subagent", "c1/task-1"))).toBe(chatChild);
    // The two SHAPES do not share an origin with each other, and that is by design
    // rather than a gap in the derivation: a sub-tab's whole ROW is indented 1rem
    // and its nesting arrow holds the glyph slot, so its title is further right by
    // both. What the rule buys is one origin per shape, which is what a reader scans.
    expect(chatChild).toBeGreaterThan(titleLeft(parent));
    expect(
      rowOf(tabIdFor("chat", "c2")).getBoundingClientRect().left -
        rowOf(parent).getBoundingClientRect().left,
    ).toBeCloseTo(probe("1rem"), 1);
  });
});

// ---------------------------------------------------------------------------
// 6. The fold's DEMAND on a run's state cell.
//
// The store's cache has one bound, `forgetRun`, reached when the transcript's run
// card unmounts — and this module is a reader that card knows nothing about, which
// is the seam that played the mark's 350ms withdraw over a run that was still
// going. The predicate is what the store asks instead of enumerating its readers.
//
// The PARKED case is the half a floor cannot carry: an `executing === false` row
// contributes nothing without its cell, and a parked run emits no frame that would
// refill one, so the mark stays withdrawn until that chat is next activated.
// ---------------------------------------------------------------------------

describe("the fold's demand on a run's state cell", () => {
  it("claims a PARKED run whose launching chat has an open tab", async () => {
    await openSubject("chat", "c1");
    unfetchedRun("wf_parked", "c1", false);

    expect(chatTabFoldsRun("wf_parked")).toBe(true);
  });

  it("claims nothing for a chat with no open tab, so the bound still holds", async () => {
    await openSubject("chat", "c2");
    liveRun("wf_elsewhere", "c1", { status: "running" });

    expect(chatTabFoldsRun("wf_elsewhere")).toBe(false);
  });

  it("claims nothing once the inventory has settled the run", async () => {
    await openSubject("chat", "c1");
    liveRun("wf_done", "c1", { status: "running" });
    expect(chatTabFoldsRun("wf_done")).toBe(true);

    // The row goes at the terminal status, and the fold stops reading that cell in
    // the same pass — so the cell is releasable while the chat tab is still open.
    settleRun("wf_done");

    expect(chatTabFoldsRun("wf_done")).toBe(false);
  });
});
