// ---------------------------------------------------------------------------
// Edit-specific tool card actions: undo, diff, conflict badges, pending
// accept/reject. Extracted from messages.ts for single-reason-to-change.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { ICON_UNDO, ICON_DIFF, ICON_CHECK, ICON_X } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { getActiveId } from "./store.js";
import { openFileGitDiff, openPendingDiff } from "./editor-openers.js";
import { onBus, BUS_PENDING_ADDED, BUS_PENDING_RESOLVED, BUS_PENDING_CLEARED } from "./bus.js";
import { undoEdit } from "./actions/messages.js";
import { resolvePendingChange } from "./actions/chat.js";
import { bindLoadingState } from "./actions/index.js";

/** Accumulated bindLoadingState unsubscribers — cleared on chat switch. */
const actionBindUnbinds: (() => void)[] = [];

/** Unsubscribe all bindLoadingState listeners owned by this module. */
export function clearActionBindings(): void {
  for (const fn of actionBindUnbinds) {
    fn();
  }
  actionBindUnbinds.length = 0;
}

/** Add "Undo" and "Diff" action buttons to a completed edit tool card. */
export function addEditActions(card: HTMLDivElement): void {
  if (card.querySelector(".tool-edit-actions") !== null) {
    return;
  }
  const filePath = card.dataset["filePath"] ?? "";
  if (filePath === "") {
    return;
  }

  const row = el("div", { className: "tool-edit-actions" });

  const undoBtn = el(
    "button",
    {
      type: "button",
      className: "turn-action-btn",
      "data-tooltip": "Undo this edit",
      "aria-label": "Undo this edit",
    },
    iconEl(ICON_UNDO),
  ) as HTMLButtonElement;
  actionBindUnbinds.push(bindLoadingState("messages.undo_edit", undoBtn));
  undoBtn.addEventListener("click", () => {
    const chatID = getActiveId();
    if (chatID === "") {
      return;
    }
    let tag = "";
    const group = card.closest(".tool-group");
    let sibling: Element | null = (group ?? card).previousElementSibling;
    while (sibling !== null) {
      const btn = sibling.querySelector<HTMLElement>(".checkpoint-restore");
      if (btn !== null) {
        tag = btn.dataset["tag"] ?? "";
        break;
      }
      sibling = sibling.previousElementSibling;
    }
    if (tag === "") {
      return;
    }
    void undoEdit.dispatch(
      { chatID, tag, filePath },
      {
        onSuccess: () => {
          undoBtn.classList.add("copied");
          setTimeout(() => {
            undoBtn.classList.remove("copied");
          }, 1500);
        },
      },
    );
  });

  const diffBtn = el(
    "button",
    {
      type: "button",
      className: "turn-action-btn",
      "data-tooltip": "View diff",
      "aria-label": "View diff",
    },
    iconEl(ICON_DIFF),
  );
  diffBtn.addEventListener("click", () => {
    openFileGitDiff(filePath);
  });

  row.append(undoBtn, diffBtn);
  card.appendChild(row);
  void import("./conflicts.js")
    .then((m) => {
      const chatID = getActiveId();
      if (chatID !== "") {
        m.renderConflictChip(row, chatID, filePath);
      }
    })
    .catch(() => {
      /* noop */
    });
}

/** Add Accept / Reject / Diff buttons to a tool card whose write is
 *  staged under Supervised mode. */
