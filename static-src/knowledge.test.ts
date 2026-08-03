// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for knowledge.ts: list render (contexts + live indexing progress),
// merge-by-name dedup, empty/error states, the inline add form, destructive
// remove, the enable hint, and the SSE-driven refetch. api-client, the
// knowledge actions, confirm, toast, and bus are mocked so we control the
// fetched payload + dispatch results and assert the rendered DOM.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

vi.mock("./toast.js", () => ({ showToast: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_PLUS_16: "<svg data-plus></svg>",
  ICON_TRASH_14: "<svg data-trash></svg>",
}));
vi.mock("./bus.js", () => ({ onSSE: vi.fn(() => () => undefined) }));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: vi.fn(() => () => undefined),
  registerCleanup: vi.fn(),
}));
vi.mock("./actions/knowledge.js", () => ({
  addKnowledge: { dispatch: vi.fn() },
  removeKnowledge: { dispatch: vi.fn() },
}));
vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn(),
  fetchKiroSetting: vi.fn(),
  CancellableSlot: class {
    start(): AbortSignal {
      return new AbortController().signal;
    }
    abort(): void {
      /* noop */
    }
  },
}));
vi.mock("./dom.js", () => ({ byId: (id: string) => document.getElementById(id) }));

import { apiGetTyped, fetchKiroSetting } from "./api-client.js";
import { onSSE } from "./bus.js";
import { confirm as confirmDialog } from "./confirm.js";
import { showToast } from "./toast.js";
import { addKnowledge, removeKnowledge } from "./actions/knowledge.js";
import { initKnowledge, loadKnowledge } from "./knowledge.js";

const mockGet = vi.mocked(apiGetTyped);
const mockFlag = vi.mocked(fetchKiroSetting);
const mockConfirm = vi.mocked(confirmDialog);
const mockAdd = vi.mocked(addKnowledge.dispatch);
const mockRemove = vi.mocked(removeKnowledge.dispatch);

/** Flush the fetch().then(render) + refreshHint microtask chains without
 *  advancing the 1500ms poll timer (fake timers keep it pending; afterEach
 *  discards it). */
async function flush(): Promise<void> {
  await vi.advanceTimersByTimeAsync(0);
}

function seedDom(): void {
  document.body.innerHTML = `
    <div id="knowledge-section">
      <button id="knowledge-add-btn"></button>
      <p id="knowledge-hint" hidden></p>
      <div id="knowledge-list"><div class="list-empty">No knowledge bases yet.</div></div>
    </div>`;
}

const list = (): HTMLElement => document.getElementById("knowledge-list") as HTMLElement;

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  seedDom();
  mockFlag.mockResolvedValue(true); // knowledge enabled by default
  mockGet.mockResolvedValue({ contexts: [] }); // default; tests override
});

afterEach(() => {
  // Discards any pending 1500ms poll timer scheduled by an indexing render.
  vi.useRealTimers();
});

describe("initKnowledge", () => {
  it("sets the add icon and builds the inline form", () => {
    initKnowledge();
    expect(document.getElementById("knowledge-add-btn")?.innerHTML).toContain("data-plus");
    expect(document.getElementById("knowledge-add-form")).not.toBeNull();
  });

  // The inverse of the deleted subscription IS the contract now: the indexing
  // notification fired only for a non-builtin mode's declared bases, whose
  // per-agent store is disjoint from the default store this list reads, so its
  // one action — refetching — could never show the base it announced.
  it("subscribes to no SSE at all", () => {
    initKnowledge();
    expect(vi.mocked(onSSE)).not.toHaveBeenCalled();
  });

  it("toggles the add form open on + click", () => {
    initKnowledge();
    const form = document.getElementById("knowledge-add-form") as HTMLFormElement;
    expect(form.hidden).toBe(true);
    (document.getElementById("knowledge-add-btn") as HTMLButtonElement).click();
    expect(form.hidden).toBe(false);
  });
});

describe("loadKnowledge render", () => {
  it("renders a context row with item count + path", async () => {
    mockGet.mockResolvedValue({
      contexts: [{ name: "docs", id: "abc12345", item_count: 7, path: "internal/api" }],
    });
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("docs");
    expect(list().textContent).toContain("7 items");
    expect(list().textContent).toContain("internal/api");
  });

  it("renders an indexing row with a progress bar + percentage", async () => {
    mockGet.mockResolvedValue({
      contexts: [{ name: "big", id: "op1", item_count: 0, items_display: "42%", indexing: true }],
    });
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("Indexing… 42%");
    const fill = list().querySelector<HTMLElement>(".knowledge-bar-fill");
    expect(fill?.style.inlineSize).toBe("42%");
  });

  it("merges duplicate names during an add, preferring the indexing entry", async () => {
    mockGet.mockResolvedValue({
      contexts: [
        { name: "docs", id: "ctx", item_count: 0, description: "…(indexing...)" },
        { name: "docs", id: "op", item_count: 0, items_display: "12%", indexing: true },
      ],
    });
    loadKnowledge();
    await flush();
    expect(list().querySelectorAll(".knowledge-row").length).toBe(1);
    expect(list().textContent).toContain("Indexing… 12%");
  });

  it("shows the empty state for no bases", async () => {
    mockGet.mockResolvedValue({ contexts: [] });
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("No knowledge bases yet.");
  });

  it("shows an error state on fetch failure", async () => {
    mockGet.mockResolvedValue(null);
    loadKnowledge();
    await flush();
    expect(list().textContent).toContain("Couldn't load knowledge bases.");
  });

  it("shows the enable hint when the knowledge flag is off", async () => {
    mockFlag.mockResolvedValue(false);
    mockGet.mockResolvedValue({ contexts: [] });
    loadKnowledge();
    await flush();
    expect((document.getElementById("knowledge-hint") as HTMLElement).hidden).toBe(false);
  });
});

