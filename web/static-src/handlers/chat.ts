// ---------------------------------------------------------------------------
// SSE handlers for chat lifecycle: create, update, delete.
// Typed through onSSE — no `unknown` unwrap boilerplate.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { upsertHeader, removeChat } from "../store.js";
import { closeTab, hasTab } from "../tabs.js";

onSSE("chat_created", (_chatID, header) => {
  if (header !== undefined) upsertHeader(header);
});

onSSE("chat_updated", (_chatID, header) => {
  if (header !== undefined) upsertHeader(header);
});

onSSE("chat_deleted", (_chatID, p) => {
  if (p?.id !== undefined) {
    // Close any open tab for this chat so a remote delete/archive from
    // another device doesn't leave an orphan tab that activates an
    // undefined session. On the originating device the tab was already
    // removed synchronously by the close button's click handler; hasTab
    // is false and closeTab is a no-op. On a second device this is the
    // only path that removes the stale tab.
    if (hasTab(p.id)) closeTab(p.id);
    removeChat(p.id);
    void import("../conflicts.js").then((m) => m.clearConflicts(p.id));
  }
});
