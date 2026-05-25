// ---------------------------------------------------------------------------
// Model switcher: the pill button + expandable model list + queued-switch
// logic. The pill expands inline to show available models; selecting one
// routes through requestModelSwitch which handles all four paths (local,
// empty chat, idle, mid-turn queue).
// ---------------------------------------------------------------------------

import { getActive, getActiveId, isThinking, contextSizeFor } from "./store.js";
import { onBus, BUS_TURN_IDLE } from "./bus.js";
import { $ } from "./dom.js";
import { humanName } from "./strings.js";
import { switchModel } from "./chat-commands.js";
import { wireArrowNav } from "./arrow-nav.js";
import { setCurrentModel, setLastModel } from "./session-context.js";
import {
  showModelPicker, hideModelPicker, refreshPickerIfVisible, getCachedModels,
} from "./picker.js";
import { refreshContextUI } from "./context-ui.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { bindLoadingState } from "./actions/index.js";

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
        const isEmpty = session === undefined
          || (session.message_count === 0 && session.messages.length === 0);
        if (isEmpty) {
          setTimeout(() => { collapseAll(); this.openRichPicker(); }, 0);
          return;
        }
        this.renderCondensedList();
      },
    });
    bindLoadingState("chat.switch_model", $.switchModelBtn, { pendingClass: "switching" });
    onBus(BUS_TURN_IDLE, (chatID: string) => this.drainQueue(chatID));
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
    list.replaceChildren();
    const session = getActive();
    if (session === undefined) return;
    const current = session.model;
    for (const m of getCachedModels()) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = m.model_id === current
        ? "pill-model-item active" : "pill-model-item";
      btn.dataset["model"] = m.model_id;
      const name = document.createElement("span");
      name.textContent = humanName(m.model_name || m.model_id);
      const meta = document.createElement("span");
      meta.className = "pill-model-meta";
      meta.textContent = `${String(m.rate_multiplier)}x`;
      btn.append(name, meta);
      btn.addEventListener("click", (e: MouseEvent) => {
        e.stopPropagation();
        collapseAll();
        if (m.model_id === current) return;
        this.requestModelSwitch(m.model_id);
      });
      list.appendChild(btn);
    }
    wireArrowNav(list, ".pill-model-item");
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
    void this.fire(session.id, modelID);
  }

  private applyLocalChoice(modelID: string): void {
    applyLocalModel(modelID);
  }

  private async fire(chatID: string, modelID: string): Promise<void> {
    this.queueState = { status: "switching", modelID };
    try {
      await switchModel(chatID, modelID);
    } finally {
      if (this.queueState.status === "switching" && this.queueState.modelID === modelID) {
        this.queueState = { status: "idle" };
      }
    }
  }

  private enqueue(modelID: string): void {
    const chatID = getActiveId();
    this.queueState = { status: "queued", modelID, chatID };
    $.switchModelBtn.classList.add("pending");
    $.switchModelBtn.setAttribute("data-tooltip", `Switch to ${humanName(modelID)} after current turn`);
  }

  private drainQueue(idleChatID: string): void {
    if (this.queueState.status !== "queued") return;
    if (this.queueState.chatID !== idleChatID) return;
    const { modelID, chatID } = this.queueState;
    this.queueState = { status: "idle" };
    $.switchModelBtn.classList.remove("pending");
    $.switchModelBtn.setAttribute("data-tooltip", "Switch model");
    if (chatID === "") return;
    void this.fire(chatID, modelID);
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
    session.model = modelID;
    session.usage.context_size = contextSizeFor(modelID);
    refreshContextUI(session);
  }
  refreshPickerIfVisible(modelID);
}
