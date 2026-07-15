// @vitest-environment happy-dom
//
// Tests for the Settings -> Tools module over the v2 tools engine:
// row rendering from the composite GET (state dots, versions, update
// badges), the action wiring (install / pin / cascade delete), the
// search-first add modal, and the SSE job-following output panel.
import { describe, it, expect, beforeEach, vi } from "vitest";

import type { ToolInfo, ToolJob, ToolsList } from "./types.js";

const mocks = vi.hoisted(() => ({
  loadDispatch: vi.fn(),
  loadCancel: vi.fn(),
  createDispatch: vi.fn(),
  installDispatch: vi.fn(),
  updateDispatch: vi.fn(),
  patchDispatch: vi.fn(),
  deleteDispatch: vi.fn(),
  searchDispatch: vi.fn(),
  jobsDispatch: vi.fn(),
  ensureDispatch: vi.fn(),
  openModal: vi.fn(),
  closeModal: vi.fn(),
  confirm: vi.fn(),
  rollingAppend: vi.fn(),
  rollingClear: vi.fn(),
  sseHandlers: new Map<string, (chatID: string, payload: unknown) => void>(),
}));

vi.mock("./modals.js", () => ({
  openModal: mocks.openModal,
  closeModal: mocks.closeModal,
  RollingOutput: class {
    clear(): void {
      mocks.rollingClear();
    }
    append(s: string): void {
      mocks.rollingAppend(s);
    }
  },
}));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirm }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/tools.js", () => ({
  loadTools: { dispatch: mocks.loadDispatch, cancel: mocks.loadCancel },
  createTool: { dispatch: mocks.createDispatch },
  installTool: { dispatch: mocks.installDispatch },
  updateTools: { dispatch: mocks.updateDispatch },
  patchTool: { dispatch: mocks.patchDispatch },
  deleteTool: { dispatch: mocks.deleteDispatch },
  searchTools: { dispatch: mocks.searchDispatch },
  getToolsJobs: { dispatch: mocks.jobsDispatch },
  ensureTool: { dispatch: mocks.ensureDispatch },
}));
vi.mock("./bus.js", () => ({
  onSSE: (type: string, fn: (chatID: string, payload: unknown) => void) => {
    mocks.sseHandlers.set(type, fn);
    return () => mocks.sseHandlers.delete(type);
  },
}));

import { initTools, loadToolsList } from "./tools.js";
import { byId } from "./dom.js";

// --- DOM fixture -------------------------------------------------------------

function mountToolsDOM(): void {
  document.body.replaceChildren();
  const add = (tag: string, id: string): HTMLElement => {
    const e = document.createElement(tag);
    e.id = id;
    document.body.appendChild(e);
    return e;
  };
  add("button", "tool-add-btn");
  add("button", "tool-update-btn");
  add("button", "tool-cancel-btn");
  add("div", "tool-update-output");
  add("div", "tools-list");
  add("div", "tool-modal");
  add("input", "tool-search");
  add("div", "tool-search-results");
  add("button", "tool-manual-toggle");
  add("div", "tool-manual-form").classList.add("hidden");
  add("input", "tool-manual-name");
  add("input", "tool-manual-version");
  add("input", "tool-manual-install");
  add("input", "tool-manual-uninstall");
  add("input", "tool-manual-probe");
  add("button", "tool-manual-add");
}

function tool(overrides: Partial<ToolInfo> & { name: string }): ToolInfo {
  return {
    source: "aqua:o/r",
    version: "1.0.0",
    installed: true,
    installing: false,
    ...overrides,
  };
}

function listWith(tools: ToolInfo[], job?: ToolJob): ToolsList {
  return { tools, system: [{ name: "git", installed: true }], ...(job ? { job } : {}) };
}

function initWith(data: ToolsList): void {
  mocks.loadDispatch.mockImplementation(
    (_args: undefined, opts?: { onSuccess?: (d: ToolsList) => void }) => {
      opts?.onSuccess?.(data);
      return Promise.resolve(data);
    },
  );
  initTools();
  loadToolsList();
}

function rowFor(name: string): HTMLElement | null {
  for (const row of document.querySelectorAll<HTMLElement>("#tools-list .list-row")) {
    if (row.querySelector(".list-row-name")?.textContent === name) {
      return row;
    }
  }
  return null;
}

