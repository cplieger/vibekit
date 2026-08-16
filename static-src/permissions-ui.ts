// ---------------------------------------------------------------------------
// "Permissions" section in the Settings panel.
//
// Two controllers live here:
//
//   - PermissionsUIController — vibekit's own complementary controls,
//     each wired to its settings.json field and re-rendered on change:
//       * supervised_default  boolean
//       * agent_ignore_files  string[]
//   - NativePolicyController — the native (Cedar) policy VIEW + editor,
//     the real tool-call authorization surface on v3.
//
// Cedar is the sole tool-call authorization surface on v3: there is no
// client-side shell classifier, no trust modes, and no per-command rule
// list — the permission dialog's "Always allow" persists a native rule.
// ---------------------------------------------------------------------------

import { patchSettings } from "./persist.js";
import type { AppSettings } from "./persist.js";
import { maybeEl } from "./dom.js";
import { apiGet } from "./api-client.js";
import { buildChip } from "./chip.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { editNativeRule, explainPolicy } from "./actions/permissions.js";
import { reconcile } from "./reconcile.js";
import { onSSE } from "./bus.js";
import { confirm } from "./confirm.js";
import type { PolicyView, PolicyRule } from "./types.js";
import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// PermissionsUIController — encapsulates all module-level state.
// ---------------------------------------------------------------------------

class PermissionsUIController {
  private ignoreFiles: string[] = [];

  initPermissions(initial: AppSettings): void {
    // Supervised-mode default for new chats. (Tool-call approval itself is
    // the native Cedar policy, rendered by NativePolicyController below.)
    const supCheckbox = maybeEl<HTMLInputElement>("supervised-default-checkbox");
    if (supCheckbox !== null) {
      supCheckbox.checked = initial.supervised_default === true;
      supCheckbox.addEventListener("change", () => {
        void patchSettings({ supervised_default: supCheckbox.checked });
      });
    }
    // Whether a SCHEDULED run's tool request is approved or refused when nobody
    // answers it. Its own switch rather than a read of the policy above, because
    // approving while watching is a different consent from approving unattended.
    const schedCheckbox = maybeEl<HTMLInputElement>("scheduled-auto-approve-checkbox");
    if (schedCheckbox !== null) {
      schedCheckbox.checked = initial.scheduled_auto_approve === true;
      schedCheckbox.addEventListener("change", () => {
        void patchSettings({ scheduled_auto_approve: schedCheckbox.checked });
      });
    }
    this.initAgentIgnoreUI(initial);
  }

  // --- Private: agent ignore files ---

  private initAgentIgnoreUI(initial: AppSettings): void {
    this.ignoreFiles = [...(initial.agent_ignore_files ?? [])];
    this.renderIgnoreChips();

    const input = maybeEl<HTMLInputElement>("agent-ignore-input");
    const addBtn = maybeEl("agent-ignore-add");
    if (input === null || addBtn === null) {
      return;
    }
    const submit = (): void => {
      const val = input.value.trim();
      if (val === "") {
        return;
      }
      if (this.ignoreFiles.includes(val)) {
        input.value = "";
        return;
      }
      this.ignoreFiles.push(val);
      void patchSettings({ agent_ignore_files: this.ignoreFiles });
      input.value = "";
      // Refocus for repeat entry (adding several files in a row).
      input.focus();
      this.renderIgnoreChips();
    };
    addBtn.addEventListener("click", submit);
    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        submit();
      }
    });
  }

  private renderIgnoreChips(): void {
    const container = maybeEl("agent-ignore-chips");
    const hint = maybeEl("agent-ignore-empty-hint");
    if (hint !== null) {
      hint.classList.toggle("hidden", this.ignoreFiles.length > 0);
    }
    if (container === null) {
      return;
    }
    // Ignore entries are immutable strings, so a key-by-entry, mount-only
    // reconcile is sufficient (no update fn needed): add/remove touches only
    // the changed chip and preserves the rest. The empty state is a sibling
    // element (agent-ignore-empty-hint), toggled above.
    reconcile(container, this.ignoreFiles, {
      key: (entry) => entry,
      mount: (entry) =>
        buildChip({
          label: entry,
          code: true,
          chipClass: "chip mono",
          onRemove: () => {
            this.ignoreFiles = this.ignoreFiles.filter((f) => f !== entry);
            void patchSettings({ agent_ignore_files: this.ignoreFiles });
            this.renderIgnoreChips();
          },
        }),
    });
  }
}

// Singleton instance — internal to the module.
const controller = new PermissionsUIController();

