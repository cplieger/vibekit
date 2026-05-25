// ---------------------------------------------------------------------------
// Supervised mode pill: lives in the prompt-row pill cluster, next to
// the autopilot/trust UI in Settings. Two functions:
//
//   1. Shows a toggle affordance for Supervised mode. Clicking the pill
//      opens a popover with an explanation + an on/off toggle. This is
//      the only chat-level enable path; Settings → Permissions has a
//      global-default toggle that applies to new chats.
//
//   2. When pending ops exist, the pill becomes a "N pending" badge
//      with a popover listing every staged file and per-row Accept /
//      Reject / View diff actions, plus bulk Accept All / Reject All.
//
// Reuse: every interactive surface comes from existing primitives —
// pill-expand.ts for the popover, file-label for file chips, tool-card
// diff-summary span for +N -M, action framework (actions/chat.ts) for
// the commands, the store for state. No new pill-chrome CSS; we
// extend the existing .pill-expandable / .pill-expanded rules.
// ---------------------------------------------------------------------------

import { getActive, version } from "./store.js";
import { effect } from "./signals.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { openPendingDiff } from "./editor-openers.js";
import { setSupervisedAction, resolveAllPendingAction, resolvePendingChangeAction, trustPendingAction, clearPendingTrustAction } from "./actions/chat.js";
import { bindLoadingState } from "./actions/index.js";
import type { PendingChange } from "./types.js";

class SupervisedPillController {
  private wired = false;
  private pill: HTMLElement | null = null;
  private content: HTMLDivElement | null = null;
  private unbinds: Array<() => void> = [];

  /** Initialise the pill. Must run after DOMContentLoaded so the #supervised-pill
   *  element exists. No-op on a second call. */
  init(): void {
    if (this.wired) return;
    this.wired = true;

    this.pill = document.getElementById("supervised-pill");
    if (this.pill === null) return;

    this.content = this.pill.querySelector(".pill-expand-content");
    if (this.content === null) return;

    makeExpandable(this.pill, this.content);
    this.render();

    effect(() => { version.value; this.render(); });
  }

  /** Render the pill in three states:
   *
   *   - hidden  : no active chat
   *   - idle    : supervised_mode toggle only (no pending ops)
   *   - pending : "N pending" badge + detail list + bulk actions
   *
   *  Called on every store change. O(pending_changes) per render, which
   *  stays tiny for realistic turns. */
  private render(): void {
    if (this.pill === null || this.content === null) return;
    for (const u of this.unbinds) u();
    this.unbinds = [];
    const s = getActive();
    if (s === undefined) {
      this.pill.classList.add("hidden");
      return;
    }
    this.pill.classList.remove("hidden");

    const supervised = s.supervised_mode === true;
    const pending = s.pending_changes;
    const pendingCount = pending.length;
    const trusted = s.trusted_this_turn === true;

    const label = trusted
      ? "Trusted · this turn"
      : pendingCount > 0
        ? `Supervised · ${pendingCount}`
        : "Supervised";
    const labelEl = this.pill.querySelector(".pill-label");
    if (labelEl !== null) labelEl.textContent = label;

    this.pill.classList.toggle("pill-active", supervised);
    this.pill.classList.toggle("has-pending", pendingCount > 0);
    this.pill.classList.toggle("is-trusted", trusted);
    this.pill.setAttribute("aria-pressed", String(supervised));
    this.pill.title = trusted
      ? "Trusted for this turn — agent writes bypass staging until the turn ends"
      : supervised
        ? (pendingCount > 0
          ? `Supervised mode on · ${String(pendingCount)} pending change${pendingCount === 1 ? "" : "s"}`
          : "Supervised mode on (file changes will be staged)")
        : "Supervised mode off (file changes apply immediately)";

    this.content.replaceChildren(this.buildPopoverBody(supervised, pending, trusted));

    // Bind loading state to bulk-action buttons.
    // Trade-off: all accept/reject buttons share the same action name, so
    // bindLoadingState disables ALL of them when any single resolve is in
    // flight. This is acceptable because rapid-fire resolves would race on
    // the server anyway; the brief bulk-disable prevents double-submits.
    const acceptAllBtn = this.content.querySelector<HTMLButtonElement>('[data-action="accept-all"]');
    const rejectAllBtn = this.content.querySelector<HTMLButtonElement>('[data-action="reject-all"]');
    const trustBtn = this.content.querySelector<HTMLButtonElement>('[data-action="trust-remaining"]');
    const stopBtn = this.content.querySelector<HTMLButtonElement>('[data-action="stop-trusting"]');
    if (acceptAllBtn) this.unbinds.push(bindLoadingState("chat.resolve_all_pending", acceptAllBtn));
    if (rejectAllBtn) this.unbinds.push(bindLoadingState("chat.resolve_all_pending", rejectAllBtn));
    if (trustBtn) this.unbinds.push(bindLoadingState("chat.trust_pending", trustBtn));
    if (stopBtn) this.unbinds.push(bindLoadingState("chat.clear_pending_trust", stopBtn));
  }

