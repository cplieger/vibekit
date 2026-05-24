// ---------------------------------------------------------------------------
// Chat lifecycle: create, activate, delete, switch, send prompt. Also owns
// the chat-tab registration (openChatTab) and load-more pagination.
//
// The store is the single source of truth; this module just wires user
// actions to server commands and renders the active session.
// ---------------------------------------------------------------------------

import {
  getActiveId, getActive, get, getSessions, setActive,
  loadList, loadMessages, upsertHeader, dequeuePrompt,
  contextSizeFor, defaultUsage, version,
} from "./store.js";
import { effect } from "./signals.js";
import type { Session } from "./types.js";
import { renderStack as renderBanners } from "./banner-stack.js";
import { sendPromptTo } from "./chat-commands.js";
import {
  openTab, activateTab, hasTab, getActiveTabId, renameTab, setTabStatus, TAB_VIEWS,
} from "./tabs.js";
import { chatSkeleton } from "./skeleton.js";
import { showModelPicker, hideModelPicker } from "./picker.js";
import { prependMessages, setLoadMore, scrollToBottom } from "./messages.js";
import { addAttachment, clearAttachments } from "./attachments.js";
import { renderSwitch, renderUpdates } from "./renderer.js";
import {
  setCurrentModel, getCurrentAgent, getLastModel, withAgent,
} from "./session-context.js";
import { applyLocalModel } from "./model-switcher.js";
import { refreshContextUI } from "./context-ui.js";
import { recompute as recomputeSendState } from "./send-state.js";
import { $ } from "./dom.js";
import { isRetentionEnabled } from "./retention.js";
import { onBus, BUS_ACTIVATE_CHAT } from "./bus.js";
import { deleteChatAction, archiveChatAction, discardTangentAction, restoreChatAction } from "./actions/chat.js";

// --- Bus: activate chat from other modules without importing chat.ts ---

onBus(BUS_ACTIVATE_CHAT, (p) => {
  activateChatView(p.chatID);
  if (p.then !== undefined) p.then();
});

// --- Chat tab registration ---

export function openChatTab(id: string, name: string, agent: string): void {
  openTab({
    id, name,
    kind: agent === "kiro_planner" ? "plan" : "chat",
    view: TAB_VIEWS.chat,
    route: { kind: "chat", id },
    onShow: () => activateChatView(id),
    onClose: () => {
      // Tangent tabs need special handling: archiving a tangent via
      // the generic flow would leave the parent chat frozen forever
      // (only cmdMergeTangent / cmdDiscardTangent unfreeze it). So
      // close-X on a tangent routes through discard_tangent instead,
      // which drops the tangent AND unfreezes the parent server-side.
      // The normal chat_deleted broadcast still fires, so the sidebar
      // / other devices clean up via handlers/chat.ts.
      const s = get(id);
      if (s?.is_tangent === true) {
        void discardTangentAction.dispatch(id);
        return;
      }
      // Retention > 0: archive so the chat appears in History.
      // Retention = 0: delete permanently (no history).
      if (isRetentionEnabled()) {
        archiveChat(id);
      } else {
        deleteChat(id);
      }
    },
  });
}

/** Archive a chat (move to archive dir). Used on tab close instead of
 *  delete so the chat appears in History for the retention window. */
function archiveChat(id: string): void {
  void archiveChatAction.dispatch(id);
  // Store is updated when the server broadcasts chat_deleted (archive
  // triggers the same SSE event so the sidebar removes the tab).
}

export function activateChatView(id: string): void {
  setActive(id);
  hideModelPicker();
  clearAttachments();
  renderBanners();
  // Prefetch conflict records so badges on historical tool calls
  // are present as soon as the messages render. Dynamic import
  // keeps the conflicts module lazy — most chats never see one.
  void import("./conflicts.js").then((m) => m.loadConflictsFor(id));

  const session = get(id);
  if (session === undefined) return;

  if (session.usage.context_size === 0 && session.model !== "") {
    session.usage.context_size = contextSizeFor(session.model);
  }

  if (session.message_count === 0 && session.messages.length === 0) {
    renderSwitch(session);
    showModelPicker(session.model, applyLocalModel, session.agent);
  } else {
    // Hydrate messages from the server, then render.
    renderSwitch(session);
    const skel = chatSkeleton();
    $.messages.appendChild(skel);
    void loadMessages(id).then((ok) => {
      skel.remove();
      if (getActiveId() !== id) return;
      if (!ok) {
        // Show retry button on failed load.
        const retry = document.createElement("div");
        retry.className = "load-error";
        retry.innerHTML = '<span>Failed to load messages.</span>';
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "btn-small";
        btn.textContent = "Retry";
        btn.addEventListener("click", () => { retry.remove(); activateChatView(id); });
        retry.appendChild(btn);
        $.messages.appendChild(retry);
        return;
      }
      const fresh = get(id);
      if (fresh !== undefined) {
        renderSwitch(fresh);
        setupLoadMore(id);
        scrollToBottom();
      }
    });
  }
  refreshContextUI(session);
}

// --- Pagination ---

