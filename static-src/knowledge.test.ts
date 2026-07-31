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
  it("sets the add icon, builds the inline form, and subscribes to the SSE", () => {
    initKnowledge();
    expect(document.getElementById("knowledge-add-btn")?.innerHTML).toContain("data-plus");
    expect(document.getElementById("knowledge-add-form")).not.toBeNull();
    expect(vi.mocked(onSSE)).toHaveBeenCalledWith("knowledge_indexing", expect.any(Function));
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

describe("SSE refetch", () => {
  it("refetches the list when a knowledge_indexing event arrives", async () => {
    initKnowledge();
    const handler = vi.mocked(onSSE).mock.calls.find((c) => c[0] === "knowledge_indexing")?.[1];
    expect(handler).toBeDefined();
    mockGet.mockResolvedValue({ contexts: [] });
    (handler as () => void)();
    await flush();
    expect(mockGet).toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Row signature (keyenc `join`).
//
// The signature only gates whether a row's children are rebuilt — row identity
// is `kb:${name}`, so a collision leaves a STALE ROW, not a missing or wrong
// one. `items_display` and `path` are adjacent free-form fields (a path may
// contain "|"), which is what made the old "|"-joined template forgeable.
// ---------------------------------------------------------------------------

describe("loadKnowledge row signature", () => {
  /** The signature expression as it was before the keyenc adoption. */
  function oldSig(c: {
    indexing?: boolean;
    item_count: number;
    items_display?: string;
    path?: string;
  }): string {
    return `${c.indexing === true ? "1" : "0"}|${String(c.item_count)}|${c.items_display ?? ""}|${c.path ?? ""}`;
  }

  async function sigFor(items_display: string, path: string): Promise<string> {
    mockGet.mockResolvedValue({
      contexts: [{ name: "kb", id: "i", item_count: 3, items_display, path }],
    });
    loadKnowledge();
    await flush();
    return list().querySelector(".knowledge-row")?.getAttribute("data-sig") ?? "";
  }

  it("distinguishes two states the old '|'-joined signature collapsed", async () => {
    // Both fields are free-form and ADJACENT, so a "|" inside items_display
    // could impersonate the boundary before `path`.
    const a = { item_count: 3, items_display: "42%|eta", path: "docs" };
    const b = { item_count: 3, items_display: "42%", path: "eta|docs" };
    // Precondition: the pre-adoption expression really did collapse these.
    expect(oldSig(a)).toBe(oldSig(b));

    // The row key is the same for both loads ("kb:kb"), so the second load
    // reuses the row and rewrites data-sig only if the signature changed.
    const sigA = await sigFor(a.items_display, a.path);
    const sigB = await sigFor(b.items_display, b.path);
    expect(sigA).not.toBe(sigB);
  });

  it("emits verbatim components for ordinary input", async () => {
    // No reserved character in any field, so each component is emitted as-is
    // and the signature is just the four fields separated by ":".
    expect(await sigFor("42%", "internal/api")).toBe("0:3:42%:internal/api");
  });

  it("escapes a reserved character instead of emitting a bare separator", async () => {
    expect(await sigFor("a:b", "")).toBe("0:3:a\\:b:");
  });
});
