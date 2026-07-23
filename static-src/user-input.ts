// ---------------------------------------------------------------------------
// User-input question dialog: shown when the agent asks a structured
// question mid-turn (kiro-cli v3 user_input tool — plan-mode
// clarifications, spec gates — forwarded as _kiro/userInput because
// vibekit declares the _meta.kiro.userInput capability).
//
// Renders the question with its options as choice cards (title +
// description + Recommended badge). A plain option answers on click; an
// option with sub-options expands a pre-checked multi-select stage
// (Confirm folds the picks into "Title [Sub1, Sub2]" — the TUI's answer
// format). A free-form question (no options) renders a textarea; when
// options exist a compact "type your own answer" field is offered too,
// mirroring the TUI where typing overrides the picker. Mirrors the
// permission/elicitation dialogs' request/response shape; styling reuses
// the approval-dialog vocabulary.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { openDialog } from "@cplieger/ui-primitives/dialog";
import type { UserInputNeededPayload, UserInputOption } from "./types.js";
import { $ } from "./dom.js";
import { trapFocus } from "./focus-trap.js";

type UserInputAction = "answered" | "dismissed";
type SubmitFn = (action: UserInputAction, answer?: string) => void;

// Resolved lazily: the dialog element only exists in the real app DOM.
let cachedDialog: HTMLDialogElement | null = null;
function dlg(): HTMLDialogElement {
  cachedDialog ??= $.userInputDialog;
  return cachedDialog;
}

// The request currently shown, so a superseding user_input_needed for a
// different request can dismiss the one still open before showing the new
// one (its agent-side question was superseded or re-asked on reconnect).
let activeRequestID: number | null = null;
let activeSubmit: SubmitFn | null = null;
let answered = false;
let releaseFocus: (() => void) | null = null;

export function showUserInputDialog(payload: UserInputNeededPayload, onSubmit: SubmitFn): void {
  // Settle any dialog already open: it's being superseded, so dismiss it.
  if (activeRequestID !== null && !answered) {
    finish("dismissed");
  }

  activeRequestID = payload.request_id;
  activeSubmit = onSubmit;
  answered = false;

  const dialogEl = dlg();
  const body = dialogEl.querySelector<HTMLElement>(".user-input-body");
  const optionsEl = dialogEl.querySelector<HTMLElement>(".user-input-options");
  const freeformEl = dialogEl.querySelector<HTMLElement>(".user-input-freeform");
  const actions = dialogEl.querySelector<HTMLElement>(".user-input-actions");
  if (!body || !optionsEl || !freeformEl || !actions) {
    return;
  }
  body.replaceChildren(
    el("strong", null, payload.question !== "" ? payload.question : "The agent has a question"),
  );
  optionsEl.replaceChildren();
  freeformEl.replaceChildren();
  actions.replaceChildren();

  const options = payload.options ?? [];
  renderOptionsStage(optionsEl, freeformEl, actions, options);

  dialogEl.oncancel = (e): void => {
    e.preventDefault();
    finish("dismissed");
  };

  openDialog(dialogEl);
  releaseFocus = trapFocus(dialogEl);
}

/** Stage 1: the choice cards (or the free-form editor when no options). */
function renderOptionsStage(
  optionsEl: HTMLElement,
  freeformEl: HTMLElement,
  actions: HTMLElement,
  options: readonly UserInputOption[],
): void {
  optionsEl.replaceChildren();
  freeformEl.replaceChildren();
  actions.replaceChildren();

  for (const opt of options) {
    optionsEl.appendChild(
      optionCard(opt, () => {
        if ((opt.sub_options ?? []).length > 0) {
          renderSubOptionsStage(optionsEl, freeformEl, actions, opt, options);
        } else {
          finish("answered", opt.title);
        }
      }),
    );
  }

  renderFreeform(freeformEl, options.length > 0);
  actions.appendChild(dismissButton());
}