function setupLoadMore(chatID: string): void {
  const session = get(chatID);
  if (session === undefined) return;
  setLoadMore(
    session.has_more ? (): void => {
      const oldest = session.messages[0];
      if (oldest === undefined) return;
      const prevCount = session.messages.length;
      void loadMessages(chatID, oldest.ts).then(() => {
        if (getActiveId() !== chatID) return;
        const s = get(chatID);
        if (s === undefined) return;
        const added = s.messages.length - prevCount;
        if (added > 0) prependMessages(s.messages.slice(0, added));
        setupLoadMore(chatID);
      });
    } : null,
    session.has_more,
  );
}

// --- Sending prompts ---

export function sendPrompt(text: string): void {
  hideModelPicker();
  const chatID = getActiveId();
  if (chatID === "") return;
  void sendPromptTo(chatID, text);
}

/** Drain the single-slot queued prompt for `chatID` if any. Called from
 *  the turn_ended handler. Safe to call when the queue is empty. */
export function drainQueuedPrompt(chatID: string): void {
  const text = dequeuePrompt(chatID);
  if (text === undefined) return;
  void sendPromptTo(chatID, text);
  // Remaining queued messages will drain on subsequent turn_ended events.
}

// --- Session lifecycle ---

/** Create a new chat. If initialPrompt is non-empty, send it immediately.
 *  Otherwise open the model picker and wait for the user to type. */
export function createSession(initialPrompt?: string): void {
  const id = `c-${String(Date.now())}-${Math.random().toString(36).slice(2, 8)}`;
  const model = getLastModel();
  const agent = getCurrentAgent();
  const session: Session = {
    id, name: "New conversation", agent, model,
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    available_commands: [],
    available_prompts: [],
    auto_approve_crew: false,
    pending_changes: [],
    usage: defaultUsage(),
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
  session.usage.context_size = contextSizeFor(model);
  upsertHeader({
    id: session.id, name: session.name,
    agent: session.agent, model: session.model,
    acp_session_id: "",
    usage: session.usage,
    created_at: Date.now(), updated_at: Date.now(),
    message_count: 0,
  });
  setActive(id);
  openChatTab(id, session.name, session.agent);

  if (initialPrompt !== undefined && initialPrompt !== "") {
    setCurrentModel(model);
    sendPrompt(initialPrompt);
  } else {
    showModelPicker(model, applyLocalModel, session.agent);
  }
}

export function switchSession(id: string): void {
  if (id === getActiveId() && getActiveTabId() === id) return;
  activateTab(id);
}

/** Attach a workspace file to the active chat's next prompt. Shows as
 *  a removable pill below the textarea. The user still types their
 *  message; the attachment is sent alongside on submit. Switches to the
 *  chat tab if needed so the user sees where their attachment went. */
export function attachPathToActiveChat(path: string): void {
  if (getActiveId() === "") {
    createSession();
  }
  const id = getActiveId();
  if (id !== "" && getActiveTabId() !== id) {
    activateTab(id);
  }
  addAttachment(path);
  $.promptInput.focus();
}

/** User-triggered chat deletion. */
export function deleteChat(id: string): void {
  void deleteChatAction.dispatch(id);
  // Store is updated when chat_deleted SSE echoes back.
}

/** Export a chat as a downloadable JSON file. Caller must guarantee
 *  `id` is non-empty; the toolbar button disables itself when no chat
 *  is active so this is an invariant at the UI boundary. */
export function exportChat(id: string): void {
  if (id === "") return;
  const a = document.createElement("a");
  a.href = `/api/chats/${encodeURIComponent(id)}/export`;
  a.download = `${id}.json`;
  a.rel = "noopener";
  // Some browsers (Firefox, WebKit) silently ignore click() on
  // detached anchors. Append, click, remove.
  document.body.appendChild(a);
  a.click();
  a.remove();
}

/** Restore an archived chat. Opens a tab and activates it after the
 *  sidebar store catches up, matching the tangent-fork pattern — the
 *  server broadcasts chat_created, but the SSE handler only updates
 *  the store (a generic chat_created is also emitted to every other
 *  connected client, none of which should auto-open tabs). The
 *  restoring client explicitly opens + activates so the user lands
 *  on the conversation they just resurrected instead of having to
 *  find it in the sidebar. */
export function restoreArchivedChat(id: string): void {
  void restoreChatAction.dispatch(id).then((d) => {
    if (d === null || d.ok !== true) return;
    void loadList().then(() => {
      const s = get(id);
      if (s === undefined) return;
      openChatTab(s.id, s.name, s.agent);
      activateChatView(s.id);
    });
  });
}

/** Create a planner session: temporarily switch currentAgent, then createSession,
 *  then restore. */
export function createPlannerSession(): void {
  withAgent("kiro_planner", () => createSession());
}

/** Wire the effect() that drives the renderer, tab-rename
 *  reconciliation, and send-button state. */
export function installStoreSubscribers(): void {
  effect(() => {
    version.value;
    const active = getActive();
    if (active !== undefined) {
      renderUpdates(active);
      refreshContextUI(active);
    }
    recomputeSendState();
    // Reconcile tab names with chat names (server auto-rename propagates here).
    for (const s of getSessions()) {
      if (hasTab(s.id)) {
        renameTab(s.id, s.name);
        setTabStatus(s.id, s.thinking ? "thinking" : "");
      }
    }
  });
}
