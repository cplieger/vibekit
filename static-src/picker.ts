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
import { $, setBusy } from "./dom.js";
import { getActive, activeSession, isEmptyChat } from "./store.js";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";
import { reconcile } from "./reconcile.js";
import { el, computed, effect } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { iconEl } from "./icon-el.js";
import { ICON_MODEL_UI } from "./icons.js";

/** Static header copy for the model picker. Describes the model choice
 *  itself — tool access is a per-mode concern on v3, not a model property,
 *  so the old "full tool access…" copy was describing the wrong thing. */
const PICKER_LABEL = "Choose a model";
const PICKER_DESCRIPTION = "Pick the model for this conversation. You can switch it anytime.";

/** Everything in the grid a keyboard reaches: the model cards, and the Retry the
 *  `unavailable` state offers instead of them. `.picker-note` is excluded because
 *  it is a line of text, not a control. */
const PICKER_FOCUSABLE = ".picker-btn:not(.picker-note), .picker-retry";

/** The Retry's accessible name and what the gesture says. `Retry` alone names no
 *  subject, and a keyboard user hears the name without the notice beside it; the
 *  failure and empty lines are `catalogNotice()`'s own copy. */
export const RETRY_LABEL = "Retry loading the model list";
const RETRY_STARTED = "Reloading the model list…";
const RETRY_LOADED = "Model list loaded.";

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

    // Drop any non-keyed notice before reconciling.
    for (const child of [...grid.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }

    // The grid is a listbox only while it HOLDS options. A `role="listbox"` whose
    // one child is a text placeholder advertises a choice that does not exist —
    // and that placeholder used to carry `role="option"` while being a div that
    // rovingFocus and focusTarget both exclude, so the listbox's only option was
    // neither focusable nor selectable and show()'s focus move no-opped.
    const notice = catalogNotice();
    if (notice === null) {
      grid.setAttribute("role", "listbox");
      grid.setAttribute("aria-label", PICKER_LABEL);
    } else {
      grid.removeAttribute("role");
      grid.removeAttribute("aria-label");
      // aria-busy is a global attribute, so it applies with or without the role —
      // and it is REMOVED once a verdict lands. Left on unconditionally, a screen
      // reader was told the list was loading forever, including after a
      // permanently-failed fetch.
      grid.appendChild(el("div", { className: "picker-btn picker-note" }, notice.text));
      if (notice.retry) {
        grid.appendChild(this.buildRetryBtn());
      }
    }
    setBusy(grid, notice?.busy === true);

    reconcile(grid, this.models, {
      key: (m: ModelInfo) => m.model_id,
      mount: (m: ModelInfo) => this.buildPickerBtn(m, currentModelId),
      update: (node, m) => {
        this.syncPickerBtn(node, m, currentModelId);
      },
    });
    // Wire once on the persistent grid (re-wiring per show() stacks keydown
    // handlers: N arrow steps / N activations per key); refresh() restores the
    // single Tab stop over the freshly reconciled buttons. The Retry joins that
    // walk, or the only door out of a failed fetch is unreachable by keyboard.
    this.nav ??= rovingFocus(grid, PICKER_FOCUSABLE, {
      orientation: "horizontal",
    });
    this.nav.refresh();
    picker.classList.remove("hidden");
    // Focus the active model button (or first, or the Retry) for keyboard users.
    const focusTarget =
      grid.querySelector<HTMLButtonElement>(".picker-btn.active") ??
      grid.querySelector<HTMLButtonElement>(PICKER_FOCUSABLE);
    focusTarget?.focus();
  }

  /** The one door out of a grid with no models in it.
   *
   *  A real `<button>`, and deliberately NOT carrying `.picker-btn`: that class
   *  also means "a model option" to the grid's own queries, so a Retry wearing it
   *  would be counted as a real card and take `aria-selected` from an override
   *  refresh. It reuses `.btn-small`, the app's small-button skin, so it needs no
   *  stylesheet of its own. */
  private buildRetryBtn(): HTMLElement {
    const btn = el(
      "button",
      { type: "button", className: "btn-small picker-retry", "aria-label": RETRY_LABEL },
      "Retry",
    );
    btn.addEventListener("click", () => {
      retryCatalog();
    });
    return btn;
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

    const hasRealButtons = grid.querySelector(".picker-btn:not(.picker-note)") !== null;
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

/** Where the pre-session catalog's fetch has GOT to, which is a different question
 *  from what the catalog holds: an empty list otherwise means four things at once,
 *  and one placeholder claiming to be loading covered all four forever.
 *
 *  Named for the phase rather than reusing the wire's `CatalogState`, which is a
 *  per-read verdict: `empty` collapses into `ready` here (an empty catalog is a
 *  real answer) and `unknown` covers nothing-answered-yet and refresh-in-flight. */
export type CatalogPhase = "unknown" | "ready" | "unavailable";

/** The line to show where a model list would be, and whether an answer is still
 *  coming. ONE statement of that copy, because the hero picker and the model
 *  pill's card both need it and two copies drift into two vocabularies. */
export interface CatalogNotice {
  readonly text: string;
  readonly busy: boolean;
  /** Whether asking again can change the answer, which is what offers the Retry.
   *  True for a settled EMPTY catalog as well as for a failed read: KAS resolves
   *  its model list asynchronously and the server reports a merely cold cache as
   *  `empty`, so a re-read genuinely can differ. False only while an answer is
   *  still coming, which the bounded refresh is already working on. */
  readonly retry: boolean;
}

let catalogPhase: CatalogPhase = "unknown";

/** Record what the client now knows, repainting a visible picker.
 *
 *  Repaint rather than a signal: the visibility effect keys on a computed string
 *  over session state, so a catalog verdict does not move it — the same reason
 *  `refreshPickerIfVisible` exists for a late `setPickerModels`. */
export function setCatalogPhase(phase: CatalogPhase): void {
  if (phase === catalogPhase) {
    return;
  }
  catalogPhase = phase;
  instance.refreshIfVisible();
}

/** The notice standing in for a model list, or null when there is a list to
 *  show. `empty` and `unavailable` deliberately name no cause: the server cannot
 *  report one (KAS omits its `model` option identically for a stale cache and for
 *  an account entitled to nothing), so copy blaming an account or an entitlement
 *  would be inventing it. model-catalog.ts owns the refresh slot behind it. */
export function catalogNotice(): CatalogNotice | null {
  if (instance.getCachedModels().length > 0) {
    return null;
  }
  switch (catalogPhase) {
    case "unknown":
      return { text: "Loading models…", busy: true, retry: false };
    case "ready":
      return { text: "No models available yet.", busy: false, retry: true };
    case "unavailable":
      return { text: "Couldn't load the model list.", busy: false, retry: true };
  }
}

/** The catalog re-read, injected by the composition root. MODULE state rather
 *  than the controller's, because the model pill's card offers the same door and
 *  a second handler is a second thing that can be wired differently. */
let catalogRetry: (() => Promise<void>) | null = null;

/** Ask for the catalog again, and SAY SO — from either surface.
 *
 *  A repeated verdict repaints nothing (`setCatalogPhase` returns early on an
 *  unchanged phase), so the shared live region is the only place the press is
 *  reported — and for the same reason the notice line cannot BE one. The second
 *  announcement reads the SETTLED notice, so a repeated answer still reports and
 *  a press refused while a loop runs re-states the current line. */
export function retryCatalog(): void {
  const run = catalogRetry;
  if (run === null) {
    return;
  }
  announce(RETRY_STARTED);
  void run().then(() => {
    const settled = catalogNotice();
    announce(settled === null ? RETRY_LOADED : settled.text);
  });
}

export function setPickerModels(models: ModelInfo[]): void {
  instance.setModels(models);
}

export function getCachedModels(): ModelInfo[] {
  return instance.getCachedModels();
}

/** Bind the picker to store state. Idempotent; called once from app.ts.
 *
 *  Both callbacks are injected rather than imported: `onSelect` lives in
 *  model-switcher.ts, which imports THIS module, and `onRetry` re-runs the
 *  catalog fetch the composition root owns — returning its PROMISE, so
 *  `retryCatalog` can report the answer rather than only the press. app.ts is
 *  what can see both. */
export function initModelPicker(
  onSelect: (modelId: string) => void,
  onRetry: () => Promise<void>,
): void {
  if (bound) {
    return;
  }
  bound = true;
  catalogRetry = onRetry;
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