function addPendingActions(
  card: HTMLDivElement,
  toolCallID: string,
  chatID: string,
  path: string,
): void {
  const status = card.querySelector(".tool-status");
  if (status !== null) {
    status.textContent = "awaiting_approval";
    status.className = "tool-status awaiting_approval";
  }
  card.querySelector(".tool-spinner")?.remove();

  const existing = card.querySelector<HTMLDivElement>(".tool-pending-actions");
  if (existing !== null) {
    existing.dataset["toolCallId"] = toolCallID;
    return;
  }

  const row = el("div", {
    className: "tool-pending-actions",
    "data-tool-call-id": toolCallID,
    "data-path": path,
  });

  const diffBtn = el(
    "button",
    {
      type: "button",
      className: "turn-action-btn",
      "data-tooltip": "View diff",
      "aria-label": "View diff",
    },
    iconEl(ICON_DIFF),
  );
  diffBtn.addEventListener("click", () => {
    openPendingDiff(chatID, row.dataset["toolCallId"] ?? toolCallID);
  });

  const rejectBtn = el(
    "button",
    {
      type: "button",
      className: "turn-action-btn",
      "data-tooltip": "Reject",
      "aria-label": "Reject change",
    },
    iconEl(ICON_X),
  ) as HTMLButtonElement;
  rejectBtn.addEventListener("click", () => {
    resolveOne(chatID, row.dataset["toolCallId"] ?? toolCallID, "reject");
  });

  const acceptBtn = el(
    "button",
    {
      type: "button",
      className: "turn-action-btn primary",
      "data-tooltip": "Accept",
      "aria-label": "Accept change",
    },
    iconEl(ICON_CHECK),
  ) as HTMLButtonElement;
  acceptBtn.addEventListener("click", () => {
    resolveOne(chatID, row.dataset["toolCallId"] ?? toolCallID, "accept");
  });

  actionBindUnbinds.push(
    bindLoadingState(["chat.resolve_pending_change", "chat.resolve_all_pending"], acceptBtn),
  );
  actionBindUnbinds.push(
    bindLoadingState(["chat.resolve_pending_change", "chat.resolve_all_pending"], rejectBtn),
  );

  row.append(diffBtn, rejectBtn, acceptBtn);
  card.appendChild(row);
}

function resolveOne(chatID: string, toolCallID: string, action: "accept" | "reject"): void {
  void resolvePendingChange.dispatch({ chatID, toolCallID, action });
}

/** Locate the tool card whose data-file-path matches `path`. */
function findToolCardForPath(path: string): HTMLDivElement | null {
  const cards = document.querySelectorAll<HTMLDivElement>(".tool-call[data-file-path]");
  for (let i = cards.length - 1; i >= 0; i--) {
    const card = cards[i];
    if (card === undefined) {
      continue;
    }
    if (card.dataset["filePath"] === path) {
      return card;
    }
  }
  return null;
}

/** Strip the pending-action row and restore the card's status pill. */
function removePendingActions(card: HTMLDivElement, finalStatus: "completed" | "failed"): void {
  card.querySelector(".tool-pending-actions")?.remove();
  const status = card.querySelector(".tool-status");
  if (status !== null) {
    status.textContent = finalStatus;
    status.className = `tool-status ${finalStatus}`;
  }
}

function findPendingRow(toolCallID: string): HTMLDivElement | null {
  return document.querySelector<HTMLDivElement>(
    `.tool-pending-actions[data-tool-call-id="${CSS.escape(toolCallID)}"]`,
  );
}

// --- Bus subscriptions ---

export function initMessageActions(): void {
  onBus(BUS_PENDING_ADDED, (payload) => {
    if (payload.chatID !== getActiveId()) {
      return;
    }
    const card = findToolCardForPath(payload.change.path);
    if (card === null) {
      return;
    }
    addPendingActions(card, payload.change.tool_call_id, payload.chatID, payload.change.path);
  });

  onBus(BUS_PENDING_RESOLVED, (payload) => {
    const row = findPendingRow(payload.toolCallID);
    if (row === null) {
      return;
    }
    const card = row.closest<HTMLDivElement>(".tool-call");
    if (card === null) {
      return;
    }
    removePendingActions(card, payload.action === "accept" ? "completed" : "failed");
  });

  onBus(BUS_PENDING_CLEARED, (payload) => {
    if (payload.chatID !== getActiveId()) {
      return;
    }
    for (const row of document.querySelectorAll<HTMLDivElement>(".tool-pending-actions")) {
      const card = row.closest<HTMLDivElement>(".tool-call");
      if (card !== null) {
        removePendingActions(card, "failed");
      }
    }
  });
}
