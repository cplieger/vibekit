// ---------------------------------------------------------------------------
// Prompt input: form submit, keydown (Enter, ↑/↓ history), send button,
// iOS viewport fix.
//
// Three send-button states driven by send-state.ts:
//   idle      → send icon, click submits
//   streaming → stop icon (pulse), click cancels the turn
//   error     → alert icon, tooltip names the failure, click RETRIES
//
// THE ERROR FACE MEANS "THERE IS NOTHING TO SEND TO", NOT "THE LAST SEND
// FAILED". Two states reach it and both are about reachability: the SSE stream is
// down, or kiro-cli could not be spawned for this chat. Every ordinary failure —
// a 429 throttle, a 5xx, a capacity refusal, a timeout, a refused model switch —
// goes to a bottom-right toast (failure-notice.ts) and to the turn's own
// transcript divider instead. An alert icon on the control whose job is to send
// reads as "this chat is dead", which was false for all of them.
//
// NOTHING HERE DISABLES THE COMPOSER, and that is the whole point of the
// module. The retired fourth state (`blocked`) set `disabled` on the button AND
// the textarea, and every failure routed to it: a 429 throttle, a 5xx, a dead
// POST, a failed bridge start, a dropped SSE stream. None of those means the
// user may not send. A failed prompt leaves the server IDLE (CmdPrompt's
// deferred ReleaseAfterPrompt runs on the error path too), so the chat is
// promptable again the moment the error arrives — and pressing Send again is
// exactly what the user wants at that moment. Disabling the textarea also made
// the pending text unrecoverable and left switching chats as the only way out.
//
// The rule: the composer stays live unless the process behind it is gone, and
// no state this module can observe proves that. A dropped SSE stream does not
// (the stream and the command POST are different connections, the POST usually
// still lands, and the reconnect replay catches the transcript up). A full
// context does not (kiro-cli compacts on the next turn). What a real failure
// earns is a report, not a lock.
//
// There is no `queued` state either: a prompt typed during a turn is a steer,
// sent straight away, so nothing waits on the button. Steers awaiting the agent
// show on the chip row (pending-steers.ts).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getActive, getActiveId } from "./store.js";
import { fixIOSViewport } from "./platform.js";
import { ICON_SEND, ICON_CANCEL, ICON_ALERT } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { collapseAll } from "./pill-expand.js";
import { setComposerValue } from "./composer-value.js";
import { signal, effect } from "@cplieger/reactive";

// Single source of truth for the "context (nearly) full" state, scoped to the
// ACTIVE chat (the prompt bar is shared). The VALUE is computed and written by
// context-ui.ts (refreshContextUI); this module owns the send button/textarea,
// so it holds the signal and is the sole reader/writer of the resulting
// placeholder + tooltip. ADVISORY ONLY — it changes what the bar says and never
// whether a send is allowed, because kiro-cli compacts on the next turn and
// refusing the send left the user nothing to do about it. Keeping the signal
// here (rather than importing it from context-ui) keeps the send-state →
// prompt-input import chain free of the heavy status/context-ui modules, so the
// transport/actions graph never loads them.
export const contextFull = signal(false);

/** Placeholder / tooltip shown while `contextFull` is true. Module-local:
 *  only prompt-input.ts renders it. */
const CONTEXT_FULL_REASON =
  "Context nearly full. kiro-cli will compact automatically on the next turn.";

/** Appended to every error tooltip. A fixed suffix rather than a test against
 *  the reason's wording: deciding whether a message already implies "resend"
 *  means matching keywords against upstream prose, and one redundant sentence in
 *  the throttle case is cheaper than a classifier that reads it wrong. */
const RETRY_HINT = "Send again to retry.";

type Submit = (text: string) => void;
type Cancel = () => void;

export type SendState =
  { kind: "idle" } | { kind: "streaming" } | { kind: "error"; reason: string };

type SendKind = SendState["kind"];

const STATE_ICON: Record<SendKind, string> = {
  idle: ICON_SEND,
  streaming: ICON_CANCEL,
  error: ICON_ALERT,
};

const DEFAULT_TOOLTIP: Record<SendKind, string> = {
  idle: "Send",
  streaming: "Cancel this turn",
  // Unreachable in practice (send-state only builds an error with a non-empty
  // reason), present because the record is exhaustive over the union.
  error: RETRY_HINT,
};

/** The reason a state carries, "" for the states that carry none. Doubles as
 *  the dedupe key: two errors differing only in reason are different states. */
function reasonOf(s: SendState): string {
  return s.kind === "error" ? s.reason : "";
}

let initialized = false;

