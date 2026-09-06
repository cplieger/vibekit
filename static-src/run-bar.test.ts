// ---------------------------------------------------------------------------
// The composer band's run bar: one line per live workflow run the ACTIVE chat
// launched.
//
// Every case names its observable, because the bar is a pure projection and the
// only thing worth pinning is what it says about store state a reader can act on:
// which runs it shows (the scope decision), what state it claims for each, where a
// click goes, and that it advances its clock without refetching.
//
// The store is REAL. `apiGet` is stubbed per run id so `invalidateRun` resolves
// into the cells the bar reads, which is the same edge the transcript's own suites
// mock. `run-view.js` is replaced so the click's destination is assertable without
// dragging the exec page into the graph.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// The graph reaches the shared DOM registry, which throws on a missing element.
// Every id has to exist before the imports below are evaluated.
for (const id of [
  "run-bar",
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const el = document.createElement(id === "run-bar" ? "ul" : "div");
  el.id = id;
  document.body.appendChild(el);
}

// scroll.ts is a self-initialising singleton over a real scroller; the canonical
// mock is what every suite in this graph uses.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// The click's destination. Replaced rather than spied so the real run page is never
// linked into this file's graph.
const openRunView = vi.hoisted(() => vi.fn());
vi.mock("./run-view.js", () => ({
  openRunView,
  noteAutoOpenedRun: vi.fn(),
  autoCloseRunSubTab: vi.fn(),
  runTabProjectsChat: vi.fn(() => false),
}));

const announce = vi.hoisted(() => vi.fn());
vi.mock("@cplieger/ui-primitives/announce", () => ({ announce }));

/** Per-run inspect answers, consulted by the stubbed `apiGet`. A run absent from
 *  this map answers null, which is the honest "nothing fetched yet" case. */
const inspect = new Map<string, unknown>();
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn((path: string) => {
    const hit = /^\/api\/runs\/([^/?]+)$/.exec(path);
    if (hit !== null) {
      const id = decodeURIComponent(hit[1] ?? "");
      const state = inspect.get(id);
      // CLONED per call, because the real `apiGet` parses fresh JSON: handing the
      // same object back twice makes the run cell's signal dedupe by identity, and
      // the refetch cases below would then pass with no re-render to protect.
      return Promise.resolve(
        state === undefined ? null : { workflowId: id, state: structuredClone(state) },
      );
    }
    return Promise.resolve(null);
  }),
}));

const { initRunBar, _resetRunBarForTest } = await import("./run-bar.js");
const { noteRunLive, noteRunSettled } = await import("./run-store.js");
const { setSessions, setActive } = await import("./store.js");
const { pushDecision, dropDecisions } = await import("./decision-dock.js");
const { apiGet } = await import("./api-client.js");

const bar = document.getElementById("run-bar") as HTMLUListElement;

let seq = 0;
/** A fresh chat id per case, so no case inherits another's live rows. */
function chatID(): string {
  seq++;
  return `c-bar-${String(seq)}`;
}

let runSeq = 0;
/** A fresh run id per case. `run-store.ts`'s cells are module state with one bound
 *  (`forgetRun`, which only the transcript card may call), so reusing an id would
 *  let a case read the state a previous case fetched for it — and a stale SETTLED
 *  cell makes the row invisible, which is exactly what the filter is for. */
function runID(tag: string): string {
  runSeq++;
  return `wf-${tag}-${String(runSeq)}`;
}

/** A run state as `GET /api/runs/{id}` answers it. One running leaf, so the
 *  counters and the elapsed clock have something to read. */
function state(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    workflowId: "wf",
    status: "running",
    runLabel: "nightly sweep",
    root: {
      nodeId: "n1",
      type: "step",
      status: "running",
      startedAt: new Date(Date.now() - 5000).toISOString(),
    },
    ...over,
  };
}

