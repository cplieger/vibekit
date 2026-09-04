import { describe, it, expect, vi, beforeEach } from "vitest";
import type { PageFind } from "./find-registry.js";
import type * as ModHistory from "./history.js";
import { ICON_TAB_RUN, outcomeIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";

/** Cache-buster for the re-imports below.
 *
 * `vi.resetModules()` does not re-evaluate a module in Browser Mode: the module
 * map is URL-keyed, so a following `await import()` hands back the CACHED
 * instance and every test after the first observes stale module state. Busting
 * the specifier per evaluation is what actually mints a fresh instance. The `.ts`
 * extension is load-bearing — written `.js` the suite still passes while coverage
 * silently attributes every evaluation to a file that does not exist.
 *
 * Only the module under test is busted. Its own dependencies keep their plain
 * specifiers, so `vi.mock` still intercepts them and a shared module the test
 * also imports is the same instance the fresh module got.
 */
let bootSeq = 0;

const dispatch = vi.fn();
const cancelSessions = vi.fn();
// Resolves with an OUTCOME: openRow branches on "gone" (the retention-off 404)
// to refresh the list, so the mock answers the ordinary arm by default.
const openPreviousSession = vi.fn(() => Promise.resolve("opened"));
const openRunView = vi.fn();
const openChatTab = vi.fn();
const searchDispatch = vi.fn(async () => ({ matches: [], scanned: 0, truncated: false }));
const deleteChatDispatch = vi.fn(async () => ({ ok: true }));
const deleteRunDispatch = vi.fn(async () => ({ ok: true }));
const confirmMock = vi.fn(async () => true);
const closeTab = vi.fn();

vi.mock("./actions/chat.js", () => ({
  loadSessions: { dispatch, cancel: cancelSessions },
  deleteChat: { dispatch: deleteChatDispatch },
}));
// The row's delete reaches two actions and the confirm dialog. Stubbed for the
// same reason the search action is: unmocked they build through actions/index.js,
// which this suite reduces to one symbol.
vi.mock("./actions/runs.js", () => ({
  deleteRun: { dispatch: deleteRunDispatch },
}));
vi.mock("./confirm.js", () => ({ confirm: confirmMock }));
// The facts line joins each row against the chat record this client already
// holds, so the store is a real input to the row builder here.
const storeGet = vi.fn((_id: string): unknown => undefined);
vi.mock("./store.js", () => ({ get: storeGet }));
vi.mock("./roles.js", () => ({
  labelForMode: (id: string) => (id === "vibe" ? "Default" : id),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
}));
vi.mock("./chat.js", () => ({ openPreviousSession, openChatTab }));
// The cross-chat search action: unmocked it builds through actions/index.js,
// which this suite stubs down to one symbol. The search MODE has its own tests.
vi.mock("./actions/chat-search.js", () => ({
  searchChats: { dispatch: searchDispatch, cancel: vi.fn() },
}));
vi.mock("./run-view.js", () => ({ openRunView }));
// The toggle takes NO callback any more: `mount` and `teardown` are what the tab
// factory reaches through this module's own lazy-imported `loadHistoryView` /
// `teardownHistoryView`, so every door into the page gets one behaviour. It only
// has to resolve here; the suites below mount the page themselves.
const toggleHistoryView = vi.fn(() => Promise.resolve());
// `hasTab` is keyed by `(kind, ref)`: ids are opaque and server-minted, so a
// chat id is no longer a tab id and the predicate takes the subject instead.
const hasTab = vi.fn((_kind: string, _ref?: string) => false);
vi.mock("./tabs.js", () => ({ toggleHistoryView, hasTab }));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: () => ({ cancel: vi.fn() }),
}));
// The row's outcome glyph comes from tool-card.ts (the one writer of that
// vocabulary), which reaches the editor and scroll subgraphs. Stub the four
// leaves it needs so this suite keeps testing the real glyph without staging the
// whole app: mocking tool-card itself would test the mock.
const noop = (): void => {
  /* noop */
};
vi.mock("./editor-openers.js", () => ({ openFileDiff: noop }));
vi.mock("./navigate.js", () => ({ openChange: noop, openAtLine: noop }));
vi.mock("./scroll.js", () => ({
  setUserScrolledUp: noop,
  preserveReadingPosition: (fn: () => void) => {
    fn();
  },
}));
vi.mock("./tool-group.js", () => ({ trackInProgress: noop }));

const chatRow = {
  session_id: "sess_chat",
  title: "A conversation",
  status: "idle",
  description: "reading files",
  updated_at: 3000,
};
const ownedRow = {
  session_id: "sess_owned",
  title: "Already open",
  status: "idle",
  chat_id: "c-existing",
  updated_at: 2000,
};
const failedRow = {
  session_id: "sess_failed",
  title: "Went wrong",
  status: "failed",
  updated_at: 1000,
};
const runRow = {
  workflow_id: "wf_1",
  name: "feature-pipeline",
  status: "completed",
  updated_at: 2500,
};

