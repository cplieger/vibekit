// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

const dispatch = vi.fn();
const cancelSessions = vi.fn();
const openPreviousSession = vi.fn();
const openRunView = vi.fn();
const openChatTab = vi.fn();
const searchDispatch = vi.fn(async () => ({ matches: [], scanned: 0, truncated: false }));

vi.mock("./actions/chat.js", () => ({
  loadSessions: { dispatch, cancel: cancelSessions },
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
const toggleHistoryView = vi.fn((onShow: () => void) => {
  onShow();
});
const hasTab = vi.fn((_id: string) => false);
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

/** A parentless run at the given status; parentless is what scopes the glyph. */
const runAt = (status: string) => ({ ...runRow, status });

async function render(payload: unknown): Promise<HTMLElement> {
  document.body.innerHTML = `<div id="history-table"></div>`;
  dispatch.mockResolvedValue(payload);
  const { showHistoryView } = await import("./history.js");
  showHistoryView();
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
    hasTab.mockReturnValue(false);
    toggleHistoryView.mockImplementation((onShow: () => void) => {
      onShow();
    });
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

  it("routes a run to the read-only run view, never to a chat", async () => {
    const c = await render({ sessions: [chatRow], runs: [runRow] });
    (c.querySelector('[data-key="r:wf_1"]') as HTMLElement).click();
    // The third argument is the parentless flag: this fixture has no
    // parent_chat_id, so its page may offer Retry.
    expect(openRunView).toHaveBeenCalledWith("wf_1", "feature-pipeline", true);
    expect(openPreviousSession).not.toHaveBeenCalled();
  });

  it("offers a Retry on load failure instead of an empty state", async () => {
    document.body.innerHTML = `<div id="history-table"></div>`;
    dispatch.mockResolvedValue(null);
    const { showHistoryView } = await import("./history.js");
    showHistoryView();
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
    const { showHistoryView } = await import("./history.js");
    showHistoryView();
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
    hasTab.mockReturnValue(false);
    toggleHistoryView.mockImplementation((onShow: () => void) => {
      onShow();
    });
  });

  it("drops a tagged session whose chat is open here", async () => {
    hasTab.mockImplementation((id: string) => id === "c-existing");
    const c = await render({ sessions: [chatRow, ownedRow], runs: [] });
    expect(c.querySelector('[data-key="s:sess_owned"]')).toBeNull();
    // Only that row goes. The rest of the list is untouched.
    expect(c.querySelector('[data-key="s:sess_chat"]')).not.toBeNull();
    expect(c.querySelectorAll("[data-key]")).toHaveLength(1);
    expect(hasTab).toHaveBeenCalledWith("c-existing");
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
// A parentless run's outcome is a GLYPH, painted through tool-card.ts's
// applyOutcome (the one writer of that vocabulary). Exhaustive over
// RUN_STATUSES, plus the two junk values a `status?: string` wire field carries.
// ---------------------------------------------------------------------------

describe("history: a run's outcome is a glyph, not a word", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    hasTab.mockReturnValue(false);
    toggleHistoryView.mockImplementation((onShow: () => void) => {
      onShow();
    });
  });

  const settled = [
    { status: "completed", cls: "is-ok", badge: "\u2713", word: "succeeded" },
    { status: "failed", cls: "is-fail", badge: "\u2717", word: "failed" },
    { status: "aborted", cls: "is-warn", badge: "\u26A0", word: "aborted" },
  ] as const;

  for (const s of settled) {
    it(`paints ${s.status} as a tinted glyph plus a shape, never the word`, async () => {
      const c = await render({ sessions: [], runs: [runAt(s.status)] });
      const row = c.querySelector('[data-key="r:wf_1"]')!;
      const icon = row.querySelector(".tool-icon")!;
      expect(icon.classList.contains(s.cls)).toBe(true);
      // Tint alone is one channel and fails WCAG 1.4.1, so the shape is not
      // optional: check / cross / warning triangle.
      expect(icon.querySelector(".tool-outcome-badge")?.textContent).toBe(s.badge);
      // The word is in the accessible name and nowhere else, and the row's own
      // "Open X" label survives in front of it.
      expect(row.getAttribute("aria-label")).toBe(`Open feature-pipeline, ${s.word}`);
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
      expect(row.getAttribute("aria-label")).toBe("Open feature-pipeline");
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

  it("scopes the glyph to a PARENTLESS run", async () => {
    // An agent-parented run's outcome is the agent's to handle, so this page
    // states no verdict on it — the same scoping the word had.
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, parent_chat_id: "c-owner" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.querySelector(".tool-icon")).toBeNull();
    expect(row.querySelector(".history-status")?.textContent).toBe("completed");
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
    hasTab.mockReturnValue(false);
    toggleHistoryView.mockImplementation((onShow: () => void) => {
      onShow();
    });
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
    expect(row.getAttribute("aria-label")).toBe("Open feature-pipeline, aborted");
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
    expect(row.getAttribute("aria-label")).toBe("Open feature-pipeline, aborted");
  });

  it("states the reason on an agent-parented run too", async () => {
    // The verdict is withheld from an agent-parented row (its recovery is the
    // agent's), but this sentence reports what VIBEKIT did to the run, and hiding
    // the app's own action from the only reader who can see it would be worse.
    const c = await render({
      sessions: [],
      runs: [{ ...runRow, status: "aborted", parent_chat_id: "c-owner", end_reason: "overran" }],
    });
    const row = c.querySelector('[data-key="r:wf_1"]')!;
    expect(row.querySelector(".tool-icon")).toBeNull();
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
    expect(row.getAttribute("aria-label")).toBe("Open feature-pipeline, succeeded");
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
    hasTab.mockReturnValue(false);
    toggleHistoryView.mockImplementation((onShow: () => void) => {
      onShow();
    });
  });

  async function restore(): Promise<HTMLElement> {
    document.body.innerHTML = `<div id="history-table"></div>`;
    dispatch.mockResolvedValue({ sessions: [chatRow], runs: [] });
    const { loadHistoryView } = await import("./history.js");
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
    const { loadHistoryView } = await import("./history.js");
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
    const { teardownHistoryView } = await import("./history.js");
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

/** Mount the view with a search box present, then type a query. */
async function search(
  result: unknown,
  query = "redis",
): Promise<{ table: HTMLElement; note: HTMLElement }> {
  document.body.innerHTML = `<div id="history-table"></div>
    <input id="hist-search-input" /><div id="hist-search-note"></div>`;
  dispatch.mockResolvedValue({ sessions: [], runs: [] });
  searchDispatch.mockResolvedValue(result as never);
  const { showHistoryView } = await import("./history.js");
  showHistoryView();

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
  };
}

describe("history: cross-chat search", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
    hasTab.mockReturnValue(false);
    toggleHistoryView.mockImplementation((onShow: () => void) => {
      onShow();
    });
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
