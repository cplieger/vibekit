// ---------------------------------------------------------------------------
// Chat history: sidebar popover listing archived chats, plus a full-page
// History table opened from the toolbar (#history-btn). Restoring a chat
// loads its messages into a new tab. Deletion is permanent (server-side).
//
// Uses loadHistory / deleteArchivedChat from actions/chat.ts
// for the table view, and raw apiGet for the lightweight sidebar fetch
// (no toast on failure — sidebar is background UI).
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { restoreArchivedChat } from "./chat.js";
import { toggleHistoryView } from "./tabs.js";
import { ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { deleteArchivedChat, loadHistory } from "./actions/chat.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { el } from "@cplieger/reactive";
import { reconcile } from "./reconcile.js";

interface ArchivedHeader {
  id: string;
  name: string;
  summary?: string;
  updated_at: number;
}

class HistoryController {
  private tableAbortController: AbortController | null = null;
  private archivedController: AbortController | null = null;
  /** Per-row delete-button bindings keyed by chat id. Cleared per row
   *  via reconcile's onRemove hook so loadHistoryTable doesn't accrue
   *  dangling subscriptions across re-renders. */
  private rowUnbinds = new Map<string, () => void>();

  init(): void {
    const toggle = document.getElementById("history-toggle");
    const list = document.getElementById("history-list");
    if (toggle !== null && list !== null) {
      toggle.addEventListener("click", () => {
        const open = !list.classList.contains("hidden");
        list.classList.toggle("hidden", open);
        if (!open) {
          void this.loadArchived();
        }
      });
    }
  }

  showView(): void {
    toggleHistoryView(
      () => {
        void this.loadHistoryTable();
      },
      () => {
        this.teardown();
      },
    );
  }

  teardown(): void {
    loadHistory.cancel();
    this.tableAbortController?.abort();
    this.tableAbortController = null;
    this.archivedController?.abort();
    this.archivedController = null;
    for (const u of this.rowUnbinds.values()) {
      u();
    }
    this.rowUnbinds.clear();
  }

  async loadHistoryTable(): Promise<void> {
    const container = document.getElementById("history-table");
    if (container === null) {
      return;
    }

    loadHistory.cancel();
    this.tableAbortController?.abort();
    this.tableAbortController = new AbortController();
    const { signal } = this.tableAbortController;

    const d = await loadHistory.dispatch(undefined);
    if (signal.aborted) {
      return;
    }
    // Bug 5: if dispatch returned null (error), bail — don't paint a misleading empty state.
    if (d === null) {
      this.flushRowUnbinds();
      container.replaceChildren();
      container.appendChild(
        el(
          "div",
          { className: "list-empty" },
          "Failed to load history. Check your connection and try again.",
        ),
      );
      return;
    }
    const chats = d.chats ?? []; // eslint-disable-line @typescript-eslint/no-unnecessary-condition

    // Drop any non-keyed sibling (empty/error placeholder) before reconcile.
    for (const child of [...container.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }

    if (chats.length === 0) {
      this.flushRowUnbinds();
      container.replaceChildren();
      container.appendChild(el("div", { className: "list-empty" }, "No archived chats."));
      return;
    }

    reconcile(container, chats, {
      key: (c) => c.id,
      mount: (c) => this.buildHistoryTableRow(c),
      onRemove: (_, key) => {
        const u = this.rowUnbinds.get(key);
        if (u !== undefined) {
          u();
          this.rowUnbinds.delete(key);
        }
      },
    });

    // Bug 1: Use signal-bound listener so it's automatically removed on next
    // loadHistoryTable call (which aborts tableAbortController).
    container.addEventListener(
      "click",
      (e) => {
        const target = (e.target as HTMLElement).closest<HTMLElement>("[data-action]");
        if (target === null) {
          return;
        }
        const row = target.closest<HTMLElement>("[data-chat-id]");
        if (row === null) {
          return;
        }
        const chatId = row.getAttribute("data-chat-id")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
        const action = target.getAttribute("data-action");
        if (action === "restore") {
          // Bug 4: optimistic-remove the row to prevent double-click dispatches.
          row.remove();
          const u = this.rowUnbinds.get(chatId);
          if (u !== undefined) {
            u();
            this.rowUnbinds.delete(chatId);
          }
          restoreArchivedChat(chatId);
        } else if (action === "delete") {
          // Bug 4: optimistic-remove the row on click.
          row.remove();
          const u = this.rowUnbinds.get(chatId);
          if (u !== undefined) {
            u();
            this.rowUnbinds.delete(chatId);
          }
          if (container.querySelector("[data-chat-id]") === null) {
            container.appendChild(el("div", { className: "list-empty" }, "No archived chats."));
          }
          void deleteArchivedChat.dispatch(chatId);
        }
      },
      { signal },
    );
  }

  private buildHistoryTableRow(chat: {
    id: string;
    name: string;
    summary?: string;
    updated_at: number;
  }): HTMLElement {
    const row = el("div", {
      className: "list-row history-table-row",
      "data-chat-entry": "",
      "data-chat-id": chat.id,
    });

    const nameWrap = el(
      "div",
      { className: "list-row-title", "data-action": "restore" },
      el("span", { className: "list-row-name" }, chat.name),
      chat.summary !== undefined && chat.summary !== ""
        ? el("span", { className: "list-row-summary" }, chat.summary)
        : null,
    );
    nameWrap.style.cursor = "pointer";

    const date = el(
      "span",
      { className: "list-row-meta" },
      new Date(chat.updated_at).toLocaleString(),
    );

    const delBtn = el(
      "button",
      {
        type: "button",
        className: "btn-small btn-danger icon-only",
        "data-tooltip": "Delete permanently",
        "aria-label": `Delete ${chat.name}`,
        "data-action": "delete",
      },
      iconEl(ICON_TRASH),
    ) as HTMLButtonElement;

    row.append(nameWrap, date, delBtn);
    this.rowUnbinds.set(chat.id, bindLoadingState("chat.delete_archived", delBtn));
    return row;
  }

  private flushRowUnbinds(): void {
    for (const u of this.rowUnbinds.values()) {
      u();
    }
    this.rowUnbinds.clear();
  }

  async loadArchived(): Promise<void> {
    const list = document.getElementById("history-list");
    const count = document.getElementById("history-count");
    const section = document.getElementById("sidebar-history");
    if (list === null) {
      return;
    }

    this.archivedController?.abort();
    this.archivedController = new AbortController();
    const { signal } = this.archivedController;

    // Intentional: uses raw apiGet instead of an action because this is a
    // sidebar background fetch — no toast desired on failure (POLICY: LOG ONLY).
    const d = await apiGet<{ chats: ArchivedHeader[] }>("/api/chats/archived", signal);
    if (signal.aborted) {
      return;
    }
    const chats = d?.chats ?? [];

    if (section !== null) {
      section.classList.toggle("hidden", chats.length === 0);
    }
    if (count !== null) {
      count.textContent = String(chats.length);
      count.classList.toggle("hidden", chats.length === 0);
    }

    // Drop non-keyed empty-state placeholder before reconcile.
    for (const child of [...list.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }

    if (chats.length === 0) {
      list.replaceChildren();
      const hint = el("p", { className: "text-muted text-sm" }, "No archived chats.");
      hint.style.padding = "var(--sp-2) var(--sp-3)";
      list.appendChild(hint);
      return;
    }

    reconcile(list, chats, {
      key: (c) => c.id,
      mount: (c) => buildArchivedSidebarRow(c),
    });

    // Bug 1: Use signal-bound listener so it's removed on next loadArchived call.
    list.addEventListener(
      "click",
      (e) => {
        const target = (e.target as HTMLElement).closest<HTMLElement>("[data-action]");
        if (target === null) {
          return;
        }
        const row = target.closest<HTMLElement>("[data-chat-id]");
        if (row === null) {
          return;
        }
        const chatId = row.getAttribute("data-chat-id")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
        if (target.getAttribute("data-action") === "restore") {
          // Bug 4: optimistic-remove row to prevent double-click.
          row.remove();
          restoreArchivedChat(chatId);
        }
      },
      { signal },
    );
  }
}

function buildArchivedSidebarRow(chat: ArchivedHeader): HTMLElement {
  return el(
    "div",
    { className: "history-row", "data-chat-id": chat.id },
    el(
      "div",
      { className: "history-name-wrap" },
      el("span", { className: "history-name" }, chat.name),
      chat.summary !== undefined && chat.summary !== ""
        ? el("span", { className: "history-summary" }, chat.summary)
        : null,
    ),
    el(
      "button",
      { type: "button", className: "history-restore", "data-action": "restore" },
      "Restore",
    ),
  );
}

const historyCtrl = new HistoryController();
registerCleanup(() => {
  historyCtrl.teardown();
});

export function initHistory(): void {
  historyCtrl.init();
}
export function showHistoryView(): void {
  historyCtrl.showView();
}
