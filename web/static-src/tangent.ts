// ---------------------------------------------------------------------------
// Tangent mode: fork a side conversation from the current chat.
//
// Parent chat is frozen (no input). Tangent appears as a sub-item in
// the sidebar. Two resolution paths:
//   - "Keep & return" (mergeTangent): merge last Q&A pair to parent,
//     unfreeze parent, delete tangent. Wired via the merge-tangent pill.
//   - "Discard" (discardTangent): delete tangent and unfreeze parent
//     without merging. Wired via the chat tab close button (chat.ts).
// ---------------------------------------------------------------------------

import { getActive, get, loadList, version } from "./store.js";
import { effect } from "./signals.js";
import { forkChat, mergeTangent } from "./actions/chat.js";
import { bindLoadingState } from "./actions/index.js";
import { openChatTab, activateChatView } from "./chat.js";

/** Wire the fork pill + merge-tangent pill in the chat prompt row.
 *  Both pills live in the per-conversation pill cluster — they only
 *  act on the active chat, so universal toolbar placement was wrong.
 *
 *  Visibility (mutually exclusive based on session state):
 *    - Fork pill: shown only when active chat is NOT frozen and NOT a tangent.
 *    - Merge pill: shown only when active chat IS a tangent. */
export function initTangent(): void {
  const forkBtn = document.getElementById("fork-pill") as HTMLButtonElement | null;
  const mergeBtn = document.getElementById("merge-tangent-pill") as HTMLButtonElement | null;

  if (forkBtn !== null) {
    forkBtn.addEventListener("click", () => {
      const session = getActive();
      if (session === undefined) return;
      if (session.frozen === true) return; // already has a tangent
      forkCurrentChat(session.id);
    });
    bindLoadingState("chat.fork", forkBtn);
  }

  if (mergeBtn !== null) {
    mergeBtn.addEventListener("click", () => {
      const session = getActive();
      if (session === undefined) return;
      if (session.is_tangent !== true) return;
      mergeCurrentTangent(session.id, session.parent_chat_id);
    });
    bindLoadingState("chat.merge_tangent", mergeBtn);
  }

  // Show/hide pills + dim parent prompt form based on active session state.
  effect(() => {
    version.value;
    const session = getActive();
    if (session === undefined) {
      forkBtn?.classList.add("hidden");
      mergeBtn?.classList.add("hidden");
      return;
    }
    // Fork: hidden if frozen (parent of active tangent) or if this IS a tangent.
    forkBtn?.classList.toggle("hidden", session.frozen === true || session.is_tangent === true);
    // Merge: shown only when this IS a tangent.
    mergeBtn?.classList.toggle("hidden", session.is_tangent !== true);

    // Frozen state: dim the prompt form.
    const form = document.getElementById("prompt-form");
    if (form !== null) {
      form.classList.toggle("frozen", session.frozen === true);
    }
  });
}

/** Imperative fork entry-point. Called by the fork pill click handler. */
function forkCurrentChat(chatID: string): void {
  const session = get(chatID);
  if (session === undefined) return;
  if (session.frozen === true) return;
  const tangentID = `tangent-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 6)}`;
  void forkChat.dispatch({ chatID: session.id, tangentID }, {
    onSuccess: () => {
      void loadList().then(() => {
        // Read fresh session state — name/agent may have changed while
        // the fork was scope-queued behind another chat mutation.
        const fresh = get(chatID);
        const name = fresh?.name ?? session.name;
        const agent = fresh?.agent ?? session.agent;
        openChatTab(tangentID, `Tangent: ${name}`, agent);
        activateChatView(tangentID);
      });
    },
    onError: () => {
      // Server may have accepted the fork but the response was lost.
      // Refresh the chat list to detect server-side state mismatch.
      console.warn("[tangent] fork dispatch failed — refreshing list to detect server-side state");
      void loadList();
    },
  });
}

/** Imperative merge entry-point. Called by the merge-tangent pill click handler.
 *  Server-side: merges last Q&A pair from tangent to parent, unfreezes parent,
 *  deletes tangent. The deletion fires SSE `chat_deleted` (closes tangent tab)
 *  and the parent unfreeze fires `chat_updated` (re-enables prompt). We
 *  proactively activate the parent for instant feedback before SSE arrives. */
function mergeCurrentTangent(tangentID: string, parentID: string | undefined): void {
  void mergeTangent.dispatch(tangentID, {
    onSuccess: () => {
      // Activate parent chat immediately. SSE chat_deleted will close the
      // (now-inactive) tangent tab; SSE chat_updated will refresh frozen state.
      if (parentID !== undefined) activateChatView(parentID);
      void loadList();
    },
    onError: () => {
      // Merge can fail if the tangent has no complete user+assistant exchange
      // to merge, or if the parent was deleted. The framework already toasted
      // "Couldn't merge tangent: <reason>"; refresh state to detect server-side
      // changes (e.g. parent gone) so the UI stays consistent.
      void loadList();
    },
  });
}
