//
// Tests for the native (Cedar) policy view + conservative editor added to
// permissions-ui.ts (initNativePolicyUI). Guards: the scope-grouped render
// with per-scope remove affordances only on writable scopes, the
// conservative Ask default flowing through an add, the deny-removal confirm
// gate, and the permissions_changed SSE refetch.
//
// B2: initNativePolicyUI() no longer fires the initial GET /api/permissions
// (it used to fetch eagerly at boot, pre-auth, for an invisible panel); the
// first load is lazy via loadNativePolicy(), wired to the Permissions tab's
// first activation. Tests call both where they need rendered data.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { PolicyView } from "./types.js";

type SSEFn = (chatID: string, payload?: unknown) => void;

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn<(path: string, signal?: AbortSignal) => Promise<PolicyView | null>>(),
  editDispatch:
    vi.fn<(args: Record<string, unknown>) => Promise<{ ok?: boolean; error?: string } | null>>(),
  explainDispatch: vi.fn<(args: Record<string, unknown>) => Promise<unknown>>(),
  confirm: vi.fn<(msg: string, label?: string) => Promise<boolean>>(),
  sseHandlers: new Map<string, SSEFn>(),
}));

vi.mock("./api-client.js", () => ({ apiGet: mocks.apiGet }));
vi.mock("./persist.js", () => ({ patchSettings: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: mocks.confirm }));
vi.mock("./bus.js", () => ({
  onSSE: (type: string, fn: SSEFn) => {
    mocks.sseHandlers.set(type, fn);
    return () => mocks.sseHandlers.delete(type);
  },
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { name: "permissions.edit_native_rule", dispatch: mocks.editDispatch },
  explainPolicy: { name: "permissions.explain", dispatch: mocks.explainDispatch },
}));

import { initNativePolicyUI, loadNativePolicy } from "./permissions-ui.js";
import { byId } from "./dom.js";

/** init + lazy first load, as wired in production (settings-tabs loader map
 *  fires loadNativePolicy on the Permissions tab's first activation). */
function initAndLoad(): void {
  initNativePolicyUI();
  loadNativePolicy();
}

function el<T extends HTMLElement>(tag: string, id: string): T {
  const e = document.createElement(tag) as T;
  e.id = id;
  return e;
}
function selectWith(id: string, values: string[], selected?: string): HTMLSelectElement {
  const s = document.createElement("select");
  s.id = id;
  for (const v of values) {
    const o = document.createElement("option");
    o.value = v;
    o.textContent = v;
    if (v === selected) {
      o.selected = true;
    }
    s.appendChild(o);
  }
  return s;
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

const sampleView: PolicyView = {
  available: true,
  writable_scopes: ["user", "workspace"],
  capabilities: ["fs_read", "fs_write", "shell", "web_fetch"],
  // Empty here on purpose: this fixture has no relaxation checkbox in its DOM,
  // so the switch is out of scope for these tests. permissions-relax.test.ts
  // owns it.
  relax_capabilities: [],
  rules: [
    {
      capability: "fs_write",
      effect: "deny",
      match: ["**/.git/**"],
      scope: "kiro",
      source: "kiro-scope",
    },
    {
      capability: "shell",
      effect: "allow",
      scope: "user",
      source: "/home/u/.kiro/settings/permissions.yaml",
    },
    {
      capability: "fs_read",
      effect: "ask",
      match: ["src/**"],
      scope: "workspace",
      source: "/home/u/.kiro/workspace-roots/ab/permissions.yaml",
    },
  ],
};

function policyRows(): HTMLElement[] {
  return Array.from(
    byId<HTMLDivElement>("native-policy-list").querySelectorAll<HTMLElement>(".native-rule"),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sseHandlers.clear();
  document.body.replaceChildren();
  document.body.appendChild(el("p", "native-policy-status"));
  document.body.appendChild(el("div", "native-policy-list"));
  document.body.appendChild(el("p", "native-policy-empty-hint"));
  document.body.appendChild(selectWith("native-rule-scope", ["workspace", "user"], "workspace"));
  document.body.appendChild(selectWith("native-rule-capability", [])); // filled by controller
  document.body.appendChild(selectWith("native-rule-effect", ["ask", "allow", "deny"], "ask"));
  document.body.appendChild(el<HTMLInputElement>("input", "native-rule-match"));
  document.body.appendChild(el<HTMLInputElement>("input", "native-rule-exclude"));
  document.body.appendChild(el<HTMLButtonElement>("button", "native-rule-add"));
  document.body.appendChild(selectWith("native-explain-capability", []));
  document.body.appendChild(el<HTMLInputElement>("input", "native-explain-resource"));
  document.body.appendChild(el<HTMLButtonElement>("button", "native-explain-run"));
  document.body.appendChild(el("p", "native-explain-result"));

  mocks.apiGet.mockResolvedValue(sampleView);
  mocks.editDispatch.mockResolvedValue({ ok: true });
  mocks.confirm.mockResolvedValue(true);
});

describe("native policy view", () => {
  it("does not fetch at init — the initial load is lazy (B2)", async () => {
    initNativePolicyUI();
    await flush();
    expect(mocks.apiGet).not.toHaveBeenCalled();
  });

  it("renders rules grouped by scope with remove buttons only on writable scopes", async () => {
    initAndLoad();
    await flush();

    const rows = policyRows();
    expect(rows).toHaveLength(3);
    // kiro rule: read-only, no remove button.
    const kiroRow = rows.find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_write",
    );
    expect(kiroRow?.querySelector(".native-rule-remove")).toBeNull();
    // user/workspace rules: writable, have a remove button.
    const userRow = rows.find((r) => r.querySelector(".native-rule-cap")?.textContent === "shell");
    expect(userRow?.querySelector(".native-rule-remove")).not.toBeNull();
    // capability picker got populated from the view.
    expect(byId<HTMLSelectElement>("native-rule-capability").options.length).toBe(4);
  });

  it("populates the effect selector defaulting to Ask (conservative)", () => {
    initNativePolicyUI();
    expect(byId<HTMLSelectElement>("native-rule-effect").value).toBe("ask");
  });

  it("refetches when a permissions_changed SSE arrives", async () => {
    // The SSE listener is registered at init (boot); only the initial fetch
    // moved to the lazy loader (B2).
    initAndLoad();
    await flush();
    expect(mocks.apiGet).toHaveBeenCalledTimes(1);
    mocks.sseHandlers.get("permissions_changed")?.("");
    await flush();
    expect(mocks.apiGet).toHaveBeenCalledTimes(2);
  });
});

