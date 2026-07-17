// ---------------------------------------------------------------------------
// Model switcher: the pill button + expandable model list + queued-switch
// logic. The pill expands inline to show available models; selecting one
// routes through requestModelSwitch which handles all four paths (local,
// empty chat, idle, mid-turn queue).
// ---------------------------------------------------------------------------

import { getActive, getActiveId, isThinking, setModel } from "./store.js";
import { $ } from "./dom.js";
import { humanName } from "./strings.js";
import { switchModel } from "./actions/chat.js";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";
import { setCurrentModel, setLastModel } from "./session-context.js";
import { refreshPickerIfVisible, getCachedModels } from "./picker.js";
import { refreshContextUI } from "./context-ui.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import {
  bindLoadingState,
  transportAction,
  retryNetwork,
  RETRY_STANDARD,
} from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { el, signal, effect } from "@cplieger/reactive";
import { patchSettings } from "./persist.js";
import type { ModelInfo } from "./types.js";

/** Canonical effort levels with display labels. Single source of truth
 *  for the UI renderer and persistence layer. */
const EFFORT_LEVELS = [
  { id: "low", label: "low" },
  { id: "medium", label: "medium" },
  { id: "high", label: "high" },
  { id: "xhigh", label: "x-high" },
  { id: "max", label: "max" },
] as const;

export type EffortLevel = (typeof EFFORT_LEVELS)[number]["id"];

/** Active reasoning effort. Module-scoped so the effort action and the row's
 *  render effect share one source of truth (rather than the action reaching
 *  into a controller instance field). */
const currentEffort = signal("");
let effortLoaded = false;

/** Dispatch a reasoning-effort change through the actions framework — the
 *  established command path, never a hand-rolled transport.send. Optimistic:
 *  the active tier flips instantly and rolls back if the server rejects the
 *  set_config_option, so the pill can't advertise an effort that never applied. */
const setEffortAction = transportAction<{ chatID: string; level: string }, { prev: string }>({
  name: "chat.set_effort",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, level }) => ({
    type: "set_effort",
    chat_id: chatID,
    payload: { level },
  }),
  optimistic: ({ level }) => {
    const prev = currentEffort.peek();
    currentEffort.value = level;
    return { prev };
  },
  rollback: (_args, op) => {
    if (op !== undefined) {
      currentEffort.value = op.prev;
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't set reasoning effort",
});

/** Model-aware effort gating. KAS advertises a per-model effort capability in
 *  the config catalog (config_option_update `_meta.kiro.hasEffort`). When the
 *  server plumbs it onto the catalog entries, gate the effort row on the CURRENT
 *  model's capability; when it isn't plumbed at all (no entry carries it), fall
 *  back to showing the row so a working control is never silently hidden. Read
 *  defensively so this compiles whether or not the catalog type carries the
 *  field yet. */