/** A parentless run at the given status. */
const runAt = (status: string) => ({ ...runRow, status });

/** A row's accessible name, which lives on its open BUTTON. The row itself is a
 *  plain container: a role on it would flatten the delete button beside it out of
 *  the accessibility tree. */
const openName = (row: Element): string | null =>
  row.querySelector("button.list-row-name")?.getAttribute("aria-label") ?? null;

async function render(payload: unknown): Promise<HTMLElement> {
  document.body.innerHTML = `<div id="history-table"></div>`;
  dispatch.mockResolvedValue(payload);
  // `loadHistoryView` rather than `showHistoryView`: the latter toggles the TAB,
  // which is a round trip that paints nothing here, while the page's own loader is
  // what every door reaches through the tab factory's lazy import.
  const { loadHistoryView } = (await import(
    /* @vite-ignore */ `./history.ts?boot=${bootSeq}`
  )) as typeof ModHistory;
  loadHistoryView();
  await vi.waitFor(() => {
    if (document.querySelectorAll("#history-table [data-key]").length === 0) {
      throw new Error("not rendered");
    }
  });
  return document.getElementById("history-table")!;
}

describe("history: previous chats and runs", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
  });

  it("merges chats and runs into ONE list, newest first", async () => {
    const c = await render({ sessions: [chatRow, failedRow], runs: [runRow] });
    const keys = [...c.querySelectorAll("[data-key]")].map((r) => r.getAttribute("data-key"));
    // Interleaved by recency rather than segregated by kind: 3000, 2500, 1000.
    expect(keys).toEqual(["s:sess_chat", "r:wf_1", "s:sess_failed"]);
  });

  it("labels which rows are runs", async () => {
    const c = await render({ sessions: [chatRow], runs: [runRow] });
    const run = c.querySelector('[data-key="r:wf_1"]')!;
    const chat = c.querySelector('[data-key="s:sess_chat"]')!;
    expect(run.querySelector(".history-kind")?.textContent).toBe("Run");
    expect(chat.querySelector(".history-kind")?.textContent).toBe("Chat");
  });

  it("hides an idle status but shows a real one", async () => {
    // KAS reports `idle` for every settled session, so showing it would put a
    // meaningless badge on nearly every row. `failed` is worth surfacing.
    const c = await render({ sessions: [chatRow, failedRow], runs: [] });
    expect(c.querySelector('[data-key="s:sess_chat"] .history-status')).toBeNull();
    expect(c.querySelector('[data-key="s:sess_failed"] .history-status')?.textContent).toBe(
      "failed",
    );
  });

  it("opens an already-owned session as its existing chat", async () => {
    const c = await render({ sessions: [ownedRow], runs: [] });
    (c.querySelector('[data-key="s:sess_owned"]') as HTMLElement).click();
    expect(openPreviousSession).toHaveBeenCalledWith(
      expect.objectContaining({ chat_id: "c-existing" }),
    );
    expect(openRunView).not.toHaveBeenCalled();
  });

  it("drops the row by refreshing when the reopen says the chat is GONE", async () => {
    // Retention is off and a close deleted the conversation after this list was
    // fetched: openPreviousSession answers "gone" (it has already said so to the
    // reader), and the page re-fetches — the server-derived list is what drops
    // the dead row, so nothing here has to reach into the rendered set.
    openPreviousSession.mockResolvedValueOnce("gone");
    const c = await render({ sessions: [ownedRow], runs: [] });
    const before = dispatch.mock.calls.length;
    (c.querySelector('[data-key="s:sess_owned"]') as HTMLElement).click();
    await vi.waitFor(() => {
      expect(dispatch.mock.calls.length).toBeGreaterThan(before);
    });
  });

  it("leaves the list alone when the reopen merely failed", async () => {
    // A network failure is NOT the ephemeral face: the row may still be
    // perfectly live, so no refresh churns the list under the reader.
    openPreviousSession.mockResolvedValueOnce("failed");
    const c = await render({ sessions: [ownedRow], runs: [] });
    const before = dispatch.mock.calls.length;
    (c.querySelector('[data-key="s:sess_owned"]') as HTMLElement).click();
    await new Promise((r) => setTimeout(r, 0));
    expect(dispatch.mock.calls.length).toBe(before);
  });

  it("routes a run to the read-only run view, never to a chat", async () => {
    // Third and last argument: the parent chat to nest the run's tab under, empty
    // here because the fixture has no `parent_chat_id`. Whether the RUN is
    // PARENTLESS is deliberately not passed — that is the run's own fact and the
    // composition root resolves it from the run store, because a chat-parented run
    // reviewed while its chat's tab is closed has an empty subject Parent without
    // being parentless.
    const c = await render({ sessions: [chatRow], runs: [runRow] });
    (c.querySelector('[data-key="r:wf_1"]') as HTMLElement).click();
    expect(openRunView).toHaveBeenCalledWith("wf_1", "feature-pipeline", "");
    expect(openPreviousSession).not.toHaveBeenCalled();
  });

  it("hands over the launching chat so an agent-launched run nests under it", async () => {
    const parented = { ...runRow, parent_chat_id: "c-launcher" };
    const c = await render({ sessions: [], runs: [parented] });
    (c.querySelector('[data-key="r:wf_1"]') as HTMLElement).click();
    // The chat id is what makes the run's tab a sub-tab of the conversation that
    // started it; `openRunView` resolves it to a TAB id, because a chat id is no
    // longer one.
    expect(openRunView).toHaveBeenCalledWith("wf_1", "feature-pipeline", "c-launcher");
  });

  it("offers a Retry on load failure instead of an empty state", async () => {
    document.body.innerHTML = `<div id="history-table"></div>`;
    dispatch.mockResolvedValue(null);
    const { loadHistoryView } = (await import(
      /* @vite-ignore */ `./history.ts?boot=${bootSeq}`
    )) as typeof ModHistory;
    loadHistoryView();
    await vi.waitFor(() => {
      if (document.querySelector(".history-error") === null) {
        throw new Error("no error state");
      }
    });
    const c = document.getElementById("history-table")!;
    expect(c.querySelector(".list-empty")?.textContent).not.toContain("No previous sessions");
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    (c.querySelector("button") as HTMLElement).click();
    await vi.waitFor(() => {
      if (c.querySelector("[data-key]") === null) {
        throw new Error("retry did not re-fetch");
      }
    });
  });

  it("says so when the workspace has nothing", async () => {
    document.body.innerHTML = `<div id="history-table"></div>`;
    dispatch.mockResolvedValue({ sessions: [], runs: [] });
    const { loadHistoryView } = (await import(
      /* @vite-ignore */ `./history.ts?boot=${bootSeq}`
    )) as typeof ModHistory;
    loadHistoryView();
    await vi.waitFor(() => {
      const t = document.getElementById("history-table")?.textContent ?? "";
      if (!t.includes("No previous sessions")) {
        throw new Error("no empty state");
      }
    });
    const c = document.getElementById("history-table")!;
    expect(c.querySelectorAll("[data-key]")).toHaveLength(0);
    expect(c.textContent).toContain("No previous sessions in this workspace.");
  });
});