describe("add flow", () => {
  it("dispatches knowledge.add with the entered path and refetches", async () => {
    initKnowledge();
    mockAdd.mockResolvedValue({ message: "Indexing 'docs' in background" });
    mockGet.mockResolvedValue({ contexts: [] });

    (document.getElementById("knowledge-add-path") as HTMLInputElement).value = "docs";
    (document.getElementById("knowledge-add-form") as HTMLFormElement).dispatchEvent(
      new Event("submit", { cancelable: true }),
    );
    await flush();

    expect(mockAdd).toHaveBeenCalledWith({ path: "docs", name: "" });
    expect(vi.mocked(showToast)).toHaveBeenCalled();
    expect((document.getElementById("knowledge-add-form") as HTMLFormElement).hidden).toBe(true);
  });

  it("keeps the form open and shows no success toast when add fails", async () => {
    initKnowledge();
    mockAdd.mockResolvedValue(null); // action reported failure (default error toast already fired)
    (document.getElementById("knowledge-add-form") as HTMLFormElement).hidden = false;
    (document.getElementById("knowledge-add-path") as HTMLInputElement).value = "bad/path";
    (document.getElementById("knowledge-add-form") as HTMLFormElement).dispatchEvent(
      new Event("submit", { cancelable: true }),
    );
    await flush();
    expect(vi.mocked(showToast)).not.toHaveBeenCalled();
    expect((document.getElementById("knowledge-add-form") as HTMLFormElement).hidden).toBe(false);
  });
});

describe("remove flow", () => {
  async function renderOneRow(): Promise<void> {
    mockGet.mockResolvedValue({ contexts: [{ name: "docs", id: "a", item_count: 3 }] });
    loadKnowledge();
    await flush();
  }

  it("dispatches knowledge.remove after a confirmed destructive prompt", async () => {
    await renderOneRow();
    mockConfirm.mockResolvedValue(true);
    (list().querySelector(".knowledge-remove") as HTMLButtonElement).click();
    await flush();
    expect(mockConfirm).toHaveBeenCalledWith(expect.any(String), "Remove", "destructive");
    expect(mockRemove).toHaveBeenCalledWith({ name: "docs" }, expect.any(Object));
  });

  it("does not remove when the confirm is cancelled", async () => {
    await renderOneRow();
    mockConfirm.mockResolvedValue(false);
    (list().querySelector(".knowledge-remove") as HTMLButtonElement).click();
    await flush();
    expect(mockRemove).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The poll's stall budget.
//
// It used to be a flat cap of 200 ticks at 1500ms — a ~5-minute ceiling past
// which the UI silently stopped updating while KAS carried on indexing, so a
// large base appeared to hang forever. The budget is stall-based now, and these
// pin the distinction: slow is not wedged.
// ---------------------------------------------------------------------------

describe("indexing poll", () => {
  function indexing(items: number) {
    return { contexts: [{ name: "big", id: "1", item_count: items, indexing: true }] };
  }

  it("keeps polling while progress advances past the old 5-minute ceiling", async () => {
    vi.useFakeTimers();
    try {
      let items = 0;
      mockGet.mockImplementation(() => {
        items += 10;
        return Promise.resolve(indexing(items));
      });
      loadKnowledge();
      await flush();

      // 400 ticks is twice the old cap. Every one advances, so every one must be
      // followed by another.
      for (let i = 0; i < 400; i++) {
        await vi.advanceTimersByTimeAsync(1500);
        await flush();
      }
      expect(mockGet.mock.calls.length).toBeGreaterThan(300);
    } finally {
      vi.useRealTimers();
    }
  });

  it("gives up once progress stalls", async () => {
    vi.useFakeTimers();
    try {
      // Same item_count every time: indexing is running but not moving.
      mockGet.mockImplementation(() => Promise.resolve(indexing(42)));
      loadKnowledge();
      await flush();

      for (let i = 0; i < 200; i++) {
        await vi.advanceTimersByTimeAsync(1500);
        await flush();
      }
      // Bounded well below the tick count, so a wedged index does not poll on
      // forever.
      expect(mockGet.mock.calls.length).toBeLessThan(60);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops polling when nothing is indexing", async () => {
    vi.useFakeTimers();
    try {
      mockGet.mockResolvedValue({ contexts: [{ name: "done", id: "1", item_count: 9 }] });
      loadKnowledge();
      await flush();
      const after = mockGet.mock.calls.length;
      await vi.advanceTimersByTimeAsync(1500 * 5);
      await flush();
      expect(mockGet.mock.calls.length).toBe(after);
    } finally {
      vi.useRealTimers();
    }
  });
});
