// @vitest-environment happy-dom
//
// Characterization + regression tests for the agent-ignore chip rendering
// and the supervised-default toggle in permissions-ui.ts. These guard the
// keyed-reconcile behaviour of renderIgnoreChips: add/remove must touch
// only the changed chip and preserve untouched node identity.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { AppSettings } from "./persist.js";

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn<(path: string, signal?: AbortSignal) => Promise<unknown>>(),
  patchSettings: vi.fn<(patch: Partial<AppSettings>) => Promise<void>>(),
}));

// Mock only the I/O edges. ui-primitives (buildChip), reconcile,
// @cplieger/reactive (el), icons, and dom (byId/maybeEl over happy-dom's real
// document) stay real so we exercise the actual DOM behaviour.
vi.mock("./api-client.js", () => ({ apiGet: mocks.apiGet }));
vi.mock("./persist.js", () => ({ patchSettings: mocks.patchSettings }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { dispatch: vi.fn() },
  explainPolicy: { dispatch: vi.fn() },
}));

import { initPermissionsUI } from "./permissions-ui.js";
import { byId } from "./dom.js";

// --- DOM fixture helpers -----------------------------------------------------

function div(id: string): HTMLDivElement {
  const e = document.createElement("div");
  e.id = id;
  return e;
}
function hint(id: string): HTMLParagraphElement {
  const e = document.createElement("p");
  e.id = id;
  e.className = "section-hint hidden";
  return e;
}
function input(id: string): HTMLInputElement {
  const e = document.createElement("input");
  e.id = id;
  e.type = "text";
  return e;
}
function button(id: string): HTMLButtonElement {
  const e = document.createElement("button");
  e.id = id;
  e.type = "button";
  return e;
}
function checkbox(id: string): HTMLInputElement {
  const e = document.createElement("input");
  e.id = id;
  e.type = "checkbox";
  return e;
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

function initWith(ignoreFiles: string[], supervised = false): void {
  const initial: AppSettings = {
    agent_ignore_files: ignoreFiles,
    supervised_default: supervised,
  };
  initPermissionsUI(initial);
}

function ignoreChips(): HTMLElement[] {
  const c = byId<HTMLDivElement>("agent-ignore-chips");
  return Array.from(c.querySelectorAll<HTMLElement>(".chip"));
}
function chipLabel(ch: HTMLElement): string | null {
  return ch.querySelector<HTMLElement>(".chip-label")?.textContent ?? null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();

  // Supervised default toggle.
  document.body.appendChild(checkbox("supervised-default-checkbox"));

  // Agent ignore section.
  document.body.appendChild(div("agent-ignore-chips"));
  document.body.appendChild(hint("agent-ignore-empty-hint"));
  document.body.appendChild(input("agent-ignore-input"));
  document.body.appendChild(button("agent-ignore-add"));

  mocks.patchSettings.mockResolvedValue(undefined);
});

// ---------------------------------------------------------------------------
// Supervised default toggle.
// ---------------------------------------------------------------------------

describe("supervised default toggle", () => {
  it("seeds from settings and PATCHes on change", async () => {
    initWith([], true);
    const box = byId<HTMLInputElement>("supervised-default-checkbox");
    expect(box.checked).toBe(true);

    box.checked = false;
    box.dispatchEvent(new Event("change"));
    await flush();

    expect(mocks.patchSettings).toHaveBeenCalledWith({ supervised_default: false });
  });
});

// ---------------------------------------------------------------------------
// Agent ignore chips: keyed reconcile add/remove preserves node identity.
// ---------------------------------------------------------------------------

describe("renderIgnoreChips — keyed reconcile", () => {
  it("clears and refocuses the path input after an add (repeat entry)", async () => {
    initWith([]);

    const pathInput = byId<HTMLInputElement>("agent-ignore-input");
    pathInput.value = ".kiroignore";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(ignoreChips()).toHaveLength(1);
    expect(pathInput.value).toBe("");
    expect(document.activeElement).toBe(pathInput);
  });

  it("preserves existing chip nodes on add and drops only the removed one", async () => {
    initWith([".gitignore", ".env.dec"]);

    expect(ignoreChips()).toHaveLength(2);
    const gitignore = ignoreChips().find((c) => chipLabel(c) === ".gitignore");
    expect(gitignore).toBeDefined();
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(true);

    // Add a third entry; the keyed reconcile must reuse the existing
    // .gitignore node rather than rebuilding the whole row.
    byId<HTMLInputElement>("agent-ignore-input").value = ".kiroignore";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(ignoreChips()).toHaveLength(3);
    const gitignoreAfter = ignoreChips().find((c) => chipLabel(c) === ".gitignore");
    expect(gitignoreAfter).toBe(gitignore);

    // Remove .kiroignore via its remove button -> only that chip goes.
    const kiro = ignoreChips().find((c) => chipLabel(c) === ".kiroignore");
    kiro?.querySelector<HTMLButtonElement>(".chip-remove")?.click();
    await flush();

    expect(ignoreChips()).toHaveLength(2);
    expect(ignoreChips().some((c) => chipLabel(c) === ".kiroignore")).toBe(false);
    expect(ignoreChips().find((c) => chipLabel(c) === ".gitignore")).toBe(gitignore);
  });

  it("ignores duplicate adds", async () => {
    initWith([".gitignore"]);

    byId<HTMLInputElement>("agent-ignore-input").value = ".gitignore";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(ignoreChips()).toHaveLength(1);
    expect(mocks.patchSettings).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// Empty states live in sibling hint elements, not inside the chip containers.
// ---------------------------------------------------------------------------

describe("empty states", () => {
  it("shows the sibling hint and keeps the chip container free of stray nodes", () => {
    initWith([]);

    const ignoreContainer = byId<HTMLDivElement>("agent-ignore-chips");
    expect(ignoreContainer.querySelectorAll(".chip")).toHaveLength(0);
    expect(ignoreContainer.querySelector("p")).toBeNull();
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(false);
  });

  it("hides the hint once an ignore file is present", () => {
    initWith([".gitignore"]);
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(true);
    expect(ignoreChips()).toHaveLength(1);
  });
});