// ---------------------------------------------------------------------------
// A chat already open HERE is not history. The server only knows ownership (it
// tags such a session with `chat_id` rather than omitting it); "open here" is
// this device's localStorage, which is why the predicate is the client's and
// reuses the tab store's own hasTab rather than a second one.
// ---------------------------------------------------------------------------

describe("history: chats already open in a tab here", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
  });

  it("drops a tagged session whose chat is open here", async () => {
    hasTab.mockImplementation((_kind: string, ref?: string) => ref === "c-existing");
    const c = await render({ sessions: [chatRow, ownedRow], runs: [] });
    expect(c.querySelector('[data-key="s:sess_owned"]')).toBeNull();
    // Only that row goes. The rest of the list is untouched.
    expect(c.querySelector('[data-key="s:sess_chat"]')).not.toBeNull();
    expect(c.querySelectorAll("[data-key]")).toHaveLength(1);
    expect(hasTab).toHaveBeenCalledWith("chat", "c-existing");
  });

  it("keeps a tagged session whose chat is NOT open here", async () => {
    // Owned but closed is exactly the case this page exists for: reopening it.
    const c = await render({ sessions: [ownedRow], runs: [] });
    expect(c.querySelector('[data-key="s:sess_owned"]')).not.toBeNull();
  });

  it("never asks the tab store about an unowned session", async () => {
    // No `chat_id` means no vibekit chat owns it, so there is no tab it could be.
    const c = await render({ sessions: [chatRow], runs: [] });
    expect(c.querySelector('[data-key="s:sess_chat"]')).not.toBeNull();
    expect(hasTab).not.toHaveBeenCalled();
  });

  it("leaves runs alone — a run is not a chat and owns no tab", async () => {
    hasTab.mockReturnValue(true);
    const c = await render({ sessions: [], runs: [runRow] });
    expect(c.querySelector('[data-key="r:wf_1"]')).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// A run's outcome is a GLYPH, painted through tool-card.ts's applyOutcome (the
// one writer of that vocabulary). Exhaustive over RUN_STATUSES, plus the two junk
// values a `status?: string` wire field carries.
// ---------------------------------------------------------------------------

describe("history: a run's outcome is a glyph, not a word", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
  });

  /** The markup a mark renders as, named off the shared set rather than inlined. */
  const markup = (svg: string): string => (iconEl(svg) as HTMLElement).outerHTML;

  // `mark` is null where the row KEEPS its identity glyph (the run glyph, tinted
  // green) and the shared silhouette otherwise, which is the ratified rule: one
  // mark per row, and its SHAPE changes for a non-success state.
  const settled = [
    { status: "completed", cls: "is-ok", mark: null, word: "succeeded" },
    { status: "failed", cls: "is-fail", mark: outcomeIcon("fail"), word: "failed" },
    { status: "aborted", cls: "is-warn", mark: outcomeIcon("warn"), word: "aborted" },
  ] as const;

  for (const s of settled) {
    it(`paints ${s.status} as one tinted mark, never the word`, async () => {
      const c = await render({ sessions: [], runs: [runAt(s.status)] });
      const row = c.querySelector('[data-key="r:wf_1"]')!;
      const icon = row.querySelector(".tool-icon")!;
      expect(icon.classList.contains(s.cls)).toBe(true);
      // ONE mark, whichever state: never a glyph plus a badge beside it.
      expect(icon.querySelectorAll("svg")).toHaveLength(1);
      // Tint alone is one channel and fails WCAG 1.4.1, so the SHAPE is not
      // optional — and this row's identity glyph is ICON_TAB_RUN rather than a
      // toolIcon, which is why the writer captures it instead of recomputing it.
      expect(icon.querySelector("svg")!.outerHTML).toBe(markup(s.mark ?? ICON_TAB_RUN));
      // The word is in the accessible name and nowhere else, and the row's own
      // "Open X" label survives in front of it.
      expect(openName(row)).toBe(`Open feature-pipeline, ${s.word}`);
      expect(row.textContent).not.toContain(s.word);
      expect(row.textContent).not.toContain(s.status);
      // The glyph REPLACES the status slot rather than joining it.
      expect(row.querySelector(".history-status")).toBeNull();
    });
  }

  for (const status of ["running", "paused"] as const) {
    it(`leaves a ${status} run its status slot and gives it no glyph`, async () => {
      const c = await render({ sessions: [], runs: [runAt(status)] });
      const row = c.querySelector('[data-key="r:wf_1"]')!;
      expect(row.querySelector(".tool-icon")).toBeNull();
      expect(row.querySelector(".history-status")?.textContent).toBe(status);
      expect(openName(row)).toBe("Open feature-pipeline");
    });
  }

  it("guesses no verdict from a status it does not know", async () => {
    // `status` is a plain string on the wire, so an unrecognised value is
    // reachable. It must fall through to the status word, not to a green check.
    const c = await render({ sessions: [], runs: [runAt("quiesced")] });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.querySelector(".tool-icon")).toBeNull();
    expect(row.querySelector(".history-status")?.textContent).toBe("quiesced");
  });

  it("shows neither glyph nor status when the run reports no status at all", async () => {
    const c = await render({ sessions: [], runs: [runAt("")] });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.querySelector(".tool-icon")).toBeNull();
    expect(row.querySelector(".history-status")).toBeNull();
  });

  // OVERTURNED. This used to assert the glyph was scoped to a PARENTLESS run, on
  // "an agent-parented run's outcome is the agent's to handle". The server lists
  // these rows now precisely because that is false once the launching chat's
  // transcript is closed or evicted — retry is offered for them, and this page is
  // the only door left — so a blank outcome would leave the reader no reason to
  // open the one door there is.
  it("states the verdict on an agent-parented run too", async () => {
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", parent_chat_id: "c-owner" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.querySelector(".tool-icon")).not.toBeNull();
    // A verdict takes the status slot's place, as it does for a parentless row.
    expect(row.querySelector(".history-status")).toBeNull();
    expect(openName(row)).toBe("Open feature-pipeline, aborted");
  });

  it("gives a chat row no outcome glyph", async () => {
    const c = await render({ sessions: [failedRow], runs: [] });
    const row = c.querySelector('[data-key="s:sess_failed"]')!;
    expect(row.querySelector(".tool-icon")).toBeNull();
    expect(row.querySelector(".history-status")?.textContent).toBe("failed");
  });
});

