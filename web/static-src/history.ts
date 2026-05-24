// ---------------------------------------------------------------------------
// Chat history: sidebar popover with archived chats, plus a dedicated
// full-page History view opened from the chat toolbar (#history-btn).
// Restoring a chat spawns a new tab and loads its messages.
//
// Auto-archive on tab-close: beforeunload posts archive for every chat
// the user has open so they're not lost on page refresh. Server-side
// retention (cleanup.periodDays) prunes the archive after N days.
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { restoreArchivedChat } from "./chat.js";
import { toggleHistoryView } from "./tabs.js";
import { escText } from "./strings.js";
import { ICON_TRASH } from "./icons.js";
import { deleteArchivedChatAction, loadHistoryAction } from "./actions/chat.js";

interface ArchivedHeader {
  id: string;
  name: string;
  summary?: string;
  updated_at: number;
}

class HistoryController {
  private historyController: AbortController | null = null;
  private archivedController: AbortController | null = null;

  init(): void {
    const toggle = document.getElementById("history-toggle");
    const list = document.getElementById("history-list");
    if (toggle !== null && list !== null) {
      toggle.addEventListener("click", () => {
        const open = !list.classList.contains("hidden");
        list.classList.toggle("hidden", open);
        if (!open) void this.loadArchived();
      });
    }
  }

  showView(): void {
    toggleHistoryView(() => { void this.loadHistoryTable(); }, () => { this.teardown(); });
  }

  teardown(): void {
    loadHistoryAction.cancel();
    this.historyController?.abort();
    this.historyController = null;
    this.archivedController?.abort();
    this.archivedController = null;
  }

  async loadHistoryTable(): Promise<void> {
    const container = document.getElementById("history-table");
    if (container === null) return;
    container.replaceChildren();

    loadHistoryAction.cancel();
    this.historyController?.abort();
    this.historyController = new AbortController();
    const { signal } = this.historyController;

    const d = await loadHistoryAction.dispatch(undefined);
    if (signal.aborted) return;
    const chats = d?.chats ?? [];
    if (chats.length === 0) {
      const empty = document.createElement("div");
      empty.className = "list-empty";
      empty.textContent = "No archived chats.";
      container.appendChild(empty);
      return;
    }

    for (const chat of chats) {
      const row = document.createElement("div");
      row.className = "list-row history-table-row";
      row.setAttribute("data-chat-entry", "");
      row.setAttribute("data-chat-id", chat.id);

      const nameWrap = document.createElement("div");
      nameWrap.className = "list-row-title";
      nameWrap.setAttribute("data-action", "restore");
      const name = document.createElement("span");
      name.className = "list-row-name";
      name.textContent = chat.name;
      nameWrap.appendChild(name);
      if (chat.summary !== undefined && chat.summary !== "") {
        const sum = document.createElement("span");
        sum.className = "list-row-summary";
        sum.textContent = chat.summary;
        nameWrap.appendChild(sum);
      }
      nameWrap.style.cursor = "pointer";

      const date = document.createElement("span");
      date.className = "list-row-meta";
      date.textContent = new Date(chat.updated_at).toLocaleString();

      const delBtn = document.createElement("button");
      delBtn.type = "button";
      delBtn.className = "btn-small btn-danger icon-only";
      delBtn.setAttribute("data-tooltip", "Delete permanently");
      delBtn.setAttribute("aria-label", `Delete ${escText(chat.name)}`);
      delBtn.setAttribute("data-action", "delete");
      delBtn.innerHTML = ICON_TRASH;

      row.append(nameWrap, date, delBtn);
      container.appendChild(row);
    }

    container.addEventListener("click", (e) => {
      const target = (e.target as HTMLElement).closest<HTMLElement>("[data-action]");
      if (target === null) return;
      const row = target.closest<HTMLElement>("[data-chat-id]");
      if (row === null) return;
      const chatId = row.getAttribute("data-chat-id")!;
      const action = target.getAttribute("data-action");
      if (action === "restore") {
        restoreArchivedChat(chatId);
      } else if (action === "delete") {
        void deleteArchivedChatAction.dispatch(chatId).then((result) => {
          if (result === null) return; // toast already fired
          row.remove();
          if (container.children.length === 0) {
            const empty = document.createElement("div");
            empty.className = "list-empty";
            empty.textContent = "No archived chats.";
            container.appendChild(empty);
          }
        });
      }
    });
  }

  async loadArchived(): Promise<void> {
    const list = document.getElementById("history-list");
    const count = document.getElementById("history-count");
    const section = document.getElementById("sidebar-history");
    if (list === null) return;

    this.archivedController?.abort();
    this.archivedController = new AbortController();
    const { signal } = this.archivedController;

    const d = await apiGet<{ chats: ArchivedHeader[] }>("/api/chats/archived", signal);
    if (signal.aborted) return;
    const chats = d?.chats ?? [];

    if (section !== null) section.classList.toggle("hidden", chats.length === 0);
    if (count !== null) {
      count.textContent = String(chats.length);
      count.classList.toggle("hidden", chats.length === 0);
    }

    list.replaceChildren();
    if (chats.length === 0) {
      const hint = document.createElement("p");
      hint.className = "text-muted text-sm";
      hint.style.padding = "var(--sp-2) var(--sp-3)";
      hint.textContent = "No archived chats.";
      list.appendChild(hint);
      return;
    }

    for (const chat of chats) {
      const row = document.createElement("div");
      row.className = "history-row";
      row.setAttribute("data-chat-id", chat.id);
      const nameWrap = document.createElement("div");
      nameWrap.className = "history-name-wrap";
      const name = document.createElement("span");
      name.className = "history-name";
      name.textContent = chat.name;
      nameWrap.appendChild(name);
      if (chat.summary !== undefined && chat.summary !== "") {
        const sum = document.createElement("span");
        sum.className = "history-summary";
        sum.textContent = chat.summary;
        nameWrap.appendChild(sum);
      }
      const restore = document.createElement("button");
      restore.type = "button";
      restore.className = "history-restore";
      restore.textContent = "Restore";
      restore.setAttribute("data-action", "restore");
      row.append(nameWrap, restore);
      list.appendChild(row);
    }

    list.addEventListener("click", (e) => {
      const target = (e.target as HTMLElement).closest<HTMLElement>("[data-action]");
      if (target === null) return;
      const row = target.closest<HTMLElement>("[data-chat-id]");
      if (row === null) return;
      const chatId = row.getAttribute("data-chat-id")!;
      if (target.getAttribute("data-action") === "restore") {
        restoreArchivedChat(chatId);
        row.remove();
      }
    });
  }

  refreshVisibility(): void {
    void this.loadArchived();
  }
}

const historyCtrl = new HistoryController();

export function initHistory(): void { historyCtrl.init(); }
export function showHistoryView(): void { historyCtrl.showView(); }


export function refreshHistoryVisibility(): void { historyCtrl.refreshVisibility(); }
