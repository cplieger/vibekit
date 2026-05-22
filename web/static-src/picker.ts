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
    if (this.models.length === 0) {
      const loading = document.createElement("div");
      loading.className = "picker-btn picker-loading";
      loading.textContent = "Loading models…";
      loading.setAttribute("aria-busy", "true");
      grid.appendChild(loading);
    }
    for (const m of this.models) {
      const btn = document.createElement("button");
      btn.className = `picker-btn${m.model_id === currentModelId ? " active" : ""}`;
      btn.setAttribute("data-model", m.model_id);
      btn.innerHTML = `<span class="picker-name">${escText(humanName(m.model_name || m.model_id))}</span>`
        + `<span class="picker-meta">${String(m.rate_multiplier)}x credits</span>`;
      btn.addEventListener("click", () => {
        for (const b of grid.querySelectorAll(".picker-btn")) b.classList.remove("active");
        btn.classList.add("active");
        this.callback?.(m.model_id);
      });
      grid.appendChild(btn);
    }
    picker.classList.remove("hidden");
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

    const hasRealButtons = grid.querySelector(".picker-btn:not(.picker-loading)") !== null;
    if (!hasRealButtons) {
      const currentId = overrideModelId ?? this.currentId;
      this.show(currentId, this.callback);
      return;
    }

    if (overrideModelId !== undefined) {
      for (const btn of grid.querySelectorAll(".picker-btn")) {
        btn.classList.toggle("active", btn.getAttribute("data-model") === overrideModelId);
      }
      this.currentId = overrideModelId;
      return;
    }
    const activeBtn = grid.querySelector(".picker-btn.active");
    const currentId = activeBtn?.getAttribute("data-model") ?? this.currentId;
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