const flush = async (): Promise<void> => {
  await Promise.resolve();
  await Promise.resolve();
};

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sseHandlers.clear();
  mountToolsDOM();
});

// ---------------------------------------------------------------------------

describe("tools list rendering", () => {
  it("renders state dots, versions, and the system group", () => {
    initWith(
      listWith([
        tool({ name: "gh", installed_version: "2.96.0" }),
        tool({ name: "broken", installed: false, last_error: "download failed" }),
        tool({ name: "cold", installed: false }),
        tool({ name: "busy", installed: false, installing: true }),
      ]),
    );

    expect(rowFor("gh")?.querySelector(".tool-state-ok")).not.toBeNull();
    expect(rowFor("gh")?.textContent).toContain("2.96.0");
    expect(rowFor("broken")?.querySelector(".tool-state-error")).not.toBeNull();
    expect(rowFor("broken")?.textContent).toContain("download failed");
    expect(rowFor("cold")?.querySelector(".tool-state-missing")).not.toBeNull();
    expect(rowFor("busy")?.querySelector(".tool-state-busy")).not.toBeNull();
    expect(rowFor("busy")?.textContent).toContain("installing…");
    // System group renders read-only rows.
    expect(rowFor("git")?.classList.contains("list-row-system")).toBe(true);
    expect(rowFor("git")?.querySelector("button")).toBeNull();
  });

  it("shows Install for missing tools and Retry after a failure", () => {
    initWith(
      listWith([
        tool({ name: "cold", installed: false }),
        tool({ name: "broken", installed: false, last_error: "x" }),
      ]),
    );
    expect(rowFor("cold")?.querySelector("button.list-row-enable")?.textContent).toBe("Install");
    expect(rowFor("broken")?.querySelector("button.list-row-enable")?.textContent).toBe("Retry");
  });

  it("offers Update when a newer version is known", () => {
    initWith(listWith([tool({ name: "gh", latest: "2.97.0" })]));
    const row = rowFor("gh");
    expect(row?.textContent).toContain("1.0.0 → 2.97.0");
    row?.querySelector<HTMLButtonElement>('button[aria-label="Update gh to 2.97.0"]')?.click();
    expect(mocks.updateDispatch).toHaveBeenCalledWith({ names: ["gh"] });
  });

  it("renders the catalog empty state when no tools are installed", () => {
    initWith(listWith([]));
    expect(byId("tools-list").textContent).toContain("No tools installed yet");
  });
});

describe("row actions", () => {
  it("Install dispatches tools.install", async () => {
    initWith(listWith([tool({ name: "cold", installed: false })]));
    mocks.installDispatch.mockResolvedValue({ job: { id: "tj-1" } });
    rowFor("cold")?.querySelector<HTMLButtonElement>("button.list-row-enable")?.click();
    await flush();
    expect(mocks.installDispatch).toHaveBeenCalledWith({ name: "cold" });
  });

  it("pin toggle PATCHes the inverse pin state", async () => {
    initWith(listWith([tool({ name: "gh", pin: false })]));
    mocks.patchDispatch.mockResolvedValue({ ok: true });
    rowFor("gh")?.querySelector<HTMLButtonElement>(".list-row-pin")?.click();
    await flush();
    expect(mocks.patchDispatch).toHaveBeenCalledWith({ name: "gh", pin: true });
  });

  it("delete confirms, then cascades through the 409 dependents flow", async () => {
    initWith(listWith([tool({ name: "java" })]));
    mocks.confirm.mockResolvedValue(true);
    mocks.deleteDispatch
      .mockResolvedValueOnce({ code: "has_dependents", dependents: ["jdtls"] })
      .mockResolvedValueOnce({ job: { id: "tj-2" } });

    rowFor("java")?.querySelector<HTMLButtonElement>('button[aria-label="Remove java"]')?.click();
    await flush();
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(2);
    expect(String(mocks.confirm.mock.calls[1]?.[0])).toContain("jdtls");
    expect(mocks.deleteDispatch).toHaveBeenNthCalledWith(1, { name: "java" });
    expect(mocks.deleteDispatch).toHaveBeenNthCalledWith(2, { name: "java", force: true });
  });

  it("delete stops when the user declines", async () => {
    initWith(listWith([tool({ name: "gh" })]));
    mocks.confirm.mockResolvedValue(false);
    rowFor("gh")?.querySelector<HTMLButtonElement>('button[aria-label="Remove gh"]')?.click();
    await flush();
    expect(mocks.deleteDispatch).not.toHaveBeenCalled();
  });
});

