// ---------------------------------------------------------------------------
// User-input card: the agent asked a structured question mid-turn (kiro-cli
// v3 `_kiro/userInput` — plan clarifications, spec gates). Rendered in the
// interaction dock, which owns the queue and the settle-once guard.
//
// Three answer shapes, all reported as a plain string because that is the
// wire contract: a clicked option sends its title, an option with sub-options
// sends the TUI's "Title [Sub1, Sub2]", and a typed answer sends its text.
// Skip dismisses, which makes the agent advance to its next phase.
//
// It was a centered <dialog> with a focus trap. A question about work in the
// transcript should not cover the transcript, and a non-modal region must not
// hold focus captive.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import type { UserInputNeededPayload, UserInputOption } from "./types.js";

type UserInputAction = "answered" | "dismissed";
type SubmitFn = (action: UserInputAction, answer?: string) => void;

// The mounted card's reporter. One card is on screen at a time (the dock
// renders its queue head), and the dock rejects a second answer for the same
// request, so the stage renderers can reach it as module state instead of
// threading it through every stage transition.
let activeSubmit: SubmitFn | null = null;

/** Build the dock card for one agent question. */
export function buildUserInputCard(
  payload: UserInputNeededPayload,
  onSubmit: SubmitFn,
): HTMLElement {
  activeSubmit = onSubmit;

  const body = el(
    "div",
    { className: "user-input-body" },
    el("strong", null, payload.question !== "" ? payload.question : "The agent has a question"),
  );
  const optionsEl = el("div", { className: "user-input-options" });
  const freeformEl = el("div", { className: "user-input-freeform" });
  const actions = el("div", { className: "user-input-actions" });

  renderOptionsStage(optionsEl, freeformEl, actions, payload.options ?? []);

  return el(
    "div",
    { className: "dock-card dock-user-input" },
    body,
    optionsEl,
    freeformEl,
    actions,
  );
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
  activeSubmit?.(action, answer);
}

/** Reset module state for test isolation. Production never calls this. */
export function _resetForTest(): void {
  activeSubmit = null;
}
