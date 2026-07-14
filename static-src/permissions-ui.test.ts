// @vitest-environment happy-dom
//
// Characterization + regression tests for the command-rules / agent-ignore
// chip rendering in permissions-ui.ts. These guard the keyed-reconcile
// conversion of renderRuleChips + renderIgnoreChips, and in particular the
// closure-freshness regression a naive key-by-pattern (mount-only) reconcile
// would introduce: after flipping a rule's mode, the chip must re-mount so its
// badge, `chip-rule-<mode>` class, AND `.chip-mode` click closure all reflect
// the new mode. The composite key (pattern:mode:priority) is what makes that
// hold.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { AppSettings } from "./persist.js";
import type { CommandRule } from "./actions/permissions.js";

type RuleMode = "allow" | "deny";

// Shape of the args the controller hands to addRule/removeRule.dispatch.
interface AddDispatchArgs {
  pattern: string;
  mode: RuleMode;
  priority: number;
  rules: CommandRule[];
  setRules: (rules: CommandRule[]) => void;
  getCurrentRules: () => CommandRule[];
}
interface RemoveDispatchArgs {
  pattern: string;
  rules: CommandRule[];
  setRules: (rules: CommandRule[]) => void;
  getCurrentRules: () => CommandRule[];
}

const mocks = vi.hoisted(() => ({
  apiGet:
    vi.fn<(path: string, signal?: AbortSignal) => Promise<{ entries: CommandRule[] } | null>>(),
  patchSettings: vi.fn<(patch: Partial<AppSettings>) => Promise<void>>(),
  addRuleDispatch: vi.fn<(args: AddDispatchArgs) => Promise<void>>(),
  removeRuleDispatch: vi.fn<(args: RemoveDispatchArgs) => Promise<void>>(),
}));

// Mock only the I/O + action edges. ui-primitives (buildChip), reconcile,
// @cplieger/reactive (el), icons, and dom (byId/maybeEl over happy-dom's real
// document) stay real so we exercise the actual DOM behaviour.
vi.mock("./api-client.js", () => ({ apiGet: mocks.apiGet }));
vi.mock("./persist.js", () => ({ patchSettings: mocks.patchSettings }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/permissions.js", () => ({
  addRule: { dispatch: mocks.addRuleDispatch },
  removeRule: { dispatch: mocks.removeRuleDispatch },
}));

import { initShellPolicyUI } from "./permissions-ui.js";
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
function select(id: string, values: string[]): HTMLSelectElement {
  const e = document.createElement("select");
  e.id = id;
  for (const v of values) {
    const opt = document.createElement("option");
    opt.value = v;
    opt.textContent = v;
    e.appendChild(opt);
  }
  return e;
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

/** initShellPolicyUI -> loadRules (async via apiGet) -> renderRuleChips. */
async function initWith(rules: CommandRule[], ignoreFiles: string[] = []): Promise<void> {
  mocks.apiGet.mockResolvedValue({ entries: rules });
  const initial: AppSettings = { agent_ignore_files: ignoreFiles };
  initShellPolicyUI(initial);
  await flush();
}

function ruleChips(): HTMLElement[] {
  const c = byId<HTMLDivElement>("command-rules-chips");
  return Array.from(c.querySelectorAll<HTMLElement>(".chip"));
}
function ruleChip(pattern: string): HTMLElement | undefined {
  return ruleChips().find((ch) => ch.dataset["pattern"] === pattern);
}
function chipLabel(ch: HTMLElement): string | null {
  return ch.querySelector<HTMLElement>(".chip-label")?.textContent ?? null;
}
function ignoreChips(): HTMLElement[] {
  const c = byId<HTMLDivElement>("agent-ignore-chips");
  return Array.from(c.querySelectorAll<HTMLElement>(".chip"));
}
function rule(pattern: string, mode: RuleMode, priority: number): CommandRule {
  return { pattern, mode, priority, created_at: 1 };
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();

  // Command rules section.
  document.body.appendChild(div("command-rules-chips"));
  document.body.appendChild(hint("command-rules-empty-hint"));
  document.body.appendChild(select("command-rules-mode", ["allow", "deny"]));
  document.body.appendChild(select("command-rules-priority", ["0", "1", "2"]));
  document.body.appendChild(input("command-rules-input"));
  document.body.appendChild(button("command-rules-add"));

  // Agent ignore section.
  document.body.appendChild(div("agent-ignore-chips"));
  document.body.appendChild(hint("agent-ignore-empty-hint"));
  document.body.appendChild(input("agent-ignore-input"));
  document.body.appendChild(button("agent-ignore-add"));

  mocks.patchSettings.mockResolvedValue(undefined);
  mocks.apiGet.mockResolvedValue({ entries: [] });

  // Optimistic local mutation, matching the real action's synchronous
  // optimistic step (without the network round-trip / rollback).
  mocks.addRuleDispatch.mockImplementation(async (args) => {
    const current = args.getCurrentRules();
    const idx = current.findIndex((e) => e.pattern === args.pattern);
    const next = [...current];
    const updated = rule(args.pattern, args.mode, args.priority);
    if (idx >= 0) {
      next[idx] = updated;
    } else {
      next.push(updated);
    }
    args.setRules(next);
  });
  mocks.removeRuleDispatch.mockImplementation(async (args) => {
    args.setRules(args.getCurrentRules().filter((e) => e.pattern !== args.pattern));
  });
});