describe("add modal", () => {
  it("opens with the featured set and installs a hit via tools.create", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({
      results: [{ name: "ripgrep", source: "aqua:BurntSushi/ripgrep", description: "grep, fast" }],
    });
    mocks.createDispatch.mockResolvedValue({ job: { id: "tj-3" } });

    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();

    expect(mocks.openModal).toHaveBeenCalledTimes(1);
    expect(mocks.searchDispatch).toHaveBeenCalledWith({ q: "" });
    const hit = byId("tool-search-results").querySelector<HTMLButtonElement>(
      'button[aria-label="Install ripgrep"]',
    );
    expect(hit).not.toBeNull();
    expect(byId("tool-search-results").textContent).toContain("grep, fast");

    hit?.click();
    await flush();
    expect(mocks.createDispatch).toHaveBeenCalledWith({ name: "ripgrep" });
    expect(mocks.closeModal).toHaveBeenCalledTimes(1);
  });

  it("submits the manual form with only filled optional fields", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({ results: [] });
    mocks.createDispatch.mockResolvedValue({ job: { id: "tj-4" } });

    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();
    byId<HTMLButtonElement>("tool-manual-toggle").click();
    expect(byId("tool-manual-form").classList.contains("hidden")).toBe(false);

    byId<HTMLInputElement>("tool-manual-name").value = "mytool";
    byId<HTMLInputElement>("tool-manual-version").value = "1.0.0";
    byId<HTMLInputElement>("tool-manual-install").value = 'curl x > "$BIN/mytool"';
    byId<HTMLButtonElement>("tool-manual-add").click();
    await flush();

    expect(mocks.createDispatch).toHaveBeenCalledWith({
      name: "mytool",
      source: "manual",
      version: "1.0.0",
      install: 'curl x > "$BIN/mytool"',
    });
  });

  it("does not submit an incomplete manual form", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({ results: [] });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();
    byId<HTMLInputElement>("tool-manual-name").value = "mytool";
    byId<HTMLButtonElement>("tool-manual-add").click();
    await flush();
    expect(mocks.createDispatch).not.toHaveBeenCalled();
  });
});

describe("job following over SSE", () => {
  it("streams output lines for the followed job and refreshes on state changes", () => {
    initWith(listWith([tool({ name: "gh" })]));
    const changed = mocks.sseHandlers.get("tool_job_changed");
    const output = mocks.sseHandlers.get("tool_job_output");
    expect(changed).toBeDefined();
    expect(output).toBeDefined();

    const loadsBefore = mocks.loadDispatch.mock.calls.length;
    changed?.("", { job: { id: "tj-9", kind: "install", names: ["gh"], state: "running" } });
    expect(mocks.rollingClear).toHaveBeenCalled();
    expect(mocks.rollingAppend).toHaveBeenCalledWith("running: install gh");
    expect(mocks.loadDispatch.mock.calls.length).toBe(loadsBefore + 1);

    output?.("", { job_id: "tj-9", lines: ["a", "b"] });
    expect(mocks.rollingAppend).toHaveBeenCalledWith("a");
    expect(mocks.rollingAppend).toHaveBeenCalledWith("b");

    // Lines for another job are ignored.
    mocks.rollingAppend.mockClear();
    output?.("", { job_id: "tj-other", lines: ["nope"] });
    expect(mocks.rollingAppend).not.toHaveBeenCalled();

    changed?.("", { job: { id: "tj-9", kind: "install", names: ["gh"], state: "done" } });
    expect(mocks.rollingAppend).toHaveBeenCalledWith("✓ install gh finished");
  });

  it("resumes a running job's output tail on load", async () => {
    const job: ToolJob = { id: "tj-boot", kind: "sync", state: "running", created_at: 1 };
    mocks.jobsDispatch.mockResolvedValue({
      active: { ...job, output_tail: ["line1", "line2"] },
    });
    initWith(listWith([tool({ name: "gh" })], job));
    await flush();
    expect(mocks.jobsDispatch).toHaveBeenCalled();
    expect(mocks.rollingAppend).toHaveBeenCalledWith("line1");
    expect(mocks.rollingAppend).toHaveBeenCalledWith("line2");
  });
});
