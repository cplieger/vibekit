// ---------------------------------------------------------------------------
// Edit-specific tool card actions: undo, diff, conflict badges, pending
// accept/reject. Extracted from messages.ts for single-reason-to-change.
// ---------------------------------------------------------------------------

import { ICON_UNDO, ICON_DIFF, ICON_CHECK, ICON_X, iconEl } from "./icons.js";
import { getActiveId } from "./store.js";
import { openFileGitDiff, openPendingDiff } from "./editor-openers.js";
import * as transport from "./transport.js";
import { onBus, BUS_PENDING_ADDED, BUS_PENDING_RESOLVED, BUS_PENDING_CLEARED } from "./bus.js";
import { resolvePendingChange } from "./chat-commands.js";

/** Add "Undo" and "Diff" action buttons to a completed edit tool card. */
export function addEditActions(el: HTMLDivElement): void {
  if (el.querySelector(".tool-edit-actions") !== null) return;
  const filePath = el.dataset["filename"] ?? "";
  if (filePath === "") return;

  const row = document.createElement("div");
  row.className = "tool-edit-actions";

  const undoBtn = document.createElement("button");
  undoBtn.type = "button";
  undoBtn.className = "turn-action-btn";
  undoBtn.replaceChildren(iconEl(ICON_UNDO));
  undoBtn.setAttribute("data-tooltip", "Undo this edit");
  undoBtn.setAttribute("aria-label", "Undo this edit");
  undoBtn.addEventListener("click", () => {
    const chatID = getActiveId();
    if (chatID === "") return;
    let tag = "";
    let sibling: Element | null = el.previousElementSibling;
    while (sibling !== null) {
      const btn = sibling.querySelector(".checkpoint-restore") as HTMLButtonElement | null;
      if (btn !== null) {
        tag = btn.dataset["tag"] ?? "";
        break;
      }
      sibling = sibling.previousElementSibling;
    }
    if (tag === "") return;
    undoBtn.disabled = true;
    void transport.send({
      type: "undo_edit",
      chat_id: chatID,
      payload: { tag, file_path: filePath },
    }).then((r) => {
      if (r.ok) {
        undoBtn.classList.add("copied");
        setTimeout(() => undoBtn.classList.remove("copied"), 1500);
      }
      undoBtn.disabled = false;
    });
  });

  const diffBtn = document.createElement("button");
  diffBtn.type = "button";
  diffBtn.className = "turn-action-btn";
  diffBtn.replaceChildren(iconEl(ICON_DIFF));
  diffBtn.setAttribute("data-tooltip", "View diff");
  diffBtn.setAttribute("aria-label", "View diff");
  diffBtn.addEventListener("click", () => {
    openFileGitDiff(filePath);
  });

  row.append(undoBtn, diffBtn);
  el.appendChild(row);
  void import("./conflicts.js").then((m) => {
    const chatID = getActiveId();
    if (chatID !== "") m.renderConflictChip(row, chatID, filePath);
  });
}

/** Re-decorate any tool-edit action rows pointing at `path` with a
 *  freshly-landed conflict chip. */
export { refreshConflictBadges } from "./messages-shared.js";

/** Add Accept / Reject / Diff buttons to a tool card whose write is
 *  staged under Supervised mode. */
function addPendingActions(el: HTMLDivElement, toolCallID: string, chatID: string, path: string): void {
  const status = el.querySelector(".tool-status");
  if (status !== null) {
    status.textContent = "awaiting_approval";
    status.className = "tool-status awaiting_approval";
  }
  el.querySelector(".tool-spinner")?.remove();

  const existing = el.querySelector<HTMLDivElement>(".tool-pending-actions");
  if (existing !== null) {
    existing.dataset["toolCallId"] = toolCallID;
    return;
  }

  const row = document.createElement("div");
  row.className = "tool-pending-actions";
  row.dataset["toolCallId"] = toolCallID;

  const diffBtn = document.createElement("button");
  diffBtn.type = "button";
  diffBtn.className = "turn-action-btn";
  diffBtn.replaceChildren(iconEl(ICON_DIFF));
  diffBtn.setAttribute("data-tooltip", "View diff");
  diffBtn.setAttribute("aria-label", "View diff");
  diffBtn.addEventListener("click", () => { openPendingDiff(chatID, toolCallID); });

  const rejectBtn = document.createElement("button");
  rejectBtn.type = "button";
  rejectBtn.className = "turn-action-btn";
  rejectBtn.replaceChildren(iconEl(ICON_X));
  rejectBtn.setAttribute("data-tooltip", "Reject");
  rejectBtn.setAttribute("aria-label", "Reject change");
  rejectBtn.addEventListener("click", () => { resolveOne(chatID, toolCallID, "reject"); });

  const acceptBtn = document.createElement("button");
  acceptBtn.type = "button";
  acceptBtn.className = "turn-action-btn primary";
  acceptBtn.replaceChildren(iconEl(ICON_CHECK));
  acceptBtn.setAttribute("data-tooltip", "Accept");
  acceptBtn.setAttribute("aria-label", "Accept change");
  acceptBtn.addEventListener("click", () => { resolveOne(chatID, toolCallID, "accept"); });

  row.append(diffBtn, rejectBtn, acceptBtn);
  row.dataset["path"] = path;
  el.appendChild(row);
}

function resolveOne(chatID: string, toolCallID: string, action: "accept" | "reject"): void {
  resolvePendingChange(chatID, toolCallID, action);
}

/** Locate the tool card whose data-file-path matches `path`. */
function findToolCardForPath(path: string): HTMLDivElement | null {
  const cards = document.querySelectorAll<HTMLDivElement>(".tool-call[data-file-path]");
  for (let i = cards.length - 1; i >= 0; i--) {
    const card = cards[i];
    if (card === undefined) continue;
    if (card.dataset["filePath"] === path) return card;
  }
  return null;
}

/** Strip the pending-action row and restore the card's status pill. */
function removePendingActions(el: HTMLDivElement, finalStatus: "completed" | "failed"): void {
  el.querySelector(".tool-pending-actions")?.remove();
  const status = el.querySelector(".tool-status");
  if (status !== null) {
    status.textContent = finalStatus;
    status.className = `tool-status ${finalStatus}`;
  }
}

function cssEscape(s: string): string {
  return s.replace(/"/g, '\\"');
}

// --- Bus subscriptions ---

export function initMessageActions(): void {
  onBus(BUS_PENDING_ADDED, (payload) => {
    const card = findToolCardForPath(payload.change.path);
    if (card === null) return;
    addPendingActions(card, payload.change.tool_call_id, payload.chatID, payload.change.path);
  });

  onBus(BUS_PENDING_RESOLVED, (payload) => {
    const row = document.querySelector<HTMLDivElement>(
      `.tool-pending-actions[data-tool-call-id="${cssEscape(payload.toolCallID)}"]`,
    );
    if (row === null) return;
    const card = row.closest<HTMLDivElement>(".tool-call");
    if (card === null) return;
    removePendingActions(card, payload.action === "accept" ? "completed" : "failed");
  });

  onBus(BUS_PENDING_CLEARED, () => {
    for (const row of document.querySelectorAll<HTMLDivElement>(".tool-pending-actions")) {
      const card = row.closest<HTMLDivElement>(".tool-call");
      if (card !== null) removePendingActions(card, "failed");
    }
  });
}
