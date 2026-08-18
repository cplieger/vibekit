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
import { activeSession, isThinking, isEmptyChat } from "./store.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { iconEl } from "./icon-el.js";
import { setSupervised } from "./actions/chat.js";
import { openFilePicker } from "./files-picker.js";
import { uploadLimitHint } from "./upload-policy.js";
import { openTangentChat } from "./chat.js";
import { submitPrompt } from "./submit.js";
import * as toast from "./toast.js";

// Row glyphs. Local constants rather than icons.ts entries: each is used once,
// by this module, and `icons.ts` is the shared vocabulary.
const ICON_ATTACH =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 01-8.49-8.49l9.19-9.19a4 4 0 015.66 5.66l-9.2 9.19a2 2 0 01-2.83-2.83l8.49-8.48"/></svg>';
const ICON_GOAL =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1" fill="currentColor"/></svg>';
const ICON_TANGENT =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 01-9 9"/></svg>';

/** KAS's own clamp on the goal loop's iteration budget, mirrored here rather
 *  than invented: `parseGoalCommand` runs
 *  `Math.min(Math.max(parseInt(n, 10), 1), 200)`.
 *
 *  Mirrored so the field can bound its own control and the composed suffix is
 *  always a value the parser keeps. KAS's DEFAULT is deliberately not mirrored:
 *  an unset cap omits the suffix entirely, so the default that applies is
 *  whatever KAS's parser says it is rather than a number vibekit restates and
 *  can drift from. */
const GOAL_MAX_FLOOR = 1;
const GOAL_MAX_CEILING = 200;

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

/** A menu row that DOES something: glyph, name, one-line hint.
 *
 *  Returns the row plus its button and hint, because a row whose availability is
 *  a function of chat state needs to reach both after construction: the button to
 *  disable, the hint to say why. */
function actionRow(opts: {
  icon: string;
  name: string;
  hint: string;
  onClick: (row: HTMLElement) => void;
}): { row: HTMLElement; btn: HTMLButtonElement; hint: HTMLElement } {
  const hint = el("span", { className: "chat-opt-hint" }, opts.hint);
  const btn = el(
    "button",
    { type: "button", className: "chat-opt-btn" },
    el("span", { className: "chat-opt-icon" }, iconEl(opts.icon)),
    el(
      "span",
      { className: "chat-opt-text" },
      el("span", { className: "chat-opt-name" }, opts.name),
      hint,
    ),
  ) as HTMLButtonElement;
  const row = el("div", { className: "chat-opt-entry" }, btn);
  btn.addEventListener("click", () => {
    opts.onClick(row);
  });
  return { row, btn, hint };
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
  }).row;
}

/** The hints the tangent row swaps between. The disabled one names what to do
 *  next rather than what went wrong, because the row is unavailable before the
 *  user has done anything, not after a mistake. */
const TANGENT_HINT = "Branch this conversation into a sub-chat that keeps its context";
const TANGENT_HINT_EMPTY = "Send a message first; a tangent inherits the conversation";

/** Start a tangent off the active chat.
 *
 *  UNAVAILABLE until the chat holds a conversation, and disabled rather than
 *  error-toasting: on a brand-new chat the fork had nothing to branch from, so it
 *  404'd server-side (`errForkParentUnknown` — a chat is client-side only until
 *  its first prompt) AFTER `openTangentChat` had already opened and activated the
 *  sub-tab. The user was left holding a stray empty sub-tab plus a failure
 *  notice, which is exactly the shape the disabled control avoids. */
function tangentRow(): HTMLElement {
  const { row, btn, hint } = actionRow({
    icon: ICON_TANGENT,
    name: "Start a tangent",
    hint: TANGENT_HINT,
    onClick: () => {
      // Re-read at CLICK time, never captured when the card was built: the card
      // is built once at init and outlives every chat switch. A disabled button
      // fires no click, so this is the guard for the window between a signal
      // write and the effect below, not the user-facing refusal.
      const session = activeSession.peek();
      if (session === undefined || isEmptyChat(session)) {
        toast.error("Send a message first, then start a tangent from it");
        return;
      }
      collapseAll();
      openTangentChat(session.id);
    },
  });

  // A projection of the ACTIVE chat, like the supervised checkbox below: the menu
  // remembers nothing, so switching tabs re-reads. Guarded on the value because
  // `activeSession` re-derives on every streaming chunk.
  effect(() => {
    const empty = isEmptyChat(activeSession.value);
    if (btn.disabled === empty) {
      return;
    }
    btn.disabled = empty;
    hint.textContent = empty ? TANGENT_HINT_EMPTY : TANGENT_HINT;
  });

  return row;
}

