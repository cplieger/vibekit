// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for specs.ts: the Specs board. Pure render helpers (task counts,
// task-node badges, card build/fill) are tested directly against happy-dom;
// the fetch action, poll primitive, tabs toggle, editor opener, and bus are
// mocked so the wiring test (SSE-driven refetch + doc-open delegation) is
// deterministic.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { Spec, SpecTaskNode } from "./types.js";

// --- Mocks for specs.ts module-load dependencies ---

const mockDispatch = vi.fn();
const mockCancel = vi.fn();
vi.mock("./actions/specs.js", () => ({
  fetchSpecs: {
    dispatch: (...a: unknown[]) => mockDispatch(...a),
    cancel: () => mockCancel(),
  },
}));

const mockPollAction = vi.fn((..._args: unknown[]) => vi.fn());
const mockRegisterCleanup = vi.fn();
vi.mock("./actions/index.js", () => ({
  pollAction: (...a: unknown[]) => mockPollAction(...a),
  registerCleanup: (...a: unknown[]) => mockRegisterCleanup(...a),
}));

const mockOpenFile = vi.fn();
vi.mock("./editor-openers.js", () => ({
  openFile: (...a: unknown[]) => mockOpenFile(...a),
}));

const mockToggle = vi.fn((onShow: () => void, _onClose?: () => void) => {
  onShow();
});
vi.mock("./tabs.js", () => ({
  toggleSpecsView: (onShow: () => void, onClose?: () => void) => mockToggle(onShow, onClose),
}));

let sseHandler: ((chatID: string, p: unknown) => void) | undefined;
vi.mock("./bus.js", () => ({
  onSSE: (type: string, fn: (chatID: string, p: unknown) => void) => {
    if (type === "spec_task_changed") {
      sseHandler = fn;
    }
    return () => {
      /* unsubscribe noop */
    };
  },
}));

const {
  taskCounts,
  renderTaskNode,
  buildSpecCard,
  fillSpecCard,
  initSpecs,
  showSpecsView,
  _resetForTest,
} = await import("./specs.js");

// --- Fixtures ---

