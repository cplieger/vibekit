// ---------------------------------------------------------------------------
// Large-card model picker: the empty-chat view, shown while a conversation has
// nothing in it yet.
//
// VISIBILITY IS DERIVED, NOT COMMANDED. The picker is a full-bleed overlay
// (`position: absolute; inset: 0; z-index: 5` inside #messages-wrap), so a
// missed hide does not degrade, it covers the transcript and lets it scroll
// underneath. It used to be driven by two imperative call sites in chat.ts, and
// every sender that did not route through one of them left it up: the goal row
// and the tangent row call submitPrompt directly, so `/goal` in a fresh chat
// opened a conversation behind a picker that never went away.
//
// The condition is "this chat has nothing in it AND nothing is on its way":
// `isEmptyChat` alone would hold the picker up for the round trip between Send
// and the server's `message_appended` echo, and `thinking` is set synchronously
// by the send action, so it is the signal that closes that window without any
// optimistic rendering.
//
// The models cache is populated by app.ts from two sources (REST at
// startup, per-session via bridge afterwards). The bridge already
// filters [Deprecated] and [Legacy] entries; the picker does no
// filtering of its own. [Internal] previews are kept on purpose.
// ---------------------------------------------------------------------------

import type { ModelInfo, Session } from "./types.js";
import { humanName } from "./strings.js";
import { $ } from "./dom.js";
import { getActive, activeSession, isEmptyChat } from "./store.js";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";
import { reconcile } from "./reconcile.js";
import { el, computed, effect } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_MODEL_UI } from "./icons.js";

/** Static header copy for the model picker. Describes the model choice
 *  itself — tool access is a per-mode concern on v3, not a model property,
 *  so the old "full tool access…" copy was describing the wrong thing. */
const PICKER_LABEL = "Choose a model";
const PICKER_DESCRIPTION = "Pick the model for this conversation. You can switch it anytime.";

/** The model to offer for `s`, or "" when the picker has no business showing.
 *
 *  An ABSENT session yields "" even though `isEmptyChat(undefined)` is true: a
 *  chat that does not exist has no model to choose, and the pre-session case is
 *  the model pill's inline list, not this overlay. */
function pickerModelFor(s: Session | undefined): string {
  if (s === undefined || !isEmptyChat(s) || s.thinking) {
    return "";
  }
  return s.model;
}

class ModelPickerController {
  private models: ModelInfo[] = [];
  private callback: ((modelId: string) => void) | null = null;
  private currentId = "";
  private nav: RovingFocusController | null = null;

  setModels(models: ModelInfo[]): void {
    this.models = models;
  }

  getCachedModels(): ModelInfo[] {
    return this.models;
  }

  /** Bind visibility to store state. The callback is registered once here
   *  rather than per show(), because nothing calls show() from outside now.
   *
   *  Keyed on a computed STRING so the effect dedups by value: `activeSession`
   *  re-derives on every streaming chunk, and show() reconciles the grid and
   *  moves focus, which must not happen on each frame of a turn. The three
   *  transitions the key expresses are hidden→shown, shown→hidden, and a model
   *  change while shown (which re-renders to move the active card). */
  bindVisibility(onSelect: (modelId: string) => void): void {
    this.callback = onSelect;
    const wanted = computed(() => pickerModelFor(activeSession.value));
    effect(() => {
      const modelID = wanted.value;
      if (modelID === "") {
        this.hide();
        return;
      }
      this.show(modelID);
    });
  }