  private buildPopoverBody(supervised: boolean, pending: PendingChange[], trusted: boolean): DocumentFragment {
    const frag = document.createDocumentFragment();

    // Header: title + toggle switch.
    const header = document.createElement("div");
    header.className = "supervised-header";

    const title = document.createElement("div");
    title.className = "supervised-title";
    title.textContent = "Supervised mode";
    header.appendChild(title);

    const toggleLabel = document.createElement("label");
    toggleLabel.className = "supervised-toggle-label";

    const toggle = document.createElement("input");
    toggle.type = "checkbox";
    toggle.id = "supervised-toggle";
    toggle.checked = supervised;
    toggleLabel.appendChild(toggle);

    const toggleText = document.createElement("span");
    toggleText.className = "supervised-toggle-text";
    toggleText.textContent = supervised ? "On" : "Off";
    toggleLabel.appendChild(toggleText);

    header.appendChild(toggleLabel);
    frag.appendChild(header);

    toggle.addEventListener("change", () => {
      const enabled = toggle.checked;
      void setSupervisedAction.dispatch({ chatID: this.currentChatID(), enabled });
    });

    // Trusted-this-turn short-circuit.
    if (trusted) {
      const explainer = document.createElement("p");
      explainer.className = "supervised-hint";
      explainer.textContent
        = "Trusted for this turn. Changes apply immediately until the turn ends. The agent's next turn starts staging again.";
      frag.appendChild(explainer);

      const actions = document.createElement("div");
      actions.className = "supervised-bulk-actions";
      const stopBtn = document.createElement("button");
      stopBtn.type = "button";
      stopBtn.className = "pill-button";
      stopBtn.dataset["action"] = "stop-trusting";
      stopBtn.textContent = "Stop trusting";
      stopBtn.addEventListener("click", () => this.stopTrusting());
      actions.appendChild(stopBtn);
      frag.appendChild(actions);
      return frag;
    }

    // Explanation paragraph.
    const hint = document.createElement("p");
    hint.className = "supervised-hint";
    hint.textContent = supervised
      ? "Every file change the agent makes will pause here for you to review before it hits disk."
      : "File changes apply immediately. Turn on to review every change before it's saved.";
    frag.appendChild(hint);

    if (pending.length === 0) {
      return frag;
    }

    const list = document.createElement("ul");
    list.className = "supervised-pending-list";
    list.setAttribute("role", "list");
    for (const change of pending) list.appendChild(this.buildRow(change));
    frag.appendChild(list);

    // Bulk actions.
    const actions = document.createElement("div");
    actions.className = "supervised-bulk-actions";

    const rejectAllBtn = document.createElement("button");
    rejectAllBtn.className = "pill-button";
    rejectAllBtn.dataset["action"] = "reject-all";
    rejectAllBtn.textContent = "Reject all";
    rejectAllBtn.addEventListener("click", () => this.bulkResolve("reject"));
    actions.appendChild(rejectAllBtn);

    const trustBtn = document.createElement("button");
    trustBtn.className = "pill-button";
    trustBtn.dataset["action"] = "trust-remaining";
    trustBtn.textContent = "Trust remaining";
    trustBtn.addEventListener("click", () => this.trustRemaining());
    actions.appendChild(trustBtn);

    const acceptAllBtn = document.createElement("button");
    acceptAllBtn.className = "pill-button primary";
    acceptAllBtn.dataset["action"] = "accept-all";
    acceptAllBtn.textContent = "Accept all";
    acceptAllBtn.addEventListener("click", () => this.bulkResolve("accept"));
    actions.appendChild(acceptAllBtn);

    frag.appendChild(actions);

    return frag;
  }

