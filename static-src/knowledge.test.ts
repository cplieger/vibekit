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
  ICON_PLUS_UI: "<svg data-plus></svg>",
  ICON_TRASH_UI: "<svg data-trash></svg>",
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
  CancellableSlot: class {
    start(): AbortSignal {
      return new AbortController().signal;
    }
    abort(): void {
      /* noop */
    }
  },
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGet: vi.fn(),
}));
vi.mock("./dom.js", () => ({ byId: (id: string) => document.getElementById(id) }));

import { apiGetTyped } from "./api-client.js";
import { onSSE } from "./bus.js";
import { confirm as confirmDialog } from "./confirm.js";
import { showToast } from "./toast.js";
import { addKnowledge, removeKnowledge } from "./actions/knowledge.js";
import { initKnowledge, loadKnowledge } from "./knowledge.js";
import { settingsPayload } from "./__test-helpers__/settings.js";

const mockGet = vi.mocked(apiGetTyped);
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

const hint = (): HTMLElement => document.getElementById("knowledge-hint") as HTMLElement;

/** The module makes two GETs through one apiGetTyped, so route by path: the hint
 *  reads /api/settings, everything else is the knowledge list. `settings` is the
 *  whole answer, so `null` models a network/decode failure. */
function routeGets(listAnswer: unknown, settings: unknown): void {
  mockGet.mockImplementation((path: string) =>
    Promise.resolve(path === "/api/settings" ? settings : listAnswer),
  );
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  seedDom();
  // knowledge enabled by default; tests override the list payload
  routeGets({ contexts: [] }, settingsPayload());
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
    // A native <progress>, so the value is the element's own and the UA reports
    // it. `position` rather than `value` alone, because it is the pair with `max`
    // that decides what is drawn — a value of 42 against a max of 1 is full.
    const bar = list().querySelector<HTMLProgressElement>("progress.knowledge-bar");
    expect(bar).not.toBeNull();
    expect(bar?.max).toBe(100);
    expect(bar?.value).toBe(42);
    expect(bar?.position).toBeCloseTo(0.42, 5);
    // The one ARIA it authors. Native <progress> reports its own value, so a name
    // is all it needs — and the bare `role="progressbar"` this replaced had none.
    expect(bar?.getAttribute("aria-label")).toBe("Indexing");
  });

  // The indeterminate case, and the reason the `pct !== null` guard survived the
  // conversion: a valueless <progress> renders an ANIMATED indeterminate bar, so
  // emitting one beside the word "Cancelled" would claim work that has stopped.
  // Rendering no bar at all is the honest answer.
  for (const display of ["Cancelled", "Failed", undefined]) {
    it(`renders no bar at all while indexing reports ${display ?? "nothing"}`, async () => {
      mockGet.mockResolvedValue({
        contexts: [
          { name: "big", id: "op1", item_count: 0, items_display: display, indexing: true },
        ],
      });
      loadKnowledge();
      await flush();
      expect(list().querySelector("progress")).toBeNull();
      // The text still says what happened, which is what the row is for.
      expect(list().textContent).toContain(display === undefined ? "Indexing…" : display);
    });
  }

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

  it("shows the enable hint when knowledge_enabled is off", async () => {
    routeGets({ contexts: [] }, settingsPayload({ knowledge_enabled: false }));
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(false);
  });

  it("keeps the enable hint hidden when knowledge_enabled is on", async () => {
    routeGets({ contexts: [] }, settingsPayload({ knowledge_enabled: true }));
    hint().hidden = false;
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(true);
  });

  // A null answer is a network, abort or decode failure — NOT "knowledge is off",
  // so the hint keeps whatever it was showing rather than asserting either state.
  it("leaves a visible hint visible when /api/settings answers null", async () => {
    routeGets({ contexts: [] }, null);
    hint().hidden = false;
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(false);
  });

  it("leaves a hidden hint hidden when /api/settings answers null", async () => {
    routeGets({ contexts: [] }, null);
    hint().hidden = true;
    loadKnowledge();
    await flush();
    expect(hint().hidden).toBe(true);
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
    // 400 real fetch-shaped turns in a real browser do not fit the 5s default:
    // each poll settles a promise chain rather than a synchronous stub. The
    // budget moved; the assertion did not.
  }, 30_000);

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
