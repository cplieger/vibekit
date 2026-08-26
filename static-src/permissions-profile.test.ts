//
// Tests for the security-profile picker in permissions-ui.ts.
//
// The picker replaced a checkbox, and it inherits that checkbox's central
// property: it must describe what is actually in force rather than what was
// clicked. Every property below is a way it could claim otherwise.
//
//   - the ladder and the selection are the SERVER's, rendered not invented,
//   - selecting a profile REPLACES the policy, and losing hand-authored rules is
//     confirmed before anything is written,
//   - the table is read-only unless Custom is active, and genuinely disabled
//     rather than only dimmed,
//   - the two doors into Custom differ by one flag: Customize seeds from the
//     profile in force, direct selection copies nothing and leaves the file's own
//     rules standing (it does NOT start blank — the merge preserves what the
//     profile mechanism did not write),
//   - an empty Custom policy says so, because it asks for everything including
//     reading a file.
//
// Only the I/O edges are mocked (apiGet, the action dispatches, confirm). The real
// DOM, the real render and the real read-back logic run.
import { describe, it, expect, beforeEach, vi } from "vitest";
import indexHtml from "../static/index.html?raw";
import type { PolicyView, PolicyRule } from "./types.js";

type SSEFn = (chatID: string, payload?: unknown) => void;

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn<(path: string, signal?: AbortSignal) => Promise<PolicyView | null>>(),
  editDispatch:
    vi.fn<(args: Record<string, unknown>) => Promise<{ ok?: boolean; error?: string } | null>>(),
  explainDispatch: vi.fn<(args: Record<string, unknown>) => Promise<unknown>>(),
  profileDispatch:
    vi.fn<(args: Record<string, unknown>) => Promise<{ ok?: boolean; error?: string } | null>>(),
  confirm: vi.fn<(msg: string, label?: string, variant?: string) => Promise<boolean>>(),
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
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { name: "permissions.edit_native_rule", dispatch: mocks.editDispatch },
  explainPolicy: { name: "permissions.explain", dispatch: mocks.explainDispatch },
  setSecurityProfile: { name: "permissions.set_profile", dispatch: mocks.profileDispatch },
}));

import { initNativePolicyUI, loadNativePolicy } from "./permissions-ui.js";
import { byId } from "./dom.js";

/** The ladder as the server reports it. A short stand-in for policyfile.Profiles():
 *  what matters is that the client renders the server's list in the server's ORDER,
 *  not what that list happens to hold. Custom last, loosest second-to-last, matching
 *  the real ladder's shape. */
const LADDER = [
  { id: "guarded", presets: ["read-workspace"] },
  { id: "read-only", presets: ["read-workspace", "read-all"] },
  { id: "trusted", presets: ["read-workspace", "read-all", "dev-shell"] },
  { id: "unrestricted", presets: ["allow-all"] },
  { id: "custom", presets: [] },
];

function userRule(capability: string): PolicyRule {
  return {
    capability,
    effect: "allow",
    scope: "user",
    source: "/home/u/.kiro/settings/permissions.yaml",
  };
}