/** Activate `chat` and let every pending run fetch land. */
async function activate(chat: string): Promise<void> {
  setSessions([{ id: chat, name: chat, messages: [] } as never]);
  setActive(chat);
  // One macrotask per fetch generation: the first render triggers `invalidateRun`,
  // whose resolution writes the cell the second render reads.
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

function rowStates(): string[] {
  return [...bar.querySelectorAll("li")].map((li) => li.dataset["state"] ?? "");
}

function rowText(sel: string): string[] {
  return [...bar.querySelectorAll(sel)].map((n) => n.textContent ?? "");
}

/** A parked step's ask, the durable kind, keyed under the launching chat with the
 *  run stamped on it — the same shape `handlers/run.ts` enqueues. */
function pushRunAsk(chat: string, runID: string): void {
  pushDecision({
    kind: "run_input",
    chatID: chat,
    runID,
    askID: `${runID}:ask-1`,
    payload: { workflow_id: runID, question: "which branch?" } as never,
    submit: () => {
      /* not answered in these cases */
    },
  });
}

const chats: string[] = [];
const runs: string[] = [];

beforeEach(() => {
  _resetRunBarForTest();
  inspect.clear();
  openRunView.mockClear();
  announce.mockClear();
  vi.mocked(apiGet).mockClear();
  initRunBar();
});

afterEach(() => {
  for (const id of runs.splice(0)) {
    noteRunSettled(id);
  }
  for (const c of chats.splice(0)) {
    dropDecisions(c);
  }
  setSessions([]);
  setActive("");
  _resetRunBarForTest();
});

/** Record a live run so the afterEach can settle it, whatever the case did. */
function live(runID: string, chat: string, executing = true): void {
  runs.push(runID);
  noteRunLive(runID, chat, executing);
}

describe("the run bar", () => {
  it("stays hidden and empty when the active chat has no live run", async () => {
    const c = chatID();
    chats.push(c);
    await activate(c);

    expect(bar.classList.contains("hidden")).toBe(true);
    expect(bar.children.length).toBe(0);
  });

  // THE SCOPE DECISION. Another chat's run is surfaced by its own tab's activity
  // dot, and a parentless run has no launching chat at all, so neither belongs on
  // this chat's line.
  it("shows one row per live run of the ACTIVE chat and none of anyone else's", async () => {
    const mine = chatID();
    const theirs = chatID();
    chats.push(mine, theirs);
    const [a, b, orphan] = [runID("mine"), runID("theirs"), runID("orphan")];
    inspect.set(a, state({ runLabel: "mine" }));
    inspect.set(b, state({ runLabel: "theirs" }));
    inspect.set(orphan, state({ runLabel: "orphan" }));
    live(a, mine);
    live(b, theirs);
    live(orphan, "");
    await activate(mine);

    expect(bar.classList.contains("hidden")).toBe(false);
    expect(rowText(".run-bar-name")).toEqual(["mine"]);
  });

  it("reads running, waiting and an unanswered ask as three states", async () => {
    const c = chatID();
    chats.push(c);
    const [running, parked, asking] = [runID("run"), runID("park"), runID("ask")];
    inspect.set(running, state({ runLabel: "a" }));
    inspect.set(parked, state({ runLabel: "b", status: "paused" }));
    inspect.set(asking, state({ runLabel: "c", status: "paused" }));
    live(running, c);
    live(parked, c);
    live(asking, c);
    // Driven through the dock, not by faking the reader: `runPendingAsks` is what
    // the bar consults and what the transcript card and the tab dot consult too.
    pushRunAsk(c, asking);
    await activate(c);

    expect(rowStates()).toEqual(["running", "waiting", "input"]);
    expect(rowText(".run-bar-state")).toEqual(["running", "waiting", "waiting for your answer"]);
  });

  it("names a run by its label, then the recipe, then a fallback", async () => {
    const c = chatID();
    chats.push(c);
    const [a, b, d] = [runID("a"), runID("b"), runID("c")];
    inspect.set(a, state({ runLabel: "the label" }));
    inspect.set(b, state({ runLabel: "", workflowName: "the recipe" }));
    inspect.set(d, state({ runLabel: "", workflowName: "" }));
    live(a, c);
    live(b, c);
    live(d, c);
    await activate(c);

    expect(rowText(".run-bar-name")).toEqual(["the label", "the recipe", "Workflow run"]);
  });

  it("says nothing about a run it has not fetched yet", async () => {
    const c = chatID();
    chats.push(c);
    // No inspect entry, so the cell stays undefined.
    live(runID("unknown"), c);
    await activate(c);

    // The row exists — the inventory says the run is live — and it claims no state
    // rather than reporting "not started" for a run that has demonstrably started.
    expect(rowStates()).toEqual(["unknown"]);
    expect(rowText(".run-bar-state")).toEqual([""]);
    expect(bar.querySelector(".run-bar-glyph")?.childElementCount).toBe(0);
  });

  it("reports the step counter and the elapsed clock", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("counters");
    inspect.set(id, state());
    live(id, c);
    await activate(c);

    expect(rowText(".run-bar-steps")).toEqual(["step 1 of 1"]);
    // `formatElapsed`'s sub-minute spelling, a tenth of a second.
    expect(rowText(".run-bar-clock")[0]).toMatch(/^\d+\.\ds$/);
  });

  it("opens the run's own tab on click, and nothing else", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("click");
    inspect.set(id, state({ runLabel: "nightly" }));
    live(id, c);
    await activate(c);

    bar.querySelector<HTMLButtonElement>(".run-bar-open")?.click();

    expect(openRunView).toHaveBeenCalledTimes(1);
    expect(openRunView).toHaveBeenCalledWith(id, "nightly", c);
  });

  // The visible span is ellipsized by CSS, responsively. A length cut in TS would
  // travel into two places a cut does not belong: `openRunView` makes this string the
  // run TAB's name, and it is the button's accessible name.
  it("passes the run's whole name to the opener and to the accessible name", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("long");
    const label = "nightly dependency sweep across every repository in the fleet";
    inspect.set(id, state({ runLabel: label }));
    live(id, c);
    await activate(c);

    const btn = bar.querySelector<HTMLButtonElement>(".run-bar-open");
    expect(rowText(".run-bar-name")).toEqual([label]);
    expect(btn?.getAttribute("aria-label")).toContain(label);

    btn?.click();
    expect(openRunView).toHaveBeenCalledWith(id, label, c);
  });

  it("drops a row when its run settles, and re-hides when it was the last", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("settle");
    inspect.set(id, state());
    live(id, c);
    await activate(c);
    expect(bar.children.length).toBe(1);

    noteRunSettled(id);

    expect(bar.children.length).toBe(0);
    expect(bar.classList.contains("hidden")).toBe(true);
  });

  // A settled state still sitting in the inventory means a `run_finished` this
  // client missed. A completed row in a live-runs bar is the one wrong thing the bar
  // can say, so the store's own answer wins over the inventory.
  it("drops a row the store can prove has finished", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("done");
    inspect.set(id, state({ status: "completed" }));
    live(id, c);
    await activate(c);

    expect(bar.children.length).toBe(0);
    expect(bar.classList.contains("hidden")).toBe(true);
  });

  // What the `untracked` render protects: a refetch that does not move the key must
  // not rebuild the row, or every `run_progress` re-fires the entry animation.
  it("keeps the same row node across a refetch that changes nothing", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("stable");
    inspect.set(id, state());
    live(id, c);
    await activate(c);
    const row = bar.firstElementChild;
    expect(row).not.toBeNull();

    const { invalidateRun } = await import("./run-store.js");
    invalidateRun(id);
    await new Promise((r) => setTimeout(r, 0));

    expect(bar.firstElementChild).toBe(row);
  });

  // A rendered run whose store cell is BLANKED under it. `runCardFor`'s disposer
  // forgets a run whose transcript card unmounts with no run tab open, and
  // `unmountTurnBody` runs that disposal for every turn crossing past TURNS_WARM — so
  // this is the ordinary case for a chat someone keeps talking in. An executing run
  // refills on its next `run_progress` frame; a PAUSED one emits none, so the bar's
  // own re-fetch is the only thing between it and a permanently nameless row.
  it("refetches a rendered run whose store cell was forgotten", async () => {
    const c = chatID();
    chats.push(c);
    const parked = runID("parked");
    inspect.set(parked, state({ runLabel: "nightly sweep", status: "paused" }));
    live(parked, c);
    await activate(c);
    expect(rowText(".run-bar-name")).toEqual(["nightly sweep"]);

    const { forgetRun } = await import("./run-store.js");
    forgetRun(parked);

    // `forgetRun` DELETES the cell's signal rather than writing it, so no subscriber
    // wakes on the forget itself: the blank is read by the next render, which any
    // other live run of this chat arriving is enough to cause. The row must not be
    // dropped in between, or a fresh hold would fetch it for the wrong reason.
    const second = runID("second");
    inspect.set(second, state({ runLabel: "the other one" }));
    live(second, c);
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    expect(rowText(".run-bar-name")).toEqual(["nightly sweep", "the other one"]);
    expect(rowStates()).toEqual(["waiting", "running"]);
  });

  it("advances the clock on the shared tick without refetching", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("tick");
    inspect.set(id, state());
    live(id, c);
    // PRIMED before the bar ever sees the run, so the row is built on the FIRST
    // render rather than on a second one after the bar's own fetch lands. That is
    // the ordinary case for a run already in the store (a chat switch back, a boot
    // restore), and it is the case that catches a hold created after its row.
    const { invalidateRun: prime } = await import("./run-store.js");
    prime(id);
    await new Promise((r) => setTimeout(r, 0));

    // Fake timers installed BEFORE the render that creates the hold, or the interval
    // the bar joins is a real one no `advanceTimersByTime` can reach.
    // `shouldAdvanceTime` keeps the awaits below resolving.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      await activate(c);
      const before = rowText(".run-bar-clock")[0];
      expect(before).not.toBe("");
      const fetches = vi.mocked(apiGet).mock.calls.length;

      // The 1s interval is `messages-blocks.ts`'s, shared with the transcript's
      // cards; the bar joins it as a holder rather than starting one of its own.
      vi.advanceTimersByTime(2000);

      expect(rowText(".run-bar-clock")[0]).not.toBe(before);
      expect(vi.mocked(apiGet).mock.calls.length).toBe(fetches);
    } finally {
      vi.useRealTimers();
    }
  });

  it("announces a count change on the same chat and stays quiet on a switch", async () => {
    const first = chatID();
    const second = chatID();
    chats.push(first, second);
    const [one, two, three] = [runID("ann1"), runID("ann2"), runID("ann3")];
    inspect.set(one, state());
    inspect.set(two, state());
    inspect.set(three, state());
    live(one, first);
    live(two, second);

    // ARRIVING at a chat that already has a run is a switch, not news — the same
    // rule `pending-steers.ts` applies to its own count.
    await activate(first);
    expect(announce).not.toHaveBeenCalled();

    // Same chat, count moves: news.
    live(three, first);
    expect(announce).toHaveBeenCalledTimes(1);
    expect(announce).toHaveBeenCalledWith("2 workflow runs in progress");
    announce.mockClear();

    // A chat switch is not, even though the count moves with it.
    await activate(second);
    expect(announce).not.toHaveBeenCalled();
  });
});
