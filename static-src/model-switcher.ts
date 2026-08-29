// ---------------------------------------------------------------------------
// Model switcher: the pill button + expandable model list + queued-switch
// logic. The pill expands inline to show available models; selecting one
// routes through requestModelSwitch which handles all four paths (local,
// empty chat, idle, mid-turn queue).
// ---------------------------------------------------------------------------

import {
  activeSession,
  get,
  getActive,
  getActiveId,
  isEmptyChat,
  isThinking,
  setEffort,
  setModel,
} from "./store.js";
import { $ } from "./dom.js";
import { humanName } from "./strings.js";
import { switchModel } from "./actions/chat.js";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";
import { setCurrentModel, setLastModel, getLastEffort, setLastEffort } from "./session-context.js";
import { refreshPickerIfVisible, getCachedModels } from "./picker.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import {
  bindLoadingState,
  transportAction,
  retryNetwork,
  RETRY_STANDARD,
} from "./actions/index.js";
import { reconcile } from "./reconcile.js";
import { el, effect } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_MODEL } from "./icons.js";
import { effortLabel, effortVocabulary, modelHasEffort, sameLevels } from "./effort.js";
import type { ModelInfo, SessionEffortLevel } from "./types.js";

// The effort vocabulary — which tiers exist, which one is live, and which one is
// the model's own default — lives in effort.ts, because the model PILL names the
// live tier too (context-ui.ts) and a second copy of the resolution order is a
// second thing that can disagree with the session.

// There is no exported EffortLevel type. Its one consumer was AppSettings'
// `model_effort` field, which is gone: effort is a per-chat string on the chat
// record, validated server-side by vibekit.EffortLevel.Valid().

/** Dispatch a reasoning-effort change through the actions framework — the
 *  established command path, never a hand-rolled transport.send.
 *
 *  Effort is PER-CHAT, on the chat record beside model, mode and supervised, so
 *  the optimistic write goes to the STORE rather than to a module signal. It used
 *  to be a module signal backed by one global `model_effort` setting keyed by the
 *  last model, which meant two chats could not disagree, switching models
 *  discarded the previous model's choice, and the value was read once per page
 *  load — so a tab switch carried the previous chat's level over. Same shape as
 *  chat.set_supervised now, which is the point. */
const setEffortAction = transportAction<{ chatID: string; level: string }, { prev: string }>({
  name: "chat.set_effort",
  scope: ({ chatID }) => `chat:${chatID}`,
  command: ({ chatID, level }) => ({
    type: "set_effort",
    chat_id: chatID,
    payload: { level },
  }),
  optimistic: ({ chatID, level }) => {
    const session = get(chatID);
    if (session === undefined) {
      return undefined;
    }
    const prev = session.effort ?? "";
    setEffort(chatID, level);
    return { prev };
  },
  rollback: ({ chatID }, op) => {
    if (op !== undefined) {
      setEffort(chatID, op.prev);
    }
  },
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  error: "Couldn't set reasoning effort",
});

/** Model-aware effort gating lives in effort.ts (`modelHasEffort`), because the
 *  pill needs the same verdict: a model with no tiers has no tier to name. */

type QueueState =
  | { status: "idle" }
  | { status: "queued"; modelID: string; chatID: string }
  | { status: "switching"; modelID: string };

class ModelSwitchController {
  private queueState: QueueState = { status: "idle" };

  init(): void {
    // The pill's glyph, from icons.ts rather than an inline literal in
    // index.html. Prepended (not appended) because the label follows it, and
    // once — init() is called a single time from app.ts. The `.switching` face
    // hides this svg by descendant selector, so wrapping it would be wrong but
    // injecting it is not.
    $.switchModelBtn.prepend(iconEl(ICON_MODEL));
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
    const scroll = this.ensureScroll(list);
    const session = getActive();
    if (session === undefined) {
      reconcile(scroll, [] as ModelInfo[], {
        key: () => "",
        mount: () => el("div"),
      });
      return;
    }
    const current = session.model;

    // Effort section: shown only when the current model advertises reasoning
    // effort (KAS `hasEffort`). Falls back to showing it when the catalog
    // doesn't carry the capability at all (see effort.ts modelHasEffort).
    if (modelHasEffort(getCachedModels(), current)) {
      this.ensureEffortRow(list);
    } else {
      this.removeEffortRow();
    }

    reconcile(scroll, getCachedModels(), {
      key: (m: ModelInfo) => m.model_id,
      mount: (m: ModelInfo) => this.buildModelOption(m),
      update: (node, m) => {
        this.syncModelOption(node, m, current);
      },
    });
    this.modelNav?.refresh();
  }

  private modelNav: RovingFocusController | null = null;

  private modelScroll: HTMLDivElement | null = null;

  private effortRow: HTMLDivElement | null = null;

  /** The tiers currently rendered as buttons, so a rebuild happens only when the
   *  vocabulary actually changed. */
  private effortLevelsShown: readonly SessionEffortLevel[] = [];

  /** The tier marked live, resolved by effortVocabulary (the chat's own choice,
   *  else the level the session reports, else the model or template default). Not
   *  persisted anywhere: writing a service default onto the chat would turn it
   *  into a vibekit choice that `StartOpts.Effort` pins to every later session. */
  private effortActive = "";

  /** The scrolling half of the card. The model options live in here and the
   *  effort section is a SIBLING below it, which is what keeps the tiers fixed
   *  while a long model list scrolls. It also carries the listbox role, because
   *  it is the element that holds only options — the card holds the section
   *  too. Created once; reconcile owns its keyed children from then on. */
  private ensureScroll(list: HTMLElement): HTMLElement {
    if (this.modelScroll === null) {
      this.modelScroll = el("div", {
        className: "pill-model-scroll",
        role: "listbox",
        "aria-label": "Available models",
      }) as HTMLDivElement;
      list.appendChild(this.modelScroll);
    }
    return this.modelScroll;
  }

