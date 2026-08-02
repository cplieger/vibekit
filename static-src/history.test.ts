// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

const dispatch = vi.fn();
const openPreviousSession = vi.fn();
const openRunView = vi.fn();

vi.mock("./actions/chat.js", () => ({
  loadSessions: { dispatch, cancel: vi.fn() },
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
}));
vi.mock("./chat.js", () => ({ openPreviousSession }));
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
    expect(openRunView).toHaveBeenCalledWith("wf_1", "feature-pipeline");
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