/** How long after `compositionend` an Enter is still treated as the IME's
 *  commit key rather than a send.
 *
 *  Ported from Crew's guard verbatim, and it is not belt-and-braces with the
 *  `isComposing` / keyCode legs: on several IMEs the commit Enter arrives just
 *  AFTER composition ended, with `isComposing` already false and no 229. The
 *  tail is the only thing that covers that ordering. */
const IME_TAIL_MS = 50;

class PromptInputController {
  // History cycling state
  private idx = -1;
  private draft = "";
  private lastActiveID = "";

  // IME composition state. `composing` is the first leg of the three-way guard
  // below; `imeTimer` is the compositionend tail that keeps it true a moment
  // longer.
  private composing = false;
  private imeTimer: ReturnType<typeof setTimeout> | undefined;

  // Send-button state
  private state: SendState = { kind: "idle" };
  private onCancel: Cancel = () => undefined;
  private onSubmit: Submit = () => undefined;

  /** Send whatever is in the composer. THE send path: the form's submit handler,
   *  Enter, and the keyboard shortcut all call this.
   *
   *  Nothing fakes a submit event any more, and that mattered. Enter and the
   *  shortcut used to run `form.dispatchEvent(new Event("submit"))`, and
   *  `new Event()` is `cancelable: false`, so the handler's preventDefault() was
   *  a no-op: the browser went on to perform the form's NATIVE submission on
   *  every send. What stopped that being a page navigation was the
   *  `action="javascript:void 0"` backstop in index.html plus CSP
   *  `form-action 'self'`, which turned it into a console violation instead —
   *  the exact failure that backstop's comment describes. Calling the send
   *  directly removes the cause rather than the symptom.
   *
   *  NOT `form.requestSubmit()`, which fires a cancelable event but runs the
   *  form's constraint validation first. The decision dock is a child of this
   *  same form, and an elicitation field carries `pattern`, `minLength`,
   *  `type=email`/`url` and `step=1` from the MCP server's own schema — so a
   *  half-typed value in a dock field is `:invalid`, and requestSubmit would
   *  silently refuse to send the chat message and report validity on the dock
   *  instead.
   *
   *  submit.ts decides what a send MEANS: a prompt on an idle chat, a steer into
   *  a turn already running. Nothing here refuses it. A previous send having
   *  failed is precisely when the user wants to press again, so the retry is the
   *  plain path rather than a special one. */
  sendComposer(): void {
    const text = $.promptInput.value.trim();
    if (text === "") {
      return;
    }
    this.exitCycling();
    this.onSubmit(text);
    setComposerValue("");
  }

  private exitCycling(): void {
    this.idx = -1;
    this.draft = "";
  }

  /** Leave cycling and put the saved draft back in the box.
   *
   *  ONE method for both keys that end cycling (Escape, and ArrowDown off the
   *  newest prompt), because exitCycling() zeroes this.draft: read it after the
   *  exit and you get "", so the user's typing is silently thrown away. Escape
   *  saved it to a local first and ArrowDown did not, which is exactly the bug
   *  this method removes the room for. */
  private restoreDraft(el: HTMLTextAreaElement): void {
    const saved = this.draft;
    this.exitCycling();
    this.setInputValue(el, saved);
  }

  /** Whether this keystroke belongs to an IME composition, so Enter must reach
   *  the browser (committing the candidate) instead of sending the prompt.
   *
   *  Three legs, and each covers a case the others miss, which is why this is a
   *  port of Crew's predicate rather than a reinvention of it:
   *
   *    - `this.composing` — set by compositionstart and held through the
   *      compositionend tail. The only leg that survives an Enter delivered
   *      after composition ended.
   *    - `e.isComposing` — the platform's own answer, read straight off the
   *      native KeyboardEvent (there is no synthetic-event wrapper here).
   *      Authoritative when the browser sets it, and it is sometimes FALSE on
   *      exactly the Enter that commits the candidate.
   *    - `e.keyCode === 229` — the "processed by the IME" sentinel. Deprecated
   *      but still what several Android and Windows IMEs report for that final
   *      Enter, which is the reason the leg exists.
   *
   *  Vibekit's shell terminal is deliberately out of scope: @cplieger/web-terminal-ui
   *  owns everything above the raw stream, IME included. */
  private isComposing(e: KeyboardEvent): boolean {
    // eslint-disable-next-line @typescript-eslint/no-deprecated -- keyCode 229 is the whole reason to port this guard: the IME's commit Enter reports it when isComposing is already false.
    return this.composing || e.isComposing || e.keyCode === 229;
  }