  private buildRow(change: PendingChange): HTMLLIElement {
    const li = document.createElement("li");
    li.className = "supervised-pending-row";
    li.dataset["toolCallId"] = change.tool_call_id;
    li.dataset["kind"] = change.kind;
    li.dataset["path"] = change.path;

    const basename = (() => {
      const parts = change.path.split(/[/\\]/);
      return parts[parts.length - 1] ?? change.path;
    })();
    const kindGlyph = change.kind === "create" ? "+" : change.kind === "delete" ? "−" : "✎";

    // Glyph
    const glyphSpan = document.createElement("span");
    glyphSpan.className = "supervised-row-glyph";
    glyphSpan.textContent = kindGlyph;
    li.appendChild(glyphSpan);

    // File label
    const fileSpan = document.createElement("span");
    fileSpan.className = "supervised-row-file";
    fileSpan.title = change.path;
    fileSpan.textContent = basename;
    li.appendChild(fileSpan);

    // Action buttons
    const actionsSpan = document.createElement("span");
    actionsSpan.className = "supervised-row-actions";

    const diffBtn = document.createElement("button");
    diffBtn.className = "pill-button";
    diffBtn.dataset["action"] = "diff";
    diffBtn.textContent = "Diff";
    diffBtn.addEventListener("click", () => {
      openPendingDiff(this.currentChatID(), change.tool_call_id);
      collapseAll();
    });
    actionsSpan.appendChild(diffBtn);

    const rejectBtn = document.createElement("button");
    rejectBtn.className = "pill-button";
    rejectBtn.dataset["action"] = "reject";
    rejectBtn.textContent = "Reject";
    rejectBtn.addEventListener("click", () => this.resolveOne(change.tool_call_id, "reject"));
    actionsSpan.appendChild(rejectBtn);

    const acceptBtn = document.createElement("button");
    acceptBtn.className = "pill-button primary";
    acceptBtn.dataset["action"] = "accept";
    acceptBtn.textContent = "Accept";
    acceptBtn.addEventListener("click", () => this.resolveOne(change.tool_call_id, "accept"));
    actionsSpan.appendChild(acceptBtn);

    this.unbinds.push(bindLoadingState("chat.resolve_pending_change", acceptBtn));
    this.unbinds.push(bindLoadingState("chat.resolve_pending_change", rejectBtn));

    li.appendChild(actionsSpan);
    return li;
  }

  private resolveOne(toolCallID: string, action: "accept" | "reject"): void {
    void resolvePendingChangeAction.dispatch({ chatID: this.currentChatID(), toolCallID, action });
  }

  private bulkResolve(action: "accept" | "reject"): void {
    void resolveAllPendingAction.dispatch({ chatID: this.currentChatID(), action });
  }

  /** Post trust_pending_changes. The server sets perTurnTrust,
   *  accepts every staged op immediately, and broadcasts
   *  pending_trust_enabled so the pill flips. */
  private trustRemaining(): void {
    void trustPendingAction.dispatch(this.currentChatID());
  }

  /** Post clear_pending_trust. Mirror of trustRemaining. */
  private stopTrusting(): void {
    void clearPendingTrustAction.dispatch(this.currentChatID());
  }

  private currentChatID(): string {
    return getActive()?.id ?? "";
  }
}

const controller = new SupervisedPillController();

/** Initialise the pill. Must run after DOMContentLoaded so the #supervised-pill
 *  element exists. No-op on a second call. */
export function initSupervisedPill(): void {
  controller.init();
}