  /** Remove the effort section from the card (current model doesn't advertise
   *  effort). Idempotent — re-added by ensureEffortRow when a model that does
   *  advertise it becomes current. */
  private removeEffortRow(): void {
    this.effortRow?.remove();
  }

  /** Build or refresh the effort section for `modelID`.
   *
   *  The tier buttons are REBUILT when the model's level vocabulary changes,
   *  because the set is per model rather than a fixed five: a row built once for
   *  the first model would keep offering `xhigh` on a model that has no such
   *  level. `renderCondensedList` runs on every open of the card, so a catalog
   *  arriving late is picked up the next time the user looks.
   *
   *  No settings fetch here. Effort arrives on the chat record (the header
   *  carries it, so an empty chat that never loads its messages still has one),
   *  which is also what makes the row correct after a tab switch rather than
   *  showing whichever chat was open when the page loaded. */
  private ensureEffortRow(list: HTMLElement): void {
    const { levels, active } = effortVocabulary(getActive(), getCachedModels(), getLastEffort());
    this.effortActive = active;
    if (this.effortRow === null) {
      this.effortRow = el("div", {
        className: "effort-row",
        role: "group",
        "aria-label": "Reasoning effort",
      }) as HTMLDivElement;
      // One effect keeps every .effort-btn's active class + aria-pressed in
      // sync with the ACTIVE CHAT's level. Reading activeSession is what makes
      // the row per-chat: a tab switch re-runs this rather than carrying the
      // previous chat's tier over. The row + controller are app-lifetime
      // singletons, so this never needs disposal.
      effect(() => {
        // Reading activeSession is what makes this per-chat: a tab switch or an
        // optimistic set_effort write re-resolves instead of carrying the previous
        // value over.
        this.effortActive = effortVocabulary(
          activeSession.value,
          getCachedModels(),
          getLastEffort(),
        ).active;
        this.syncEffortActive();
      });
    }
    if (!sameLevels(this.effortLevelsShown, levels)) {
      this.buildEffortButtons(levels);
    }
    // The effect above only re-runs when the ACTIVE CHAT's own level changes, and
    // neither a rebuild nor a newly-resolved live tier is a signal read, so
    // re-apply the mark here.
    this.syncEffortActive();
    // Ensure it's the LAST child. It sits below the scroller rather than
    // inside it, so reconcile — which owns the scroller's keyed children —
    // never sees this row at all.
    if (list.lastElementChild !== this.effortRow) {
      list.appendChild(this.effortRow);
    }
  }

  /** Replace the row's contents with one button per tier in `levels`. */
  private buildEffortButtons(levels: readonly SessionEffortLevel[]): void {
    const row = this.effortRow;
    if (row === null) {
      return;
    }
    row.replaceChildren(el("span", { className: "effort-label" }, "Effort"));
    for (const level of levels) {
      const btn = el(
        "button",
        {
          type: "button",
          className: "effort-btn",
          "data-level": level.id,
          "aria-pressed": "false",
        },
        effortLabel(level),
      );
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        this.setEffort(level.id);
      });
      row.appendChild(btn);
    }
    this.effortLevelsShown = [...levels];
  }

  /** Mark the live tier, resolved by effortVocabulary. A chat that has chosen
   *  nothing still RUNS at a level, so marking nothing claimed the session had no
   *  effort level at all. `aria-pressed` follows the same value, so the visual and
   *  announced states cannot disagree. */
  private syncEffortActive(): void {
    for (const btn of this.effortRow?.querySelectorAll<HTMLButtonElement>(".effort-btn") ?? []) {
      const on = btn.dataset["level"] === this.effortActive;
      btn.classList.toggle("active", on);
      btn.setAttribute("aria-pressed", on ? "true" : "false");
    }
  }

  /** Apply a reasoning-effort level to the active chat.
   *
   *  ONE path, where there used to be two. The empty-chat branch existed because
   *  `set_effort` answered 409 with no bridge, so a pick before the first prompt
   *  wrote the global `model_effort` setting instead — a different store, a
   *  different key and a different scope reached by the same click. The command
   *  persists on the chat record and tolerates a bridgeless chat now (mirroring
   *  set_mode, which auto-creates), so there is nothing left to branch on and no
   *  settings write at all.
   *
   *  It does record the pick as the level a NEW chat opens on (`last_effort`, the
   *  twin of `last_model`). That is ambient memory rather than the chat's state:
   *  the level still lives on this chat's record, so two chats keep disagreeing,
   *  and the next new chat stops reopening at the model's default tier.
   *
   *  A repeat pick of the level THIS CHAT has already chosen is dropped. It is a
   *  no-op command and the store write it drives is a no-op too, but the POST is
   *  not free and a fast double or triple click sent one each: measured on the
   *  live instance, one click on `max` produced three identical `set_effort`
   *  commands 80ms apart. The guard reads the chat's own choice rather than the
   *  MARKED tier deliberately — a chat marked at the model default has chosen
   *  nothing, and clicking that tier to pin it explicitly has to reach the
   *  server. */
  private setEffort(level: string): void {
    const session = getActive();
    if (session === undefined) {
      return;
    }
    if ((session.effort ?? "") === level) {
      return;
    }
    setLastEffort(level);
    void setEffortAction.dispatch({ chatID: session.id, level });
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
    const isEmpty = isEmptyChat(session);
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
    // derived usage.context_size); the context bar's active-session effect
    // (chat.ts) repaints from the NEW object inside this write.
    setModel(session.id, modelID);
  }
  refreshPickerIfVisible(modelID);
}