function leaf(id: string, md: string, extra: Partial<SpecTaskNode> = {}): SpecTaskNode {
  return { task_id: id, markdown_status: md, is_leaf: true, sub_tasks: [], ...extra };
}
function parent(id: string, md: string, kids: SpecTaskNode[]): SpecTaskNode {
  return { task_id: id, markdown_status: md, is_leaf: false, sub_tasks: kids };
}
function spec(over: Partial<Spec> = {}): Spec {
  const merged: Spec = {
    name: "demo",
    has_requirements: true,
    has_design: true,
    has_tasks: true,
    requirements_path: ".kiro/specs/demo/requirements.md",
    design_path: ".kiro/specs/demo/design.md",
    tasks_path: ".kiro/specs/demo/tasks.md",
    tasks: [],
    ...over,
  };
  // Respect exactOptionalPropertyTypes: a missing document has no path key
  // at all (not an explicit undefined).
  if (!merged.has_requirements) {
    delete merged.requirements_path;
  }
  if (!merged.has_design) {
    delete merged.design_path;
  }
  if (!merged.has_tasks) {
    delete merged.tasks_path;
  }
  return merged;
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

// --- Pure helpers ---

describe("taskCounts", () => {
  it("counts completed vs total across the whole tree", () => {
    const tasks = [
      parent("1", "completed", [leaf("1.1", "completed"), leaf("1.2", "not_started")]),
      leaf("2", "completed"),
    ];
    expect(taskCounts(tasks)).toEqual({ done: 3, total: 4 });
  });

  it("handles an empty tree", () => {
    expect(taskCounts([])).toEqual({ done: 0, total: 0 });
  });
});

describe("renderTaskNode", () => {
  it("renders the markdown status glyph + task text, spacer for a leaf", () => {
    const node = renderTaskNode(leaf("2.1 Do a thing", "in_progress"), 0, "demo");
    expect(node.querySelector(".spec-check")?.getAttribute("data-status")).toBe("in_progress");
    expect(node.querySelector(".spec-task-text")?.textContent).toBe("2.1 Do a thing");
    expect(node.querySelector(".spec-toggle")).toBeNull();
    expect(node.querySelector(".spec-toggle-spacer")).not.toBeNull();
  });

  it("renders execution + PBT badges, the optional marker, and the failing example", () => {
    const node = renderTaskNode(
      leaf("3", "completed", {
        execution_status: "succeed",
        is_optional: true,
        pbt_result: { status: "failed", failing_example: "n = 42" },
      }),
      0,
      "demo",
    );
    expect(node.querySelector(".spec-badge-exec")?.classList.contains("exec-succeed")).toBe(true);
    expect(node.querySelector(".spec-badge-optional")).not.toBeNull();
    expect(node.querySelector(".spec-badge-pbt")?.classList.contains("pbt-failed")).toBe(true);
    expect(node.querySelector(".spec-pbt-example")?.textContent).toBe("n = 42");
  });

  it("gives a parent a collapse toggle and nested children", () => {
    const node = renderTaskNode(parent("1", "completed", [leaf("1.1", "completed")]), 0, "demo");
    expect(node.querySelector(".spec-toggle")).not.toBeNull();
    expect(node.querySelector(".spec-node-children .spec-node")).not.toBeNull();
    expect(node.classList.contains("collapsed")).toBe(false);
  });
});

describe("buildSpecCard", () => {
  it("renders the name, doc trio, progress count, and task tree", () => {
    const card = buildSpecCard(
      spec({
        tasks: [leaf("1", "completed"), leaf("2", "not_started")],
        updated_at: "2026-07-10T12:00:00Z",
      }),
    );
    expect(card.querySelector(".spec-name")?.textContent).toBe("demo");
    expect(card.querySelectorAll(".spec-doc").length).toBe(3);
    expect(
      card.querySelector('button.spec-doc[data-path=".kiro/specs/demo/requirements.md"]'),
    ).not.toBeNull();
    expect(card.querySelector(".spec-progress-count")?.textContent).toBe("1 / 2 done");
    expect(card.querySelectorAll(".spec-tree > .spec-node").length).toBe(2);
  });

  it("renders a missing document as a muted non-button chip", () => {
    const card = buildSpecCard(spec({ has_design: false, tasks: [] }));
    const design = [...card.querySelectorAll(".spec-doc")].find((e) => e.textContent === "Design");
    expect(design?.tagName).toBe("SPAN");
    expect(design?.classList.contains("spec-doc-missing")).toBe(true);
  });

  it("shows an empty-tasks hint when there is no tasks.md", () => {
    const card = buildSpecCard(spec({ has_tasks: false, tasks: [] }));
    expect(card.querySelector(".spec-empty-tasks")).not.toBeNull();
  });

  it("shows an inline error when the task status is unavailable", () => {
    const card = buildSpecCard(spec({ error: "task status unavailable", tasks: [] }));
    expect(card.querySelector(".spec-inline-error")).not.toBeNull();
  });

  it("updates status in place via fillSpecCard (live-update path)", () => {
    const card = buildSpecCard(spec({ tasks: [leaf("1", "not_started")] }));
    expect(card.querySelector(".spec-check")?.getAttribute("data-status")).toBe("not_started");
    fillSpecCard(card, spec({ tasks: [leaf("1", "completed", { execution_status: "succeed" })] }));
    expect(card.querySelector(".spec-check")?.getAttribute("data-status")).toBe("completed");
    expect(card.querySelector(".spec-badge-exec.exec-succeed")).not.toBeNull();
  });
});

// --- Wiring: SSE refetch + doc-open delegation ---

describe("initSpecs board wiring", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    sseHandler = undefined;
    _resetForTest();
    document.body.innerHTML = `<button id="specs-btn"></button><div id="specs-list"></div>`;
  });

  it("loads on open and refetches (debounced) on a spec_task_changed SSE", () => {
    vi.useFakeTimers();
    mockDispatch.mockResolvedValue({ specs: [] });
    initSpecs();
    showSpecsView(); // mockToggle invokes onShow → controller.start()

    expect(mockDispatch).toHaveBeenCalledTimes(1); // initial load
    expect(mockPollAction).toHaveBeenCalledTimes(1);
    expect(sseHandler).toBeTypeOf("function");

    sseHandler?.("", { feature_name: "demo", changes: [] });
    vi.advanceTimersByTime(300); // past the debounce window
    expect(mockDispatch).toHaveBeenCalledTimes(2); // refetch

    vi.useRealTimers();
  });

  it("opens a document in the editor when a doc chip is clicked", async () => {
    mockDispatch.mockResolvedValue({ specs: [spec({ tasks: [] })] });
    initSpecs();
    showSpecsView();
    await flush(); // let the initial load render the card

    const list = document.getElementById("specs-list");
    const btn = list?.querySelector<HTMLElement>("button.spec-doc[data-path]");
    expect(btn).not.toBeNull();
    btn?.click();
    expect(mockOpenFile).toHaveBeenCalledWith(".kiro/specs/demo/requirements.md");
  });
});
