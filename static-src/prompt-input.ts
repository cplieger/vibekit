// ---------------------------------------------------------------------------
// Prompt input: form submit, keydown (Enter, ↑/↓ history), send button,
// iOS viewport fix.
//
// Four send-button states driven by send-state.ts:
//   idle    → send icon, input enabled, click submits
//   busy    → stop icon (pulse), input enabled, click cancels the turn
//   queued  → hourglass, input disabled, auto-sends on turn_ended
//   blocked → alert icon, input disabled, tooltip explains why
//
// The send button IS the error surface. Whenever the user can't submit,
// the button shows why via icon + tooltip. No inline error cards, no
// toasts.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getActive, getActiveId } from "./store.js";
import { fixIOSViewport } from "./platform.js";
import { ICON_SEND, ICON_CANCEL, ICON_ALERT } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { collapseAll } from "./pill-expand.js";
import { signal, effect, el } from "@cplieger/reactive";

// Single source of truth for the "context (nearly) full → block the next
// prompt" state, scoped to the ACTIVE chat (the prompt bar is shared). The
// VALUE is computed and written by context-ui.ts (refreshContextUI); this
// module owns the send button/textarea, so it holds the signal and is the sole
// reader/writer of the resulting `disabled` DOM props + placeholder. Keeping
// the signal here (rather than importing it from context-ui) keeps the
// send-state → prompt-input import chain free of the heavy status/context-ui
// modules, so the transport/actions graph never loads them.
export const sendDisabled = signal(false);

/** Placeholder / tooltip shown while `sendDisabled` is true. Module-local:
 *  only prompt-input.ts renders it. */
const SEND_DISABLED_REASON =
  "Context nearly full. kiro-cli will compact automatically on the next turn.";

type Submit = (text: string) => void;
type Cancel = () => void;

export type SendState =
  | { kind: "idle" }
  | { kind: "streaming" }
  | { kind: "queued"; count: number }
  | { kind: "blocked"; reason: string };

type SendKind = SendState["kind"];

const STATE_ICON: Record<SendKind, string> = {
  idle: ICON_SEND,
  streaming: ICON_CANCEL,
  queued: ICON_SEND,
  blocked: ICON_ALERT,
};

const DEFAULT_TOOLTIP: Record<SendKind, string> = {
  idle: "Send",
  streaming: "Cancel this turn",
  queued: "Send — the queue delivers in order",
  blocked: "Cannot send right now",
};

let initialized = false;

class PromptInputController {
  // History cycling state
  private idx = -1;
  private draft = "";
  private lastActiveID = "";

  // Send-button state
  private state: SendState = { kind: "idle" };
  private onCancel: Cancel = () => undefined;

  private exitCycling(): void {
    this.idx = -1;
    this.draft = "";
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

  private setInputValue(el: HTMLTextAreaElement, v: string): void {
    el.value = v;
    el.setSelectionRange(v.length, v.length);
  }

  private applyButtonState(): void {
    const k = this.state.kind;
    // Context-full (the sole `sendDisabled` signal from context-ui.ts) is an
    // orthogonal disable that only applies while idle — blocked owns its own
    // disable, and typing-ahead during a streaming turn (which QUEUES) must
    // stay allowed. This is the single place the send-button/textarea
    // `disabled` + placeholder are written.
    const ctxFull = k === "idle" && sendDisabled.value;
    $.sendBtn.replaceChildren(iconEl(STATE_ICON[k]));
    // The queue badge: a streaming turn with pending messages shows Cancel
    // (stopping is the guarantee), a resting queue shows Send with the count.
    if (k === "queued") {
      $.sendBtn.appendChild(
        el("span", { className: "send-queue-badge" }, String(this.state.count)),
      );
    }
    const tooltip = ctxFull
      ? SEND_DISABLED_REASON
      : this.state.kind === "blocked"
        ? this.state.reason
        : DEFAULT_TOOLTIP[k];
    $.sendBtn.setAttribute("data-tooltip", tooltip);
    $.sendBtn.setAttribute("aria-label", tooltip);
    $.sendBtn.classList.toggle("streaming", k === "streaming");
    $.sendBtn.classList.toggle("queued", k === "queued");
    $.sendBtn.classList.toggle("blocked", k === "blocked");

    // ONE button, two meanings: type=button while streaming so a click cannot
    // fire the form — clicking always CANCELS during a turn, while Enter still
    // submits (the keydown dispatches the form event directly). Two gestures,
    // one rule each; the label never decides.
    $.sendBtn.type = k === "streaming" ? "button" : "submit";
    $.sendBtn.disabled = k === "blocked" || ctxFull;
    $.promptInput.disabled = k === "blocked" || ctxFull;
    $.promptInput.placeholder = ctxFull ? SEND_DISABLED_REASON : "Message Kiro...";
  }

  setSendState(next: SendState): void {
    if (
      this.state.kind === next.kind &&
      (this.state.kind !== "blocked" ||
        (next as { kind: "blocked"; reason: string }).reason === this.state.reason)
    ) {
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
    $.sendBtn.addEventListener("click", (e: MouseEvent) => {
      if (this.state.kind === "streaming") {
        e.preventDefault();
        e.stopPropagation();
        this.onCancel();
      }
    });

    // One effect keeps the disable state in sync with the context-full signal
    // (runs once immediately for the initial paint, then on every change).
    // setSendState() calls applyButtonState directly for send-state kind
    // changes (this.state isn't a signal), so both inputs stay in sync.
    effect(() => {
      void sendDisabled.value;
      this.applyButtonState();
    });

    form.addEventListener("submit", (e: Event) => {
      e.preventDefault();
      // Enter is defined independently of the label: it appends to the queue
      // whenever a turn is streaming OR the queue is already non-empty, and
      // the drain is the only thing that sends (prompt-queue owns that
      // decision). Only a hard block refuses input.
      if (this.state.kind === "blocked") {
        return;
      }
      const text = input.value.trim();
      if (text === "") {
        return;
      }
      this.exitCycling();
      onSubmit(text);
      input.value = "";
    });

    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (getActiveId() !== this.lastActiveID) {
        this.lastActiveID = getActiveId();
        this.exitCycling();
      }

      if (e.key === "Enter" && !e.shiftKey && !e.ctrlKey) {
        e.preventDefault();
        form.dispatchEvent(new Event("submit"));
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
          this.exitCycling();
          this.setInputValue(input, this.draft);
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
        const d = this.draft;
        this.exitCycling();
        this.setInputValue(input, d);
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

    fixIOSViewport(input);
  }
}

const instance = new PromptInputController();

/** Called by send-state.ts whenever inputs change. */
export function setSendState(next: SendState): void {
  instance.setSendState(next);
}

export function initPromptInput(onSubmit: Submit, onCancel: Cancel): void {
  instance.init(onSubmit, onCancel);
}
