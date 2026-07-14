// @vitest-environment happy-dom
//
// Regression tests for the tool edit modal's save path (saveToolFromModal).
// Guards the C2 merge fix: editing a tool must MERGE the form fields into
// the existing manifest entry, never rebuild it from scratch. Pre-populated
// entries carry fields the form never shows (enabled, auto_update, requires,
// update, method, shims, sha256, ...) — the old from-scratch rebuild
// silently destroyed them (install method fell back to "binary", dependency
// ordering broke, disabled entries flipped enabled) and the corrupted
// manifest was then PUT back over the good copy.
import { describe, it, expect, beforeEach, vi } from "vitest";

type ToolsData = Record<string, Record<string, Record<string, unknown>>>;

const mocks = vi.hoisted(() => ({
  loadDispatch: vi.fn(),
  loadCancel: vi.fn(),
  saveDispatch: vi.fn(),
  installDispatch: vi.fn(),
  seedDispatch: vi.fn(),
  enableDispatch: vi.fn(),
  deleteDispatch: vi.fn(),
  patchDispatch: vi.fn(),
  openModal: vi.fn(),
  closeModal: vi.fn(),
  confirm: vi.fn(),
}));

// Mock only the I/O + modal edges. dom (byId/$ over happy-dom's real
// document), reconcile, icons/icon-el, and @cplieger/reactive (el) stay real
// so the row render + edit-button click path is the actual DOM behaviour.
vi.mock("./modals.js", () => ({
  openModal: mocks.openModal,
  closeModal: mocks.closeModal,
  RollingOutput: class {
    clear(): void {
      /* noop */
    }
    append(_s: string): void {
      /* noop */
    }
  },
}));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirm }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/tools.js", () => ({
  installTools: { dispatch: mocks.installDispatch },
  saveTools: { dispatch: mocks.saveDispatch },
  seedMcp: { dispatch: mocks.seedDispatch },
  loadTools: { dispatch: mocks.loadDispatch, cancel: mocks.loadCancel },
  enableTool: { dispatch: mocks.enableDispatch },
  deleteTool: { dispatch: mocks.deleteDispatch },
  patchTool: { dispatch: mocks.patchDispatch },
}));

import { initTools, loadToolsList } from "./tools.js";
import { byId } from "./dom.js";

// --- DOM fixture -------------------------------------------------------------

const METHOD_OPTIONS = [
  "go",
  "npm",
  "pip",
  "cargo",
  "apt",
  "binary",
  "runtimes",
  "custom",
  "mcp",
  "lsp",
];

function mountToolsDOM(): void {
  document.body.replaceChildren();
  const add = (tag: string, id: string): HTMLElement => {
    const e = document.createElement(tag);
    e.id = id;
    document.body.appendChild(e);
    return e;
  };
  // Elements the $ registry needs.
  add("button", "tool-add-btn");
  add("button", "tool-update-btn");
  add("div", "tool-update-output");
  add("div", "tools-list");
  add("div", "tool-modal");
  // Modal form fields (byId in tools.ts).
  add("span", "tool-modal-title");
  const cat = add("select", "tool-cat") as HTMLSelectElement;
  for (const m of METHOD_OPTIONS) {
    const opt = document.createElement("option");
    opt.value = m;
    opt.textContent = m;
    cat.appendChild(opt);
  }
  add("input", "tool-name");
  add("input", "tool-version");
  add("input", "tool-install");
  add("input", "tool-binaries");
  add("input", "tool-package");
  add("button", "tool-modal-save");
  // Visibility-toggled labels + hint slots.
  for (const id of [
    "tool-version-label",
    "tool-package-label",
    "tool-install-label",
    "tool-binaries-label",
  ]) {
    add("label", id);
  }
  for (const id of [
    "tool-cat-hint",
    "tool-version-hint",
    "tool-package-hint",
    "tool-install-hint",
  ]) {
    add("p", id);
  }
}

/** Wire the manager against a fresh manifest fixture. */
function initWith(data: ToolsData): void {
  mocks.loadDispatch.mockImplementation(
    (_args: undefined, opts?: { onSuccess?: (d: ToolsData) => void }) => {
      opts?.onSuccess?.(data);
      return Promise.resolve(data);
    },
  );
  initTools();
  loadToolsList();
}

function clickEdit(name: string): void {
  const btn = document.querySelector<HTMLButtonElement>(`button[aria-label="Edit ${name}"]`);
  expect(btn).not.toBeNull();
  btn?.click();
}

function savedData(): ToolsData {
  expect(mocks.saveDispatch).toHaveBeenCalledTimes(1);
  return mocks.saveDispatch.mock.calls[0]?.[0] as ToolsData;
}

beforeEach(() => {
  vi.clearAllMocks();
  mountToolsDOM();
  mocks.saveDispatch.mockResolvedValue({});
});

// ---------------------------------------------------------------------------
// C2: edit merges into the existing entry.
// ---------------------------------------------------------------------------

describe("saveToolFromModal — merge on edit", () => {
  it("editing an lsp entry preserves requires/method/enabled/update/auto_update/shims", () => {
    initWith({
      lsp: {
        pyright: {
          version: "1.1.391",
          method: "npm",
          enabled: true,
          auto_update: false,
          requires: ["runtimes.node"],
          update: { method: "npm" },
          shims: ["pyright-langserver"],
          description: "Python language server",
        },
      },
    });

    clickEdit("pyright");
    // The lsp schema exposes only the version field; bump it.
    byId<HTMLInputElement>("tool-version").value = "1.1.412";
    byId<HTMLButtonElement>("tool-modal-save").click();

    const entry = savedData()["lsp"]?.["pyright"];
    expect(entry).toMatchObject({
      version: "1.1.412", // the one edited field
      method: "npm", // everything below was silently dropped pre-fix
      enabled: true,
      auto_update: false,
      requires: ["runtimes.node"],
      update: { method: "npm" },
      shims: ["pyright-langserver"],
      description: "Python language server",
    });
    expect(mocks.closeModal).toHaveBeenCalledTimes(1);
  });

  it("clearing a VISIBLE field deletes only that key; hidden metadata survives", () => {
    initWith({
      binary: {
        "golangci-lint": {
          version: "v2.0.0",
          install: "curl -fsSL example | tar -xz -C ${BIN}",
          sha256: "abc123",
          enabled: true,
        },
      },
    });

    clickEdit("golangci-lint");
    // The binary schema shows version + install; the user clears install.
    byId<HTMLInputElement>("tool-install").value = "";
    byId<HTMLButtonElement>("tool-modal-save").click();

    const entry = savedData()["binary"]?.["golangci-lint"];
    expect(entry).toBeDefined();
    expect(entry).not.toHaveProperty("install"); // visible + cleared → deleted
    expect(entry).toMatchObject({
      version: "v2.0.0",
      sha256: "abc123", // hidden key: preserved
      enabled: true,
    });
  });

  it("adding a brand-new tool still builds the entry from the form", () => {
    initWith({});

    byId<HTMLButtonElement>("tool-add-btn").click(); // opens the Add modal
    byId<HTMLSelectElement>("tool-cat").value = "go";
    byId<HTMLInputElement>("tool-name").value = "goimports";
    byId<HTMLInputElement>("tool-version").value = "v0.30.0";
    byId<HTMLInputElement>("tool-package").value = "golang.org/x/tools/cmd/goimports@${VERSION}";
    byId<HTMLButtonElement>("tool-modal-save").click();

    expect(savedData()["go"]?.["goimports"]).toEqual({
      version: "v0.30.0",
      package: "golang.org/x/tools/cmd/goimports@${VERSION}",
    });
  });
});