/** One selectable answer card: title + optional description + badge. */
function optionCard(opt: UserInputOption, onPick: () => void): HTMLElement {
  const title = el("span", { className: "user-input-option-title" }, opt.title);
  const head = el("span", { className: "user-input-option-head" }, title);
  if (opt.recommended === true) {
    head.appendChild(el("span", { className: "user-input-recommended" }, "Recommended"));
  }
  const card = el(
    "button",
    { type: "button", className: "user-input-option" },
    head,
  ) as HTMLButtonElement;
  if (opt.description !== undefined && opt.description !== "") {
    card.appendChild(el("span", { className: "user-input-option-desc" }, opt.description));
  }
  if ((opt.sub_options ?? []).length > 0) {
    card.appendChild(
      el(
        "span",
        { className: "user-input-option-more" },
        opt.sub_options_label ?? "Choose details\u2026",
      ),
    );
  }
  card.addEventListener("click", onPick);
  return card;
}

/** Stage 2 for an option with sub-options: a pre-checked multi-select.
 *  Confirm answers with the TUI's "Title [Sub1, Sub2]" format; Back
 *  returns to the options stage. */
function renderSubOptionsStage(
  optionsEl: HTMLElement,
  freeformEl: HTMLElement,
  actions: HTMLElement,
  opt: UserInputOption,
  all: readonly UserInputOption[],
): void {
  optionsEl.replaceChildren();
  freeformEl.replaceChildren();
  actions.replaceChildren();

  optionsEl.appendChild(
    el("div", { className: "user-input-sub-label" }, opt.sub_options_label ?? opt.title),
  );

  const boxes: { box: HTMLInputElement; title: string }[] = [];
  for (const sub of opt.sub_options ?? []) {
    const box = el("input", { type: "checkbox" }) as HTMLInputElement;
    box.checked = true; // TUI parity: all pre-selected, untick to exclude
    boxes.push({ box, title: sub.title });
    const label = el(
      "label",
      { className: "user-input-sub-option" },
      box,
      el("span", { className: "user-input-option-title" }, sub.title),
    );
    if (sub.description !== undefined && sub.description !== "") {
      label.appendChild(el("span", { className: "user-input-option-desc" }, sub.description));
    }
    optionsEl.appendChild(label);
  }

  const confirm = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Confirm",
  ) as HTMLButtonElement;
  confirm.addEventListener("click", () => {
    const picked = boxes.filter((b) => b.box.checked).map((b) => b.title);
    finish("answered", `${opt.title} [${picked.join(", ")}]`);
  });
  const back = el(
    "button",
    { type: "button", className: "btn-small" },
    "Back",
  ) as HTMLButtonElement;
  back.addEventListener("click", () => {
    renderOptionsStage(optionsEl, freeformEl, actions, all);
  });
  actions.append(confirm, back, dismissButton());
}

/** The typed-answer editor. Primary (textarea) for a free-form question;
 *  compact alternative under the cards when options exist. */
function renderFreeform(freeformEl: HTMLElement, hasOptions: boolean): void {
  const input = el("textarea", {
    className: "user-input-text",
    rows: hasOptions ? "1" : "3",
    placeholder: hasOptions ? "Or type your own answer\u2026" : "Type your answer\u2026",
    "aria-label": "Your answer",
  }) as HTMLTextAreaElement;
  const send = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    "Send",
  ) as HTMLButtonElement;
  send.addEventListener("click", () => {
    const text = input.value.trim();
    if (text !== "") {
      finish("answered", text);
    } else {
      input.focus();
    }
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send.click();
    }
  });
  freeformEl.append(input, send);
}

function dismissButton(): HTMLButtonElement {
  const btn = el(
    "button",
    { type: "button", className: "btn-small confirm-danger" },
    "Skip",
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    finish("dismissed");
  });
  return btn;
}

function finish(action: UserInputAction, answer?: string): void {
  if (answered) {
    return;
  }
  answered = true;
  const submit = activeSubmit;
  closeDialog();
  submit?.(action, answer);
}

function closeDialog(): void {
  releaseFocus?.();
  releaseFocus = null;
  activeSubmit = null;
  activeRequestID = null;
  const dialogEl = dlg();
  if (dialogEl.open) {
    dialogEl.close();
  }
}

/** Reset module state (cached dialog element + active request) for test
 *  isolation. Production never calls this. */
export function _resetForTest(): void {
  cachedDialog = null;
  activeRequestID = null;
  activeSubmit = null;
  answered = false;
  releaseFocus = null;
}