  /** Drop composition state. Blur and Escape.
   *
   *  Defensive rather than decorative: a browser that never delivers
   *  compositionend (some Android IMEs, when the field loses focus mid-candidate)
   *  would otherwise leave the flag stuck true and disable Enter for the rest of
   *  the page's life — a worse failure than the bug being fixed. */
  private resetIME(): void {
    clearTimeout(this.imeTimer);
    this.imeTimer = undefined;
    this.composing = false;
  }

  private userPrompts(): string[] {
    const s = getActive();
    if (s === undefined) {
      return [];
    }
    const out: string[] = [];
    for (let i = s.messages.length - 1; i >= 0; i--) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const m = s.messages[i]!;
      if (m.role === "user" && (m.content ?? "") !== "") {
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
        out.push(m.content!);
      }
    }
    return out;
  }

  private cursorOnFirstLine(el: HTMLTextAreaElement): boolean {
    const pos = el.selectionStart;
    if (pos !== el.selectionEnd) {
      return false;
    }
    return !el.value.slice(0, pos).includes("\n");
  }

  private cursorOnLastLine(el: HTMLTextAreaElement): boolean {
    const pos = el.selectionStart;
    if (pos !== el.selectionEnd) {
      return false;
    }
    return !el.value.slice(pos).includes("\n");
  }

  /** Show a value in the box WITHOUT announcing it.
   *
   *  History cycling is navigation, not editing: the displayed prompt is a
   *  preview of something already sent, and `this.draft` holds what the user
   *  actually typed until they leave the cycle. So no `input` event — the
   *  module's own listener treats one as leaving the cycle, and the per-chat
   *  draft layer would record the old prompt as the new draft.
   *
   *  The corollary belongs to that layer and is why it reads its own map rather
   *  than this element: while a history item is on screen the textarea is not the
   *  draft, so a save that read `.value` here sent the old prompt to the server
   *  and overwrote the real one with it. */
  private setInputValue(el: HTMLTextAreaElement, v: string): void {
    el.value = v;
    el.setSelectionRange(v.length, v.length);
  }

  private applyButtonState(): void {
    const k = this.state.kind;
    // Context-full is advisory and only says anything while idle: text typed
    // mid-turn is a STEER (it does not size the next prompt), and after a
    // failure the error is the more useful thing to report. It writes the
    // placeholder and the tooltip; it does NOT refuse a send.
    const ctxFull = k === "idle" && contextFull.value;
    const reason = reasonOf(this.state);
    $.sendBtn.replaceChildren(iconEl(STATE_ICON[k]));
    const tooltip =
      reason !== ""
        ? `${reason} ${RETRY_HINT}`
        : ctxFull
          ? CONTEXT_FULL_REASON
          : DEFAULT_TOOLTIP[k];
    $.sendBtn.setAttribute("data-tooltip", tooltip);
    $.sendBtn.setAttribute("aria-label", tooltip);
    $.sendBtn.classList.toggle("streaming", k === "streaming");
    $.sendBtn.classList.toggle("failed", k === "error");

    // ONE button, two meanings: type=button while streaming so a click cannot
    // fire the form — clicking always CANCELS during a turn, while Enter still
    // submits (the keydown dispatches the form event directly). Two gestures,
    // one rule each; the label never decides.
    $.sendBtn.type = k === "streaming" ? "button" : "submit";
    // Written unconditionally rather than left alone, so nothing that ever set
    // them can leave the composer stuck off. See the module header for why no
    // observable state earns a lock.
    $.sendBtn.disabled = false;
    $.promptInput.disabled = false;
    $.promptInput.placeholder = ctxFull ? CONTEXT_FULL_REASON : "Message Kiro...";
  }

  setSendState(next: SendState): void {
    if (this.state.kind === next.kind && reasonOf(this.state) === reasonOf(next)) {
      return;
    }
    this.state = next;
    this.applyButtonState();
  }

  init(onSubmit: Submit, onCancel: Cancel): void {
    if (initialized) {
      return;
    }
    initialized = true;

    const form = $.promptForm;
    const input = $.promptInput;

    // The one button's streaming-click = cancel. type=button in that state
    // keeps the click out of the form's submit path entirely.
    this.onCancel = onCancel;
    this.onSubmit = onSubmit;
    $.sendBtn.addEventListener("click", (e: MouseEvent) => {
      if (this.state.kind === "streaming") {
        e.preventDefault();
        e.stopPropagation();
        this.onCancel();
      }
    });

    // One effect keeps the tooltip/placeholder in sync with the context-full
    // signal (runs once immediately for the initial paint, then on every
    // change). setSendState() calls applyButtonState directly for send-state
    // kind changes (this.state isn't a signal), so both inputs stay in sync.
    effect(() => {
      void contextFull.value;
      this.applyButtonState();
    });

    form.addEventListener("submit", (e: Event) => {
      e.preventDefault();
      this.sendComposer();
    });

    // Composition listeners live beside the other input listeners, on the same
    // element. compositionstart clears any pending tail so back-to-back
    // compositions cannot have a stale timer flip the flag false mid-candidate.
    input.addEventListener("compositionstart", () => {
      clearTimeout(this.imeTimer);
      this.imeTimer = undefined;
      this.composing = true;
    });
    input.addEventListener("compositionend", () => {
      this.composing = true;
      clearTimeout(this.imeTimer);
      this.imeTimer = setTimeout(() => {
        this.composing = false;
        this.imeTimer = undefined;
      }, IME_TAIL_MS);
    });

    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (getActiveId() !== this.lastActiveID) {
        this.lastActiveID = getActiveId();
        this.exitCycling();
      }

      // Escape drops composition state UNCONDITIONALLY, ahead of the
      // history-cycling branch below. That branch is guarded on `idx !== -1`, so
      // a reset placed inside it would only ever fire while cycling history —
      // the wrong condition entirely. No stopPropagation here either: the
      // cycling branch stops it because it consumes the key, while a plain
      // Escape still has to reach the global handler that collapses pills and
      // closes the dock.
      if (e.key === "Escape") {
        this.resetIME();
      }

      if (e.key === "Enter" && !e.shiftKey && !e.ctrlKey) {
        // A composing Enter falls THROUGH to the browser, which commits the IME
        // candidate. The check has to sit inside this branch rather than as an
        // early return at the top of the handler: an early return would also
        // break ArrowUp/ArrowDown history navigation during composition, which
        // is not the bug.
        if (this.isComposing(e)) {
          return;
        }
        e.preventDefault();
        this.sendComposer();
        return;
      }

      if (e.key === "ArrowUp" && this.cursorOnFirstLine(input)) {
        const prompts = this.userPrompts();
        if (prompts.length === 0) {
          return;
        }
        if (this.idx === -1) {
          this.draft = input.value;
        }
        const next = Math.min(this.idx + 1, prompts.length - 1);
        if (next === this.idx) {
          return;
        }
        this.idx = next;
        e.preventDefault();
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
        this.setInputValue(input, prompts[this.idx]!);
        return;
      }

      if (e.key === "ArrowDown" && this.cursorOnLastLine(input) && this.idx !== -1) {
        e.preventDefault();
        if (this.idx === 0) {
          this.restoreDraft(input);
          return;
        }
        this.idx -= 1;
        const prompts = this.userPrompts();
        this.setInputValue(input, prompts[this.idx] ?? "");
        return;
      }

      if (e.key === "Escape" && this.idx !== -1) {
        e.preventDefault();
        e.stopPropagation();
        this.restoreDraft(input);
        return;
      }
    });

    input.addEventListener("input", () => {
      if (this.idx !== -1) {
        this.exitCycling();
      }
    });
    input.addEventListener("focus", () => {
      collapseAll();
    });
    // The composer had no blur listener at all. It earns one now: a browser that
    // drops compositionend (some Android IMEs, when the field loses focus
    // mid-candidate) must not leave the flag stuck true and Enter disabled for
    // the rest of the page's life. The draft layer keeps its OWN blur listener
    // for its own concern; see the note above setComposerValue for why this
    // module does not import it.
    input.addEventListener("blur", () => {
      this.resetIME();
    });

    fixIOSViewport(input);
  }
}

