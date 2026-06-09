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

import { getActive, activeVersion } from "./store.js";
import { effect, el } from "@cplieger/reactive";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { openPendingDiff } from "./editor-openers.js";
import {
  setSupervised,
  resolveAllPending,
  resolvePendingChange,
  trustPending,
  clearPendingTrust,
} from "./actions/chat.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import type { PendingChange } from "./types.js";

class SupervisedPillController {
  private wired = false;
  private pill: HTMLElement | null = null;
  private content: HTMLDivElement | null = null;
  private unbinds: (() => void)[] = [];

  /** Initialise the pill. Must run after DOMContentLoaded so the #supervised-pill
   *  element exists. No-op on a second call. */
  init(): void {
    if (this.wired) {
      return;
    }
    this.wired = true;

    this.pill = document.getElementById("supervised-pill");
    if (this.pill === null) {
      return;
    }

    this.content = this.pill.querySelector(".pill-expand-content");
    if (this.content === null) {
      return;
    }

    makeExpandable(this.pill, this.content, {
      onExpand: () => {
        this.pill?.setAttribute("aria-expanded", "true");
      },
      onCollapse: () => {
        this.pill?.setAttribute("aria-expanded", "false");
      },
    });
    this.pill.setAttribute("aria-expanded", "false");
    this.render();

    effect(() => {
      // eslint-disable-next-line @typescript-eslint/no-unused-expressions
      activeVersion.value;
      this.render();
    });
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
    if (this.pill === null || this.content === null) {
      return;
    }
    for (const u of this.unbinds) {
      u();
    }
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
    if (labelEl !== null) {
      labelEl.textContent = label;
    }

    this.pill.classList.toggle("pill-active", supervised);
    this.pill.classList.toggle("has-pending", pendingCount > 0);
    this.pill.classList.toggle("is-trusted", trusted);
    this.pill.setAttribute("aria-pressed", String(supervised));
    this.pill.title = trusted
      ? "Trusted for this turn — agent writes bypass staging until the turn ends"
      : supervised
        ? pendingCount > 0
          ? `Supervised mode on · ${String(pendingCount)} pending change${pendingCount === 1 ? "" : "s"}`
          : "Supervised mode on (file changes will be staged)"
        : "Supervised mode off (file changes apply immediately)";

    this.content.replaceChildren(this.buildPopoverBody(supervised, pending, trusted));

    // Bind loading state to bulk-action buttons.
    // Trade-off: all accept/reject buttons share the same action name, so
    // bindLoadingState disables ALL of them when any single resolve is in
    // flight. This is acceptable because rapid-fire resolves would race on
    // the server anyway; the brief bulk-disable prevents double-submits.
    const acceptAllBtn = this.content.querySelector<HTMLButtonElement>(
      '[data-action="accept-all"]',
    );
    const rejectAllBtn = this.content.querySelector<HTMLButtonElement>(
      '[data-action="reject-all"]',
    );
    const trustBtn = this.content.querySelector<HTMLButtonElement>(
      '[data-action="trust-remaining"]',
    );
    const stopBtn = this.content.querySelector<HTMLButtonElement>('[data-action="stop-trusting"]');
    if (acceptAllBtn) {
      this.unbinds.push(
        bindLoadingState(["chat.resolve_all_pending", "chat.resolve_pending_change"], acceptAllBtn),
      );
    }
    if (rejectAllBtn) {
      this.unbinds.push(
        bindLoadingState(["chat.resolve_all_pending", "chat.resolve_pending_change"], rejectAllBtn),
      );
    }
    if (trustBtn) {
      this.unbinds.push(bindLoadingState("chat.trust_pending", trustBtn));
    }
    if (stopBtn) {
      this.unbinds.push(bindLoadingState("chat.clear_pending_trust", stopBtn));
    }
  }