function view(profile: string, rules: PolicyRule[] = []): PolicyView {
  return {
    available: true,
    writable_scopes: ["user", "workspace"],
    capabilities: ["fs_read", "fs_write", "shell"],
    relax_capabilities: [],
    profiles: LADDER,
    profile,
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

function radios(): HTMLInputElement[] {
  return [...byId("security-profile-list").querySelectorAll<HTMLInputElement>("input[type=radio]")];
}
function radioFor(id: string): HTMLInputElement {
  const found = radios().find((r) => r.value === id);
  if (!found) {
    throw new Error(`no radio for profile ${id}`);
  }
  return found;
}
function customizeBtn(): HTMLButtonElement {
  return byId<HTMLButtonElement>("security-profile-customize");
}
function statusText(): string {
  return byId("security-profile-status").textContent ?? "";
}
/** The description rendered under one profile's radio. Read out of the DOM rather
 *  than imported, because profileDescription is module-private and what a reader
 *  actually sees is the rendered row. */
function descriptionFor(id: string): string {
  return radioFor(id).closest("label")?.querySelector(".profile-desc")?.textContent ?? "";
}
/** The Security-profile section's hint, straight out of the shipped markup. Read
 *  from static/index.html rather than a fixture because the copy IS part of the
 *  deliverable here: a hint describing a mechanism the server no longer has is the
 *  class of defect this change exists to remove. */
function securityProfileHint(): string {
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  return doc.querySelector("#security-profile-section .section-hint")?.textContent ?? "";
}
function ruleRemoveButtons(): HTMLButtonElement[] {
  return [...byId("native-policy-list").querySelectorAll<HTMLButtonElement>("button")];
}
function addRuleBtn(): HTMLButtonElement {
  return byId<HTMLButtonElement>("native-rule-add");
}

/** init + the lazy first load, as wired in production. */
async function mount(v: PolicyView): Promise<void> {
  mocks.apiGet.mockResolvedValue(v);
  initNativePolicyUI();
  loadNativePolicy();
  await flush();
}

/** Click a profile radio the way a user does: the browser sets checked, then the
 *  change event fires. */
async function pick(id: string): Promise<void> {
  const r = radioFor(id);
  r.checked = true;
  r.dispatchEvent(new Event("change"));
  await flush();
}

/** Every profile dispatch the picker issued. */
function profileCalls(): Record<string, unknown>[] {
  return mocks.profileDispatch.mock.calls.map((c) => c[0]);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sseHandlers.clear();
  document.body.replaceChildren();
  const section = tag("div", "native-policy-section");
  section.appendChild(tag("p", "native-policy-status"));
  section.appendChild(tag("div", "native-policy-list"));
  section.appendChild(tag("p", "native-policy-empty-hint"));
  const adder = document.createElement("div");
  adder.setAttribute("data-rule-form", "add");
  const add = tag<HTMLButtonElement>("button", "native-rule-add");
  adder.appendChild(add);
  section.appendChild(adder);
  document.body.appendChild(section);

  document.body.appendChild(tag("div", "security-profile-list"));
  document.body.appendChild(tag("button", "security-profile-customize"));
  document.body.appendChild(tag("p", "security-profile-status"));

  mocks.editDispatch.mockResolvedValue({ ok: true });
  mocks.profileDispatch.mockResolvedValue({ ok: true });
  mocks.confirm.mockResolvedValue(true);
});

describe("the profile ladder", () => {
  // The ladder decides what one click grants, so policyfile owns it and the client
  // renders it. A client that filtered or sorted this would offer a posture the
  // server does not have, or put the loosest option in the middle of a list a
  // reader scans from cautious to permissive.
  it("renders the server's ladder in the server's order", async () => {
    await mount(view("guarded"));
    expect(radios().map((r) => r.value)).toEqual(LADDER.map((p) => p.id));
  });

  it("selects the profile the server says is in force", async () => {
    await mount(view("trusted"));
    expect(radioFor("trusted").checked).toBe(true);
    expect(radios().filter((r) => r.checked)).toHaveLength(1);
  });

  it("renders nothing at all when the server reports no ladder", async () => {
    await mount({ ...view("guarded"), profiles: [] });
    expect(radios()).toHaveLength(0);
  });

  // Re-selecting the active profile would otherwise clear the policy and rewrite it
  // for no reason, and on a Custom profile it would delete the user's own rules
  // behind a confirm they did not expect to see.
  it("does nothing when the profile already in force is re-selected", async () => {
    await mount(view("trusted"));
    await pick("trusted");
    expect(mocks.profileDispatch).not.toHaveBeenCalled();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });
});

describe("the Customize button", () => {
  it("is offered on a named profile and hidden on Custom", async () => {
    await mount(view("trusted"));
    expect(customizeBtn().classList.contains("hidden")).toBe(false);

    await mount(view("custom", [userRule("shell")]));
    expect(customizeBtn().classList.contains("hidden")).toBe(true);
  });

  // The seeding door. `seed` is the entire difference between the two ways into
  // Custom, so it is the flag worth pinning rather than the endpoint.
  it("switches to Custom asking the server to seed from the profile in force", async () => {
    await mount(view("trusted"));
    customizeBtn().click();
    await flush();
    expect(profileCalls()).toEqual([{ profile: "custom", seed: true }]);
  });

  it("takes no confirmation, because it loses nothing", async () => {
    await mount(view("trusted"));
    customizeBtn().click();
    await flush();
    expect(mocks.confirm).not.toHaveBeenCalled();
  });
});

describe("selecting a profile", () => {
  it("starts Custom blank when picked from the list", async () => {
    await mount(view("trusted"));
    await pick("custom");
    expect(profileCalls()).toEqual([{ profile: "custom", seed: false }]);
  });

  // The server MERGES rather than replaces, so hand-authored rules SURVIVE the
  // switch and keep applying beside the new profile. That is the surprise worth a
  // confirm now — a grant outliving the posture change that was supposed to narrow
  // it — and the count is in the message because "your rules" is not something a
  // reader can check against the table while a modal covers it. It used to say
  // DELETED, which the merge made false; a confirm that describes the wrong outcome
  // is worse than none.
  it("warns that rules the user authored survive the switch and keep applying", async () => {
    await mount(view("custom", [userRule("shell"), userRule("fs_write")]));
    await pick("trusted");

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    const [message, label, variant] = mocks.confirm.mock.calls[0] ?? [];
    expect(message).toContain("2 custom rules STAY");
    expect(message).toContain("keep applying");
    expect(message).toContain("Trusted");
    expect(message).not.toContain("DELETED");
    expect(label).toBe("Switch anyway");
    // Still destructive styling: a grant persisting past a posture change is the
    // security surprise that styling is for.
    expect(variant).toBe("destructive");
    expect(profileCalls()).toEqual([{ profile: "trusted", seed: false }]);
  });

  it("writes nothing when that confirmation is declined", async () => {
    await mount(view("custom", [userRule("shell")]));
    mocks.confirm.mockResolvedValue(false);
    await pick("trusted");
    expect(mocks.profileDispatch).not.toHaveBeenCalled();
  });

  // Leaving a Custom profile that holds nothing loses nothing, so a confirm there
  // would be a dialog that teaches the reader to click through dialogs.
  it("does not confirm when Custom holds no rules of its own", async () => {
    await mount(view("custom"));
    await pick("guarded");
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(profileCalls()).toEqual([{ profile: "guarded", seed: false }]);
  });

  // A read-only baseline rule is not the user's to lose: it comes from a scope they
  // cannot edit, so counting it would confirm a deletion that is not happening.
  it("counts only rules in writable scopes as the user's own", async () => {
    await mount(
      view("custom", [
        { capability: "fs_write", effect: "ask", scope: "kiro", source: "kiro-scope" },
        {
          capability: "fs_read",
          effect: "allow",
          scope: "session",
          source: "preset:read-workspace",
        },
      ]),
    );
    await pick("guarded");
    expect(mocks.confirm).not.toHaveBeenCalled();
  });

  // The loosest profile earns its own confirm: it is the one that grants `power`,
  // and it must state what it cannot silence or a prompt afterwards reads as broken
  // rather than as bounded.
  it("confirms the loosest profile by naming power and the prompts it keeps", async () => {
    await mount(view("guarded"));
    await pick("unrestricted");

    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    const [message, label] = mocks.confirm.mock.calls[0] ?? [];
    expect(message).toContain("power");
    expect(message).toContain("does NOT");
    expect(message).toContain(".git");
    expect(label).toBe("Allow everything");
    expect(profileCalls()).toEqual([{ profile: "unrestricted", seed: false }]);
  });

  it("writes nothing when the loosest confirmation is declined", async () => {
    await mount(view("guarded"));
    mocks.confirm.mockResolvedValue(false);
    await pick("unrestricted");
    expect(mocks.profileDispatch).not.toHaveBeenCalled();
  });

  // The paint comes from the server, never from the click. A refused switch that
  // left the radio where the user put it would show a posture that is not in force,
  // which is the class of lie this whole panel was rebuilt to stop telling.
  it("repaints from the server and reports a refused switch", async () => {
    await mount(view("guarded"));
    mocks.profileDispatch.mockResolvedValue({ error: "policy file is not writable" });
    await pick("trusted");

    expect(radioFor("guarded").checked).toBe(true);
    expect(statusText()).toContain("policy file is not writable");
  });
});

describe("the table outside Custom", () => {
  // With a profile in charge, a hand-edit would be a second writer of one posture
  // and the first thing to disagree with the picker. Disabled rather than only
  // dimmed: a dimmed control is still in the tab order and still clickable by
  // keyboard, which is the version of this that looks locked and is not.
  it("disables every editing control on a named profile", async () => {
    await mount(view("trusted", [userRule("shell")]));
    expect(addRuleBtn().disabled).toBe(true);
    for (const b of ruleRemoveButtons()) {
      expect(b.disabled).toBe(true);
    }
    expect(byId("native-policy-section").classList.contains("native-policy-locked")).toBe(true);
  });

  it("enables them again on Custom", async () => {
    await mount(view("custom", [userRule("shell")]));
    expect(addRuleBtn().disabled).toBe(false);
    for (const b of ruleRemoveButtons()) {
      expect(b.disabled).toBe(false);
    }
    expect(byId("native-policy-section").classList.contains("native-policy-locked")).toBe(false);
  });

  // The lock runs AFTER the rows are rebuilt. Locking first would disable controls
  // that are about to be replaced by fresh enabled ones, which looks like it works
  // until the row count changes.
  it("locks rows that the same load rebuilt", async () => {
    await mount(view("trusted", [userRule("shell"), userRule("fs_write"), userRule("mcp")]));
    const buttons = ruleRemoveButtons();
    expect(buttons.length).toBeGreaterThan(0);
    for (const b of buttons) {
      expect(b.disabled).toBe(true);
    }
  });
});

describe("what a profile description promises", () => {
  // A profile has two halves and they reach different places. Its presets ride the
  // session door and cover only the sessions vibekit opens; Kiro creates a workflow
  // step's session itself, so a preset never arrives there. Only the loosest rung
  // also writes a durable user-scope rule, which is the half a step session reads —
  // so it is the only one that may claim step coverage, and it has to disclose the
  // durability it is buying that coverage with.
  it("names the durable user-scope rule and the step coverage on the loosest rung", async () => {
    await mount(view("guarded"));
    const desc = descriptionFor("unrestricted");
    expect(desc).toContain("durable");
    expect(desc).toContain("user permissions file");
    expect(desc).toContain("workflow steps");
    expect(desc).toContain("every Kiro client on this machine");
  });

  // The inverse of the same defect: a rung that quietly under-delivers. Each of
  // these sends presets and writes no file rule, so a workflow step gets none of
  // what the description promises unless the description says so.
  //
  // And it has to say it WITHOUT overstating the restriction, which is the trap the
  // first draft fell into ("a workflow step still asks even for that", where "that"
  // was reading a file). An uncovered step is not bare: it carries the bundled
  // agent's policy, which allows workspace reads and a few read-only commands, so a
  // description claiming a step asks for reads sends a reader up the ladder for
  // coverage they already have.
  it("says a workflow step is uncovered and what it still gets, on every rung that writes no rule", async () => {
    await mount(view("guarded"));
    for (const id of ["guarded", "read-only", "trusted"]) {
      const desc = descriptionFor(id);
      expect(desc).toContain("A workflow step is not covered");
      expect(desc).toContain("reads this workspace");
      expect(desc).toContain("asks before writing a file");
    }
  });

  // Subagents are covered on EVERY rung, so no rung may claim them: invoke_sub_agent
  // creates no session, so a subagent rides its parent's session id and inherits
  // whatever the parent was seeded with. Only step sessions were ever uncovered, and
  // billing the loosest rung as the only one covering subagents pushed a reader to
  // it on a false premise — the same class of defect as the posture claim this
  // change exists to fix.
  it("claims subagent coverage on no rung, because every rung already has it", async () => {
    await mount(view("guarded"));
    for (const id of ["guarded", "read-only", "trusted", "unrestricted", "custom"]) {
      expect(descriptionFor(id)).not.toContain("subagent");
    }
  });

  it("claims no durable rule on any rung that does not write one", async () => {
    await mount(view("guarded"));
    for (const id of ["guarded", "read-only", "trusted", "custom"]) {
      expect(descriptionFor(id)).not.toContain("durable");
    }
  });

  // Custom's own rules DO reach a step session — it sends no preset, so the files
  // are its whole policy — but only the scope that was MEASURED may be claimed. The
  // rule-add form defaults to workspace scope, and workspace reachability for a
  // session Kiro created itself is an inference rather than a measurement, so a flat
  // "they apply to workflow steps too" promised coverage for the scope a user's
  // rules land in by default on the strength of that inference.
  it("scopes Custom's step claim to the scope that was measured", async () => {
    await mount(view("guarded"));
    const desc = descriptionFor("custom");
    expect(desc).toContain("workflow steps");
    expect(desc).toContain("user scope");
  });
});

describe("the Security profile section hint", () => {
  it("states the durable rule, the step coverage and that the user's rules are kept", () => {
    const hint = securityProfileHint();
    expect(hint).toContain("durable rule to your user permissions file");
    expect(hint).toContain("workflow steps");
    expect(hint).toContain("never deleted");
  });

  // The hint used to say a selection "replaces the rules below with its own", which
  // the merge made false in the direction that matters: a reader would expect their
  // own rules to be gone and they are not.
  it("no longer claims a selection replaces the rules below", () => {
    expect(securityProfileHint()).not.toContain("replaces the rules below");
  });
});

describe("an empty Custom policy", () => {
  // Custom sends no presets, so with no rules of its own the agent asks before it
  // may even read a file. Leaving that to be discovered one prompt at a time is the
  // same defect as a control that silently does nothing.
  it("says that everything asks, including reading a file", async () => {
    await mount(view("custom"));
    expect(statusText()).toContain("no rules");
    expect(statusText()).toContain("reading a file");
  });

  it("says nothing once the table holds a rule", async () => {
    await mount(view("custom", [userRule("fs_read")]));
    expect(statusText()).toBe("");
  });

  it("says nothing on a named profile, which has presets behind it", async () => {
    await mount(view("guarded"));
    expect(statusText()).toBe("");
  });
});
