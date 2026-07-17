// ---------------------------------------------------------------------------
// "Permissions" section in the Settings panel.
//
// Two controllers live here:
//
//   - PermissionsUIController — vibekit's own complementary controls,
//     each wired to its settings.json field and re-rendered on change:
//       * shell_policy        no_commands | safe_commands | all_commands
//       * supervised_default  boolean
//       * agent_ignore_files  string[]
//     (plus the command-rule list, served from /api/permissions/commands).
//   - NativePolicyController — the native (Cedar) policy VIEW + editor,
//     the real tool-call authorization surface on v3.
//
// The old imperative "tool approval" mode (trust-all | trust-list |
// prompt, backed by the kiro-cli --trust-all-tools / --trust-tools spawn
// flags) was removed in 2026-07: those flags are inert on v3 (KAS's Cedar
// engine ignores them) and fed nothing else. Cedar is the approval surface.
// ---------------------------------------------------------------------------

import { patchSettings } from "./persist.js";
import type { AppSettings } from "./persist.js";
import { maybeEl } from "./dom.js";
import { apiGet } from "./api-client.js";
import { buildChip } from "./chip.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import {
  addRule,
  removeRule,
  editNativeRule,
  explainPolicy,
  type CommandRule,
} from "./actions/permissions.js";
import { reconcile } from "./reconcile.js";
import { onSSE } from "./bus.js";
import { confirm } from "./confirm.js";
import type { PolicyView, PolicyRule } from "./types.js";
import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// PermissionsUIController — encapsulates all module-level state.
// ---------------------------------------------------------------------------

type ShellPolicy = "no_commands" | "safe_commands" | "all_commands";
type RuleMode = "allow" | "deny";

class PermissionsUIController {
  private currentShellPolicy: ShellPolicy = "safe_commands";
  private commandRules: CommandRule[] = [];
  private ignoreFiles: string[] = [];
  private rulesController: AbortController | null = null;

  initPermissions(initial: AppSettings): void {
    // Supervised-mode default for new chats. (The old trust-all /
    // trust-list / prompt "tool approval" radios were removed — Cedar,
    // rendered by NativePolicyController below, is the approval surface
    // on v3.)
    const supCheckbox = maybeEl<HTMLInputElement>("supervised-default-checkbox");
    if (supCheckbox !== null) {
      supCheckbox.checked = initial.supervised_default === true;
      supCheckbox.addEventListener("change", () => {
        void patchSettings({ supervised_default: supCheckbox.checked });
      });
    }
  }

  initShellPolicy(initial: AppSettings): void {
    this.currentShellPolicy = initial.shell_policy ?? "safe_commands";

    for (const p of ["no_commands", "safe_commands", "all_commands"] as ShellPolicy[]) {
      const id = `shell-policy-${p}`;
      const radio = maybeEl<HTMLInputElement>(id);
      if (radio === null) {
        continue;
      }
      radio.checked = this.currentShellPolicy === p;
      radio.addEventListener("change", () => {
        if (!radio.checked) {
          return;
        }
        this.currentShellPolicy = p;
        void patchSettings({ shell_policy: p });
      });
    }

    const input = maybeEl<HTMLInputElement>("command-rules-input");
    const addBtn = maybeEl("command-rules-add");
    const modeSel = maybeEl<HTMLSelectElement>("command-rules-mode");
    const prioSel = maybeEl<HTMLSelectElement>("command-rules-priority");
    if (input !== null && addBtn !== null) {
      const submit = (): void => {
        const val = input.value.trim();
        if (val === "") {
          return;
        }
        const mode = modeSel?.value === "deny" ? "deny" : "allow";
        const priority = parseInt(prioSel?.value ?? "0", 10) || 0;
        void this.addRule(val, mode, priority);
        input.value = "";
        // Refocus for repeat entry (adding several rules in a row is the
        // common flow; without this every add costs a pointer round-trip).
        input.focus();
      };
      addBtn.addEventListener("click", submit);
      input.addEventListener("keydown", (e: KeyboardEvent) => {
        if (e.key === "Enter") {
          e.preventDefault();
          submit();
        }
      });
      registerCleanup(bindLoadingState("permissions.add_rule", addBtn as HTMLButtonElement));
      registerCleanup(
        bindLoadingState("permissions.remove_rule", addBtn as HTMLButtonElement, {
          preserveDisabled: true,
        }),
      );
    }

    void this.loadRules();
    this.initAgentIgnoreUI(initial);
  }

  async addWhitelistEntry(pattern: string): Promise<void> {
    await this.addRule(pattern, "allow", 0);
  }

  // --- Private: command rules ---

  /** Public: aborts any in-flight rules load. Wired to global cleanup
   *  so beforeunload doesn't leak the fetch handle. */
  cancelRulesLoad(): void {
    this.rulesController?.abort();
    this.rulesController = null;
  }

