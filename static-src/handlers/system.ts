// ---------------------------------------------------------------------------
// System-level handlers: settings_updated, transport:gap,
// compaction_started.
//
// SSE events flow through bus (onSSE). `transport:gap` is a client-side
// event emitted by transport.ts on reconnect when the server's replay
// ring no longer covers our last-seen event id; we refetch the active
// chat and refresh the whole header list so stale usage/names/model
// counters reconcile alongside the message history.
// ---------------------------------------------------------------------------

import { onSSE, onBus, BUS_TRANSPORT_GAP } from "../bus.js";
import { syncSettings } from "../settings.js";
import { restoreLastModel } from "../session-context.js";
import { setWorkspaceRoot } from "../workspace.js";
import {
  getSessions,
  getActiveId,
  setThinking,
  setAgentStatus,
  setCurrentMode,
  clearSteers,
  clearTurnFailed,
  clearTurnDone,
} from "../store.js";
import { dropDecisions } from "../decision-dock.js";
import { loadList, loadMessages } from "../store-load.js";
import { drainModelSwitchQueue } from "../model-switcher.js";
import { refreshCompactionThreshold } from "../status.js";
import { refreshRetention } from "../retention.js";
import { closeTab, hasTab, getOpenTabIDs, isEditorTabID } from "../tabs.js";

// The handshake states the workspace root. It is the only way the client learns
// where the workspace is, and every relative agent path needs it to become
// openable — see workspace.ts. Recorded here rather than in transport.ts, whose
// own handshake hook returns early on the first connection of a page load (it
// only cares about replay gaps) and would therefore miss it exactly once: on the
// connection that matters.
onSSE("connected", (_chatID, p) => {
  if (typeof p.workspace === "string" && p.workspace !== "") {
    setWorkspaceRoot(p.workspace);
  }
});

onSSE("settings_updated", () => {
  // Reconcile our cache from the server's view. Use restoreLastModel
  // (cache-only) rather than setLastModel — setLastModel calls
  // patchSettings, which writes the value back to the server, which
  // re-broadcasts settings_updated, looping forever at debounce speed.
  void syncSettings().then((s) => {
    if (s.last_model !== undefined) {
      restoreLastModel(s.last_model);
    }
  });
  refreshCompactionThreshold();
  void refreshRetention();
});

onBus(BUS_TRANSPORT_GAP, (_gap) => {
  // Whole-store reconcile:
  //   1. Clear the local `thinking` flag on every chat. We lost the
  //      turn_ended/error events that would normally clear it, so the
  //      send button would otherwise stay "busy" forever. This is the
  //      safe DEFAULT for chats the server says nothing about; the
  //      chats that ARE genuinely mid-turn get an authoritative
  //      turn_state event in the same connect replay (synthesized
  //      per busy chat, handlers/messages.ts), which re-sets thinking
  //      and restores the streaming transcript — so the old
  //      "false-idle until the next event" window collapses to the
  //      same replay burst.
  //   2. Reload the header list so name / usage / mode changes we
  //      missed during the outage reconcile.
  //   3. If a chat is active, reload its messages from scratch so
  //      streaming tails and tool-call updates heal.
  for (const s of getSessions()) {
    if (s.thinking) {
      setThinking(s.id, false);
    }
    // Agent-declared status is as untrustworthy as `thinking` after a
    // gap: the clearing chat_status may be among the dropped events.
    setAgentStatus(s.id, "", "");
    // Same reasoning for the two outcome latches. Each normally stands until the
    // next turn, but "the next turn" may have happened inside the outage, so a red
    // or green dot after a gap is a claim this client can no longer support.
    clearTurnFailed(s.id);
    clearTurnDone(s.id);
    // And the same for the dock's unanswered asks: the frames that answered or
    // abandoned them may be among the dropped events, so an `input` dot after a
    // gap is a claim this client cannot support either. Safe to clear rather than
    // guess, because every unresolved request is re-pushed by the connect replay
    // (`streamInitialState` lists the whole pending set, all three kinds, on EVERY
    // connect) — and that replay lands after this handler, which runs off the
    // `connected` frame itself.
    dropDecisions(s.id);
    // Steers are KAS's state, and a gap means we may have missed the frames
    // that resolved or dropped them. Clearing is the honest default for the same
    // reason `thinking` is cleared above: a chip claiming the agent has not read
    // something is a claim this client can no longer support. A turn still
    // running re-announces its buffer through the connect replay.
    clearSteers(s.id);
    // Same reasoning for a queued mid-turn model switch: its drain rides
    // turn_ended, which we missed during the gap. Fire it for the now-idle
    // chat so the switch isn't stranded behind a stuck ".pending" pill.
    drainModelSwitchQueue(s.id);
  }
  void loadList().then(() => {
    // Reconcile tabs: close any chat/plan tab whose session no longer
    // exists on the server. During a gap the server may have deleted
    // chats (user action on another device, retention cleanup, rewind
    // discard) and we missed the chat_deleted SSE.
    const sessionIDs = new Set(getSessions().map((s) => s.id));
    // Walk open tabs via the tabs module (avoids DOM scraping).
    for (const id of getOpenTabIDs().filter(
      (id) => id !== "" && !id.startsWith("__") && !isEditorTabID(id),
    )) {
      if (!sessionIDs.has(id) && hasTab(id)) {
        closeTab(id);
      }
    }
  });
  const id = getActiveId();
  if (id !== "") {
    void loadMessages(id);
  }
});

// The slash-command catalog is GONE, server and client, and should not come
// back as a palette. Measured: of the 90 commands a session reports here, 47 of
// the 49 custom-agent names are already mode ids reachable through the mode
// pill, the 5 workflow entries are launched from the configuration browser's
// own row, and the 23 steering entries map onto file attachment (declined with
// _kiro/session/context). That leaves the 13 skills as the only unique
// contribution — and there is no _kiro/skill* method anywhere in the bundle, so
// a skill entry can only be sent as text and hoped over. A palette where one
// category in four silently degrades to model prose teaches users that every
// entry is a command. Skills are DISCOVERABLE instead, on the /docs Skills tab.

// compaction_started is advisory. The `thinking` flag is already true
// at this point (set by the prompt send), and the completed state is
// persisted as a `compacted` event message server-side that appends
// through the normal message_appended path. No additional UI is
// needed today; this handler exists so the SSE type is wired and
// future enhancements (e.g. a "compacting..." toast) have a hook.
onSSE("compaction_started", () => {
  // intentional no-op; see comment above
});

// Mode switch echo: when the agent switches modes (via a switch_mode
// tool call that the user approved), the server broadcasts the new
// mode_id. Reflect it in the store so any UI reading current_mode_id
// stays current without waiting for the next chat_updated rebuild.
onSSE("mode_changed", (chatID, p) => {
  if (chatID === "") {
    return;
  }
  if (typeof p.mode_id !== "string" || p.mode_id === "") {
    return;
  }
  setCurrentMode(chatID, p.mode_id);
});