/** Set a goal: send the command KAS's own parser claims.
 *
 *  A typed `/goal` is NOT the `/compact` failure shape, and the difference is the
 *  whole reason this row sends text. `/compact` reached the model because nothing
 *  parsed it. `/goal` is intercepted on the prompt path BEFORE the model is
 *  invoked: with `_meta.kiro.settings.goal` declared (vibekit declares it —
 *  `internal/kascap/table.go`), `session/prompt` runs `parseGoalCommand(userText)`
 *  and, on a match, `launchGoal(...)` and returns `end_turn` without ever calling
 *  the model.
 *
 *  Why not the bundled recipe, which this row used to launch: the recipe's own
 *  repeat node is written `maxIterations: 200`, and `launchGoal` applies the
 *  user's bound by MUTATING that node on a clone before handing the inline
 *  workflow to `_kiro/workflow/new`. Loading the recipe by source instead runs the
 *  unmutated node, so every goal launched that way iterated up to 200 whatever
 *  the user asked for. The cap is only reachable through the parser.
 *
 *  The run KAS starts is parented on the CALLING session, so its frames arrive on
 *  this chat's topic and it renders in this chat. That is the right home for a
 *  goal set from this chat's composer, and it is why there is no launcher-owned
 *  run tab here.
 *
 *  There is no clear verb, and its absence is measured rather than an omission:
 *  `parseGoalCommand` returns null only for a bare `/goal` and otherwise takes
 *  the whole body as the objective, so `/goal clear` would launch a goal whose
 *  objective is the word "clear". Stopping a goal is cancelling its run, which
 *  the run surface already does. */
function goalRow(): HTMLElement {
  return actionRow({
    icon: ICON_GOAL,
    name: "Set a goal",
    hint: "The agent iterates toward it until it reports success",
    onClick: openGoalForm,
  }).row;
}

/** Toggle the inline goal form on the row. */
function openGoalForm(row: HTMLElement): void {
  const existing = row.querySelector(".chat-opt-form");
  if (existing !== null) {
    existing.remove();
    return;
  }
  row.appendChild(goalForm());
}

/** The inline form: the objective, and an optional cap on the loop.
 *
 *  Inline rather than a modal, the same idiom the Workflows tab uses for a
 *  recipe's inputs. */
function goalForm(): HTMLElement {
  const description = el("input", {
    type: "text",
    className: "chat-opt-input",
    placeholder: "Make the test suite pass",
    "aria-label": "Goal",
  }) as HTMLInputElement;
  // Deliberately a text field with a numeric keypad rather than type="number".
  // A number input sanitizes a non-numeric value to "" and lets the browser
  // refuse an out-of-range one before submit, which would put the bounds in the
  // browser's hands — and they are KAS's bounds, applied to a suffix KAS parses.
  // Owning them here is what makes the clamp and the drop below real rather than
  // decorative, on every engine and for a pasted or autofilled value.
  const cap = el("input", {
    type: "text",
    className: "chat-opt-input",
    inputMode: "numeric",
    "aria-label": "Max iterations",
  }) as HTMLInputElement;

  const form = el(
    "form",
    { className: "chat-opt-form" },
    el("label", { className: "chat-opt-input-label" }, "Goal", description),
    el("label", { className: "chat-opt-input-label" }, "Max iterations (optional)", cap),
    el("button", { type: "submit", className: "btn-small" }, "Set goal"),
  );

  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    // Resolved at SUBMIT time, never captured when the card was built: the card
    // is built once at init and outlives every chat switch.
    const id = activeSession.peek()?.id ?? "";
    if (id === "") {
      toast.error("Open a chat first, then set a goal in it");
      return;
    }
    const objective = description.value.trim();
    if (objective === "") {
      // Refused here rather than sent: a bare `/goal` is exactly what
      // parseGoalCommand returns null for, so it would fall through to the model
      // as prose — the failure this row exists to avoid.
      toast.error("Describe the goal before setting it");
      return;
    }
    if (isThinking(id)) {
      // Send means STEER mid-turn (submit.ts), and `_session/steer` is not the
      // prompt path: parseGoalCommand has exactly one call site and it is
      // `session/prompt`, so a steered command reaches the running turn as prose.
      toast.error("Wait for this turn to finish, then set the goal");
      return;
    }
    form.remove();
    collapseAll();
    void submitPrompt(id, goalCommand(objective, cap.value));
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

/** Compose exactly what `parseGoalCommand` accepts.
 *
 *  Its shape is `/\s+--max\s+(\d+)$/` against the body, so the suffix must be
 *  LAST and must be digits — anything else is read as part of the objective. That
 *  is why a cap that is not a whole number is DROPPED rather than passed through:
 *  `--max soon` would silently become the tail of the goal statement.
 *
 *  An absent cap omits the suffix, so KAS's own default applies. */
function goalCommand(objective: string, cap: string): string {
  const command = `/goal ${objective}`;
  const raw = cap.trim();
  if (raw === "") {
    return command;
  }
  const n = Number(raw);
  if (!Number.isInteger(n)) {
    return command;
  }
  const bounded = Math.min(Math.max(n, GOAL_MAX_FLOOR), GOAL_MAX_CEILING);
  return `${command} --max ${bounded}`;
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
