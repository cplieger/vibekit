//
// Tests for the workspace Cedar relaxation switch (D103) in permissions-ui.ts.
//
// Four properties, and each one is a way the switch could lie:
//   - it writes what it claims (one bare workspace allow rule per member of the
//     server's set, and nothing else),
//   - it reverses cleanly (removing exactly those rules, leaving a narrower
//     hand-authored rule alone),
//   - switching ON takes the widening confirm the effect editor uses, and a
//     cancel writes nothing,
//   - the read-back is honest about a partial set rather than rounding it to a
//     boolean.
//
// Only the I/O edges are mocked (apiGet, the action dispatch, confirm). The real
// DOM, the real render and the real read-back logic run.
import { describe, it, expect, beforeEach, vi } from "vitest";
import type { PolicyView, PolicyRule } from "./types.js";

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

/** The relaxation set the server reports. A short stand-in for
 *  policyfile.RelaxCapabilities(): what matters here is that the client uses the
 *  server's list verbatim, not what that list happens to hold. */
const RELAX = ["fs_read", "fs_write", "shell"];

/** A bare workspace allow rule — the exact shape the relaxation writes. */
function relaxRule(capability: string): PolicyRule {
  return {
    capability,
    effect: "allow",
    scope: "workspace",
    source: "/home/u/.kiro/workspace-roots/ab/permissions.yaml",
  };
}

function view(rules: PolicyRule[], relax = RELAX): PolicyView {
  return {
    available: true,
    writable_scopes: ["user", "workspace"],
    capabilities: [...RELAX, "web_fetch"],
    relax_capabilities: relax,
    rules,
  };
}

async function flush(): Promise<void> {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve();
  }
}

function tag<T extends HTMLElement>(name: string, id: string): T {
  const e = document.createElement(name) as T;
  e.id = id;
  return e;
}

function box(): HTMLInputElement {
  return byId<HTMLInputElement>("workspace-relax-checkbox");
}
function statusText(): string {
  return byId("workspace-relax-status").textContent ?? "";
}

/** init + the lazy first load, as wired in production. */
async function mount(v: PolicyView): Promise<void> {
  mocks.apiGet.mockResolvedValue(v);
  initNativePolicyUI();
  loadNativePolicy();
  await flush();
}

/** Click the switch and let its writes settle. `checked` is set first because
 *  the handler reads the input's post-click state, exactly as a real click
 *  leaves it. */
async function click(on: boolean): Promise<void> {
  const b = box();
  b.checked = on;
  b.dispatchEvent(new Event("change"));
  await flush();
}

/** Every rule-edit dispatch the switch issued. */
function edits(): Record<string, unknown>[] {
  return mocks.editDispatch.mock.calls.map((c) => c[0]);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sseHandlers.clear();
  document.body.replaceChildren();
  document.body.appendChild(tag("p", "native-policy-status"));
  document.body.appendChild(tag("div", "native-policy-list"));
  document.body.appendChild(tag("p", "native-policy-empty-hint"));
  const add = tag<HTMLButtonElement>("button", "native-rule-add");
  document.body.appendChild(add);
  const relax = tag<HTMLInputElement>("input", "workspace-relax-checkbox");
  relax.type = "checkbox";
  document.body.appendChild(relax);
  document.body.appendChild(tag("p", "workspace-relax-status"));

  mocks.editDispatch.mockResolvedValue({ ok: true });
  mocks.confirm.mockResolvedValue(true);
});

describe("relaxation read-back", () => {
  it("reads off when no relaxation rule is present", async () => {
    await mount(view([]));
    expect(box().checked).toBe(false);
    expect(box().indeterminate).toBe(false);
    expect(statusText()).toBe("");
  });

  it("reads on, and names the set, when every member has its rule", async () => {
    await mount(view(RELAX.map(relaxRule)));
    expect(box().checked).toBe(true);
    expect(box().indeterminate).toBe(false);
    expect(statusText()).toContain("3 capabilities allowed");
    for (const c of RELAX) {
      expect(statusText()).toContain(c);
    }
  });

  // A partial set is a real state (an interrupted write, or a rule removed by
  // hand from the Active policy list). Reporting it as plain off would invite a
  // click that then tries to write rules that already exist, and reporting it as
  // on would claim capabilities that still ask.
  it("reads indeterminate for a partial set and names what still asks", async () => {
    await mount(view([relaxRule("shell")]));
    expect(box().checked).toBe(false);
    expect(box().indeterminate).toBe(true);
    expect(statusText()).toContain("1 of 3");
    expect(statusText()).toContain("fs_read");
    expect(statusText()).toContain("fs_write");
    expect(statusText()).not.toContain("Still asking for shell");
  });

  // The glob check is the whole reason isRelaxRule exists: a narrow
  // hand-authored allow is not the blanket grant this switch writes, and
  // counting it would report the relaxation as partly on when it is off.
  it("does not count a narrower hand-authored allow as the relaxation", async () => {
    await mount(view([{ ...relaxRule("fs_write"), match: ["src/**"] }]));
    expect(box().checked).toBe(false);
    expect(box().indeterminate).toBe(false);
  });

  it("does not count a user-scope allow as the workspace relaxation", async () => {
    await mount(view([{ ...relaxRule("shell"), scope: "user" }]));
    expect(box().indeterminate).toBe(false);
    expect(box().checked).toBe(false);
  });

  it("does not count an ask rule as an allow", async () => {
    await mount(view([{ ...relaxRule("shell"), effect: "ask" }]));
    expect(box().checked).toBe(false);
    expect(box().indeterminate).toBe(false);
  });

  // Painting the read-back must not look like a user click, or every refetch
  // would re-issue the whole write.
  it("writes nothing while rendering its own state", async () => {
    await mount(view(RELAX.map(relaxRule)));
    expect(mocks.editDispatch).not.toHaveBeenCalled();
  });
});