// ---------------------------------------------------------------------------
// A run one of vibekit's own bounds stopped (D56c). Both bounds terminate through
// the same cancel a person uses, and KAS's status vocabulary has no "cancelled",
// so an overrun and a click both land on `aborted` — the row cannot tell them
// apart from the status alone, which is the whole reason `end_reason` exists.
// ---------------------------------------------------------------------------

describe("history: an overrun reads differently from a cancel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
  });

  it("says a wall-clock overrun stopped the run", async () => {
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", end_reason: "overran" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.textContent).toContain("ran past its time limit");
  });

  it("says a step's turn cap stopped the run", async () => {
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", end_reason: "step_cap" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.textContent).toContain("a step ran past its turn limit");
  });

  it("says a restart interrupted the run", async () => {
    // The fourth fact, and the reason it needs its own value: a run cut off by a
    // restart stopped mid-step through the same cancel a person uses, so without
    // the sentence the reader is left inferring it from a run that simply stops.
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", end_reason: "orphaned" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.textContent).toContain("the server restarted while it was running");
    expect(openName(row)).toBe("Open feature-pipeline, aborted");
  });

  it("says nothing extra for a user cancel, which is the same status", async () => {
    // The distinguisher is the ABSENCE of a reason. If this row grew a sentence,
    // the field would be describing every abort rather than the two vibekit
    // caused.
    const c = await render({ sessions: [], runs: [runAt("aborted")] });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.textContent).not.toContain("ran past");
    expect(row.querySelector(".list-row-summary")).toBeNull();
  });

  it("settles a run the terminal frame has not caught up with yet", async () => {
    // A bound cancels at a node boundary, so KAS can still report `running` for a
    // run vibekit already stopped. The reason outranks the status, or the row
    // reads "running" forever.
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "running", end_reason: "overran" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.querySelector(".tool-icon")).not.toBeNull();
    expect(row.querySelector(".history-status")).toBeNull();
    expect(openName(row)).toBe("Open feature-pipeline, aborted");
  });

  it("states the reason on an agent-parented run too", async () => {
    // This sentence reports what VIBEKIT did to the run, so it was always stated
    // whatever launched it. The glyph now is too (see "states the verdict on an
    // agent-parented run too"), so the row carries both.
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", parent_chat_id: "c-owner", end_reason: "overran" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.querySelector(".tool-icon")).not.toBeNull();
    expect(row.textContent).toContain("ran past its time limit");
  });

  it("ignores an end reason it does not recognise", async () => {
    // `end_reason` is a plain string on the wire. An unknown value must not print
    // a raw enum at the reader, and must not repaint a COMPLETED run as aborted
    // with nothing on the row to explain why: one vocabulary decides both.
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "completed", end_reason: "quiesced" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.textContent).not.toContain("quiesced");
    expect(openName(row)).toBe("Open feature-pipeline, succeeded");
  });
});

