// ---------------------------------------------------------------------------
// "Tool permissions" section in the Settings panel.
//
// Three orthogonal layers live here, encapsulated in the
// PermissionsUIController class. The module wires each to its
// settings.json field and re-renders on change.
//
//   1. permission_mode     trust-all | trust-list | prompt
//   2. shell_policy        no_commands | safe_commands | all_commands
//   3. supervised_default  boolean
//
// Layers (1) and (2) apply on next bridge spawn. Layer (3) applies
// when a new chat is created.
// ---------------------------------------------------------------------------

import { patchSettings } from "./persist.js";
import type { PermissionMode, AppSettings } from "./persist.js";
import { el } from "./dom.js";
import { apiGet } from "./api-client.js";
import { buildChip } from "./ui-primitives.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { addRule, removeRule, type CommandRule } from "./actions/permissions.js";
import { reconcile } from "./reconcile.js";

// Common kiro-cli tool names. Shown as "+" menu suggestions when adding to
// the trust list.
const SUGGESTED_TOOLS = [
  "readFile",
  "readCode",
  "readMultipleFiles",
  "listDirectory",
  "fileSearch",
  "grepSearch",
  "fsWrite",
  "fsAppend",
  "strReplace",
  "deleteFile",
  "smartRelocate",
  "semanticRename",
  "executePwsh",
  "webFetch",
  "remote_web_search",
] as const;

// ---------------------------------------------------------------------------
// PermissionsUIController — encapsulates all module-level state.
// ---------------------------------------------------------------------------

type ShellPolicy = "no_commands" | "safe_commands" | "all_commands";
type RuleMode = "allow" | "deny";

class PermissionsUIController {
  private currentMode: PermissionMode = "trust-all";
  private currentList: string[] = [];
  private currentShellPolicy: ShellPolicy = "safe_commands";
  private commandRules: CommandRule[] = [];
  private ignoreFiles: string[] = [];
  private rulesController: AbortController | null = null;

  initPermissions(initial: AppSettings): void {
    this.currentMode = initial.permission_mode ?? "trust-all";
    this.currentList = [...(initial.trust_tools ?? [])];

    for (const m of ["trust-all", "trust-list", "prompt"] as PermissionMode[]) {
      const radio = el<HTMLInputElement>(`perm-mode-${m}`);
      radio.checked = this.currentMode === m;
      radio.addEventListener("change", () => {
        if (!radio.checked) {
          return;
        }
        this.currentMode = m;
        void patchSettings({ permission_mode: m });
        this.renderEditor();
      });
    }

    const supCheckbox = document.getElementById(
      "supervised-default-checkbox",
    ) as HTMLInputElement | null;
    if (supCheckbox !== null) {
      supCheckbox.checked = initial.supervised_default === true;
      supCheckbox.addEventListener("change", () => {
        void patchSettings({ supervised_default: supCheckbox.checked });
      });
    }

    const adder = el("trust-list-add");
    adder.setAttribute("aria-label", "Add trusted tool");
    adder.setAttribute("aria-expanded", "false");
    adder.addEventListener("click", () => {
      this.toggleMenu();
    });

    const menu = el<HTMLDivElement>("trust-list-menu");
    document.addEventListener("click", (e: MouseEvent) => {
      const t = e.target as Node;
      if (!adder.contains(t) && !menu.contains(t)) {
        menu.classList.add("hidden");
        adder.setAttribute("aria-expanded", "false");
      }
    });

    this.renderEditor();
  }

