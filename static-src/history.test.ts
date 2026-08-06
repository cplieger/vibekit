// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

const dispatch = vi.fn();
const openPreviousSession = vi.fn();
const openRunView = vi.fn();
const openChatTab = vi.fn();
const searchDispatch = vi.fn(async () => ({ matches: [], scanned: 0, truncated: false }));

vi.mock("./actions/chat.js", () => ({
  loadSessions: { dispatch, cancel: vi.fn() },
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
vi.mock("./tabs.js", () => ({
  toggleHistoryView: (onShow: () => void) => {
    onShow();
  },
}));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: () => ({ cancel: vi.fn() }),
}));

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