  show(currentModelId: string): void {
    const picker = $.modelPicker;
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const grid = picker.querySelector<HTMLElement>(".picker-grid")!;
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const label = picker.querySelector(".picker-label")!;
    this.currentId = currentModelId;
    picker.setAttribute("aria-label", PICKER_LABEL);

    // The glyph is BUILT here rather than salvaged out of the markup. It used
    // to be an inline <svg> in index.html that this line found and re-appended,
    // which is why the null branch existed; icons.ts owns the geometry now and
    // the model pill renders the same `d` at 12px.
    label.replaceChildren(
      iconEl(ICON_MODEL_UI),
      document.createTextNode(" "),
      document.createTextNode(PICKER_LABEL),
    );

    let desc = picker.querySelector(".picker-desc");
    if (desc === null) {
      desc = el("div", { className: "picker-desc" });
      label.after(desc);
    }
    desc.textContent = PICKER_DESCRIPTION;

    grid.setAttribute("role", "listbox");
    grid.setAttribute("aria-label", PICKER_LABEL);

    // Drop any non-keyed loading placeholder before reconciling.
    for (const child of [...grid.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }

    if (this.models.length === 0) {
      const loading = el(
        "div",
        { className: "picker-btn picker-loading", "aria-busy": "true", role: "option" },
        "Loading models…",
      );
      grid.appendChild(loading);
    }
    reconcile(grid, this.models, {
      key: (m: ModelInfo) => m.model_id,
      mount: (m: ModelInfo) => this.buildPickerBtn(m, currentModelId),
      update: (node, m) => {
        this.syncPickerBtn(node, m, currentModelId);
      },
    });
    // Wire once on the persistent grid (re-wiring per show() stacks keydown
    // handlers: N arrow steps / N activations per key); refresh() restores the
    // single Tab stop over the freshly reconciled buttons.
    this.nav ??= rovingFocus(grid, ".picker-btn:not(.picker-loading)", {
      orientation: "horizontal",
    });
    this.nav.refresh();
    picker.classList.remove("hidden");
    // Focus the active model button (or first) for keyboard users.
    const focusTarget =
      grid.querySelector<HTMLButtonElement>(".picker-btn.active") ??
      grid.querySelector<HTMLButtonElement>(".picker-btn:not(.picker-loading)");
    focusTarget?.focus();
  }

  private buildPickerBtn(m: ModelInfo, currentModelId: string): HTMLElement {
    const btn = el(
      "button",
      { "data-model": m.model_id, role: "option" },
      el("span", { className: "picker-name" }, humanName(m.model_name || m.model_id)),
      el("span", { className: "picker-meta" }, `${String(m.rate_multiplier)}x credits`),
    );
    btn.addEventListener("click", () => {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const grid = $.modelPicker.querySelector<HTMLElement>(".picker-grid")!;
      for (const b of grid.querySelectorAll(".picker-btn")) {
        b.classList.remove("active");
        b.setAttribute("aria-selected", "false");
      }
      btn.classList.add("active");
      btn.setAttribute("aria-selected", "true");
      this.callback?.(m.model_id);
    });
    this.syncPickerBtn(btn, m, currentModelId);
    return btn;
  }

  private syncPickerBtn(btn: HTMLElement, m: ModelInfo, currentModelId: string): void {
    const isCurrent = m.model_id === currentModelId;
    btn.className = `picker-btn${isCurrent ? " active" : ""}`;
    btn.setAttribute("aria-selected", isCurrent ? "true" : "false");
  }

  hide(): void {
    $.modelPicker.classList.add("hidden");
    // The callback is NOT cleared: it is registered once at bind and outlives
    // every show/hide cycle. Clearing it here used to be safe because chat.ts
    // re-supplied one on each show, and it is exactly what would make a derived
    // re-show render dead cards.
    this.currentId = "";
  }

  refreshIfVisible(overrideModelId?: string): void {
    const picker = $.modelPicker;
    if (picker.classList.contains("hidden") || this.callback === null) {
      return;
    }
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const grid = picker.querySelector<HTMLElement>(".picker-grid")!;

    // Read current model from store rather than relying solely on this.currentId.
    const storeModel = getActive()?.model;
    const effectiveId = overrideModelId ?? storeModel ?? this.currentId;

    const hasRealButtons = grid.querySelector(".picker-btn:not(.picker-loading)") !== null;
    if (!hasRealButtons) {
      this.show(effectiveId);
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
    this.show(currentId);
  }
}

const instance = new ModelPickerController();

let bound = false;

export function setPickerModels(models: ModelInfo[]): void {
  instance.setModels(models);
}

export function getCachedModels(): ModelInfo[] {
  return instance.getCachedModels();
}

/** Bind the picker to store state. Idempotent; called once from app.ts.
 *
 *  `onSelect` is injected rather than imported because it lives in
 *  model-switcher.ts, which imports THIS module — app.ts is the composition
 *  root that can see both. */
export function initModelPicker(onSelect: (modelId: string) => void): void {
  if (bound) {
    return;
  }
  bound = true;
  instance.bindVisibility(onSelect);
}

/** Re-render an already-visible picker.
 *
 *  Still needed after the visibility effect: the model CATALOG is not session
 *  state, so a late `setPickerModels` (the REST fetch, or a bridge's own list)
 *  changes what the grid should contain without changing the effect's key. */
export function refreshPickerIfVisible(overrideModelId?: string): void {
  instance.refreshIfVisible(overrideModelId);
}
