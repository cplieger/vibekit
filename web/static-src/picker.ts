// ---------------------------------------------------------------------------
// Large-card model picker: shown when an empty chat needs a model and
// as the fallback view for the switch button when no active session
// exists yet.
//
// The models cache is populated by app.ts from two sources (REST at
// startup, per-session via bridge afterwards). The bridge already
// filters [Deprecated] and [Legacy] entries; the picker does no
// filtering of its own. [Internal] previews are kept on purpose.
// ---------------------------------------------------------------------------

import type { ModelInfo } from "./types.js";
import { escText, humanName } from "./strings.js";
import { $ } from "./dom.js";
import { getActive } from "./store.js";
import { wireArrowNav } from "./arrow-nav.js";

/** Per-agent label + description for the picker header. The agent name
 *  from the session is the lookup key; unknown agents fall back to the
 *  default build-agent copy. */
const AGENT_INFO: Record<string, { label: string; description: string }> = {
  "": {
    label: "Choose a model",
    description: "Start a build session with full tool access."
      + " The agent can read, write, and run commands in your workspace.",
  },
  "kiro_planner": {
    label: "Choose a model",
    description: "Start a planning session."
      + " The agent will help you think through architecture"
      + " and design without modifying files.",
  },
};

class ModelPickerController {
  private models: ModelInfo[] = [];
  private callback: ((modelId: string) => void) | null = null;
  private currentId = "";

  setModels(models: ModelInfo[]): void {
    this.models = models;
  }

  getCachedModels(): ModelInfo[] {
    return this.models;
  }

  show(currentModelId: string, onSelect: (modelId: string) => void, agent?: string): void {
    const picker = $.modelPicker;
    const grid = picker.querySelector(".picker-grid") as HTMLDivElement;
    const label = picker.querySelector(".picker-label") as HTMLDivElement;
    const info = AGENT_INFO[agent ?? ""] ?? AGENT_INFO[""]!;
    this.callback = onSelect;
    this.currentId = currentModelId;
    picker.setAttribute("aria-label", info.label);

    const svg = label.querySelector("svg");
    const svgHtml = svg !== null ? svg.outerHTML + " " : "";
    label.innerHTML = svgHtml + escText(info.label);

    let desc = picker.querySelector(".picker-desc") as HTMLDivElement | null;
    if (desc === null) {
      desc = document.createElement("div");
      desc.className = "picker-desc";
      label.after(desc);
    }
    desc.textContent = info.description;

    grid.replaceChildren();
    grid.setAttribute("role", "listbox");
    grid.setAttribute("aria-label", info.label);
    if (this.models.length === 0) {
      const loading = document.createElement("div");
      loading.className = "picker-btn picker-loading";
      loading.textContent = "Loading models…";
      loading.setAttribute("aria-busy", "true");
      loading.setAttribute("role", "option");
      grid.appendChild(loading);
    }
    for (const m of this.models) {
      const btn = document.createElement("button");
      btn.className = `picker-btn${m.model_id === currentModelId ? " active" : ""}`;
      btn.setAttribute("data-model", m.model_id);
      btn.setAttribute("role", "option");
      btn.setAttribute("aria-selected", m.model_id === currentModelId ? "true" : "false");
      btn.innerHTML = `<span class="picker-name">${escText(humanName(m.model_name || m.model_id))}</span>`
        + `<span class="picker-meta">${String(m.rate_multiplier)}x credits</span>`;
      btn.addEventListener("click", () => {
        for (const b of grid.querySelectorAll(".picker-btn")) {
          b.classList.remove("active");
          b.setAttribute("aria-selected", "false");
        }
        btn.classList.add("active");
        btn.setAttribute("aria-selected", "true");
        this.callback?.(m.model_id);
      });
      grid.appendChild(btn);
    }
    wireArrowNav(grid, ".picker-btn:not(.picker-loading)", { orientation: "horizontal" });
    picker.classList.remove("hidden");
    // Focus the active model button (or first) for keyboard users.
    const focusTarget = grid.querySelector<HTMLButtonElement>(".picker-btn.active")
      ?? grid.querySelector<HTMLButtonElement>(".picker-btn:not(.picker-loading)");
    focusTarget?.focus();
  }

  hide(): void {
    $.modelPicker.classList.add("hidden");
    this.callback = null;
    this.currentId = "";
  }

  refreshIfVisible(overrideModelId?: string): void {
    const picker = $.modelPicker;
    if (picker.classList.contains("hidden") || this.callback === null) return;
    const grid = picker.querySelector(".picker-grid") as HTMLDivElement;

    // Read current model from store rather than relying solely on this.currentId.
    const storeModel = getActive()?.model;
    const effectiveId = overrideModelId ?? storeModel ?? this.currentId;

    const hasRealButtons = grid.querySelector(".picker-btn:not(.picker-loading)") !== null;
    if (!hasRealButtons) {
      this.show(effectiveId, this.callback);
      return;
    }

    if (overrideModelId !== undefined) {
      for (const btn of grid.querySelectorAll(".picker-btn")) {
        const isActive = btn.getAttribute("data-model") === overrideModelId;
        btn.classList.toggle("active", isActive);
        btn.setAttribute("aria-selected", isActive ? "true" : "false");
      }
      this.currentId = overrideModelId;
      return;
    }
    const activeBtn = grid.querySelector(".picker-btn.active");
    const currentId = activeBtn?.getAttribute("data-model") ?? effectiveId;
    this.show(currentId, this.callback);
  }
}

const instance = new ModelPickerController();

export function setPickerModels(models: ModelInfo[]): void {
  instance.setModels(models);
}

export function getCachedModels(): ModelInfo[] {
  return instance.getCachedModels();
}

export function showModelPicker(
  currentModelId: string,
  onSelect: (modelId: string) => void,
  agent?: string,
): void {
  instance.show(currentModelId, onSelect, agent);
}

export function hideModelPicker(): void {
  instance.hide();
}

export function refreshPickerIfVisible(overrideModelId?: string): void {
  instance.refreshIfVisible(overrideModelId);
}