describe("switching the relaxation on", () => {
  it("writes one bare workspace allow rule per member of the server's set", async () => {
    await mount(view([]));
    await click(true);

    expect(edits()).toHaveLength(RELAX.length);
    for (const [i, capability] of RELAX.entries()) {
      expect(edits()[i]).toEqual({
        op: "add",
        scope: "workspace",
        capability,
        effect: "allow",
        confirm: true,
      });
    }
  });

  // Bare means bare: a match or exclude on the written rule would make the off
  // path unable to find it by signature, and would silently narrow the grant the
  // confirm described.
  it("writes no match or exclude", async () => {
    await mount(view([]));
    await click(true);
    for (const e of edits()) {
      expect(e["match"]).toBeUndefined();
      expect(e["exclude"]).toBeUndefined();
    }
  });

  it("never writes at user scope, and never per-chat", async () => {
    await mount(view([]));
    await click(true);
    for (const e of edits()) {
      expect(e["scope"]).toBe("workspace");
    }
  });

  it("takes the widening confirm before writing anything", async () => {
    await mount(view([]));
    await click(true);
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    const [message, label] = mocks.confirm.mock.calls[0] ?? [];
    expect(message).toContain("WIDENS");
    expect(message).toContain("fs_read");
    expect(label).toBe("Widen workspace policy");
  });

  it("writes nothing when the confirm is declined", async () => {
    await mount(view([]));
    mocks.confirm.mockResolvedValue(false);
    await click(true);
    expect(mocks.editDispatch).not.toHaveBeenCalled();
  });

  it("repaints from the server after a declined confirm, so the box cannot stay on", async () => {
    await mount(view([]));
    mocks.confirm.mockResolvedValue(false);
    const before = mocks.apiGet.mock.calls.length;
    await click(true);
    expect(mocks.apiGet.mock.calls.length).toBeGreaterThan(before);
    expect(box().checked).toBe(false);
  });

  // Each rule is its own atomic file write, so the SET is not atomic. Claiming
  // success on a partial write is the failure this guards.
  it("reports a partial write instead of claiming success", async () => {
    await mount(view([]));
    mocks.editDispatch
      .mockResolvedValueOnce({ ok: true })
      .mockResolvedValueOnce({ error: "disk full" })
      .mockResolvedValueOnce({ ok: true });
    mocks.apiGet.mockResolvedValue(view([relaxRule("fs_read"), relaxRule("shell")]));

    await click(true);

    // The report survives the refetch that follows the write, and sits beside
    // the read-back rather than replacing it: the reader needs both "one write
    // failed" and "here is what is actually on".
    expect(statusText()).toContain("2 of 3 rules written");
    expect(statusText()).toContain("1 failed");
    expect(statusText()).toContain("2 of 3 allowed");
    expect(box().indeterminate).toBe(true);
  });

  it("clears a stale partial-write report on the next attempt", async () => {
    await mount(view([]));
    mocks.editDispatch.mockResolvedValueOnce({ error: "disk full" });
    mocks.apiGet.mockResolvedValue(view([relaxRule("fs_write"), relaxRule("shell")]));
    await click(true);
    expect(statusText()).toContain("failed");

    mocks.editDispatch.mockResolvedValue({ ok: true });
    mocks.apiGet.mockResolvedValue(view(RELAX.map(relaxRule)));
    await click(true);
    expect(statusText()).not.toContain("failed");
    expect(box().checked).toBe(true);
  });
});

describe("switching the relaxation off", () => {
  it("removes exactly the rules it wrote", async () => {
    await mount(view(RELAX.map(relaxRule)));
    await click(false);

    expect(edits()).toHaveLength(RELAX.length);
    for (const [i, capability] of RELAX.entries()) {
      expect(edits()[i]).toEqual({
        op: "remove",
        scope: "workspace",
        capability,
        effect: "allow",
        confirm: true,
      });
    }
  });

  // Narrowing is not widening: the effect editor confirms a widening change and
  // removeRule confirms only a deny, so switching off must ask nothing.
  it("asks for no confirmation", async () => {
    await mount(view(RELAX.map(relaxRule)));
    await click(false);
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  it("leaves the box off and the status clear once the rules are gone", async () => {
    await mount(view(RELAX.map(relaxRule)));
    mocks.apiGet.mockResolvedValue(view([]));
    await click(false);
    expect(box().checked).toBe(false);
    expect(box().indeterminate).toBe(false);
    expect(statusText()).toBe("");
  });

  it("round-trips: on then off returns the policy to where it started", async () => {
    await mount(view([]));
    await click(true);
    const written = edits().filter((e) => e["op"] === "add");

    mocks.editDispatch.mockClear();
    mocks.apiGet.mockResolvedValue(view(RELAX.map(relaxRule)));
    await loadAndFlush();
    await click(false);
    const removed = edits().filter((e) => e["op"] === "remove");

    expect(removed.map((e) => e["capability"])).toEqual(written.map((e) => e["capability"]));
  });
});

describe("an empty relaxation set", () => {
  // A build whose server reports no relaxable capabilities must not present a
  // switch that writes nothing while looking like it did something.
  it("writes nothing when the server reports no set", async () => {
    await mount(view([], []));
    await click(true);
    expect(mocks.editDispatch).not.toHaveBeenCalled();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });
});

async function loadAndFlush(): Promise<void> {
  loadNativePolicy();
  await flush();
}