// ---------------------------------------------------------------------------
// The tab-restore path. It needs a plain loader because the toggle-style opener
// would CLOSE the already-active tab it was fired from.
// ---------------------------------------------------------------------------

describe("history: the tab-restore loader", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
  });

  async function restore(): Promise<HTMLElement> {
    document.body.innerHTML = `<div id="history-table"></div>`;
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    const { loadHistoryView } = (await import(
      /* @vite-ignore */ `./history.ts?boot=${bootSeq}`
    )) as typeof ModHistory;
    loadHistoryView();
    await vi.waitFor(() => {
      if (document.querySelectorAll("#history-table [data-key]").length === 0) {
        throw new Error("not rendered");
      }
    });
    return document.getElementById("history-table")!;
  }

  it("fills the page without going through the tab toggle", async () => {
    const c = await restore();
    expect(c.querySelectorAll("[data-key]")).toHaveLength(1);
    // The toggle is the thing that would have closed a restored, active tab.
    expect(toggleHistoryView).not.toHaveBeenCalled();
  });

  it("is a reload when fired again, never a close", async () => {
    const c = await restore();
    const { loadHistoryView } = (await import(
      /* @vite-ignore */ `./history.ts?boot=${bootSeq}`
    )) as typeof ModHistory;
    loadHistoryView();
    await vi.waitFor(() => {
      if (dispatch.mock.calls.length < 2) {
        throw new Error("not reloaded");
      }
    });
    expect(toggleHistoryView).not.toHaveBeenCalled();
    expect(c.querySelectorAll("[data-key]")).toHaveLength(1);
  });

  it("tears the page's in-flight work down on close", async () => {
    await restore();
    const { teardownHistoryView } = (await import(
      /* @vite-ignore */ `./history.ts?boot=${bootSeq}`
    )) as typeof ModHistory;
    cancelSessions.mockClear();
    teardownHistoryView();
    expect(cancelSessions).toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Cross-chat search: a second MODE over the same container, not a filter of the
// loaded list.
// ---------------------------------------------------------------------------

const match = (over: Record<string, unknown> = {}) => ({
  id: "c-redis",
  name: "Redis migration",
  best: {
    message_id: "m1",
    turn_message_id: "m1",
    excerpt: "we moved the cache to redis",
    role: "user",
    turn: 1,
    offset: 0,
  },
  hits: 3,
  score: 12,
  updated_at: 5000,
  ...over,
});

/** The page's find, reached the way Ctrl-F and the toolbar magnifier reach it. */
async function openFind(): Promise<PageFind> {
  const { pageFind } = await import("./find-registry.js");
  const find = pageFind("history");
  if (find === undefined) {
    throw new Error("the history page registered no find");
  }
  // A POPUP: nothing is built until it opens, so there is no field to type into
  // before this call.
  find.open();
  return find;
}

/** Mount the view, open the search popup, then type a query. */
async function search(
  result: unknown,
  query = "redis",
): Promise<{ table: HTMLElement; note: HTMLElement; find: PageFind }> {
  // No host markup at all now: the box is the shared page popup
  // (search-popup.ts over search-shell.ts), mounted into the view itself and
  // positioned against the viewport, which is what stops this box, the
  // transcript's and the file browser's from drifting apart again.
  document.body.innerHTML = `<div id="history-view"><div id="history-table"></div></div>`;
  dispatch.mockResolvedValue({ sessions: [], runs: [] });
  searchDispatch.mockResolvedValue(result as never);
  // `loadHistoryView` rather than `showHistoryView`: the latter toggles the TAB,
  // which is a round trip that paints nothing here, while the page's own loader is
  // what every door reaches through the tab factory's lazy import.
  const { loadHistoryView } = (await import(
    /* @vite-ignore */ `./history.ts?boot=${bootSeq}`
  )) as typeof ModHistory;
  loadHistoryView();
  const find = await openFind();

  const input = document.getElementById("hist-search-input") as HTMLInputElement;
  input.value = query;
  input.dispatchEvent(new Event("input"));
  await vi.waitFor(() => {
    if (searchDispatch.mock.calls.length === 0) {
      throw new Error("not searched");
    }
  });
  await vi.waitFor(() => {
    if ((document.getElementById("hist-search-note")?.textContent ?? "") === "") {
      throw new Error("no note yet");
    }
  });
  return {
    table: document.getElementById("history-table")!,
    note: document.getElementById("hist-search-note")!,
    find,
  };
}

describe("history: cross-chat search", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
  });

  it("renders matching CHATS with their best line", async () => {
    const { table } = await search({ matches: [match()], scanned: 12, truncated: false });
    const row = table.querySelector("[data-search-chat]");
    expect(row?.getAttribute("data-search-chat")).toBe("c-redis");
    expect(row?.textContent).toContain("Redis migration");
    expect(row?.textContent).toContain("we moved the cache to redis");
    expect(row?.textContent).toContain("3 matches");
  });

  // A title-only match has no line to quote, and must not render an empty row
  // that looks like a rendering bug.
  it("explains a title-only match instead of showing an empty excerpt", async () => {
    const titleOnly = match({
      hits: 0,
      best: { message_id: "", turn_message_id: "", excerpt: "", role: "", turn: 0, offset: 0 },
    });
    const { table } = await search({ matches: [titleOnly], scanned: 4, truncated: false });
    expect(table.textContent).toContain("matches the conversation name");
  });

  it("opens the matched chat on click", async () => {
    const { table } = await search({ matches: [match()], scanned: 3, truncated: false });
    table.querySelector<HTMLElement>("[data-search-chat]")!.click();
    expect(openChatTab).toHaveBeenCalledWith("c-redis", "Redis migration");
    // Search results are chats that already exist; adopting a session is the
    // OTHER door and must not fire here.
    expect(openPreviousSession).not.toHaveBeenCalled();
  });

  // The honest-empty-state rule: without saying the scan was capped, "no
  // matches" implies the text is nowhere.
  it("says older conversations were not searched when the scan truncated", async () => {
    const { note } = await search({ matches: [], scanned: 500, truncated: true });
    expect(note.textContent).toContain("were not searched");
  });

  it("reports the scanned count on a clean empty result", async () => {
    const { note } = await search({ matches: [], scanned: 7, truncated: false });
    expect(note.textContent).toContain("7 conversations");
    expect(note.textContent).not.toContain("were not searched");
  });

  it("surfaces a failed search rather than an empty list", async () => {
    const { note } = await search(null);
    expect(note.textContent).toContain("Search failed");
  });

  it("is a role=search landmark carrying the shared field attributes", async () => {
    // The box used to be hand-authored markup with no role at all, so it was the
    // one search on the page not reachable by landmark navigation.
    await search({ matches: [match()], scanned: 3, truncated: false });
    const region = document.getElementById("hist-search");
    expect(region?.getAttribute("role")).toBe("search");
    expect(region?.getAttribute("aria-label")).toBe("Search conversations");
    // The same popup the transcript's find is, by class as well as by behaviour.
    expect(region?.className).toContain("page-find");
    expect(region?.className).toContain("search-pop");
    expect(region?.className).toContain("uip-popup");
    const input = document.getElementById("hist-search-input") as HTMLInputElement;
    expect(input.getAttribute("autocomplete")).toBe("off");
    expect(input.getAttribute("enterkeyhint")).toBe("search");
    expect(input.placeholder, "the one string the page boxes genuinely differ on").toBe(
      "Search conversations\u2026",
    );
  });

  it("ships NO match-case toggle, because the endpoint would not read it", async () => {
    // GET /api/chats/search takes only `q`. chat.searchOneChat states why —
    // "Case-INSENSITIVE, always ... a cross-chat 'which conversation was that in'
    // is asked from memory, and memory does not remember capitalisation" — and
    // titleHits folds unconditionally. A toggle here would be wired to nothing,
    // which is worse than its absence.
    await search({ matches: [match()], scanned: 3, truncated: false });
    expect(document.querySelector('#hist-search [aria-label="Match case"]')).toBeNull();
    // And the query carries nothing but the text, so there is no flag to be
    // silently dropped on the way to a server that would ignore it.
    expect(searchDispatch).toHaveBeenLastCalledWith("redis");
  });

  it("carries the MAGNIFIER, because it reaches past what is on screen", async () => {
    // The server reads every chat file on disk, so this box finds conversations
    // the loaded list does not contain. A funnel would promise it only narrows
    // what is here, which is what the docs and git boxes DO promise — same
    // component, the other glyph.
    await search({ matches: [match()], scanned: 3, truncated: false });
    expect(document.querySelector("#hist-search .page-find-icon circle")).not.toBeNull();
    expect(document.querySelector("#hist-search .page-find-icon polygon")).toBeNull();
    // And the × says which of the two it is closing.
    expect(document.querySelector('#hist-search [aria-label="Close search"]')).not.toBeNull();
  });

  it("closes on Escape, and the close is what returns the full list", async () => {
    const { table, find } = await search({ matches: [match()], scanned: 3, truncated: false });
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    const input = document.getElementById("hist-search-input") as HTMLInputElement;
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    // The popup's leave lifecycle hides the panel on a transitionend (or its
    // 400ms fallback), and focus only leaves the field once it does — a real
    // browser does not move focus on the same tick the key was handled.
    await vi.waitFor(() => {
      expect(find.focused()).toBe(false);
    });
    expect(input.value).toBe("");
    await vi.waitFor(() => {
      if (table.querySelectorAll("[data-key]").length === 0) {
        throw new Error("list not restored");
      }
    });
    expect(table.querySelector("[data-search-chat]")).toBeNull();
  });

  it("returns to the full list when the box is cleared", async () => {
    const { table } = await search({ matches: [match()], scanned: 3, truncated: false });
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });

    const input = document.getElementById("hist-search-input") as HTMLInputElement;
    input.value = "";
    input.dispatchEvent(new Event("input"));
    await vi.waitFor(() => {
      if (table.querySelectorAll("[data-key]").length === 0) {
        throw new Error("list not restored");
      }
    });
    expect(table.querySelector("[data-search-chat]")).toBeNull();
  });
});

