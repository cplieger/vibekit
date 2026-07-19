// ---------------------------------------------------------------------------
// System-level handlers: settings_updated, transport:gap, commands_updated,
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
import {
  getSessions,
  getActiveId,
  setThinking,
  setAgentStatus,
  setCurrentMode,
  invalidateSession,
} from "../store.js";
import { loadList, loadMessages } from "../store-load.js";
import { maybeDrainIfIdle } from "../prompt-queue.js";
import { drainModelSwitchQueue } from "../model-switcher.js";
import { refreshCompactionThreshold } from "../status.js";
import { refreshRetention } from "../retention.js";
import { closeTab, hasTab, getOpenTabIDs } from "../tabs.js";

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
    // A prompt queued before the outage would strand if we missed the
    // turn_ended that should have drained it: thinking is now cleared, so no
    // future turn_ended will fire for that turn. Re-drain any chat that is
    // idle with a pending queue so the queued prompt (the user's intent)
    // still gets sent instead of sitting forever behind a "queued" button.
    maybeDrainIfIdle(s.id);
    // Same reasoning for a queued mid-turn model switch: its drain rides
    // turn_ended, which we missed during the gap. Fire it for the now-idle
    // chat so the switch isn't stranded behind a stuck ".pending" pill.
    drainModelSwitchQueue(s.id);
  }
  void loadList().then(() => {
    // Reconcile tabs: close any chat/plan tab whose session no longer
    // exists on the server. During a gap the server may have deleted
    // chats (user action on another device, retention cleanup, tangent
    // discard) and we missed the chat_deleted SSE.
    const sessionIDs = new Set(getSessions().map((s) => s.id));
    // Walk open tabs via the tabs module (avoids DOM scraping).
    for (const id of getOpenTabIDs().filter(
      (id) => id !== "" && !id.startsWith("__") && !id.startsWith("editor:"),
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

// commands_updated: decoded but currently UNCONSUMED. The server still
// broadcasts the per-session slash-command catalog (v3
// available_commands_update, filtered of browser-incompatible entries)
// and the wire decoder stays registered (wire/registry.gen.ts), but no
// client feature reads it today — typed slash commands like /compact
// ride the ordinary prompt envelope and kiro-cli parses them natively.
// A future type-ahead popover would subscribe here.

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

// checkpoint_restored arrives after the server rolls the workspace back
// and truncates the chat transcript to match. The server's own chat_updated
// broadcast fires first and updates the header (message_count,
// oldest_checkpoint_tag); we follow up by reloading messages so the DOM drops
// stale checkpoint lines referring to truncated turns.
onSSE("checkpoint_restored", (chatID, _payload) => {
  if (chatID === "") {
    return;
  }
  if (getActiveId() === chatID) {
    // Refetch-then-swap: loadMessages replaces the message array wholesale,
    // rebuilds the index, and emits ONCE — the keyed reconcile then trims the
    // rolled-back tail in a single render. Do NOT pre-clear messages here: the
    // old empties-then-refetches sequence painted an empty transcript for the
    // whole network round-trip (the flashing bug the render rewrite fixed).
    void loadMessages(chatID);
  } else {
    // Background chat: just invalidate the cache so the next switch refetches.
    invalidateSession(chatID);
  }
});
