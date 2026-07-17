// ---------------------------------------------------------------------------
// Chat history: sidebar popover listing archived chats, plus a full-page
// History table opened from the toolbar (#history-btn). Restoring a chat
// loads its messages into a new tab. Deletion is permanent (server-side) and
// is guarded by a destructive confirm dialog.
//
// Uses loadHistory / deleteArchivedChat from actions/chat.ts
// for the table view, and raw apiGet for the lightweight sidebar fetch
// (no toast on failure — sidebar is background UI).
// ---------------------------------------------------------------------------

import { apiGet } from "./api-client.js";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { restoreArchivedChat } from "./chat.js";
import { toggleHistoryView } from "./tabs.js";
import { ICON_TRASH, ICON_EXPORT } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { downloadChatExport } from "./chat-export.js";
import { confirm as confirmDialog } from "./confirm.js";
import { deleteArchivedChat, loadHistory } from "./actions/chat.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
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
      // Trigger disclosure over the sidebar list: the primitive announces the
      // expanded/collapsed state on the (native button) toggle and animates
      // the region — the old hidden-class flip told AT nothing. The archived
      // chats load lazily on each open, as before.
      const startOpen = !list.classList.contains("hidden");
      list.classList.remove("hidden");
      createDisclosure(toggle, list, {
        open: startOpen,
        onToggle: (open) => {
          if (open) {
            void this.loadArchived();
          }
        },
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

    // Stable skeleton (150ms show-delay) while the fetch is in flight; skipped
    // when the table already holds rows (a re-open) so it doesn't flash.
    const skeleton = skeletonTiming(() => showTableSkeleton(container));

    const d = await loadHistory.dispatch(undefined);
    skeleton.cancel();
    if (signal.aborted) {
      return;
    }
    // Error: don't paint a misleading empty state — offer a retry (no dead end).
    if (d === null) {
      this.flushRowUnbinds();
      container.replaceChildren(this.buildErrorState());
      return;
    }
    const chats = d.chats ?? []; // eslint-disable-line @typescript-eslint/no-unnecessary-condition

    // Drop any non-keyed sibling (skeleton / empty / error placeholder) before reconcile.
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

    // Bug 1: Use signal-bound listeners so they're automatically removed on the
    // next loadHistoryTable call (which aborts tableAbortController).
    container.addEventListener(
      "click",
      (e) => {
        this.handleRowAction(container, e.target as HTMLElement);
      },
      { signal },
    );
    // Restore is a non-native activation target (a role="button" title block),
    // so translate Enter/Space into the same click path for keyboard users.
    container.addEventListener(
      "keydown",
      (e) => {
        if (e.key !== "Enter" && e.key !== " ") {
          return;
        }
        const restore = (e.target as HTMLElement).closest<HTMLElement>('[data-action="restore"]');
        if (restore === null) {
          return;
        }
        e.preventDefault();
        restore.click();
      },
      { signal },
    );
  }

  /** Dispatch a click on a `[data-action]` control to its handler. */
  private handleRowAction(container: HTMLElement, targetEl: HTMLElement): void {
    const target = targetEl.closest<HTMLElement>("[data-action]");
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
      // Optimistic-remove the row to prevent double-click dispatches.
      row.remove();
      this.dropRowUnbind(chatId);
      restoreArchivedChat(chatId);
    } else if (action === "delete") {
      // Destructive + permanent: confirm before removing (no optimistic remove
      // until the user has agreed).
      void this.confirmAndDelete(container, row, chatId);
    } else if (action === "export") {
      // Non-destructive: keep the row. The server export path falls back
      // to the archive dir, so archived chats export without restoring.
      const name = row.querySelector<HTMLElement>(".list-row-name")?.textContent ?? "";
      downloadChatExport(chatId, name, "md");
    }
  }

  /** Confirm, then permanently delete an archived chat. */
  private async confirmAndDelete(
    container: HTMLElement,
    row: HTMLElement,
    chatId: string,
  ): Promise<void> {
    const name = row.querySelector<HTMLElement>(".list-row-name")?.textContent ?? "this chat";
    const ok = await confirmDialog(
      `Delete "${name}" permanently? This can't be undone.`,
      "Delete",
      "destructive",
    );
    if (!ok) {
      return;
    }
    row.remove();
    this.dropRowUnbind(chatId);
    if (container.querySelector("[data-chat-id]") === null) {
      container.appendChild(el("div", { className: "list-empty" }, "No archived chats."));
    }
    void deleteArchivedChat.dispatch(chatId);
  }

  private dropRowUnbind(chatId: string): void {
    const u = this.rowUnbinds.get(chatId);
    if (u !== undefined) {
      u();
      this.rowUnbinds.delete(chatId);
    }
  }

  /** Error placeholder with a Retry button so a failed load isn't a dead end. */
  private buildErrorState(): HTMLElement {
    const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
    retry.addEventListener("click", () => {
      void this.loadHistoryTable();
    });
    return el(
      "div",
      { className: "list-empty history-error" },
      el("span", {}, "Couldn't load history. Check your connection and try again."),
      retry,
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
      {
        className: "list-row-title",
        role: "button",
        tabindex: "0",
        "aria-label": `Restore ${chat.name}`,
        "data-action": "restore",
      },
      el("span", { className: "list-row-name" }, chat.name),
      chat.summary !== undefined && chat.summary !== ""
        ? el("span", { className: "list-row-summary" }, chat.summary)
        : null,
    );

    const date = el(
      "span",
      { className: "list-row-meta" },
      new Date(chat.updated_at).toLocaleString(),
    );

    const exportBtn = el(
      "button",
      {
        type: "button",
        className: "btn-small icon-only",
        "data-tooltip": "Export as Markdown",
        "aria-label": `Export ${chat.name}`,
        "data-action": "export",
      },
      iconEl(ICON_EXPORT),
    ) as HTMLButtonElement;

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

    row.append(nameWrap, date, exportBtn, delBtn);
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
          // Optimistic-remove row to prevent double-click.
          row.remove();
          restoreArchivedChat(chatId);
        }
      },
      { signal },
    );
  }
}

/** Skeleton rows for the full-page history table (matches .history-table-row
 *  dimensions). Skipped when the table already holds rows so a re-open doesn't
 *  flash placeholders. Returns a teardown that removes the skeleton. */
function showTableSkeleton(container: HTMLElement): () => void {
  if (container.querySelector("[data-chat-id]") !== null) {
    return () => {
      /* already populated — nothing shown */
    };
  }
  const wrap = el("div", { className: "history-skeleton", "aria-hidden": "true" });
  for (let i = 0; i < 4; i++) {
    const row = el("div", { className: "list-row history-table-row history-skel-row" });
    const title = el("div", { className: "list-row-title" });
    title.appendChild(skelBar("history-skel-name", "60%"));
    title.appendChild(skelBar("history-skel-summary", "42%"));
    row.appendChild(title);
    row.appendChild(skelBar("history-skel-date", "8rem"));
    wrap.appendChild(row);
  }
  container.replaceChildren(wrap);
  return () => {
    wrap.remove();
  };
}

function skelBar(className: string, width: string): HTMLElement {
  const bar = el("div", { className: `skeleton ${className}` });
  bar.style.width = width;
  return bar;
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
      {
        type: "button",
        className: "history-restore",
        "aria-label": `Restore ${chat.name}`,
        "data-action": "restore",
      },
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
