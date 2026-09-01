// ---------------------------------------------------------------------------
// SSE handlers for chat lifecycle: create, update, delete.
// Typed through onSSE — no `unknown` unwrap boilerplate.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { upsertHeader, removeChat, getActiveId, setAgentStatus } from "../store.js";
import { dropDecisions } from "../decision-dock.js";
import { dropComposerState, adoptRemoteComposerState } from "../composer-state.js";
import { parseRoute, replaceRoute } from "../router.js";

// Defensive `=== undefined` guards: the wire decoder marks payloads
// non-nullable but the test suite (and a malformed frame at runtime)
// can hand us undefined. See handlers/messages.ts for the full
// rationale.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

// There is no adoptHeader wrapper any more: the chat id is the server's from
// the moment the chat exists, so the ordinary openTab emit covers it.

onSSE("chat_created", (_chatID, header) => {
  if (header === undefined) {
    return;
  }
  upsertHeader(header);
  // Convergence fix: `applyInitialRoute` canonicalizes an active chat to
  // /chat/{id}, so what remains is a chat becoming active while the route
  // is still "/" — including one created on another device. Never hijack
  // a reader who has navigated elsewhere meanwhile.
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

// draft_changed: the composer state one chat now holds, after a set_draft
// or set_attachments write on any device — lets an idle device converge on
// a draft it is not typing. The local map is authoritative for the chat on
// screen, so this updates the map for every other chat and is ignored for
// the live one (adoptRemoteComposerState).
onSSE("draft_changed", (chatID, p) => {
  if (chatID === "" || p === undefined) {
    return;
  }
  adoptRemoteComposerState(chatID, p.text ?? "", p.attachments ?? []);
});

// chat_status: the agent's self-declared activity. Ephemeral — cleared on
// the next prompt send and on a transport gap, never persisted.
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
  // No tab close here, deliberately: a deleted chat's tabs are closed by
  // the membership coordinator under the same lock that removed the
  // record, so a close from here would race the deleting device's own.
  //
  // The two per-chat cleanups below still run — each is keyed by chat id
  // and outlives the tab, and each is idempotent.
  dropDecisions(p.id);
  dropComposerState(p.id);
  removeChat(p.id);
  // Drop the chat's in-memory banner entries; persisted dismissals are not
  // pruned here since only the BannerEntry DOM objects need dropping.
  void import("../banner-stack.js").then(
    (m) => {
      m.clearBannersForChat(p.id);
    },
    (e: unknown) => {
      console.warn("[handlers/chat] clearBannersForChat import failed", e);
    },
  );
});
