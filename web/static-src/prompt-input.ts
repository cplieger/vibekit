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
import { ICON_SEND, ICON_SPINNER, ICON_HOURGLASS, ICON_ALERT } from "./icons.js";
import { collapseAll } from "./pill-expand.js";

type Submit = (text: string) => void;
type Cancel = () => void;

export type SendState =
  | { kind: "idle" }
  | { kind: "busy" }
  | { kind: "queued" }
  | { kind: "blocked"; reason: string };

type SendKind = SendState["kind"];

const STATE_ICON: Record<SendKind, string> = {
  idle: ICON_SEND,
  busy: ICON_SPINNER,
  queued: ICON_HOURGLASS,
  blocked: ICON_ALERT,
};

const DEFAULT_TOOLTIP: Record<SendKind, string> = {
  idle: "Send",
  busy: "Agent is thinking…",
  queued: "Queued — will send when the current turn finishes",
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

  private exitCycling(): void { this.idx = -1; this.draft = ""; }

  private userPrompts(): string[] {
    const s = getActive();
    if (s === undefined) return [];
    const out: string[] = [];
    for (let i = s.messages.length - 1; i >= 0; i--) {
      const m = s.messages[i]!;
      if (m.role === "user" && (m.content ?? "") !== "") out.push(m.content!);
    }
    return out;
  }

  private cursorOnFirstLine(el: HTMLTextAreaElement): boolean {
    const pos = el.selectionStart;
    if (pos !== el.selectionEnd) return false;
    return el.value.slice(0, pos).indexOf("\n") === -1;
  }

  private cursorOnLastLine(el: HTMLTextAreaElement): boolean {
    const pos = el.selectionStart;
    if (pos !== el.selectionEnd) return false;
    return el.value.slice(pos).indexOf("\n") === -1;
  }

  private setInputValue(el: HTMLTextAreaElement, v: string): void {
    el.value = v;
    el.setSelectionRange(v.length, v.length);
  }

  private applyButtonState(): void {
    const k = this.state.kind;
    $.sendBtn.innerHTML = STATE_ICON[k];
    const tooltip = this.state.kind === "blocked" ? this.state.reason : DEFAULT_TOOLTIP[k];
    $.sendBtn.setAttribute("data-tooltip", tooltip);
    $.sendBtn.classList.toggle("busy", k === "busy");
    $.sendBtn.classList.toggle("queued", k === "queued");
    $.sendBtn.classList.toggle("blocked", k === "blocked");

    const disableForm = k === "queued" || k === "blocked" || k === "busy";
    $.sendBtn.disabled = disableForm;
    $.promptInput.disabled = k === "queued" || k === "blocked";

    const cancelHalf = document.getElementById("cancel-half");
    const divider = document.querySelector(".send-divider");
    const wrap = document.getElementById("send-wrap");
    if (cancelHalf !== null) cancelHalf.classList.toggle("hidden", k !== "busy");
    if (divider !== null) divider.classList.toggle("hidden", k !== "busy");
    if (wrap !== null) wrap.classList.toggle("send-wrap-busy", k === "busy");
  }

  setSendState(next: SendState): void {
    if (this.state.kind === next.kind &&
        (this.state.kind !== "blocked" || (next as { kind: "blocked"; reason: string }).reason === this.state.reason)) return;
    this.state = next;
    this.applyButtonState();
  }

  init(onSubmit: Submit, onCancel: Cancel): void {
    if (initialized) return;
    initialized = true;

    const form = $.promptForm;
    const input = $.promptInput;

    const cancelHalf = document.getElementById("cancel-half");
    cancelHalf?.addEventListener("click", (e: MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (this.state.kind === "busy") onCancel();
    });

    this.applyButtonState();

    form.addEventListener("submit", (e: Event) => {
      e.preventDefault();
      if (this.state.kind === "queued" || this.state.kind === "blocked" || this.state.kind === "busy") return;
      const text = input.value.trim();
      if (text === "") return;
      this.exitCycling();
      onSubmit(text);
      input.value = "";
    });

    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (getActiveId() !== this.lastActiveID) {
        this.lastActiveID = getActiveId();
        this.exitCycling();
      }

      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        form.dispatchEvent(new Event("submit"));
        return;
      }

      if (e.key === "ArrowUp" && this.cursorOnFirstLine(input)) {
        const prompts = this.userPrompts();
        if (prompts.length === 0) return;
        if (this.idx === -1) this.draft = input.value;
        const next = Math.min(this.idx + 1, prompts.length - 1);
        if (next === this.idx) return;
        this.idx = next;
        e.preventDefault();
        this.setInputValue(input, prompts[this.idx]!);
        return;
      }

      if (e.key === "ArrowDown" && this.cursorOnLastLine(input) && this.idx !== -1) {
        e.preventDefault();
        if (this.idx === 0) { this.exitCycling(); this.setInputValue(input, this.draft); return; }
        this.idx -= 1;
        const prompts = this.userPrompts();
        this.setInputValue(input, prompts[this.idx] ?? "");
        return;
      }

      if (e.key === "Escape" && this.idx !== -1) {
        e.preventDefault();
        const d = this.draft;
        this.exitCycling();
        this.setInputValue(input, d);
        return;
      }

      if (this.idx !== -1 && !e.metaKey && !e.ctrlKey && !e.altKey) this.exitCycling();
    });

    input.addEventListener("input", () => { if (this.idx !== -1) this.exitCycling(); });
    input.addEventListener("focus", () => { collapseAll(); });

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
