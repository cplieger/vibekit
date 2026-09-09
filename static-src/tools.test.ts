//
// Tests for the Settings -> Tools module over the v2 tools engine:
// row rendering from the composite GET (state dots, versions, update
// badges), the action wiring (install / pin / cascade delete), the
// search-first add modal, and the SSE job-following output panel.
import { describe, it, expect, beforeEach, beforeAll, afterAll, vi } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { ToolInfo, Job, Inventory } from "./types.js";

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
  catalogInfoDispatch: vi.fn(),
  refreshCatalogDispatch: vi.fn(),
  cancelJobDispatch: vi.fn(),
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
  getCatalogInfo: { dispatch: mocks.catalogInfoDispatch },
  refreshCatalog: { dispatch: mocks.refreshCatalogDispatch },
  cancelToolJob: { dispatch: mocks.cancelJobDispatch },
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
import { KEY_ATTR } from "./reconcile.js";
import { join, split } from "@cplieger/keyenc";

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
  // The two job-owning pills carry their authored face here, because JobPill
  // CAPTURES it from the markup to restore when the job settles: an empty
  // fixture button would test the busy face against nothing to go back to.
  // Mirrors static/index.html — glyph, then label span, then aria-label.
  addPill("tool-update-btn", "Update all", "Update all tools");
  add("button", "tool-cancel-btn");
  addPill("tool-catalog-refresh-btn", "Refresh catalog", "Refresh the tool catalog");
  add("p", "tool-catalog-meta").classList.add("hidden");
  add("div", "tool-update-output");
  // Both list hosts carry the classes index.html gives them, because the
  // geometry guards below measure real layout against the shipped stylesheet and
  // the cap that produced the overlap is `.tool-search-results`'s.
  add("div", "tools-list").className = "list-container";
  add("div", "tool-modal");
  add("input", "tool-search").className = "tool-form-input";
  // Mirrors index.html's classes: the button's two glyph faces are picked by
  // `.tool-search-go`, so a bare fixture button would show both at once.
  add("button", "tool-search-btn").className = "action-pill tool-search-go";
  // The footer's permanent sentence lives in the markup; the module only toggles
  // the apt caveat, so the fixture has to carry both the way index.html does.
  const note = add("p", "tool-shell-note");
  note.textContent =
    "Not listed? Install it in the shell; the engine only manages what it installed.";
  const apt = document.createElement("span");
  apt.id = "tool-shell-note-apt";
  apt.className = "hidden";
  apt.textContent =
    " Debian packages are not searchable here: apt needs root and this container has none.";
  note.appendChild(apt);
  // Mirrors static/index.html: the order picker carries its three options,
  // because the module reads `value` and falls back to relevance on anything
  // it does not recognise — an optionless select would read "" and take that
  // fallback whatever the test selected.
  const sort = add("select", "tool-sort") as HTMLSelectElement;
  for (const value of ["relevance", "name-asc", "name-desc"]) {
    const opt = document.createElement("option");
    opt.value = value;
    sort.appendChild(opt);
  }
  add("output", "tool-results-count");
  add("div", "tool-search-results").className = "list-container tool-search-results";
}

function addPill(id: string, label: string, aria: string): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.id = id;
  btn.type = "button";
  btn.className = "action-pill";
  btn.setAttribute("aria-label", aria);
  btn.appendChild(document.createElementNS("http://www.w3.org/2000/svg", "svg"));
  const span = document.createElement("span");
  span.textContent = label;
  btn.appendChild(span);
  document.body.appendChild(btn);
  return btn;
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

function listWith(tools: ToolInfo[], job?: Job): Inventory {
  return { tools, system: [{ name: "git", installed: true }], ...(job ? { job } : {}) };
}

function seedLoad(data: Inventory): void {
  mocks.loadDispatch.mockImplementation(
    (_args: undefined, opts?: { onSuccess?: (d: Inventory) => void }) => {
      opts?.onSuccess?.(data);
      return Promise.resolve(data);
    },
  );
}

