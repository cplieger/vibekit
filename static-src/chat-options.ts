// ---------------------------------------------------------------------------
// The chat-actions menu: the composer's tray for what acts on THIS chat.
//
// One `+` pill expanding to a small card (the standard pill-expand pattern —
// inline diagonal growth, one open at a time, no floating popup). Four
// residents: attach a file, set a goal, start a tangent, and the supervised
// switch.
//
// Deliberately a menu rather than four more pills. A pill earns its prompt-row
// slot by changing per MESSAGE — the model and the mode do, and the row is
// already tight enough on a phone that the slot layer needed its own
// minimum-size rule. None of these four change per message, and four more pills
// would grow the row without bound.
//
// Two of the four are here because they had nowhere else to be. The supervised
// toggle has been homeless since the supervised pill died with the staged-write
// store: the pill's expanded list became KAS's, but the per-chat CHOICE still
// needs a control. Attach arrived from the other direction — it WAS a pill, and
// it does not change per message either, so the same rule that keeps the other
// three out of the row applies to it.
//
// Every row is built here; static/index.html carries an empty card.
// ---------------------------------------------------------------------------

import { el, effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { activeSession } from "./store.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { iconEl } from "./icon-el.js";
import { setSupervised } from "./actions/chat.js";
import { openFilePicker } from "./files-picker.js";
import { uploadLimitHint } from "./upload-policy.js";
import { openTangentChat } from "./chat.js";
import { loadRecipes, launchRun } from "./actions/runs.js";
import { openLiveRunView } from "./run-view.js";
import * as toast from "./toast.js";
import type { Recipe } from "./types.js";

// Row glyphs. Local constants rather than icons.ts entries: each is used once,
// by this module, and `icons.ts` is the shared vocabulary.
const ICON_ATTACH =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/></svg>';
const ICON_GOAL =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1" fill="currentColor"/></svg>';
const ICON_TANGENT =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 01-9 9"/></svg>';

/** The recipe NAME and source vibekit looks for when the goal row is used.
 *
 *  A LOOKUP key, never a launch key. The source POSTed is always the one the
 *  recipe row itself reported, because `POST /api/runs` re-resolves the source
 *  against a fresh `listRecipes` and refuses anything absent from it — so a
 *  hand-built `bundled://goal` would be a launch that can only ever fail on a
 *  build whose recipe set does not include one. Resolving instead means the row
 *  works whatever the list says, and says so plainly when there is no goal
 *  recipe in it. */
const GOAL_RECIPE_NAME = "goal";
const GOAL_RECIPE_SOURCE = "bundled://goal";

let bound = false;

/** Wire the chat actions pill. Idempotent; called once from app.ts. */
export function initChatOptions(): void {
  if (bound) {
    return;
  }
  bound = true;

  const pill = $.chatOptionsBtn;
  const card = $.chatOptionsCard;

  card.append(attachRow(), goalRow(), tangentRow(), supervisedRow());

  makeExpandable(pill, card, { haspopup: "dialog" });
}

/** A menu row that DOES something: glyph, name, one-line hint. */
function actionRow(opts: {
  icon: string;
  name: string;
  hint: string;
  onClick: (row: HTMLElement) => void;
}): HTMLElement {
  const btn = el(
    "button",
    { type: "button", className: "chat-opt-btn" },
    el("span", { className: "chat-opt-icon" }, iconEl(opts.icon)),
    el(
      "span",
      { className: "chat-opt-text" },
      el("span", { className: "chat-opt-name" }, opts.name),
      el("span", { className: "chat-opt-hint" }, opts.hint),
    ),
  ) as HTMLButtonElement;
  const row = el("div", { className: "chat-opt-entry" }, btn);
  btn.addEventListener("click", () => {
    opts.onClick(row);
  });
  return row;
}

/** Attach a file. The former paperclip pill, moved here unchanged in behaviour.
 *
 *  Collapse FIRST, then open: `openFilePicker` opens a modal over the composer,
 *  and an expanded card left behind it sits under the modal still carrying
 *  pointer-events.
 *
 *  Both calls are SYNCHRONOUS inside the click handler, and that ordering is a
 *  requirement rather than a preference. The OS file dialog is two clicks deeper
 *  (the picker's own "Upload here" calls `input.click()` inside its handler),
 *  so this door does not itself need a gesture — but putting an `await` anywhere
 *  on this path would move the picker's open off the user's gesture, and the
 *  browser's user-activation window is the one thing a file input cannot ask for
 *  again later. */
function attachRow(): HTMLElement {
  return actionRow({
    icon: ICON_ATTACH,
    name: "Attach a file",
    // The cap used to live in the pill's tooltip, which is gone with the pill.
    // It is stated where the choice is made, because the alternative is
    // discovering it as a server 413.
    hint: `Pick a workspace file or upload one (${uploadLimitHint().toLowerCase()})`,
    onClick: () => {
      collapseAll();
      openFilePicker();
    },
  });
}

/** Start a tangent off the active chat. */
function tangentRow(): HTMLElement {
  return actionRow({
    icon: ICON_TANGENT,
    name: "Start a tangent",
    hint: "Branch this conversation into a sub-chat that keeps its context",
    onClick: () => {
      const id = activeSession.peek()?.id ?? "";
      if (id === "") {
        // Nothing to branch from. A tangent inherits a conversation, so there is
        // no degraded version of one taken off a chat that has none — the New
        // chat button is that.
        toast.error("Open a chat first, then start a tangent from it");
        return;
      }
      collapseAll();
      openTangentChat(id);
    },
  });
}

/** Set a goal: launch the goal workflow and open its run tab.
 *
 *  NOT a typed `/goal`. That verb has a real parser upstream and a real
 *  iterating state machine behind it, but the TUI drives it as a structured
 *  command call, so text in the composer is prose to the model whatever the
 *  connection declares — the `/compact` failure exactly. A deterministic verb
 *  has to own its handler and call its own endpoint, and the endpoint here is
 *  `POST /api/runs`, which exists end to end today.
 *
 *  Inputs are collected inline, in an expanding row section rather than a modal —
 *  the same idiom the Workflows tab uses — and they are the recipe's OWN declared
 *  inputs, read off the wire. Nothing is invented: a recipe that declares no
 *  iteration cap gets no cap field, and a recipe that declares nothing at all
 *  launches with nothing. */
function goalRow(): HTMLElement {
  return actionRow({
    icon: ICON_GOAL,
    name: "Set a goal",
    hint: "Run the goal workflow: the agent iterates until it reports success",
    onClick: (row) => {
      void openGoalForm(row);
    },
  });
}

/** Resolve the goal recipe and render its input form into `row`. */
async function openGoalForm(row: HTMLElement): Promise<void> {
  const existing = row.querySelector(".chat-opt-form");
  if (existing !== null) {
    existing.remove();
    return;
  }
  const d = await loadRecipes.dispatch(undefined);
  if (d === null) {
    // loadRecipes carries its own error toast.
    return;
  }
  const recipe = d.recipes.find(
    (r) => r.name === GOAL_RECIPE_NAME || r.source === GOAL_RECIPE_SOURCE,
  );
  if (recipe === undefined) {
    toast.error("This build has no goal workflow available");
    return;
  }
  const declared = Object.keys(recipe.inputs ?? {});
  if (declared.length === 0) {
    // The recipe takes nothing, so there is nothing to collect and no field to
    // fabricate. Launch it.
    launch(recipe, {});
    return;
  }
  row.appendChild(goalForm(recipe, declared));
}

/** The inline input form: one field per DECLARED input, in declared order. */
function goalForm(recipe: Recipe, declared: string[]): HTMLElement {
  const fields = new Map<string, HTMLInputElement>();
  const form = el("form", { className: "chat-opt-form" });
  for (const key of declared) {
    const input = el("input", {
      type: "text",
      className: "chat-opt-input",
      placeholder: recipe.inputs?.[key] ?? "string",
      "aria-label": `Goal input ${key}`,
    }) as HTMLInputElement;
    fields.set(key, input);
    form.appendChild(el("label", { className: "chat-opt-input-label" }, key, input));
  }
  form.appendChild(el("button", { type: "submit", className: "btn-small" }, "Launch"));
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    const inputs: Record<string, string> = {};
    for (const [key, field] of fields) {
      if (field.value !== "") {
        inputs[key] = field.value;
      }
    }
    form.remove();
    launch(recipe, inputs);
  });
  form.addEventListener("keydown", (e: KeyboardEvent) => {
    // Escape closes the form without closing the whole card, so a mistyped goal
    // does not cost the menu. The card's own Escape (createPopup) still fires
    // once the form is gone.
    if (e.key === "Escape") {
      e.stopPropagation();
      form.remove();
    }
  });
  return form;
}

