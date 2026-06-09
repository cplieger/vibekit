// ---------------------------------------------------------------------------
// Model switcher: the pill button + expandable model list + queued-switch
// logic. The pill expands inline to show available models; selecting one
// routes through requestModelSwitch which handles all four paths (local,
// empty chat, idle, mid-turn queue).
// ---------------------------------------------------------------------------

import { getActive, getActiveId, isThinking, contextSizeFor, setModel } from "./store.js";
import { onBus, BUS_TURN_IDLE } from "./bus.js";
import { $ } from "./dom.js";
import { humanName } from "./strings.js";
import { switchModel } from "./actions/chat.js";
import { wireArrowNav } from "./arrow-nav.js";
import { setCurrentModel, setLastModel } from "./session-context.js";
import {
  showModelPicker,
  hideModelPicker,
  refreshPickerIfVisible,
  getCachedModels,
} from "./picker.js";
import { refreshContextUI } from "./context-ui.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { bindLoadingState } from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { el } from "@cplieger/reactive";
import { send } from "./transport.js";
import { patchSettings } from "./persist.js";
import type { ModelInfo } from "./types.js";

/** Canonical effort levels with display labels. Single source of truth
 *  for the UI renderer and persistence layer. */
export const EFFORT_LEVELS = [
  { id: "low", label: "low" },
  { id: "medium", label: "medium" },
  { id: "high", label: "high" },
  { id: "xhigh", label: "x-high" },
  { id: "max", label: "max" },
] as const;

export type EffortLevel = (typeof EFFORT_LEVELS)[number]["id"];

type QueueState =
  | { status: "idle" }
  | { status: "queued"; modelID: string; chatID: string }
  | { status: "switching"; modelID: string };

class ModelSwitchController {
  private queueState: QueueState = { status: "idle" };

  init(): void {
    const expandContent = $.modelSwitchList;
    makeExpandable($.switchModelBtn, expandContent, {
      onExpand: () => {
        const session = getActive();
        const isEmpty =
          session === undefined || (session.message_count === 0 && session.messages.length === 0);
        if (isEmpty) {
          setTimeout(() => {
            collapseAll();
            this.openRichPicker();
          }, 0);
          return;
        }
        this.renderCondensedList();
      },
    });
    bindLoadingState("chat.switch_model", $.switchModelBtn, { pendingClass: "switching" });
    onBus(BUS_TURN_IDLE, (chatID: string) => {
      this.drainQueue(chatID);
    });
  }

  private openRichPicker(): void {
    const currentModel = getActive()?.model ?? "";
    const agent = getActive()?.agent ?? "";
    showModelPicker(
      currentModel,
      (modelID: string) => {
        this.applyLocalChoice(modelID);
        hideModelPicker();
      },
      agent,
    );
  }

  private renderCondensedList(): void {
    const list = $.modelSwitchList;
    list.setAttribute("role", "listbox");
    list.setAttribute("aria-label", "Available models");
    const session = getActive();
    if (session === undefined) {
      reconcile(list, [] as ModelInfo[], {
        key: () => "",
        mount: () => el("div"),
      });
      return;
    }
    const current = session.model;

    // Effort row: always visible at the top. Five tier buttons.
    // The active level is read from vibekit's config (stored in the
    // session context); clicking dispatches set_effort.
    this.ensureEffortRow(list);

    reconcile(list, getCachedModels(), {
      key: (m: ModelInfo) => m.model_id,
      mount: (m: ModelInfo) => this.buildModelOption(m),
      update: (node, m) => {
        this.syncModelOption(node, m, current);
      },
    });
    wireArrowNav(list, ".pill-model-item");
  }

  private effortRow: HTMLDivElement | null = null;
  private currentEffort = "";
  private effortLoaded = false;