// ---------------------------------------------------------------------------
// Command rules: mode flip in place + closure freshness.
// ---------------------------------------------------------------------------

describe("renderRuleChips — mode flip", () => {
  it("flips allow→deny in place and keeps the toggle closure fresh on re-click", async () => {
    await initWith([rule("git *", "allow", 0), rule("npm *", "allow", 0)]);

    expect(ruleChips()).toHaveLength(2);
    const chip = ruleChip("git *");
    expect(chip).toBeDefined();
    expect(chip?.querySelector<HTMLElement>(".chip-mode")?.textContent).toBe("Allow");
    expect(chip?.className).toContain("chip-rule-allow");

    // First flip: allow -> deny.
    chip?.querySelector<HTMLElement>(".chip-mode")?.click();
    await flush();

    expect(mocks.addRuleDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.addRuleDispatch.mock.calls[0]?.[0].mode).toBe("deny");

    const flipped = ruleChip("git *");
    expect(flipped).toBeDefined();
    // Composite key changed (git *:allow:0 -> git *:deny:0) so the chip is a
    // brand-new node, not the stale reused one.
    expect(flipped).not.toBe(chip);
    expect(flipped?.querySelector<HTMLElement>(".chip-mode")?.textContent).toBe("Deny");
    expect(flipped?.className).toContain("chip-rule-deny");

    // Second flip on the CURRENT chip: the fresh closure captured mode "deny",
    // so clicking again must dispatch "allow". A key-by-pattern mount-only
    // reconcile would reuse the original closure and wrongly dispatch "deny".
    flipped?.querySelector<HTMLElement>(".chip-mode")?.click();
    await flush();

    expect(mocks.addRuleDispatch).toHaveBeenCalledTimes(2);
    expect(mocks.addRuleDispatch.mock.calls[1]?.[0].mode).toBe("allow");

    // The other chip was untouched throughout.
    expect(ruleChip("npm *")?.querySelector<HTMLElement>(".chip-mode")?.textContent).toBe("Allow");
  });

  it("clears and refocuses the pattern input after an add (repeat entry)", async () => {
    await initWith([]);

    const patternInput = byId<HTMLInputElement>("command-rules-input");
    patternInput.value = "git *";
    byId<HTMLButtonElement>("command-rules-add").click();
    await flush();

    expect(mocks.addRuleDispatch).toHaveBeenCalledTimes(1);
    expect(patternInput.value).toBe("");
    expect(document.activeElement).toBe(patternInput);
  });

  it("removes a rule chip via its remove button", async () => {
    await initWith([rule("git *", "allow", 0), rule("npm *", "deny", 0)]);
    expect(ruleChips()).toHaveLength(2);

    ruleChip("git *")?.querySelector<HTMLButtonElement>(".chip-remove")?.click();
    await flush();

    expect(mocks.removeRuleDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.removeRuleDispatch.mock.calls[0]?.[0].pattern).toBe("git *");
    expect(ruleChips()).toHaveLength(1);
    expect(ruleChip("git *")).toBeUndefined();
    expect(ruleChip("npm *")).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// Command rules: priority change re-mounts with a P2 badge.
// ---------------------------------------------------------------------------

describe("renderRuleChips — priority change", () => {
  it("re-mounts the chip with a P2 badge when the priority changes", async () => {
    await initWith([rule("git *", "allow", 0)]);

    const chip = ruleChip("git *");
    expect(chip?.querySelector<HTMLElement>(".chip-mode")?.textContent).toBe("Allow");

    // Re-add the same pattern at priority 2 through the add form.
    byId<HTMLInputElement>("command-rules-input").value = "git *";
    byId<HTMLSelectElement>("command-rules-mode").value = "allow";
    byId<HTMLSelectElement>("command-rules-priority").value = "2";
    byId<HTMLButtonElement>("command-rules-add").click();
    await flush();

    const updated = ruleChip("git *");
    expect(updated).toBeDefined();
    // Composite key changed (git *:allow:0 -> git *:allow:2) -> fresh node.
    expect(updated).not.toBe(chip);
    expect(updated?.querySelector<HTMLElement>(".chip-mode")?.textContent).toBe("Allow P2");
    expect(ruleChips()).toHaveLength(1);
  });
});

// ---------------------------------------------------------------------------
// Agent ignore chips: keyed reconcile add/remove preserves node identity.
// ---------------------------------------------------------------------------

describe("renderIgnoreChips — keyed reconcile", () => {
  it("clears and refocuses the path input after an add (repeat entry)", async () => {
    await initWith([], []);

    const pathInput = byId<HTMLInputElement>("agent-ignore-input");
    pathInput.value = ".kiroignore";
    byId<HTMLButtonElement>("agent-ignore-add").click();
    await flush();

    expect(ignoreChips()).toHaveLength(1);
    expect(pathInput.value).toBe("");
    expect(document.activeElement).toBe(pathInput);
  });

  it("preserves existing chip nodes on add and drops only the removed one", async () => {
    await initWith([], [".gitignore", ".env.dec"]);

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
});

// ---------------------------------------------------------------------------
// Empty states live in sibling hint elements, not inside the chip containers.
// ---------------------------------------------------------------------------

describe("empty states", () => {
  it("shows sibling hints and keeps the chip containers free of stray nodes", async () => {
    await initWith([], []);

    const rulesContainer = byId<HTMLDivElement>("command-rules-chips");
    expect(rulesContainer.querySelectorAll(".chip")).toHaveLength(0);
    // The empty <p> no longer lives inside the reconcile container.
    expect(rulesContainer.querySelector("p")).toBeNull();
    expect(byId("command-rules-empty-hint").classList.contains("hidden")).toBe(false);

    const ignoreContainer = byId<HTMLDivElement>("agent-ignore-chips");
    expect(ignoreContainer.querySelectorAll(".chip")).toHaveLength(0);
    expect(ignoreContainer.querySelector("p")).toBeNull();
    expect(byId("agent-ignore-empty-hint").classList.contains("hidden")).toBe(false);
  });

  it("hides the command-rules hint once a rule is present", async () => {
    await initWith([rule("git *", "allow", 0)]);
    expect(byId("command-rules-empty-hint").classList.contains("hidden")).toBe(true);
    expect(ruleChips()).toHaveLength(1);
  });
});