describe("history: the per-row delete", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
    confirmMock.mockResolvedValue(true);
    deleteChatDispatch.mockResolvedValue({ ok: true });
    deleteRunDispatch.mockResolvedValue({ ok: true });
  });

  it("gives every row a delete button, chats and runs alike", async () => {
    const c = await render({ sessions: [ownedRow], runs: [runRow] });
    for (const key of ["s:sess_owned", "r:wf_1"]) {
      const btn = c.querySelector(`[data-key="${key}"] [data-history-delete]`);
      expect(btn, `${key} has no delete button`).not.toBeNull();
      // Named for the row, so a screen reader announces which thing it removes.
      expect(btn?.getAttribute("aria-label")).toMatch(/^Delete /);
    }
  });

  it("keeps the delete button out of a control, so it is not nested in one", async () => {
    // A role="button" on the row is Children-Presentational: it flattens this
    // button out of the accessibility tree, which axe reports as
    // nested-interactive on every row. The open control is a real button beside
    // it instead, and the platform gives that one Enter and Space — which the
    // row's role never had, because nothing ever added the key handler it needs.
    const c = await render({ sessions: [ownedRow], runs: [runRow] });
    for (const key of ["s:sess_owned", "r:wf_1"]) {
      const row = c.querySelector<HTMLElement>(`[data-key="${key}"]`)!;
      expect(row.getAttribute("role"), `${key} row is a control`).toBeNull();
      expect(row.getAttribute("tabindex"), `${key} row is focusable`).toBeNull();
      const open = row.querySelector("button.list-row-name");
      expect(open, `${key} has no open button`).not.toBeNull();
      expect(open?.getAttribute("aria-label")).toMatch(/^Open /);
      // Nothing else in the row may be interactive: two controls, no nesting.
      const controls = [...row.querySelectorAll("button, [role='button'], [tabindex]")];
      expect(controls).toHaveLength(2);
    }
  });

  it("deletes a conversation through the chat-delete command and closes no tab itself", async () => {
    const c = await render({ sessions: [ownedRow], runs: [] });
    (c.querySelector("[data-history-delete]") as HTMLElement).click();
    await vi.waitFor(() => {
      expect(deleteChatDispatch).toHaveBeenCalledWith("c-existing");
    });
    // delete_chat is the ONE path that also reaps the chat's KAS session chain,
    // which is what makes this delete reach the underlying files. The TAB is the
    // membership coordinator's: it closes every tab for a deleted chat under the
    // same lock that removed the record and emits the removal, so a `close_tab`
    // from here would be a second close for a tab the server has already dropped.
    expect(closeTab).not.toHaveBeenCalled();
    expect(deleteRunDispatch).not.toHaveBeenCalled();
  });

  it("deletes a run through the run-delete endpoint", async () => {
    const c = await render({ sessions: [], runs: [runRow] });
    (c.querySelector("[data-history-delete]") as HTMLElement).click();
    await vi.waitFor(() => {
      expect(deleteRunDispatch).toHaveBeenCalledWith("wf_1");
    });
    expect(deleteChatDispatch).not.toHaveBeenCalled();
    // A run is not a tab, so nothing closes.
    expect(closeTab).not.toHaveBeenCalled();
  });

  it("does not ALSO open the row it is deleting", async () => {
    // The button sits inside the row, so its click reaches the container's own
    // delegated handler too. Without the guard the confirm dialog would open
    // over a chat that had just been activated behind it.
    const c = await render({ sessions: [ownedRow], runs: [] });
    (c.querySelector("[data-history-delete]") as HTMLElement).click();
    await vi.waitFor(() => {
      expect(deleteChatDispatch).toHaveBeenCalled();
    });
    expect(openPreviousSession).not.toHaveBeenCalled();
    expect(openRunView).not.toHaveBeenCalled();
  });

  it("deletes nothing when the confirm is declined", async () => {
    confirmMock.mockResolvedValue(false);
    const c = await render({ sessions: [ownedRow], runs: [runRow] });
    (c.querySelector('[data-key="s:sess_owned"] [data-history-delete]') as HTMLElement).click();
    (c.querySelector('[data-key="r:wf_1"] [data-history-delete]') as HTMLElement).click();
    await vi.waitFor(() => {
      expect(confirmMock).toHaveBeenCalledTimes(2);
    });
    expect(deleteChatDispatch).not.toHaveBeenCalled();
    expect(deleteRunDispatch).not.toHaveBeenCalled();
    expect(closeTab).not.toHaveBeenCalled();
  });

  it("leaves the row in place when the delete failed", async () => {
    // The action returns null on a non-2xx, and the row's own subject is what
    // survives: a failed delete must not refresh the list, because a refresh is
    // what would take the row away for a chat the server still holds. The TAB is
    // not this module's concern any more — the server closes tabs for a chat it
    // actually deleted.
    deleteChatDispatch.mockResolvedValue(null as never);
    const c = await render({ sessions: [ownedRow], runs: [] });
    const before = dispatch.mock.calls.length;
    (c.querySelector("[data-history-delete]") as HTMLElement).click();
    await vi.waitFor(() => {
      expect(deleteChatDispatch).toHaveBeenCalled();
    });
    expect(c.querySelector('[data-key="s:sess_owned"]')).not.toBeNull();
    expect(dispatch.mock.calls).toHaveLength(before);
    expect(closeTab).not.toHaveBeenCalled();
  });
});