  private ensureEffortRow(list: HTMLElement): void {
    // Load persisted effort on first call.
    if (!this.effortLoaded) {
      this.effortLoaded = true;
      void import("./api-client.js")
        .then(({ apiGet }) => apiGet<Record<string, any>>("/api/settings")) // eslint-disable-line @typescript-eslint/no-explicit-any
        .then((settings) => {
          const me = (settings as Record<string, unknown> | null)?.["model_effort"] as
            | { effort?: string; last_model?: string }
            | undefined;
          if (me?.effort && me.last_model === getActive()?.model) {
            this.currentEffort = me.effort;
            if (this.effortRow !== null) {
              for (const btn of this.effortRow.querySelectorAll<HTMLButtonElement>(".effort-btn")) {
                btn.classList.toggle("active", btn.dataset["level"] === this.currentEffort);
              }
            }
          }
        });
    }
    if (this.effortRow === null) {
      this.effortRow = el(
        "div",
        { className: "effort-row", "aria-label": "Reasoning effort" },
        el("span", { className: "effort-label" }, "Effort"),
      ) as HTMLDivElement;
      for (const { id: level, label } of EFFORT_LEVELS) {
        const btn = el(
          "button",
          { type: "button", className: "effort-btn", "data-level": level },
          label,
        );
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          this.setEffort(level);
        });
        this.effortRow.appendChild(btn);
      }
    }
    // Sync active state.
    for (const btn of this.effortRow.querySelectorAll<HTMLButtonElement>(".effort-btn")) {
      btn.classList.toggle("active", btn.dataset["level"] === this.currentEffort);
    }
    // Ensure it's the first child (reconcile manages keyed children
    // after it; the effort row is un-keyed so reconcile ignores it).
    if (list.firstElementChild !== this.effortRow) {
      list.prepend(this.effortRow);
    }
  }

  private setEffort(level: string): void {
    const session = getActive();
    if (session === undefined) {
      return;
    }
    this.currentEffort = level;
    if (this.effortRow !== null) {
      for (const btn of this.effortRow.querySelectorAll<HTMLButtonElement>(".effort-btn")) {
        btn.classList.toggle("active", btn.dataset["level"] === level);
      }
    }
    // Dispatch to server (applies to active session).
    void send({
      type: "set_effort",
      chat_id: session.id,
      request_id: `effort-${Date.now()}`,
      payload: { level },
    });
    // Persist so effort restores on next bridge spawn for this model.
    void patchSettings({
      model_effort: {
        last_model: session.model,
        effort: level as "low" | "medium" | "high" | "xhigh" | "max",
      },
    });
  }

  private buildModelOption(m: ModelInfo): HTMLElement {
    const label = humanName(m.model_name || m.model_id);
    const opt = el(
      "div",
      {
        "data-model": m.model_id,
        role: "option",
        "aria-label": `${label}, ${String(m.rate_multiplier)}x credits`,
      },
      el("span", null, label),
      el("span", { className: "pill-model-meta" }, `${String(m.rate_multiplier)}x`),
    );
    // Click handler reads the live "current" each time so a switch from
    // another path (hotkey, REST sync) doesn't leave a stale handler.
    opt.addEventListener("click", (e: MouseEvent) => {
      e.stopPropagation();
      collapseAll();
      if (m.model_id === getActive()?.model) {
        return;
      }
      this.requestModelSwitch(m.model_id);
    });
    this.syncModelOption(opt, m, getActive()?.model ?? "");
    return opt;
  }

  private syncModelOption(opt: HTMLElement, m: ModelInfo, current: string): void {
    const isCurrent = m.model_id === current;
    opt.className = isCurrent ? "pill-model-item active" : "pill-model-item";
    opt.setAttribute("aria-selected", isCurrent ? "true" : "false");
  }

  requestModelSwitch(modelID: string): void {
    const session = getActive();
    if (session === undefined) {
      this.applyLocalChoice(modelID);
      return;
    }
    const isEmpty = session.message_count === 0 && session.messages.length === 0;
    if (isEmpty) {
      this.applyLocalChoice(modelID);
      return;
    }
    const switchInFlight = this.queueState.status === "switching";
    if (isThinking(session.id) || switchInFlight) {
      this.enqueue(modelID);
      return;
    }
    this.fire(session.id, modelID);
  }

  private applyLocalChoice(modelID: string): void {
    applyLocalModel(modelID);
  }

  private fire(chatID: string, modelID: string): void {
    this.queueState = { status: "switching", modelID };
    void switchModel.dispatch(
      { chatID, model: modelID },
      {
        onSettled: () => {
          if (this.queueState.status === "switching" && this.queueState.modelID === modelID) {
            this.queueState = { status: "idle" };
          }
        },
      },
    );
  }

  private enqueue(modelID: string): void {
    const chatID = getActiveId();
    this.queueState = { status: "queued", modelID, chatID };
    $.switchModelBtn.classList.add("pending");
    $.switchModelBtn.setAttribute(
      "data-tooltip",
      `Switch to ${humanName(modelID)} after current turn`,
    );
  }

  private drainQueue(idleChatID: string): void {
    if (this.queueState.status !== "queued") {
      return;
    }
    if (this.queueState.chatID !== idleChatID) {
      return;
    }
    const { modelID, chatID } = this.queueState;
    this.queueState = { status: "idle" };
    $.switchModelBtn.classList.remove("pending");
    $.switchModelBtn.setAttribute("data-tooltip", "Switch model");
    if (chatID === "") {
      return;
    }
    this.fire(chatID, modelID);
  }
}

const controller = new ModelSwitchController();

export function initModelSwitcher(): void {
  controller.init();
}

export function applyLocalModel(modelID: string): void {
  setCurrentModel(modelID);
  setLastModel(modelID);
  const session = getActive();
  if (session !== undefined) {
    setModel(session.id, modelID);
    session.usage.context_size = contextSizeFor(modelID);
    refreshContextUI(session);
  }
  refreshPickerIfVisible(modelID);
}