  private async loadRules(): Promise<void> {
    this.rulesController?.abort();
    this.rulesController = new AbortController();
    const { signal } = this.rulesController;
    const data = await apiGet<{ entries: CommandRule[] }>("/api/permissions/commands", signal);
    if (signal.aborted) {
      return;
    }
    if (data === null) {
      return;
    }
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
    this.commandRules = data.entries ?? [];
    this.renderRuleChips();
  }

  private renderRuleChips(): void {
    const container = maybeEl("command-rules-chips");
    const hint = maybeEl("command-rules-empty-hint");
    if (hint !== null) {
      hint.classList.toggle("hidden", this.commandRules.length > 0);
    }
    if (container === null) {
      return;
    }
    // Composite key (pattern:mode:priority) so a mode flip or priority change
    // re-mounts the chip fresh — rebuilding its badge text, the
    // `chip-rule-<mode>` class, AND the `.chip-mode` click closure together.
    // A key-by-pattern, mount-only reconcile would reuse the stale node and
    // leave all three pointing at the previous render. The empty state is a
    // sibling element (command-rules-empty-hint), toggled above, so this
    // container holds only chips — no replaceChildren/reconcile mixing.
    reconcile(container, this.commandRules, {
      key: (entry) => `${entry.pattern}:${entry.mode}:${String(entry.priority)}`,
      mount: (entry) => {
        const modeLabel = entry.mode === "deny" ? "Deny" : "Allow";
        const prioLabel = entry.priority > 0 ? ` P${String(entry.priority)}` : "";
        const chip = buildChip({
          label: entry.pattern,
          code: true,
          badge: { text: modeLabel + prioLabel, className: "chip-mode" },
          chipClass: `chip mono chip-rule-${entry.mode}`,
          onRemove: () => {
            void this.removeRule(entry.pattern);
          },
        });
        chip.dataset["pattern"] = entry.pattern;
        // Click the mode label to flip allow↔deny in place. The closure
        // captures THIS render's entry, and the composite key guarantees a
        // fresh mount on every flip, so the toggle direction stays correct.
        const modeEl = chip.querySelector<HTMLElement>(".chip-mode");
        if (modeEl !== null) {
          modeEl.addEventListener("click", (e) => {
            e.stopPropagation();
            const next: RuleMode = entry.mode === "allow" ? "deny" : "allow";
            void this.addRule(entry.pattern, next, entry.priority);
          });
        }
        return chip;
      },
    });
  }

  private setRules = (rules: CommandRule[]): void => {
    this.commandRules = rules;
    this.renderRuleChips();
  };

  private async removeRule(pattern: string): Promise<void> {
    if (!this.commandRules.some((e) => e.pattern === pattern)) {
      return;
    }
    await removeRule.dispatch({
      pattern,
      rules: this.commandRules,
      setRules: this.setRules,
      getCurrentRules: () => this.commandRules,
    });
  }

  private async addRule(pattern: string, mode: RuleMode, priority = 0): Promise<void> {
    const clean = pattern.trim();
    if (clean === "") {
      return;
    }
    await addRule.dispatch({
      pattern: clean,
      mode,
      priority,
      rules: this.commandRules,
      setRules: this.setRules,
      getCurrentRules: () => this.commandRules,
    });
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
      // Refocus for repeat entry, same as the command-rules adder.
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
registerCleanup(() => {
  controller.cancelRulesLoad();
});

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

  init(): void {
    const addBtn = maybeEl<HTMLButtonElement>("native-rule-add");
    if (addBtn === null) {
      return; // permissions panel not present in this build
    }
    addBtn.addEventListener("click", () => {
      void this.addRule();
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
    row.append(el("span", { className: `native-rule-effect eff-${r.effect}` }, r.effect));
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

  private async addRule(): Promise<void> {
    const scope = (maybeEl<HTMLSelectElement>("native-rule-scope")?.value ?? "workspace") as
      "user" | "workspace";
    const capability = maybeEl<HTMLSelectElement>("native-rule-capability")?.value ?? "";
    const effect = maybeEl<HTMLSelectElement>("native-rule-effect")?.value ?? "ask";
    const match = splitGlobs(maybeEl<HTMLInputElement>("native-rule-match")?.value ?? "");
    if (capability === "") {
      return;
    }
    const res = await editNativeRule.dispatch({ op: "add", scope, capability, effect, match });
    if (res !== null && res.error === undefined) {
      const mi = maybeEl<HTMLInputElement>("native-rule-match");
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
    const res = await explainPolicy.dispatch({ capability, resource });
    if (res === null) {
      out.textContent = "Could not evaluate (no active session).";
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
export function initShellPolicyUI(initial: AppSettings): void {
  controller.initShellPolicy(initial);
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

/** Thin alias kept for permission.ts: "approve and trust this
 *  command for the future" maps to adding an allow rule. */
export async function addWhitelistEntry(pattern: string): Promise<void> {
  await controller.addWhitelistEntry(pattern);
}
