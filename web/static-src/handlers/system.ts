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
  getSessions, getActiveId, get, setThinking, loadList, loadMessages,
  setAvailableCommands, setCurrentMode, clearMsgIndex, invalidateSession,
} from "../store.js";
import { refreshCompactionThreshold } from "../status.js";
import { refreshRetention } from "../retention.js";
import { closeTab, hasTab, getOpenTabIDs } from "../tabs.js";

onSSE("settings_updated", () => {
  // Reconcile our cache from the server's view. Use restoreLastModel
  // (cache-only) rather than setLastModel — setLastModel calls
  // patchSettings, which writes the value back to the server, which
  // re-broadcasts settings_updated, looping forever at debounce speed.
  void syncSettings().then((s) => {
    if (s.last_model !== undefined) restoreLastModel(s.last_model);
  });
  refreshCompactionThreshold();
  void refreshRetention();
});

onBus(BUS_TRANSPORT_GAP, (_gap) => {
  // Whole-store reconcile:
  //   1. Clear the local `thinking` flag on every chat. We lost the
  //      turn_ended/error events that would normally clear it, so the
  //      send button would otherwise stay "busy" forever. The server
  //      is the source of truth — if a turn is genuinely still in
  //      flight, the next SSE event (or a prompt 409) will flip it
  //      back. Clearing eagerly is safe because the UI only reads
  //      `thinking` to lock input; a false negative (clearing while
  //      actually busy) causes at worst a user-visible momentary
  //      "idle" that corrects on the next event, which is much less
  //      bad than a permanently wedged send button.
  //   2. Reload the header list so name / usage / mode changes we
  //      missed during the outage reconcile.
  //   3. If a chat is active, reload its messages from scratch so
  //      streaming tails and tool-call updates heal.
  for (const s of getSessions()) {
    if (s.thinking) setThinking(s.id, false);
  }
  void loadList().then(() => {
    // Reconcile tabs: close any chat/plan tab whose session no longer
    // exists on the server. During a gap the server may have deleted
    // chats (user action on another device, retention cleanup, tangent
    // discard) and we missed the chat_deleted SSE.
    const sessionIDs = new Set(getSessions().map((s) => s.id));
    // Walk open tabs via the tabs module (avoids DOM scraping).
    for (const id of getOpenTabIDs()
      .filter((id) => id !== "" && !id.startsWith("__") && !id.startsWith("editor:"))) {
      if (!sessionIDs.has(id) && hasTab(id)) closeTab(id);
    }
  });
  const id = getActiveId();
  if (id !== "") void loadMessages(id);
});

// commands_updated arrives once per session after session/new, and
// again whenever kiro-cli's available command set changes (e.g. when
// an MCP server loads and exposes new prompts). The store copies the
// list onto the chat so the slash-command popover can read the
// current catalog synchronously. Payload is kiro-cli's
// _kiro.dev/commands/available pre-filtered of terminal-only entries
// (see translate_commands.go's handleCommandsAvailable).
onSSE("commands_updated", (chatID, payload) => {
  if (chatID === "") return;
  setAvailableCommands(chatID, payload.commands, payload.prompts);
});

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
  if (chatID === "") return;
  if (typeof p.mode_id !== "string" || p.mode_id === "") return;
  setCurrentMode(chatID, p.mode_id);
});

// Steering inclusion: render "Context loaded: tech.md, go.md" badges
// in the message list when kiro-cli reports which steering docs were loaded.
onSSE("steering_loaded", (chatID, payload) => {
  if (chatID === "" || getActiveId() !== chatID) return;
  if (!Array.isArray(payload?.documents)) return;
  const docs = payload.documents;
  if (docs.length === 0) return;
  const msgs = document.getElementById("messages");
  if (msgs === null) return;
  // Dedup: skip if a steering-badge already exists for this chat
  if (msgs.querySelector(".steering-badge") !== null) return;
  const badge = document.createElement("div");
  badge.className = "steering-badge";
  badge.textContent = `Context loaded: ${docs.join(", ")}`;
  msgs.appendChild(badge);
});

// checkpoint_restored arrives after the server rolls the workspace back
// and truncates the chat transcript to match. The new-turn tail is now
// gone on disk, so the cleanest recovery is: drop our cached messages
// and refetch. The server's own chat_updated broadcast fires first and
// updates the header (message_count, oldest_checkpoint_tag); we follow
// up by reloading messages so the DOM drops stale checkpoint lines
// referring to truncated turns.
onSSE("checkpoint_restored", (chatID, _payload) => {
  if (chatID === "") return;
  const s = get(chatID);
  if (s === undefined) return;
  // Clear local messages so renderSwitch starts fresh on the next
  // loadMessages response. The activeId check guards against
  // reloading a chat the user isn't looking at (server-side archive
  // flow etc. don't restore checkpoints, but defence in depth).
  // Clear the auto-approve crew cache — checkpoint restore may have
  // rolled back past the crew event that set it.
  delete (s as unknown as Record<string, unknown>)["_hasCrew"];

  if (getActiveId() === chatID) {
    s.messages = [];
    s.has_more = false;
    clearMsgIndex(chatID);
    // Rely on the version-effect's renderUpdates (triggered by
    // loadMessages bumping version) rather than an explicit
    // renderSwitch here — avoids a redundant intermediate render
    // that flashes empty state before messages arrive.
    void loadMessages(chatID);
  } else {
    // Background chat: just invalidate the cache so the next switch
    // refetches from scratch.
    invalidateSession(chatID);
  }
});