  private buildPopoverBody(
    supervised: boolean,
    pending: PendingChange[],
    trusted: boolean,
  ): DocumentFragment {
    const frag = document.createDocumentFragment();

    // Header: title + toggle switch.
    const toggle = el("input", {
      type: "checkbox",
      id: "supervised-toggle",
      checked: supervised,
    }) as HTMLInputElement;
    frag.appendChild(
      el(
        "div",
        { className: "supervised-header" },
        el("div", { className: "supervised-title" }, "Supervised mode"),
        el(
          "label",
          { className: "supervised-toggle-label" },
          toggle,
          el("span", { className: "supervised-toggle-text" }, supervised ? "On" : "Off"),
        ),
      ),
    );

    toggle.addEventListener("change", () => {
      const enabled = toggle.checked;
      void setSupervised.dispatch({ chatID: this.currentChatID(), enabled }, { silent: true });
    });
    this.unbinds.push(bindLoadingState("chat.set_supervised", toggle));

    // Trusted-this-turn short-circuit.
    if (trusted) {
      frag.appendChild(
        el(
          "p",
          { className: "supervised-hint" },
          "Trusted for this turn. Changes apply immediately until the turn ends. The agent's next turn starts staging again.",
        ),
      );

      const stopBtn = el(
        "button",
        { type: "button", className: "pill-button", "data-action": "stop-trusting" },
        "Stop trusting",
      );
      stopBtn.addEventListener("click", () => {
        this.stopTrusting();
      });
      frag.appendChild(el("div", { className: "supervised-bulk-actions" }, stopBtn));
      return frag;
    }

    // Explanation paragraph.
    frag.appendChild(
      el(
        "p",
        { className: "supervised-hint" },
        supervised
          ? "Every file change the agent makes will pause here for you to review before it hits disk."
          : "File changes apply immediately. Turn on to review every change before it's saved.",
      ),
    );

    if (pending.length === 0) {
      return frag;
    }

    const list = el("ul", { className: "supervised-pending-list", role: "list" });
    for (const change of pending) {
      list.appendChild(this.buildRow(change));
    }
    frag.appendChild(list);

    // Bulk actions.
    const rejectAllBtn = el(
      "button",
      { className: "pill-button", "data-action": "reject-all" },
      "Reject all",
    );
    rejectAllBtn.addEventListener("click", () => {
      this.bulkResolve("reject");
    });

    const trustBtn = el(
      "button",
      { className: "pill-button", "data-action": "trust-remaining" },
      "Trust remaining",
    );
    trustBtn.addEventListener("click", () => {
      this.trustRemaining();
    });

    const acceptAllBtn = el(
      "button",
      { className: "pill-button primary", "data-action": "accept-all" },
      "Accept all",
    );
    acceptAllBtn.addEventListener("click", () => {
      this.bulkResolve("accept");
    });

    frag.appendChild(
      el("div", { className: "supervised-bulk-actions" }, rejectAllBtn, trustBtn, acceptAllBtn),
    );

    return frag;
  }

  private buildRow(change: PendingChange): HTMLLIElement {
    const basename = (() => {
      const parts = change.path.split(/[/\\]/);
      return parts[parts.length - 1] ?? change.path;
    })();
    const kindGlyph = change.kind === "create" ? "+" : change.kind === "delete" ? "−" : "✎";

    // Action buttons (named for addEventListener + loading-state binding).
    const diffBtn = el("button", { className: "pill-button", "data-action": "diff" }, "Diff");
    diffBtn.addEventListener("click", () => {
      openPendingDiff(this.currentChatID(), change.tool_call_id);
      collapseAll();
    });

    const rejectBtn = el(
      "button",
      { className: "pill-button", "data-action": "reject" },
      "Reject",
    ) as HTMLButtonElement;
    rejectBtn.addEventListener("click", () => {
      this.resolveOne(change.tool_call_id, "reject");
    });

    const acceptBtn = el(
      "button",
      { className: "pill-button primary", "data-action": "accept" },
      "Accept",
    ) as HTMLButtonElement;
    acceptBtn.addEventListener("click", () => {
      this.resolveOne(change.tool_call_id, "accept");
    });

    this.unbinds.push(
      bindLoadingState(["chat.resolve_pending_change", "chat.resolve_all_pending"], acceptBtn),
    );
    this.unbinds.push(
      bindLoadingState(["chat.resolve_pending_change", "chat.resolve_all_pending"], rejectBtn),
    );

    return el(
      "li",
      {
        className: "supervised-pending-row",
        "data-tool-call-id": change.tool_call_id,
        "data-kind": change.kind,
        "data-path": change.path,
      },
      el("span", { className: "supervised-row-glyph" }, kindGlyph),
      el("span", { className: "supervised-row-file", title: change.path }, basename),
      el("span", { className: "supervised-row-actions" }, diffBtn, rejectBtn, acceptBtn),
    ) as HTMLLIElement;
  }

  private resolveOne(toolCallID: string, action: "accept" | "reject"): void {
    void resolvePendingChange.dispatch({ chatID: this.currentChatID(), toolCallID, action });
  }

  private bulkResolve(action: "accept" | "reject"): void {
    void resolveAllPending.dispatch({ chatID: this.currentChatID(), action });
  }

  /** Post trust_pending_changes. The server sets perTurnTrust,
   *  accepts every staged op immediately, and broadcasts
   *  pending_trust_enabled so the pill flips. */
  private trustRemaining(): void {
    void trustPending.dispatch(this.currentChatID());
  }

  /** Post clear_pending_trust. Mirror of trustRemaining. */
  private stopTrusting(): void {
    void clearPendingTrust.dispatch(this.currentChatID());
  }

  private currentChatID(): string {
    return getActive()?.id ?? "";
  }

  dispose(): void {
    for (const u of this.unbinds) {
      u();
    }
    this.unbinds = [];
  }
}

const controller = new SupervisedPillController();
registerCleanup(() => {
  controller.dispose();
});

/** Initialise the pill. Must run after DOMContentLoaded so the #supervised-pill
 *  element exists. No-op on a second call. */
export function initSupervisedPill(): void {
  controller.init();
}