  initShellPolicy(initial: AppSettings): void {
    this.currentShellPolicy = initial.shell_policy ?? "safe_commands";

    for (const p of ["no_commands", "safe_commands", "all_commands"] as ShellPolicy[]) {
      const id = `shell-policy-${p}`;
      const radio = document.getElementById(id) as HTMLInputElement | null;
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

    const input = document.getElementById("command-rules-input") as HTMLInputElement | null;
    const addBtn = document.getElementById("command-rules-add");
    const modeSel = document.getElementById("command-rules-mode") as HTMLSelectElement | null;
    const prioSel = document.getElementById("command-rules-priority") as HTMLSelectElement | null;
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

  // --- Private: permission mode ---

  private renderEditor(): void {
    const editor = el<HTMLDivElement>("trust-list-editor");
    editor.classList.toggle("hidden", this.currentMode !== "trust-list");
    if (this.currentMode !== "trust-list") {
      return;
    }
    this.renderChips();
    const hint = el<HTMLParagraphElement>("trust-list-empty-hint");
    hint.classList.toggle("hidden", this.currentList.length > 0);
  }

  private renderChips(): void {
    const chips = el<HTMLDivElement>("trust-list-chips");
    chips.replaceChildren();
    for (const name of this.currentList) {
      chips.appendChild(
        buildChip({
          label: name,
          onRemove: () => {
            this.removeTool(name);
          },
        }),
      );
    }
  }

  private addTool(name: string): void {
    const clean = name.trim();
    if (clean === "") {
      return;
    }
    if (this.currentList.includes(clean)) {
      return;
    }
    this.currentList.push(clean);
    void patchSettings({ trust_tools: this.currentList });
    this.renderEditor();
  }

  private removeTool(name: string): void {
    this.currentList = this.currentList.filter((n) => n !== name);
    void patchSettings({ trust_tools: this.currentList });
    this.renderEditor();
  }

  private toggleMenu(): void {
    const menu = el<HTMLDivElement>("trust-list-menu");
    const adder = el("trust-list-add");
    if (!menu.classList.contains("hidden")) {
      menu.classList.add("hidden");
      adder.setAttribute("aria-expanded", "false");
      return;
    }
    menu.replaceChildren();

    const remaining = SUGGESTED_TOOLS.filter((n) => !this.currentList.includes(n));
    reconcile(menu, remaining, {
      key: (name: string) => name,
      mount: (name: string) => {
        const item = document.createElement("button");
        item.type = "button";
        item.className = "chip-menu-item";
        item.textContent = name;
        item.addEventListener("click", () => {
          this.addTool(name);
          menu.classList.add("hidden");
          adder.setAttribute("aria-expanded", "false");
        });
        return item;
      },
    });

    menu.classList.remove("hidden");
    adder.setAttribute("aria-expanded", "true");
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
    const container = document.getElementById("command-rules-chips");
    if (container === null) {
      return;
    }
    container.replaceChildren();

    if (this.commandRules.length === 0) {
      const hint = document.createElement("p");
      hint.className = "text-muted text-sm";
      hint.textContent =
        "No rules. Add an allow pattern to auto-approve under Safe mode, or a deny pattern to force a prompt even under Allow all.";
      container.appendChild(hint);
      return;
    }

    for (const entry of this.commandRules) {
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
      // Click the mode label to flip allow↔deny in place.
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const modeEl = chip.querySelector(".chip-mode")!;
      modeEl.addEventListener("click", (e) => {
        e.stopPropagation();
        const next: RuleMode = entry.mode === "allow" ? "deny" : "allow";
        void this.addRule(entry.pattern, next, entry.priority);
      });
      container.appendChild(chip);
    }
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

    const input = document.getElementById("agent-ignore-input") as HTMLInputElement | null;
    const addBtn = document.getElementById("agent-ignore-add");
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
    const container = document.getElementById("agent-ignore-chips");
    if (container === null) {
      return;
    }
    container.replaceChildren();
    if (this.ignoreFiles.length === 0) {
      const hint = document.createElement("p");
      hint.className = "text-muted text-sm";
      hint.textContent =
        "No ignore files. Common choices: .gitignore (keeps all gitignored paths off-limits to reads), .kiroignore (dedicated vibekit-only list).";
      container.appendChild(hint);
      return;
    }
    for (const entry of this.ignoreFiles) {
      container.appendChild(
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
      );
    }
  }
}

// Singleton instance — internal to the module.
const controller = new PermissionsUIController();
registerCleanup(() => {
  controller.cancelRulesLoad();
});

// Public delegate functions preserving the existing module API.
export function initPermissionsUI(initial: AppSettings): void {
  controller.initPermissions(initial);
}
export function initShellPolicyUI(initial: AppSettings): void {
  controller.initShellPolicy(initial);
}

/** Thin alias kept for permission.ts: "approve and trust this
 *  command for the future" maps to adding an allow rule. */
export async function addWhitelistEntry(pattern: string): Promise<void> {
  await controller.addWhitelistEntry(pattern);
}