describe("history: the row's facts line", () => {
  function header(over: Record<string, unknown> = {}): unknown {
    return {
      id: "c-existing",
      model: "claude-opus-5",
      current_mode_id: "vibe",
      message_count: 34,
      usage: { turn_count: 17, credits: 12.5, context_pct: 0, context_size: 0 },
      ...over,
    };
  }

  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    bootSeq++;
    hasTab.mockReturnValue(false);
    storeGet.mockReturnValue(undefined);
  });

  it("states model, mode, turns, messages and credits from the chat record", async () => {
    // Every field comes from /api/chats, which the store already holds, so the
    // richer row costs no request and no new field on /api/sessions — KAS's
    // session row carries none of this.
    storeGet.mockReturnValue(header());
    const c = await render({ sessions: [ownedRow], runs: [] });
    const facts = c.querySelector('[data-key="s:sess_owned"] .history-facts')?.textContent;
    expect(facts).toBe("claude-opus-5 · Default · 17 turns · 34 msg · 12.50 cr");
  });

  it("omits a zero credit total rather than printing 0.00", async () => {
    // An unmetered chat would otherwise carry a cost column that says nothing on
    // every row.
    storeGet.mockReturnValue(header({ usage: { turn_count: 1, credits: 0 } }));
    const c = await render({ sessions: [ownedRow], runs: [] });
    const facts = c.querySelector('[data-key="s:sess_owned"] .history-facts')?.textContent;
    expect(facts).toBe("claude-opus-5 · Default · 1 turn · 34 msg");
  });

  it("renders NO facts line for a chat the store does not know", async () => {
    // The alternative is placeholders, and "unknown model · 0 turns" reads as
    // fact. A row with nothing to say keeps its old height instead.
    storeGet.mockReturnValue(undefined);
    const c = await render({ sessions: [ownedRow], runs: [] });
    expect(c.querySelector('[data-key="s:sess_owned"] .history-facts')).toBeNull();
  });

  it("states a run's duration, and nothing when it never started", async () => {
    const c = await render({
      sessions: [],
      runs: [
        {
          workflow_id: "wf_timed",
          name: "nightly",
          status: "completed",
          started_at: 1000,
          updated_at: 901000,
        },
        { workflow_id: "wf_untimed", name: "never-ran", status: "failed", updated_at: 2000 },
      ],
    });
    expect(c.querySelector('[data-key="r:wf_timed"] .history-facts')?.textContent).toBe("15m");
    expect(c.querySelector('[data-key="r:wf_untimed"] .history-facts')).toBeNull();
  });
});
