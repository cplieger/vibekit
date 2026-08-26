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

// There is no adoptHeader wrapper any more, and no publishArrangement call
// behind it. It existed to republish the shared arrangement on the frame that
// turned a client-minted chat into a real one — a transition that changed no tab,
// so nothing emitted for it and the chat would otherwise never enter `tab_order`.
// The chat id is the server's from the moment the chat exists, so there is no
// transition and nothing to republish: the ordinary openTab emit covers it.

onSSE("chat_created", (_chatID, header) => {
  if (header === undefined) {
    return;
  }
  upsertHeader(header);
  // The URL rewrite survives, and it is now a convergence fix rather than a
  // ghost patch: `applyInitialRoute` canonicalizes any active chat to /chat/{id},
  // so what is left for this frame is a chat that becomes active while the route is
  // still "/" — including one created on ANOTHER device. Only then: never hijack a
  // reader who has navigated to settings, files or another chat meanwhile.
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

// draft_changed: the composer state one chat now holds, after a set_draft or a
// set_attachments write on ANY device. It exists so an idle device converges on a
// draft it is not typing — before it, a phone that had looked at a chat kept
// whatever it saw until the next full activation, and a tab switch could then
// flush that stale copy back over the newer one.
//
// The whole rule lives in adoptRemoteComposerState: the LOCAL map is
// authoritative for the chat on screen, so the frame updates the map for every
// other chat and is ignored for the live one. Chat-scoped, so an empty envelope id
// is unroutable and dropped.
onSSE("draft_changed", (chatID, p) => {
  if (chatID === "" || p === undefined) {
    return;
  }
  adoptRemoteComposerState(chatID, p.text ?? "", p.attachments ?? []);
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
  // NO tab close here, and its absence is load-bearing rather than an omission.
  // A deleted chat's tabs are closed by the membership coordinator, under the
  // same lock that removed the record, and the removal frame is what takes them
  // off every strip — so a `close_tab` from here would be a second close for a
  // tab the server has already dropped, and on the deleting device it would race
  // its own delete.
  //
  // The three per-chat cleanups below still run, and they still have to: each is
  // keyed by CHAT id and outlives the tab, the frame's own teardown may not have
  // landed yet, and every one of them is idempotent.
  //
  // The dock's queue survived a delete without this, so a chat recreated under
  // the same id inherited a card for a request the server has forgotten.
  dropDecisions(p.id);
  // The composer's per-chat state is the other thing that outlives the tab.
  // Without this the deleted chat kept its draft map entry, and the next chat
  // switch flushed a set_draft under an id the server has forgotten.
  dropComposerState(p.id);
  removeChat(p.id);
  // There is no fold-override cleanup here any more, and its absence is the
  // point: the overrides are per-chat localStorage now (fold-state.ts) with their
  // own chat cap and oldest-first eviction, so nothing has to be told a chat is
  // gone. The call existed only because the state lived in one global blob where
  // an entry per deleted chat accumulated forever.
  //
  // Drop the chat's in-memory banner entries. The persisted DISMISSALS are not
  // pruned here for the same reason, and only the BannerEntry objects — which are
  // this session's DOM — still need dropping.
  void import("../banner-stack.js").then(
    (m) => {
      m.clearBannersForChat(p.id);
    },
    (e: unknown) => {
      console.warn("[handlers/chat] clearBannersForChat import failed", e);
    },
  );
});