function currentModelHasEffort(models: readonly ModelInfo[], modelID: string): boolean {
  let plumbed = false;
  let current = false;
  for (const m of models) {
    const cap = (m as { has_effort?: unknown }).has_effort;
    if (cap !== undefined) {
      plumbed = true;
      if (m.model_id === modelID) {
        current = cap === true;
      }
    }
  }
  return plumbed ? current : true;
}

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
        // Always the inline list (models + effort row) — including on an
        // empty chat. The old empty-chat branch collapsed the pill and
        // re-showed the hero picker, which is ALREADY the empty-state
        // content behind the prompt bar, so the click looked like a no-op
        // and the effort control was unreachable before the first prompt.
        this.renderCondensedList();
      },
    });
    // Roving focus is wired ONCE on the persistent list element (re-wiring per
    // render stacks keydown handlers: N arrow steps / N activations per key).
    // renderCondensedList() calls refresh() after each reconcile instead.
    this.modelNav = rovingFocus(expandContent, ".pill-model-item");
    bindLoadingState("chat.switch_model", $.switchModelBtn, { pendingClass: "switching" });
    // The queued mid-turn model switch drains from the per-chat `turn_ended`
    // SSE (handlers/turn.ts → drainModelSwitchQueue), NOT the active-only
    // `turn:idle` bus event — so a switch queued on a background chat is no
    // longer stranded until that chat happens to become the active tab.
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

    // Effort row: shown only when the current model advertises reasoning
    // effort (KAS `hasEffort`). Falls back to showing it when the catalog
    // doesn't carry the capability at all (see currentModelHasEffort).
    if (currentModelHasEffort(getCachedModels(), current)) {
      this.ensureEffortRow(list);
    } else {
      this.removeEffortRow();
    }

    reconcile(list, getCachedModels(), {
      key: (m: ModelInfo) => m.model_id,
      mount: (m: ModelInfo) => this.buildModelOption(m),
      update: (node, m) => {
        this.syncModelOption(node, m, current);
      },
    });
    this.modelNav?.refresh();
  }

  private modelNav: RovingFocusController | null = null;

  private effortRow: HTMLDivElement | null = null;

  /** Remove the effort row from the list (current model doesn't advertise
   *  effort). Idempotent — re-added by ensureEffortRow when a model that does
   *  advertise it becomes current. */
  private removeEffortRow(): void {
    this.effortRow?.remove();
  }

  private ensureEffortRow(list: HTMLElement): void {
    // Load persisted effort on first call.
    if (!effortLoaded) {
      effortLoaded = true;
      void import("./api-client.js")
        .then(({ apiGet }) => apiGet<Record<string, any>>("/api/settings")) // eslint-disable-line @typescript-eslint/no-explicit-any
        .then((settings) => {
          const me = (settings as Record<string, unknown> | null)?.["model_effort"] as
            { effort?: string; last_model?: string } | undefined;
          if (me?.effort && me.last_model === getActive()?.model) {
            currentEffort.value = me.effort;
          }
        });
    }
    if (this.effortRow === null) {
      this.effortRow = el(
        "div",
        { className: "effort-row", role: "group", "aria-label": "Reasoning effort" },
        el("span", { className: "effort-label" }, "Effort"),
      ) as HTMLDivElement;
      for (const { id: level, label } of EFFORT_LEVELS) {
        const btn = el(
          "button",
          {
            type: "button",
            className: "effort-btn",
            "data-level": level,
            "aria-pressed": "false",
          },
          label,
        );
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          this.setEffort(level);
        });
        this.effortRow.appendChild(btn);
      }
      // One effect keeps every .effort-btn's active class + aria-pressed in
      // sync with the currentEffort signal (replaces the three hand-rolled
      // sync loops). The row + controller are app-lifetime singletons, so this
      // never needs disposal.
      const row = this.effortRow;
      effect(() => {
        const lvl = currentEffort.value;
        for (const btn of row.querySelectorAll<HTMLButtonElement>(".effort-btn")) {
          const active = btn.dataset["level"] === lvl;
          btn.classList.toggle("active", active);
          btn.setAttribute("aria-pressed", active ? "true" : "false");
        }
      });
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
    // Empty chat = no bridge yet, so set_effort would 409 (no session to
    // apply it to). Persist the choice locally instead: spawnBridge seeds
    // it at session/new via StartOpts.Effort (effortForModel reads the
    // same model_effort setting keyed by last_model).
    const isEmpty = session.message_count === 0 && session.messages.length === 0;
    if (isEmpty) {
      currentEffort.value = level;
      void patchSettings({
        model_effort: { last_model: session.model, effort: level as EffortLevel },
      });
      return;
    }
    // Dispatch through the actions framework (optimistic flip + rollback live
    // in setEffortAction). Persist only after the server accepts, so a rejected
    // set_config_option doesn't leave a stale saved effort for this model.
    void setEffortAction.dispatch(
      { chatID: session.id, level },
      {
        onSuccess: () => {
          void patchSettings({
            model_effort: {
              last_model: session.model,
              effort: level as EffortLevel,
            },
          });
        },
      },
    );
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

  /** Drain a queued mid-turn model switch for the chat whose turn just ended.
   *  Called from the per-chat `turn_ended` SSE (handlers/turn.ts) and from the
   *  reconnect-gap recovery (handlers/system.ts), so a background chat's queued
   *  switch fires when ITS turn ends rather than waiting to become active. */
  drainForChat(chatID: string): void {
    this.drainQueue(chatID);
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

/** Drain any queued mid-turn model switch for `chatID`. Called by the
 *  `turn_ended` SSE handler (and the reconnect-gap recovery) so a switch
 *  queued while a background chat was thinking still fires when that chat's
 *  turn ends — model-switcher.ts stays the queue owner. */
export function drainModelSwitchQueue(chatID: string): void {
  controller.drainForChat(chatID);
}

export function applyLocalModel(modelID: string): void {
  setCurrentModel(modelID);
  setLastModel(modelID);
  const session = getActive();
  if (session !== undefined) {
    // setModel replaces the session object in the store (and refreshes the
    // derived usage.context_size); re-read it so the context bar renders the
    // NEW model. Refreshing from the stale reference was the "picker says
    // sonnet, pill says auto" desync: the old object still carried the old
    // model and its rAF-batched updateContextBar write landed last.
    setModel(session.id, modelID);
    const updated = getActive();
    if (updated !== undefined) {
      refreshContextUI(updated);
    }
  }
  refreshPickerIfVisible(modelID);
}