function initWith(data: Inventory): void {
  seedLoad(data);
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

  it("delete asks once and forces when the row already names its dependents", async () => {
    initWith(listWith([tool({ name: "java", dependents: ["jdtls"] })]));
    mocks.confirm.mockResolvedValue(true);
    mocks.deleteDispatch.mockResolvedValue({ job: { id: "tj-4" } });

    rowFor("java")?.querySelector<HTMLButtonElement>('button[aria-label="Remove java"]')?.click();
    await flush();
    await flush();

    // No unforced probe: the refusal was already known, so the round trip
    // the 409 used to buy is gone.
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(String(mocks.confirm.mock.calls[0]?.[0])).toContain("jdtls");
    expect(mocks.deleteDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.deleteDispatch).toHaveBeenCalledWith({ name: "java", force: true });
  });

  it("disable asks once and forces when the row already names its dependents", async () => {
    initWith(listWith([tool({ name: "typescript", dependents: ["typescript-language-server"] })]));
    mocks.confirm.mockResolvedValue(true);
    mocks.patchDispatch.mockResolvedValue({ job: { id: "tj-5" } });

    rowFor("typescript")?.querySelector<HTMLInputElement>('input[type="checkbox"]')?.click();
    await flush();
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(String(mocks.confirm.mock.calls[0]?.[0])).toContain("typescript-language-server");
    expect(mocks.patchDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.patchDispatch).toHaveBeenCalledWith({
      name: "typescript",
      disabled: true,
      force: true,
    });
  });

  it("restores the switch when the disable pre-flight is declined", async () => {
    initWith(listWith([tool({ name: "java", dependents: ["jdtls"] })]));
    mocks.confirm.mockResolvedValue(false);
    const box = rowFor("java")?.querySelector<HTMLInputElement>('input[type="checkbox"]');
    box?.click();
    await flush();
    await flush();

    // Nothing moved server-side, so every keyed component is unchanged and
    // reconcile reuses the node — the switch has to be put back by hand.
    expect(mocks.patchDispatch).not.toHaveBeenCalled();
    expect(box?.checked).toBe(true);
    expect(box?.disabled).toBe(false);
  });

  it("still cascades through the 409 when the row named no dependents", async () => {
    // The field is advisory: the engine re-derives the set under the manifest
    // lock, so a row rendered before a dependent was enabled is still refused.
    initWith(listWith([tool({ name: "java" })]));
    mocks.confirm.mockResolvedValue(true);
    mocks.patchDispatch
      .mockResolvedValueOnce({ code: "has_dependents", dependents: ["jdtls"] })
      .mockResolvedValueOnce({ job: { id: "tj-6" } });

    rowFor("java")?.querySelector<HTMLInputElement>('input[type="checkbox"]')?.click();
    await flush();
    await flush();

    expect(mocks.patchDispatch).toHaveBeenNthCalledWith(1, { name: "java", disabled: true });
    expect(mocks.patchDispatch).toHaveBeenNthCalledWith(2, {
      name: "java",
      disabled: true,
      force: true,
    });
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

  // ONE list rather than two labelled blocks. The old split put every catalog
  // hit ahead of every Debian one whatever it scored, which is what buried the
  // best answer; both rows still appear, each carrying its own version, so no
  // real choice is hidden.
  it("renders catalog and apt hits in one list, each row carrying its version", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({
      results: [
        { name: "jq", source: "aqua:jqlang/jq", version: "1.8.1", description: "json" },
        { name: "jq", source: "apt:jq", version: "1.7.1-3", apt: true, description: "distro json" },
      ],
      apt_available: true,
    });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();

    const box = byId("tool-search-results");
    expect(box.querySelectorAll(".tool-hit")).toHaveLength(2);
    // No block heads: the two corpora are one list now.
    expect(box.querySelectorAll(".tool-block-head")).toHaveLength(0);
    const text = box.textContent ?? "";
    // Both versions present: that is why both rows are kept rather than deduped.
    expect(text).toContain("1.8.1");
    expect(text).toContain("1.7.1-3");
    expect(byId("tool-results-count").textContent).toBe("2 shown");
  });

  // Chips on their own line under the name. Inline after the name they started
  // wherever that name ended, so a column of rows put them at as many
  // different offsets as there were name lengths.
  //
  // The name is a DIRECT child of `.tool-hit-text` rather than wrapped in a
  // `.tool-hit-title` block: that block carried no CSS rule, and `overflow` does
  // not apply to a non-replaced inline box, so the ellipsis `.list-row-name`
  // declares could never fire through it.
  it("puts every row's chips in a row of their own, under the name", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({
      results: [{ name: "pyright", source: "npm:pyright", version: "1.1.0", lsp: true }],
    });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();

    const text = byId("tool-search-results").querySelector(".tool-hit-text");
    const kids = [...(text?.children ?? [])].map((e) => e.className);
    expect(kids).toEqual(["list-row-name", "tool-hit-chips", "tool-hit-desc"]);
    // Every chip is in the chip row, none left beside the name.
    expect(text?.querySelectorAll(".list-row-name .tool-source-chip")).toHaveLength(0);
    expect(
      [...(text?.querySelectorAll(".tool-hit-chips .tool-source-chip") ?? [])].map(
        (e) => e.textContent,
      ),
    ).toEqual(["npm", "1.1.0", "LSP"]);
  });

  // The order picker re-paints what is already in hand. Re-querying for it
  // would also throw the server's relevance order away and then ask for it
  // back, and relevance is the one order only the server can produce (it scores
  // both corpora on one scale, and aliases never reach the client).
  it("reorders without a second search, and relevance is the server's own order", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({
      results: [
        { name: "python3", source: "apt:python3", apt: true },
        { name: "black", source: "aqua:psf/black", description: "Python formatter" },
      ],
    });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();

    const names = (): string[] =>
      [...byId("tool-search-results").querySelectorAll(".list-row-name")].map(
        (e) => e.textContent ?? "",
      );
    expect(names()).toEqual(["python3", "black"]);
    const searches = mocks.searchDispatch.mock.calls.length;

    const sort = byId<HTMLSelectElement>("tool-sort");
    sort.value = "name-asc";
    sort.dispatchEvent(new Event("change"));
    expect(names()).toEqual(["black", "python3"]);

    sort.value = "name-desc";
    sort.dispatchEvent(new Event("change"));
    expect(names()).toEqual(["python3", "black"]);

    sort.value = "relevance";
    sort.dispatchEvent(new Event("change"));
    expect(names()).toEqual(["python3", "black"]);
    expect(mocks.searchDispatch.mock.calls.length).toBe(searches);
  });

  // An apt row is not a catalog entry, so the engine has no source to hydrate
  // from and the request must carry it. A catalog row omits it, which is what
  // lets the engine resolve the source it published.
  it("sends the source for an apt hit and omits it for a catalog hit", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({
      results: [{ name: "sl", source: "apt:sl", version: "5.02-1", apt: true }],
      apt_available: true,
    });
    mocks.createDispatch.mockResolvedValue({ job: { id: "tj-9" } });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();

    byId("tool-search-results").querySelector<HTMLButtonElement>("button")?.click();
    await flush();
    expect(mocks.createDispatch).toHaveBeenCalledWith({ name: "sl", source: "apt:sl" });
  });

  // With apt unavailable the engine returns no Debian hits at all, so silence
  // would leave a reader unable to tell "no such package" from "this container
  // cannot install one".
  //
  // Both halves are read off the FOOTER rather than the result list: the shell
  // sentence is permanent markup at the modal's bottom, and the apt caveat is the
  // one clause the module decides. Inside the capped scroller they scrolled away
  // from exactly the empty result that needed them.
  it("says why Debian packages are missing when apt is unavailable", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({ results: [], apt_available: false });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();

    const note = byId("tool-shell-note");
    expect(byId("tool-shell-note-apt").classList.contains("hidden")).toBe(false);
    expect(note.textContent ?? "").toContain("apt needs root");
    // The shell is always the fallback, and it is out of the list entirely.
    expect(note.textContent ?? "").toContain("Install it in the shell");
    expect(byId("tool-search-results").textContent ?? "").not.toContain("Install it in the shell");
  });

  // The caveat is a fact about the RESULT SET, so an available-apt search has to
  // take it back down: a footer is permanent, so a one-way write would leave the
  // claim standing over a search that contradicts it.
  it("drops the apt caveat again once apt is available", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({ results: [], apt_available: false });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();
    expect(byId("tool-shell-note-apt").classList.contains("hidden")).toBe(false);

    mocks.searchDispatch.mockResolvedValue({ results: [], apt_available: true });
    const input = byId<HTMLInputElement>("tool-search");
    input.value = "sl";
    byId<HTMLButtonElement>("tool-search-btn").click();
    await flush();
    expect(byId("tool-shell-note-apt").classList.contains("hidden")).toBe(true);
  });

  // The bar was a debounced input alone: no button, no Enter, and nothing to
  // press when a reader wanted the search to run now. Both immediate doors CANCEL
  // the pending debounce rather than racing it into a second identical query,
  // which is what the call count pins.
  it("searches on the button and on Enter, once per gesture", async () => {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({ results: [] });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();
    const opening = mocks.searchDispatch.mock.calls.length;

    const input = byId<HTMLInputElement>("tool-search");
    input.value = "ripgrep";
    input.dispatchEvent(new Event("input"));
    byId<HTMLButtonElement>("tool-search-btn").click();
    await flush();
    expect(mocks.searchDispatch).toHaveBeenLastCalledWith({ q: "ripgrep" });
    expect(mocks.searchDispatch.mock.calls.length).toBe(opening + 1);

    input.value = "terraform";
    input.dispatchEvent(new Event("input"));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", cancelable: true }));
    await flush();
    expect(mocks.searchDispatch).toHaveBeenLastCalledWith({ q: "terraform" });
    expect(mocks.searchDispatch.mock.calls.length).toBe(opening + 2);

    // The debounce is still armed for the plain typing path; neither gesture
    // left a trailing timer behind that would fire a third query.
    await new Promise((r) => setTimeout(r, 300));
    expect(mocks.searchDispatch.mock.calls.length).toBe(opening + 2);
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
    const job: Job = { id: "tj-boot", kind: "sync", state: "running", created_at: 1 };
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

describe("a job-owning pill becomes its own cancel control", () => {
  const live = (id: string, kind: string): unknown => ({
    job: { id, kind, state: "running", created_at: 1 },
  });
  const settled = (id: string, kind: string): unknown => ({
    job: { id, kind, state: "done", created_at: 1 },
  });

  function faceOf(id: string): { label: string; aria: string; tip: string; busy: boolean } {
    const btn = byId<HTMLButtonElement>(id);
    return {
      label: btn.querySelector("span")?.textContent ?? "",
      aria: btn.getAttribute("aria-label") ?? "",
      tip: btn.getAttribute("data-tooltip") ?? "",
      busy: btn.classList.contains("is-busy"),
    };
  }

  it("Refresh catalog launches, turns into a spinning Cancel, cancels, and comes back", () => {
    mountToolsDOM();
    initTools();
    const btn = byId<HTMLButtonElement>("tool-catalog-refresh-btn");
    btn.click();
    expect(mocks.refreshCatalogDispatch).toHaveBeenCalledTimes(1);

    const sse = mocks.sseHandlers.get("tool_job_changed");
    expect(sse).toBeDefined();
    sse?.("", live("tj-1", "catalog-refresh"));

    expect(faceOf("tool-catalog-refresh-btn")).toEqual({
      label: "Cancel",
      aria: "Cancel the running catalog refresh",
      // The visible word stays short so the pill does not resize its row; the
      // full meaning reaches a pointer and a screen reader instead.
      tip: "Cancel the running catalog refresh",
      busy: true,
    });
    // Clickable, not disabled: the busy face IS the cancel affordance.
    expect(btn.disabled).toBe(false);
    expect(btn.querySelector(".icon-spinner")).not.toBeNull();
    // No second Cancel pill beside it, which is the whole point.
    expect(byId("tool-cancel-btn").classList.contains("hidden")).toBe(true);

    btn.click();
    expect(mocks.cancelJobDispatch).toHaveBeenCalledWith({ id: "tj-1" });
    expect(faceOf("tool-catalog-refresh-btn").label).toBe("Cancelling…");
    // A second click does not re-send a cancel already on the wire.
    btn.click();
    expect(mocks.cancelJobDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.refreshCatalogDispatch).toHaveBeenCalledTimes(1);

    sse?.("", settled("tj-1", "catalog-refresh"));
    expect(faceOf("tool-catalog-refresh-btn")).toEqual({
      label: "Refresh catalog",
      aria: "Refresh the tool catalog",
      // Dropped, not left pointing at work that has finished.
      tip: "",
      busy: false,
    });
    btn.click();
    expect(mocks.refreshCatalogDispatch).toHaveBeenCalledTimes(2);
  });

  it("Update all carries the cancel for an update job, per-tool updates included", () => {
    mountToolsDOM();
    initTools();
    const sse = mocks.sseHandlers.get("tool_job_changed");

    sse?.("", { job: { id: "tj-2", kind: "update", names: ["gh"], state: "running" } });
    expect(faceOf("tool-update-btn")).toEqual({
      label: "Cancel",
      aria: "Cancel the running update",
      tip: "Cancel the running update",
      busy: true,
    });
    // The sibling pill is untouched: one kind, one owner.
    expect(faceOf("tool-catalog-refresh-btn").busy).toBe(false);
    expect(byId("tool-cancel-btn").classList.contains("hidden")).toBe(true);

    byId<HTMLButtonElement>("tool-update-btn").click();
    expect(mocks.cancelJobDispatch).toHaveBeenCalledWith({ id: "tj-2" });
    expect(mocks.updateDispatch).not.toHaveBeenCalled();

    sse?.("", { job: { id: "tj-2", kind: "update", names: ["gh"], state: "cancelled" } });
    expect(faceOf("tool-update-btn").label).toBe("Update all");
  });

  it("falls back to the shared Cancel pill for a job no pill owns", () => {
    mountToolsDOM();
    initTools();
    const sse = mocks.sseHandlers.get("tool_job_changed");

    sse?.("", live("tj-3", "install"));
    expect(byId("tool-cancel-btn").classList.contains("hidden")).toBe(false);
    expect(faceOf("tool-update-btn").busy).toBe(false);
    expect(faceOf("tool-catalog-refresh-btn").busy).toBe(false);

    byId<HTMLButtonElement>("tool-cancel-btn").click();
    expect(mocks.cancelJobDispatch).toHaveBeenCalledWith({ id: "tj-3" });

    sse?.("", settled("tj-3", "install"));
    expect(byId("tool-cancel-btn").classList.contains("hidden")).toBe(true);
    // A settled job is not cancellable: the follow target outlives it, the
    // live-job reference does not.
    byId<HTMLButtonElement>("tool-cancel-btn").click();
    expect(mocks.cancelJobDispatch).toHaveBeenCalledTimes(1);
  });

  it("seeds the busy face from a job already running when the panel opens", () => {
    mocks.jobsDispatch.mockResolvedValue(null);
    initWith(
      listWith([tool({ name: "gh" })], {
        id: "tj-boot",
        kind: "update",
        state: "running",
        created_at: 1,
      }),
    );
    expect(faceOf("tool-update-btn").busy).toBe(true);
  });

  it("lets the stream outrank a snapshot that names a job it already finished", () => {
    // Inventory.job is a snapshot and loadToolsList runs once per job event, so a
    // GET issued while a job was queued can resolve after the event that finished
    // it. Adopting it would strand the pill on Cancel forever.
    mountToolsDOM();
    initTools();
    mocks.sseHandlers.get("tool_job_changed")?.("", settled("tj-4", "update"));
    seedLoad(
      listWith([tool({ name: "gh" })], {
        id: "tj-4",
        kind: "update",
        state: "running",
        created_at: 1,
      }),
    );
    loadToolsList();
    expect(faceOf("tool-update-btn").busy).toBe(false);
  });
});

describe("catalog refresh UI", () => {
  it("renders the freshness line with catalog age and the failure suffix", async () => {
    mountToolsDOM();
    initTools();
    mocks.catalogInfoDispatch.mockImplementation(
      (_arg: unknown, opts?: { onSuccess?: (info: unknown) => void }) => {
        opts?.onSuccess?.({
          entries: 716,
          refs: { mise: "v2026.7.11", aqua: "v4.541.0" },
          generated: new Date(Date.now() - 3 * 3600 * 1000).toISOString(),
          source: "remote",
          url: "https://example.invalid/tool-catalog.json",
          fetched_at: Date.now() - 60 * 1000,
          last_error: "fetch catalog: boom",
          scheduled: true,
        });
        return Promise.resolve(null);
      },
    );
    loadToolsList();
    await Promise.resolve();
    const meta = byId<HTMLParagraphElement>("tool-catalog-meta");
    expect(meta.classList.contains("hidden")).toBe(false);
    expect(meta.textContent).toContain("716 tools");
    expect(meta.textContent).toContain("aqua v4.541.0 + mise v2026.7.11");
    expect(meta.textContent).toContain("compiled 3 h ago");
    expect(meta.textContent).toContain("checked 1 min ago");
    expect(meta.textContent).toContain("auto-refresh on");
    expect(meta.textContent).toContain("last refresh failed");
  });

  it("shows auto-refresh off when the schedule is disabled", async () => {
    mountToolsDOM();
    initTools();
    mocks.catalogInfoDispatch.mockImplementation(
      (_arg: unknown, opts?: { onSuccess?: (info: unknown) => void }) => {
        opts?.onSuccess?.({
          entries: 716,
          source: "baked",
          url: "https://example.invalid/tool-catalog.json",
          scheduled: false,
        });
        return Promise.resolve(null);
      },
    );
    loadToolsList();
    await Promise.resolve();
    const meta = byId<HTMLParagraphElement>("tool-catalog-meta");
    expect(meta.textContent).toContain("auto-refresh off");
  });
});

// ---------------------------------------------------------------------------
// Reconcile list keys (keyenc `join`).
//
// This list was ALREADY injective before the adoption: a tool name is
// validated colon-free and unique server-side, so the `tool:<name>:` prefix
// could not be forged. The keys are joined for uniformity with the app's other
// composite keys, and these tests pin the encoding so a future loosening of
// any component can't reintroduce ambiguity. Had a collision been possible the
// effect would be a REMOUNT of the earlier duplicate on every pass (dropped
// focus, restarted animation), never a dropped row.
// ---------------------------------------------------------------------------

describe("tools list keys", () => {
  function keyFor(name: string): string {
    return rowFor(name)?.getAttribute(KEY_ATTR) ?? "";
  }

  it("emits verbatim components for ordinary tool rows", () => {
    initWith(listWith([tool({ name: "gh", latest: "2.97.0", pin: true })]));
    // Twelve components: marker, name, version, latest, installed, installing,
    // pin, disabled, dependents, checksum, essential, error-state.
    expect(keyFor("gh")).toBe("tool:gh:1.0.0:2.97.0:true:false:true:false:::false:ok");
    expect(split(keyFor("gh"))).toHaveLength(12);
  });

  it("keys the dependents set so a pre-flight cannot read a stale row", () => {
    // The disable/remove pre-flight reads the row's captured ToolInfo, and
    // enabling a dependent elsewhere changes this set without touching any
    // other component. Out of the key, the row would never remount.
    initWith(listWith([tool({ name: "java", dependents: ["jdtls", "kotlin-ls"] })]));
    expect(split(keyFor("java"))[8]).toBe("jdtls,kotlin-ls");
  });

  it("keys the label and system branches in their own namespaces", () => {
    initWith(listWith([tool({ name: "gh" })]));
    expect(keyFor("git")).toBe("sys:git");
    const label = document.querySelector<HTMLElement>("#tools-list .list-group-label");
    expect(label?.getAttribute(KEY_ATTR)).toBe(join("label", "built into the image"));
  });

  it("escapes a component that carries the separator instead of shifting the split", () => {
    // A colon in a version string would have added a component under the old
    // array-join; escaped, the key still splits into exactly twelve.
    initWith(listWith([tool({ name: "odd", version: "1.0:beta" })]));
    const key = keyFor("odd");
    expect(key).toContain("1.0\\:beta");
    expect(split(key)).toEqual([
      "tool",
      "odd",
      "1.0:beta",
      "",
      "true",
      "false",
      "false",
      "false",
      "",
      "",
      "false",
      "ok",
    ]);
  });

  it("keeps a missing optional field distinct from a name that mimics it", () => {
    // Two rows whose OLD keys ("tool:a:1.0.0:..." shapes) are distinguished
    // only because each component boundary is now unforgeable.
    initWith(listWith([tool({ name: "a", version: "1.0.0:2.0.0" }), tool({ name: "b" })]));
    expect(keyFor("a")).not.toBe(keyFor("b"));
    expect(split(keyFor("a"))[2]).toBe("1.0.0:2.0.0");
  });
});

// ---------------------------------------------------------------------------
// Pre-bundled rows, and the honesty chips.
// ---------------------------------------------------------------------------

describe("pre-bundled tools", () => {
  function labels(): string[] {
    return [...document.querySelectorAll<HTMLElement>("#tools-list .list-group-label")].map(
      (e) => e.textContent ?? "",
    );
  }

  it("groups an essential entry ahead of the ones the user added, labelling both", () => {
    initWith(listWith([tool({ name: "ripgrep" }), tool({ name: "gh", essential: true })]));
    expect(labels()).toEqual([
      "pre-bundled, kept current by the catalog",
      "added by you",
      "built into the image",
    ]);
    const names = [...document.querySelectorAll<HTMLElement>("#tools-list .list-row-name")].map(
      (e) => e.textContent ?? "",
    );
    // gh is second in the inventory and first on screen.
    expect(names.slice(0, 2)).toEqual(["gh", "ripgrep"]);
  });

  it("labels nothing when nothing is essential", () => {
    initWith(listWith([tool({ name: "ripgrep" })]));
    expect(labels()).toEqual(["built into the image"]);
  });

  it("offers no remove control on an essential row, but keeps the switch", () => {
    initWith(listWith([tool({ name: "gh", essential: true })]));
    const row = rowFor("gh");
    expect(row?.querySelector('[aria-label="Remove gh"]')).toBeNull();
    expect(row?.querySelector(".tool-toggle")).not.toBeNull();
    // The box is reserved so the switch column does not step between groups,
    // and the ghost is out of the accessibility tree.
    const ghost = row?.querySelector(".list-row-btn-ghost");
    expect(ghost).not.toBeNull();
    expect(ghost?.getAttribute("aria-hidden")).toBe("true");
    expect(ghost?.tagName).toBe("SPAN");
  });

  it("keeps the remove control on a row the user added", () => {
    initWith(listWith([tool({ name: "ripgrep" })]));
    const row = rowFor("ripgrep");
    expect(row?.querySelector('[aria-label="Remove ripgrep"]')).not.toBeNull();
    expect(row?.querySelector(".list-row-btn-ghost")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The apt group: Debian packages the engine does not manage.
// ---------------------------------------------------------------------------

describe("unmanaged apt packages", () => {
  /** The read-only rows, minus the system group's own (`git`, from listWith). */
  function aptRows(): string[] {
    return [...document.querySelectorAll<HTMLElement>("#tools-list .list-row-system")]
      .map((r) => r.querySelector(".list-row-name")?.textContent ?? "")
      .filter((n) => n !== "git");
  }

  function rowFor(name: string): HTMLElement | undefined {
    return [...document.querySelectorAll<HTMLElement>("#tools-list .list-row")].find(
      (r) => r.querySelector(".list-row-name")?.textContent === name,
    );
  }

  it("lists them under their own label, with the version and an apt chip", () => {
    initWith({
      ...listWith([tool({ name: "ripgrep" })]),
      apt_packages: [
        { name: "gcc", version: "4:14.2.0-1" },
        { name: "libc6-dev", version: "2.41-12" },
      ],
    });
    const labels = [...document.querySelectorAll<HTMLElement>("#tools-list .list-group-label")].map(
      (e) => e.textContent ?? "",
    );
    expect(labels).toContain("installed with apt, outside the engine");
    expect(aptRows()).toEqual(["gcc", "libc6-dev"]);

    const gcc = rowFor("gcc");
    expect(gcc?.querySelector(".tool-source-chip")?.textContent).toBe("apt");
    expect(gcc?.textContent).toContain("4:14.2.0-1");
    // Read-only, and asserted rather than assumed: no manifest row stands
    // behind one, so the engine can neither update it nor prove nothing else
    // needs it. A control here would offer an action that cannot be honoured.
    expect(gcc?.querySelector("button")).toBeNull();
    expect(gcc?.querySelector("input")).toBeNull();
  });

  it("renders no group when the engine could not answer, and none when it answered empty", () => {
    // Absent and empty mean different things — apt is not this host's package
    // manager or the enumeration failed, against nothing unmanaged is installed
    // — and this is the one place the distinction does not reach the reader.
    initWith(listWith([tool({ name: "ripgrep" })]));
    expect(aptRows()).toEqual([]);
    initWith({ ...listWith([tool({ name: "ripgrep" })]), apt_packages: [] });
    expect(aptRows()).toEqual([]);
  });
});

describe("row honesty chips", () => {
  function chips(name: string): string[] {
    return [...(rowFor(name)?.querySelectorAll<HTMLElement>(".tool-source-chip") ?? [])].map(
      (e) => e.textContent ?? "",
    );
  }

  it("chips an apt row, and reads the source rather than the checksum", () => {
    initWith(listWith([tool({ name: "gcc", source: "apt:gcc" })]));
    expect(chips("gcc")).toEqual(["apt"]);
  });

  it("chips a hand-installed row, replacing updateOne's silence", () => {
    initWith(listWith([tool({ name: "codeql", source: "manual" })]));
    expect(chips("codeql")).toEqual(["self-managed"]);
  });

  it("chips an unverified download and says nothing about a verified one", () => {
    initWith(
      listWith([
        tool({ name: "node", checksum: "unverified" }),
        tool({ name: "ripgrep", checksum: "verified" }),
      ]),
    );
    expect(chips("node")).toEqual(["no checksum"]);
    expect(chips("ripgrep")).toEqual([]);
  });

  it("says nothing for a package-manager source, which reports no checksum at all", () => {
    initWith(listWith([tool({ name: "prettier", source: "npm:prettier" })]));
    expect(chips("prettier")).toEqual([]);
  });

  it("keeps the LSP badge alongside an honesty chip", () => {
    initWith(listWith([tool({ name: "gopls", lsp: true, checksum: "unverified" })]));
    expect(chips("gopls")).toEqual(["LSP", "no checksum"]);
  });
});

// ---------------------------------------------------------------------------
// Real-layout guards, measured against the shipped stylesheet.
//
// Both defects here were geometry, so neither is reachable from a structural
// assertion: the DOM was correct in each case and the boxes were not.
// ---------------------------------------------------------------------------

describe("a capped result list never shrinks a row below its content", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  /** Enough hits to overflow `.tool-search-results`' 17rem cap, each with the
   *  three-line body a real hit renders (name, chips, description). Below the cap
   *  there is no shrink pressure and the defect cannot appear. */
  async function overflowingResults(n: number): Promise<HTMLElement[]> {
    initWith(listWith([]));
    mocks.searchDispatch.mockResolvedValue({
      results: Array.from({ length: n }, (_v, i) => ({
        name: `tool-number-${String(i)}`,
        source: "aqua:owner/repo",
        version: "1.2.3",
        lsp: true,
        description: "A representative catalog description for this entry",
      })),
    });
    byId<HTMLButtonElement>("tool-add-btn").click();
    await flush();
    return [...byId("tool-search-results").querySelectorAll<HTMLElement>(".tool-hit")];
  }

  it("gives every row a box at least as tall as what it paints", async () => {
    const rows = await overflowingResults(12);
    expect(rows).toHaveLength(12);

    for (const row of rows) {
      // `.list-row` states a `min-block-size`, which REPLACES a flex item's
      // automatic minimum size — so without `flex-shrink: 0` the row collapsed to
      // that floor under content needing three times it, and with no `overflow`
      // here it painted the surplus over the rows below.
      expect(
        row.clientHeight,
        `${row.textContent ?? ""} overflows its own box by ${String(row.scrollHeight - row.clientHeight)}px`,
      ).toBeGreaterThanOrEqual(row.scrollHeight);

      // The same fact from the paint side: no child may cross the row's edge.
      const box = row.getBoundingClientRect();
      for (const kid of row.children) {
        const k = kid.getBoundingClientRect();
        expect(k.bottom, `${kid.className} paints below its row`).toBeLessThanOrEqual(
          box.bottom + 0.5,
        );
        expect(k.top, `${kid.className} paints above its row`).toBeGreaterThanOrEqual(
          box.top - 0.5,
        );
      }
    }
  });

  it("still honours the 17rem cap, so the fix is the row and not the scroller", async () => {
    const rows = await overflowingResults(12);
    const box = byId("tool-search-results");

    // 272px is 17rem. The container is what scrolls; the rows keep their height.
    expect(box.clientHeight).toBeLessThanOrEqual(273);
    expect(box.scrollHeight).toBeGreaterThan(box.clientHeight);
    const total = rows.reduce((sum, r) => sum + r.getBoundingClientRect().height, 0);
    expect(total).toBeGreaterThan(box.clientHeight);
  });
});

describe("an installed row measures the same whether or not it carries a badge", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
    document.documentElement.removeAttribute("data-pointer");
  });

  /** One row that earns a chip and one that earns none, rendered by the real
   *  row builder. `apt` is the cheapest chip to provoke; a plain aqua source with
   *  a verified checksum earns nothing. */
  function twoRows(): { badged: HTMLElement; plain: HTMLElement } {
    initWith(
      listWith([
        tool({ name: "gcc", source: "apt:gcc" }),
        tool({ name: "ripgrep", checksum: "verified" }),
      ]),
    );
    const badged = rowFor("gcc");
    const plain = rowFor("ripgrep");
    expect(badged?.querySelector(".tool-source-chip")).not.toBeNull();
    expect(plain?.querySelector(".tool-source-chip")).toBeNull();
    if (badged === null || plain === null) {
      throw new Error("both rows must render");
    }
    return { badged, plain };
  }

  it("keeps the chip inline with the name on a fine pointer", () => {
    document.documentElement.dataset["pointer"] = "fine";
    const { badged, plain } = twoRows();

    // The wrap was an unconditional flex COLUMN, so the chip took its own line
    // and added 20-22px to every badged row — the reported ragged column.
    const name = badged.querySelector<HTMLElement>(".list-row-name");
    const chip = badged.querySelector<HTMLElement>(".tool-source-chip");
    expect(name).not.toBeNull();
    expect(chip).not.toBeNull();
    const nameBox = name?.getBoundingClientRect();
    const chipBox = chip?.getBoundingClientRect();
    // Vertical OVERLAP, not a shared top: the chip's font is smaller than the name's,
    // so the two boxes never share an edge and how far apart their tops sit is a
    // property of the two font stacks rather than of the layout. A top-plus-tolerance
    // assertion therefore reads a different fontconfig as a regression — measured 1.7px
    // apart on a CI runner against 0.3px locally, against a 1px tolerance. A chip on
    // the name's line always STARTS before that line ends, whatever the metrics, and
    // the coarse-pointer test below asserts the exact inverse: that pair is what pins
    // the row-versus-column decision from both sides.
    expect(chipBox?.top, "the chip shares the name's line").toBeLessThan(nameBox?.bottom ?? 0);
    expect(chipBox?.left, "the chip sits after the name").toBeGreaterThan(nameBox?.right ?? 0);

    expect(badged.getBoundingClientRect().height).toBeCloseTo(
      plain.getBoundingClientRect().height,
      1,
    );
  });

  it("gives the chip its own line under a finger, at no cost in row height", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    const { badged, plain } = twoRows();

    const nameBox = badged.querySelector<HTMLElement>(".list-row-name")?.getBoundingClientRect();
    const chipBox = badged.querySelector<HTMLElement>(".tool-source-chip")?.getBoundingClientRect();
    expect(chipBox?.top, "the chip drops below the name").toBeGreaterThan(nameBox?.bottom ?? 0);

    // And it FITS: both lines sit inside the slack this row's own 44px action
    // buttons already force at this tier, so the column costs no extra height and
    // the table stays uniform on a finger too. A `padding-block` here was measured
    // taking the badged row to 56.4 against the plain one's 52.
    expect(plain.getBoundingClientRect().height).toBeGreaterThanOrEqual(36);
    expect(badged.getBoundingClientRect().height).toBeCloseTo(
      plain.getBoundingClientRect().height,
      1,
    );
  });

  it("centres a chipless row's text in the floored box", () => {
    document.documentElement.dataset["pointer"] = "coarse";
    const { plain } = twoRows();
    const row = plain.getBoundingClientRect();
    const name = plain.querySelector<HTMLElement>(".list-row-name")?.getBoundingClientRect();
    const above = (name?.top ?? 0) - row.top;
    const below = row.bottom - (name?.bottom ?? 0);
    expect(Math.abs(above - below), "the label is vertically centred").toBeLessThanOrEqual(1);
  });
});

describe("the search bar reports that a search is running", () => {
  let style: HTMLStyleElement;

  beforeAll(() => {
    style = mountAppCSS();
  });

  afterAll(() => {
    style.remove();
  });

  const shown = (el: Element | null): boolean =>
    el !== null && getComputedStyle(el).display !== "none";

  it("swaps the magnifier for a spinning glyph while tools.search is pending", () => {
    initWith(listWith([]));
    const btn = byId<HTMLButtonElement>("tool-search-btn");
    const glyph = btn.querySelector(".tool-search-glyph");
    const spinner = btn.querySelector(".tool-search-spinner");

    // Both faces ship in the button, so the swap moves no nodes and cannot
    // resize the row. Idle shows the magnifier alone.
    expect(shown(glyph)).toBe(true);
    expect(shown(spinner)).toBe(false);

    // `is-busy` is the pending class `bindLoadingState("tools.search", …)` adds.
    btn.classList.add("is-busy");
    expect(shown(glyph)).toBe(false);
    expect(shown(spinner)).toBe(true);
    // And it is a SPINNER rather than a static arc: the registry glyph carries
    // `.icon-spinner`, which is what 15-input.css rotates.
    expect(getComputedStyle(spinner as Element).animationName).toBe("vk-spin-rotate");
  });
});