const instance = new PromptInputController();

/** Called by send-state.ts whenever inputs change. */
export function setSendState(next: SendState): void {
  instance.setSendState(next);
}

/** Put a failed send's text back in the box, so retrying is not retyping.
 *
 *  submit.ts calls this on its failure paths, beside the attachment restore it
 *  already did. Only writes into an EMPTY box: the send is asynchronous, so the
 *  user may already be typing the next thing, and their live draft outranks text
 *  they watched fail. */
export function restorePromptText(text: string): void {
  if (text === "") {
    return;
  }
  const input = $.promptInput;
  if (input.value !== "") {
    return;
  }
  // Announced, not just assigned: the draft layer recorded "" when the submit
  // cleared the box, and without the event a reload would lose text the user can
  // see sitting there.
  setComposerValue(text);
}

export function initPromptInput(onSubmit: Submit, onCancel: Cancel): void {
  instance.init(onSubmit, onCancel);
}

/** Send the composer's contents. The keyboard shortcut's entry point, and the
 *  same path Enter and the send button take.
 *
 *  Exported rather than left as a synthetic submit event dispatched at the form:
 *  see PromptInputController.sendComposer for why faking that event performed a
 *  native form submission on every send. */
export function sendComposer(): void {
  instance.sendComposer();
}