describe("native policy editor", () => {
  it("adds a rule with the selected scope/capability/effect and splits comma globs", async () => {
    initAndLoad();
    await flush();

    byId<HTMLSelectElement>("native-rule-capability").value = "fs_write";
    byId<HTMLInputElement>("native-rule-match").value = "src/** , dist/**";
    byId<HTMLInputElement>("native-rule-exclude").value = "**/secrets/** , **/.git/**";
    byId<HTMLButtonElement>("native-rule-add").click();
    await flush();

    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    const args = mocks.editDispatch.mock.calls[0]?.[0];
    expect(args).toMatchObject({
      op: "add",
      scope: "workspace",
      capability: "fs_write",
      effect: "ask", // conservative default carried from the select
      match: ["src/**", "dist/**"],
      exclude: ["**/secrets/**", "**/.git/**"],
    });
    // both glob inputs cleared, match refocused (repeat entry), refetched.
    expect(byId<HTMLInputElement>("native-rule-match").value).toBe("");
    expect(byId<HTMLInputElement>("native-rule-exclude").value).toBe("");
    expect(document.activeElement).toBe(byId<HTMLInputElement>("native-rule-match"));
    expect(mocks.apiGet).toHaveBeenCalledTimes(2);
  });

  it("renders an effect select on writable rows and a static badge on read-only rows", async () => {
    initAndLoad();
    await flush();

    const rows = policyRows();
    const kiroRow = rows.find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_write",
    );
    expect(kiroRow?.querySelector("select.native-rule-effect")).toBeNull();
    expect(kiroRow?.querySelector("span.native-rule-effect")?.textContent).toBe("deny");

    const userRow = rows.find((r) => r.querySelector(".native-rule-cap")?.textContent === "shell");
    const sel = userRow?.querySelector<HTMLSelectElement>("select.native-rule-effect");
    expect(sel).not.toBeNull();
    expect(sel?.value).toBe("allow");
  });

  it("dispatches op=update on a narrowing effect change (no confirm)", async () => {
    initAndLoad();
    await flush();

    // workspace fs_read rule: ask → deny narrows; no confirm dialog.
    const row = policyRows().find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_read",
    );
    const sel = row?.querySelector<HTMLSelectElement>("select.native-rule-effect");
    expect(sel).not.toBeNull();
    sel!.value = "deny";
    sel!.dispatchEvent(new Event("change"));
    await flush();

    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "update",
      scope: "workspace",
      capability: "fs_read",
      effect: "ask",
      new_effect: "deny",
      match: ["src/**"],
      confirm: false,
    });
    // Success → refetch.
    expect(mocks.apiGet).toHaveBeenCalledTimes(2);
  });

  it("requires confirmation for a widening effect change and reverts on cancel", async () => {
    mocks.confirm.mockResolvedValue(false);
    initAndLoad();
    await flush();

    // workspace fs_read rule: ask → allow widens.
    const row = policyRows().find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "fs_read",
    );
    const sel = row?.querySelector<HTMLSelectElement>("select.native-rule-effect");
    sel!.value = "allow";
    sel!.dispatchEvent(new Event("change"));
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch).not.toHaveBeenCalled();
    expect(sel!.value).toBe("ask"); // reverted

    // Confirmed → dispatches with confirm=true.
    mocks.confirm.mockResolvedValue(true);
    sel!.value = "allow";
    sel!.dispatchEvent(new Event("change"));
    await flush();
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "update",
      new_effect: "allow",
      confirm: true,
    });
  });

  it("pre-flights a shell explain without a resource instead of dispatching", async () => {
    initAndLoad();
    await flush();

    const capSel = byId<HTMLSelectElement>("native-explain-capability");
    const opt = document.createElement("option");
    opt.value = "shell";
    opt.textContent = "shell";
    capSel.appendChild(opt);
    capSel.value = "shell";
    byId<HTMLInputElement>("native-explain-resource").value = "   ";
    byId<HTMLButtonElement>("native-explain-run").click();
    await flush();

    expect(mocks.explainDispatch).not.toHaveBeenCalled();
    expect(byId("native-explain-result").textContent).toContain("Enter a command");
  });

  it("removes a non-deny rule directly (no confirm)", async () => {
    initAndLoad();
    await flush();
    const userRow = policyRows().find(
      (r) => r.querySelector(".native-rule-cap")?.textContent === "shell",
    );
    userRow?.querySelector<HTMLButtonElement>(".native-rule-remove")?.click();
    await flush();

    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "remove",
      scope: "user",
      capability: "shell",
      effect: "allow",
      confirm: false,
    });
  });

  it("requires confirmation to remove a deny rule and sends confirm=true", async () => {
    // Add a writable deny rule to the sample so it has a remove button.
    mocks.apiGet.mockResolvedValue({
      ...sampleView,
      rules: [
        {
          capability: "shell",
          effect: "deny",
          match: ["sudo *"],
          scope: "workspace",
          source: "/w/permissions.yaml",
        },
      ],
    });
    initAndLoad();
    await flush();

    const denyRow = policyRows()[0];
    denyRow?.querySelector<HTMLButtonElement>(".native-rule-remove")?.click();
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch.mock.calls[0]?.[0]).toMatchObject({
      op: "remove",
      effect: "deny",
      confirm: true,
    });
  });

  it("does NOT remove a deny rule when confirmation is cancelled", async () => {
    mocks.confirm.mockResolvedValue(false);
    mocks.apiGet.mockResolvedValue({
      ...sampleView,
      rules: [
        {
          capability: "shell",
          effect: "deny",
          match: ["sudo *"],
          scope: "workspace",
          source: "/w/permissions.yaml",
        },
      ],
    });
    initAndLoad();
    await flush();

    policyRows()[0]?.querySelector<HTMLButtonElement>(".native-rule-remove")?.click();
    await flush();

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    expect(mocks.editDispatch).not.toHaveBeenCalled();
  });

  it("runs an explain simulation and renders the effect", async () => {
    mocks.explainDispatch.mockResolvedValue({
      effect: "ask",
      scope: "workspace",
      source: "/w/permissions.yaml",
    });
    initAndLoad();
    await flush();

    byId<HTMLSelectElement>("native-explain-capability").value = "fs_write";
    byId<HTMLInputElement>("native-explain-resource").value = "/etc/hosts";
    byId<HTMLButtonElement>("native-explain-run").click();
    await flush();

    expect(mocks.explainDispatch).toHaveBeenCalledWith({
      capability: "fs_write",
      resource: "/etc/hosts",
    });
    expect(byId("native-explain-result").textContent).toContain("Effect: ask");
  });
});
