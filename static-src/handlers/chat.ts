// ---------------------------------------------------------------------------
// SSE handlers for chat lifecycle: create, update, delete.
// Typed through onSSE — no `unknown` unwrap boilerplate.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { upsertHeader, removeChat, getActiveId, setAgentStatus } from "../store.js";
import { closeTab, hasTab } from "../tabs.js";
import { parseRoute, replaceRoute } from "../router.js";

// Defensive `=== undefined` guards: the wire decoder marks payloads
// non-nullable but the test suite (and a malformed frame at runtime)
// can hand us undefined. See handlers/messages.ts for the full
// rationale.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

onSSE("chat_created", (_chatID, header) => {
  if (header === undefined) {
    return;
  }
  upsertHeader(header);
  // B4: a zero-message ghost chat keeps "/" as its URL (applyInitialRoute
  // deliberately doesn't rewrite it — the id exists nowhere but this tab's
  // memory). Once the server persists the chat and echoes chat_created,
  // flip the URL to the now-real id — but only when it's this device's
  // active chat AND we're still sitting on "/" (never hijack a user who
  // navigated to settings/files/another chat meanwhile).
  const route = parseRoute(location.pathname);
  if (header.id === getActiveId() && route.kind === "chat" && route.id === "") {
    replaceRoute({ kind: "chat", id: header.id });
  }
});

onSSE("chat_updated", (_chatID, header) => {
  if (header === undefined) {
    return;
  }
  upsertHeader(header);
});

// chat_status: the agent's self-declared activity (KAS focus_update via
// update_session_information). Ephemeral by design — the store field is
// cleared on the next prompt send (setThinking(true)) and on a transport
// gap, and never persisted, so a stale "in_progress" can't survive a
// restart. The chat.ts store effect projects it onto the tab (waiting dot
// + description tooltip).
onSSE("chat_status", (chatID, p) => {
  if (chatID === "" || p === undefined) {
    return;
  }
  setAgentStatus(chatID, p.status ?? "", p.description ?? "");
});

onSSE("chat_deleted", (_chatID, p) => {
  if (p === undefined || typeof p.id !== "string" || p.id === "") {
    return;
  }
  // Close any open tab for this chat so a remote delete/archive from
  // another device doesn't leave an orphan tab that activates an
  // undefined session. On the originating device the tab was already
  // removed synchronously by the close button's click handler; hasTab
  // is false and closeTab is a no-op. On a second device this is the
  // only path that removes the stale tab.
  if (hasTab(p.id)) {
    closeTab(p.id, { skipOnClose: true });
  }
  removeChat(p.id);
  // Drop any banners for the deleted chat — otherwise their
  // BannerEntry objects + dismissed_banners localStorage entries
  // accumulate over a long session.
  void import("../banner-stack.js").then(
    (m) => {
      m.clearBannersForChat(p.id);
    },
    (e: unknown) => {
      console.warn("[handlers/chat] clearBannersForChat import failed", e);
    },
  );
});