// ---------------------------------------------------------------------------
// NativePolicyController — the native (Cedar) policy VIEW + conservative
// file-writing editor.
//
// The VIEW (GET /api/permissions) is the source of truth for what kiro-cli
// ENFORCES: the layered rule set (kiro/administration/user/workspace/agent/
// session) with per-rule capability, 3-valued effect, path match/exclude, and
// source provenance. The EDITOR writes the user/workspace permissions.yaml
// (POST /api/permissions/rules), which KAS hot-reloads — the server never
// mutates the running policy directly. It is conservative: a new rule
// defaults to Ask (server-enforced), and removing a Deny (which widens
// access) is confirmed first. The list is a pure server projection: refetched
// after every edit and on the permissions_changed SSE, so it can't drift from
// what KAS enforces.
// ---------------------------------------------------------------------------

const NATIVE_SCOPE_ORDER = ["kiro", "administration", "user", "workspace", "agent", "session"];
const NATIVE_SCOPE_LABEL: Record<string, string> = {
  kiro: "Kiro built-in (read-only)",
  administration: "Administration (read-only)",
  user: "User — global",
  workspace: "Workspace",
  agent: "Agent — current mode (read-only)",
  session: "Session — runtime (read-only)",
};

function splitGlobs(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

function shortSource(src: string): string {
  if (src === "") {
    return "";
  }
  const parts = src.split("/");
  return parts.length <= 2 ? src : ".../" + parts.slice(-2).join("/");
}

class NativePolicyController {
  private writable = new Set<string>();
  private ctrl: AbortController | null = null;
  /** The relaxation's membership, straight from the policy view. Never a local
   *  constant: the set decides what one click grants, and policyfile is where
   *  the capability vocabulary and the exclusion reasons live. */
  private relaxCaps: string[] = [];
  /** Suppresses the relaxation checkbox's change handler while a render writes
   *  its state, so painting the read-back cannot look like a user click. */
  private paintingRelax = false;
  /** A partial-write report, carried across the refetch that follows it. It has
   *  to live here rather than being written straight to the DOM: every switch
   *  ends in a reload, and the reload repaints this same line — so a note
   *  written before it would be erased by the very render meant to show what
   *  landed. */
  private relaxNote = "";

  init(): void {
    const addBtn = maybeEl<HTMLButtonElement>("native-rule-add");
    if (addBtn === null) {
      return; // permissions panel not present in this build
    }
    addBtn.addEventListener("click", () => {
      void this.addRule();
    });
    const relaxBox = maybeEl<HTMLInputElement>("workspace-relax-checkbox");
    relaxBox?.addEventListener("change", () => {
      if (this.paintingRelax) {
        return;
      }
      void this.setRelaxed(relaxBox.checked, relaxBox);
    });
    maybeEl<HTMLButtonElement>("native-explain-run")?.addEventListener("click", () => {
      void this.runExplain();
    });
    registerCleanup(bindLoadingState("permissions.edit_native_rule", addBtn));
    registerCleanup(
      onSSE("permissions_changed", () => {
        void this.load();
      }),
    );
    registerCleanup(
      onSSE("policy_error", (_chatID, payload) => {
        const msgs = (payload.errors ?? []).map((e) => e.message).filter((m) => m !== "");
        this.showStatus(
          msgs.length > 0 ? "Policy error: " + msgs.join("; ") : "Policy error.",
          true,
        );
      }),
    );
    registerCleanup(() => {
      this.cancel();
    });
    // The initial fetch is deliberately NOT fired here (B2): init() runs
    // during boot (restoreAll, before the auth check), and /api/permissions
    // is utility-bridge-backed — an expensive fetch for a panel that isn't
    // visible. refresh() fires on the Permissions tab's first activation
    // instead (settings-tabs loader map, wired in settings.ts); the
    // permissions_changed SSE registered above keeps it fresh afterwards.
  }

  /** Fetch + render the policy view. Public for the lazy tab loader. */
  refresh(): void {
    void this.load();
  }

  cancel(): void {
    this.ctrl?.abort();
    this.ctrl = null;
  }

  private async load(): Promise<void> {
    this.ctrl?.abort();
    this.ctrl = new AbortController();
    const { signal } = this.ctrl;
    const data = await apiGet<PolicyView>("/api/permissions", signal);
    if (signal.aborted || data === null) {
      return;
    }
    this.writable = new Set(data.writable_scopes);
    this.relaxCaps = data.relax_capabilities;
    this.populateCapabilities(data.capabilities);
    if (data.available) {
      this.showStatus("", false);
    } else {
      this.showStatus(
        "Live policy unavailable (no active session) — showing your saved user/workspace rules only.",
        false,
      );
    }
    this.render(data.rules);
    this.renderRelaxState(data.rules);
  }

  // --- The workspace relaxation switch ---------------------------------------
  //
  // One switch, N rules, and it has to be N: policyRuleBody carries exactly one
  // capability and Upsert is deliberately discrete-rule (no auto-merge into a
  // match array), which is what makes each rule individually removable from the
  // Active policy list above. There is no batch op on the wire and adding one
  // would trade that reversibility for a single round trip.
  //
  // Scope is always workspace. Per-chat is unexpressible — policyfile defines
  // exactly two writable scopes, user and workspace — and user scope would leak
  // the grant into every other workspace on the box.

  /** Is this rule one the relaxation wrote? Bare (no globs), allow, workspace,
   *  and a member of the set. The glob check is what keeps a hand-authored
   *  `fs_write allow match:src/**` from reading as the relaxation being on. */
  private isRelaxRule(r: PolicyRule): boolean {
    return (
      r.scope === "workspace" &&
      r.effect === "allow" &&
      (r.match ?? []).length === 0 &&
      (r.exclude ?? []).length === 0 &&
      this.relaxCaps.includes(r.capability)
    );
  }

  /** Paint the switch from the rules the server just reported, and say what is
   *  actually on rather than rounding a partial state to a boolean. A partial
   *  set is a real state — an interrupted write, a rule removed by hand from the
   *  list above — so the box goes indeterminate and the status line names the
   *  count. Reporting it as simply off would invite a click that then tries to
   *  write rules that already exist. */
  private renderRelaxState(rules: PolicyRule[]): void {
    const box = maybeEl<HTMLInputElement>("workspace-relax-checkbox");
    if (box === null) {
      return;
    }
    const total = this.relaxCaps.length;
    const present = new Set(rules.filter((r) => this.isRelaxRule(r)).map((r) => r.capability));
    const n = present.size;
    this.paintingRelax = true;
    box.checked = total > 0 && n === total;
    box.indeterminate = n > 0 && n < total;
    this.paintingRelax = false;
    const status = maybeEl("workspace-relax-status");
    if (status === null) {
      return;
    }
    let text = "";
    if (n === total && total > 0) {
      text = `On. ${String(total)} capabilities allowed without asking: ${this.relaxCaps.join(", ")}.`;
    } else if (n > 0) {
      const missing = this.relaxCaps.filter((c) => !present.has(c));
      text =
        `Partly on: ${String(n)} of ${String(total)} allowed. Still asking for ${missing.join(", ")}. ` +
        `Switch on to add the rest, or off to remove all ${String(n)}.`;
    }
    if (this.relaxNote !== "") {
      text = text === "" ? this.relaxNote : `${this.relaxNote} ${text}`;
    }
    status.textContent = text;
    status.classList.toggle("hidden", text === "");
    status.classList.toggle("native-policy-status-error", this.relaxNote !== "");
  }

  /** Turn the relaxation on or off. Switching ON widens what the agent may do,
   *  so it takes the same confirm the effect editor uses before a widening
   *  change; switching OFF narrows and needs none, matching removeRule (which
   *  confirms only for a deny). Rolls the box back on cancel or on a write that
   *  landed nothing, and reports a partial result rather than claiming success:
   *  each rule is its own atomic file write, so the SET is not atomic. */
  private async setRelaxed(on: boolean, box: HTMLInputElement): Promise<void> {
    const caps = this.relaxCaps;
    if (caps.length === 0) {
      return;
    }
    // A fresh attempt supersedes the last one's report.
    this.relaxNote = "";
    if (on) {
      const ok = await confirm(
        `Allow ${String(caps.length)} capabilities in this workspace without asking (${caps.join(", ")})? ` +
          `That WIDENS what the agent is allowed to do, in every chat here, until you switch it back off.`,
        "Widen workspace policy",
      );
      if (!ok) {
        void this.load();
        return;
      }
    }
    box.disabled = true;
    let failed = 0;
    for (const capability of caps) {
      const res = await editNativeRule.dispatch(
        on
          ? { op: "add", scope: "workspace", capability, effect: "allow", confirm: true }
          : { op: "remove", scope: "workspace", capability, effect: "allow", confirm: true },
      );
      if (res === null || res.error !== undefined) {
        failed++;
      }
    }
    box.disabled = false;
    if (failed > 0) {
      this.relaxNote =
        `${String(caps.length - failed)} of ${String(caps.length)} rules ${on ? "written" : "removed"}; ` +
        `${String(failed)} failed.`;
    }
    // Repaint from the server either way: the read-back is the only honest
    // report of what landed, and the note above rides that same render.
    void this.load();
  }

  private populateCapabilities(caps: string[]): void {
    for (const id of ["native-rule-capability", "native-explain-capability"]) {
      const sel = maybeEl<HTMLSelectElement>(id);
      // Populate only when empty: idempotent across refetches (so an SSE
      // reload doesn't reset an in-progress selection) and correct per fresh
      // DOM in tests.
      if (sel === null || sel.options.length > 0) {
        continue;
      }
      sel.replaceChildren(
        ...caps.map((c) => {
          const opt = el("option", { value: c }, c) as HTMLOptionElement;
          opt.value = c; // set the property explicitly so the value is usable everywhere
          return opt;
        }),
      );
    }
  }

  private render(rules: PolicyRule[]): void {
    const emptyHint = maybeEl("native-policy-empty-hint");
    if (emptyHint !== null) {
      emptyHint.classList.toggle("hidden", rules.length > 0);
    }
    const list = maybeEl("native-policy-list");
    if (list === null) {
      return;
    }
    const groups = new Map<string, PolicyRule[]>();
    for (const r of rules) {
      const g = groups.get(r.scope) ?? [];
      g.push(r);
      groups.set(r.scope, g);
    }
    const order = [
      ...NATIVE_SCOPE_ORDER,
      ...[...groups.keys()].filter((s) => !NATIVE_SCOPE_ORDER.includes(s)),
    ];
    const frag: HTMLElement[] = [];
    for (const scope of order) {
      const grp = groups.get(scope);
      if (grp === undefined || grp.length === 0) {
        continue;
      }
      frag.push(
        el("div", { className: "native-policy-scope-label" }, NATIVE_SCOPE_LABEL[scope] ?? scope),
      );
      for (const r of grp) {
        frag.push(this.ruleRow(r));
      }
    }
    list.replaceChildren(...frag);
  }

  private ruleRow(r: PolicyRule): HTMLElement {
    const row = el("div", { className: `native-rule native-rule-${r.effect}` });
    if (this.writable.has(r.scope)) {
      row.append(this.effectSelect(r));
    } else {
      row.append(el("span", { className: `native-rule-effect eff-${r.effect}` }, r.effect));
    }
    row.append(el("span", { className: "native-rule-cap mono" }, r.capability));
    const globs = [
      ...(r.match ?? []).map((m) => "+" + m),
      ...(r.exclude ?? []).map((e) => "\u2212" + e),
    ];
    if (globs.length > 0) {
      row.append(el("span", { className: "native-rule-globs mono" }, globs.join("  ")));
    }
    const src = el("span", { className: "native-rule-src" }, shortSource(r.source));
    if (r.source !== "") {
      src.setAttribute("title", r.source);
    }
    row.append(src);
    if (this.writable.has(r.scope)) {
      const rm = el("button", { type: "button", className: "native-rule-remove" }, "\u00d7");
      rm.setAttribute("aria-label", "Remove rule");
      rm.setAttribute("title", "Remove rule");
      rm.addEventListener("click", () => {
        void this.removeRule(r);
      });
      row.append(rm);
    }
    return row;
  }

  /** Build the in-place effect editor for a writable rule: a select styled
   *  as the effect badge. A widening change (deny→ask, deny→allow,
   *  ask→allow) grants the agent more than it had, so it is confirmed
   *  first — same guardrail as removing a deny. The select reverts on
   *  cancel or a failed write; a successful write refetches the view. */
  private effectSelect(r: PolicyRule): HTMLSelectElement {
    const sel = el("select", {
      className: `native-rule-effect eff-${r.effect}`,
    }) as HTMLSelectElement;
    for (const eff of ["allow", "ask", "deny"]) {
      const opt = el("option", { value: eff }, eff) as HTMLOptionElement;
      opt.value = eff;
      opt.selected = eff === r.effect;
      sel.append(opt);
    }
    sel.setAttribute("aria-label", `Effect for the ${r.capability} rule`);
    sel.setAttribute("title", "Change this rule's effect");
    sel.addEventListener("change", () => {
      void this.updateEffect(r, sel);
    });
    return sel;
  }

  private async updateEffect(r: PolicyRule, sel: HTMLSelectElement): Promise<void> {
    const next = sel.value;
    if (next === r.effect) {
      return;
    }
    const RANK: Record<string, number> = { deny: 2, ask: 1, allow: 0 };
    let confirmFlag = false;
    if ((RANK[next] ?? 0) < (RANK[r.effect] ?? 0)) {
      const ok = await confirm(
        `Change this "${r.capability}" rule from ${r.effect} to ${next}? That WIDENS what the agent is allowed to do.`,
        "Widen rule",
      );
      if (!ok) {
        sel.value = r.effect;
        return;
      }
      confirmFlag = true;
    }
    const res = await editNativeRule.dispatch({
      op: "update",
      scope: r.scope as "user" | "workspace",
      capability: r.capability,
      effect: r.effect,
      new_effect: next,
      match: r.match ?? [],
      exclude: r.exclude ?? [],
      confirm: confirmFlag,
    });
    if (res !== null && res.error === undefined) {
      void this.load();
    } else {
      sel.value = r.effect;
    }
  }

  private async addRule(): Promise<void> {
    const scope = (maybeEl<HTMLSelectElement>("native-rule-scope")?.value ?? "workspace") as
      "user" | "workspace";
    const capability = maybeEl<HTMLSelectElement>("native-rule-capability")?.value ?? "";
    const effect = maybeEl<HTMLSelectElement>("native-rule-effect")?.value ?? "ask";
    const match = splitGlobs(maybeEl<HTMLInputElement>("native-rule-match")?.value ?? "");
    const exclude = splitGlobs(maybeEl<HTMLInputElement>("native-rule-exclude")?.value ?? "");
    if (capability === "") {
      return;
    }
    const res = await editNativeRule.dispatch({
      op: "add",
      scope,
      capability,
      effect,
      match,
      exclude,
    });
    if (res !== null && res.error === undefined) {
      const mi = maybeEl<HTMLInputElement>("native-rule-match");
      const xi = maybeEl<HTMLInputElement>("native-rule-exclude");
      if (xi !== null) {
        xi.value = "";
      }
      if (mi !== null) {
        mi.value = "";
        // Refocus for repeat entry (adding several rules in a row).
        mi.focus();
      }
      void this.load();
    }
  }

  private async removeRule(r: PolicyRule): Promise<void> {
    const scope = r.scope as "user" | "workspace";
    let confirmFlag = false;
    if (r.effect === "deny") {
      const ok = await confirm(
        `Remove this deny rule for "${r.capability}"? That WIDENS what the agent is allowed to do.`,
        "Remove deny rule",
      );
      if (!ok) {
        return;
      }
      confirmFlag = true;
    }
    const res = await editNativeRule.dispatch({
      op: "remove",
      scope,
      capability: r.capability,
      effect: r.effect,
      match: r.match ?? [],
      exclude: r.exclude ?? [],
      confirm: confirmFlag,
    });
    if (res !== null && res.error === undefined) {
      void this.load();
    }
  }

  private async runExplain(): Promise<void> {
    const capability = maybeEl<HTMLSelectElement>("native-explain-capability")?.value ?? "";
    const resource = (maybeEl<HTMLInputElement>("native-explain-resource")?.value ?? "").trim();
    const out = maybeEl("native-explain-result");
    if (capability === "" || out === null) {
      return;
    }
    // Shell decisions are always resource-scoped (there is no
    // command-independent shell decision to simulate).
    if (capability === "shell" && resource === "") {
      out.textContent = "Enter a command to test the shell capability.";
      return;
    }
    const res = await explainPolicy.dispatch({ capability, resource });
    if (res === null) {
      out.textContent = "Could not evaluate — check that a chat session is active.";
      return;
    }
    const parts = [`Effect: ${res.effect}`];
    if (res.scope !== undefined && res.scope !== "") {
      parts.push(`scope: ${res.scope}`);
    }
    if (res.source !== undefined && res.source !== "") {
      parts.push(`source: ${shortSource(res.source)}`);
    }
    out.textContent = parts.join(" \u00b7 ");
  }

  private showStatus(text: string, isError: boolean): void {
    const s = maybeEl("native-policy-status");
    if (s === null) {
      return;
    }
    s.textContent = text;
    s.classList.toggle("hidden", text === "");
    s.classList.toggle("native-policy-status-error", isError);
  }
}

const nativePolicy = new NativePolicyController();

// Public delegate functions preserving the existing module API.
export function initPermissionsUI(initial: AppSettings): void {
  controller.initPermissions(initial);
}

/** Initialise the native Cedar policy view + editor: wires the add-rule /
 *  explain controls and the permissions_changed SSE refetch. Does NOT fetch
 *  GET /api/permissions — the initial load is lazy (loadNativePolicy). */
export function initNativePolicyUI(): void {
  nativePolicy.init();
}

/** Load the native policy view. Wired to the Permissions tab's first
 *  activation (settings-tabs loader map) instead of boot, so the
 *  bridge-backed /api/permissions endpoint isn't hit for an invisible
 *  panel. Safe to call repeatedly (in-flight loads are aborted). */
export function loadNativePolicy(): void {
  nativePolicy.refresh();
}