/** Launch the goal run and open it as a LAUNCHER-OWNED run tab.
 *
 *  Owned rather than a review, so the tab carries the run's own Pause / Resume /
 *  Cancel: this is the surface that started the run, and a run reached from
 *  History is deliberately read-only. */
function launch(recipe: Recipe, inputs: Record<string, string>): void {
  collapseAll();
  void launchRun.dispatch(
    { source: recipe.source, inputs },
    {
      onSuccess: (r) => {
        openLiveRunView(r.workflow_id, r.name);
      },
    },
  );
}

/** The supervised switch: the one resident that is a SWITCH rather than an
 *  action, which is why it sorts last and keeps the label/checkbox shape. */
function supervisedRow(): HTMLElement {
  const supervised = el("input", {
    type: "checkbox",
    id: "chat-opt-supervised",
  }) as HTMLInputElement;
  supervised.addEventListener("change", () => {
    const id = activeSession.peek()?.id ?? "";
    if (id === "") {
      // No chat yet: nothing to persist against. Reset the visual; the
      // supervised DEFAULT for brand-new chats lives in Settings →
      // Permissions, which is where a "before the first prompt" choice
      // belongs.
      supervised.checked = false;
      return;
    }
    void setSupervised.dispatch({ chatID: id, enabled: supervised.checked });
  });

  const row = el(
    "label",
    { className: "chat-opt-row", for: "chat-opt-supervised" },
    supervised,
    el(
      "span",
      { className: "chat-opt-text" },
      el("span", { className: "chat-opt-name" }, "Supervised mode"),
      el(
        "span",
        { className: "chat-opt-hint" },
        "Review this chat's file changes at the end of each turn",
      ),
    ),
  );

  // The checkbox mirrors the ACTIVE chat's persisted choice; the menu is a
  // projection, so switching tabs re-reads rather than remembers.
  effect(() => {
    supervised.checked = activeSession.value?.supervised_mode === true;
  });

  return row;
}
