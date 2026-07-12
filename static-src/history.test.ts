// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for history.ts: the full-page archived-chats table. The chat/tabs/
// export/confirm modules and the load/delete actions are mocked so the wiring
// (confirm-before-delete, keyboard restore, error-retry, loading skeleton) is
// deterministic against happy-dom.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

const h = vi.hoisted(() => ({
  mockConfirm: vi.fn(),
  mockRestore: vi.fn(),
  mockDownload: vi.fn(),
  mockDeleteDispatch: vi.fn(),
  mockLoadDispatch: vi.fn(),
  mockLoadCancel: vi.fn(),
}));

vi.mock("./chat.js", () => ({
  restoreArchivedChat: (...a: unknown[]) => h.mockRestore(...a),
}));
vi.mock("./tabs.js", () => ({
  toggleHistoryView: (onShow: () => void) => {
    onShow();
  },
}));
vi.mock("./chat-export.js", () => ({
  downloadChatExport: (...a: unknown[]) => h.mockDownload(...a),
}));
vi.mock("./confirm.js", () => ({
  confirm: (...a: unknown[]) => h.mockConfirm(...a),
}));
vi.mock("./actions/chat.js", () => ({
  deleteArchivedChat: { dispatch: (...a: unknown[]) => h.mockDeleteDispatch(...a) },
  loadHistory: {
    dispatch: (...a: unknown[]) => h.mockLoadDispatch(...a),
    cancel: () => h.mockLoadCancel(),
  },
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { showHistoryView } = await import("./history.js");

async function flush(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

function table(): HTMLElement {
  const t = document.getElementById("history-table");
  if (t === null) {
    throw new Error("missing #history-table");
  }
  return t;
}

function seedDom(): void {
  document.body.innerHTML = `
    <div id="history-table" class="list-container">
      <div class="list-empty">No archived chats.</div>
    </div>`;
}

function chat(id: string, name: string): { id: string; name: string; updated_at: number } {
  return { id, name, updated_at: 1_700_000_000_000 };
}

beforeEach(() => {
  vi.clearAllMocks();
  seedDom();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("history table", () => {
  it("renders archived chats with keyboard-operable restore titles", async () => {
    h.mockLoadDispatch.mockResolvedValue({ chats: [chat("c1", "First"), chat("c2", "Second")] });
    showHistoryView();
    await flush();

    expect(table().querySelectorAll("[data-chat-id]").length).toBe(2);
    const title = table().querySelector<HTMLElement>('[data-action="restore"]');
    expect(title?.getAttribute("role")).toBe("button");
    expect(title?.getAttribute("tabindex")).toBe("0");
    expect(title?.getAttribute("aria-label")).toBe("Restore First");
  });

  it("restores a chat when Enter is pressed on the title", async () => {
    h.mockLoadDispatch.mockResolvedValue({ chats: [chat("c1", "First")] });
    showHistoryView();
    await flush();

    const title = table().querySelector<HTMLElement>('[data-action="restore"]');
    title?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await flush();
    expect(h.mockRestore).toHaveBeenCalledWith("c1");
  });

  it("does not delete when the confirm is dismissed", async () => {
    h.mockLoadDispatch.mockResolvedValue({ chats: [chat("c1", "First")] });
    h.mockConfirm.mockResolvedValue(false);
    showHistoryView();
    await flush();

    table().querySelector<HTMLButtonElement>('[data-action="delete"]')?.click();
    await flush();

    expect(h.mockConfirm).toHaveBeenCalled();
    expect(h.mockDeleteDispatch).not.toHaveBeenCalled();
    expect(table().querySelector('[data-chat-id="c1"]')).not.toBeNull();
  });

  it("deletes permanently once the destructive confirm is accepted", async () => {
    h.mockLoadDispatch.mockResolvedValue({ chats: [chat("c1", "First")] });
    h.mockConfirm.mockResolvedValue(true);
    showHistoryView();
    await flush();

    table().querySelector<HTMLButtonElement>('[data-action="delete"]')?.click();
    await flush();

    expect(h.mockConfirm).toHaveBeenCalledWith(
      expect.stringContaining("permanently"),
      "Delete",
      "destructive",
    );
    expect(h.mockDeleteDispatch).toHaveBeenCalledWith("c1");
    expect(table().querySelector('[data-chat-id="c1"]')).toBeNull();
  });

  it("exports without deleting the row", async () => {
    h.mockLoadDispatch.mockResolvedValue({ chats: [chat("c1", "First")] });
    showHistoryView();
    await flush();

    table().querySelector<HTMLButtonElement>('[data-action="export"]')?.click();
    await flush();
    expect(h.mockDownload).toHaveBeenCalledWith("c1", "First", "md");
    expect(table().querySelector('[data-chat-id="c1"]')).not.toBeNull();
  });

  it("offers a Retry on load failure and re-fetches on click", async () => {
    h.mockLoadDispatch
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce({ chats: [chat("c1", "First")] });
    showHistoryView();
    await flush();

    expect(table().querySelector(".history-error")).not.toBeNull();
    table().querySelector<HTMLButtonElement>(".history-error button")?.click();
    await flush();

    expect(table().querySelector(".history-error")).toBeNull();
    expect(table().querySelectorAll("[data-chat-id]").length).toBe(1);
  });

  it("shows a loading skeleton after the show-delay, then clears it", async () => {
    vi.useFakeTimers();
    let resolve: (v: unknown) => void = () => {
      /* replaced below */
    };
    h.mockLoadDispatch.mockReturnValue(
      new Promise((r) => {
        resolve = r;
      }),
    );
    showHistoryView();
    await vi.advanceTimersByTimeAsync(160); // past the 150ms show-delay
    expect(table().querySelector(".history-skeleton")).not.toBeNull();

    resolve({ chats: [] });
    await vi.advanceTimersByTimeAsync(0);
    expect(table().querySelector(".history-skeleton")).toBeNull();
  });
});
