// ---------------------------------------------------------------------------
// System-level handlers: settings_updated, transport:gap,
// compaction_started.
//
// SSE events flow through bus (onSSE). `transport:gap` is a client-side event
// emitted by transport.ts on reconnect when the server's replay ring no
// longer covers our last-seen event id; we refetch the active chat and
// refresh the header list so stale usage/names/model counters reconcile.
// ---------------------------------------------------------------------------

import { onSSE, onBus, BUS_TRANSPORT_GAP } from "../bus.js";
import { adoptThemeFromSettings, syncSettings } from "../settings.js";
import { restoreLastModel, restoreLastEffort } from "../session-context.js";
import { setWorkspaceRoot } from "../workspace.js";
import {
  getSessions,
  getActiveId,
  setAgentStatus,
  setCurrentMode,
  forgetSteers,
  clearTurnFailed,
  clearTurnDone,
  bumpSyncEpoch,
} from "../store.js";
import { dropDecisions, dropRunDecisions } from "../decision-dock.js";
import { loadList, loadMessages } from "../store-load.js";
import { clearTurnState } from "../turn-teardown.js";
import { refreshTurnRail } from "../turn-rail.js";
import { refreshRetention } from "../retention.js";
import { invalidateCachedRuns, rebuildLiveRuns } from "../run-store.js";

// The handshake states the workspace root — the only way the client learns
// where the workspace is, needed to make relative agent paths openable.
// Recorded here rather than in transport.ts, whose handshake hook returns
// early on the first connection of a page load and would miss it.
onSSE("connected", (_chatID, p) => {
  if (typeof p.workspace === "string" && p.workspace !== "") {
    setWorkspaceRoot(p.workspace);
  }
});

onSSE("settings_updated", () => {
  // Use restoreLastModel (cache-only), not setLastModel — that calls
  // patchSettings, which re-broadcasts settings_updated, looping forever.
  void syncSettings().then((s) => {
    // Null means the refetch failed; the local cache is a better answer
    // than inventing one, and the next frame or reload re-syncs.
    if (s === null) {
      return;
    }
    restoreLastModel(s.last_model);
    restoreLastEffort(s.last_effort, s.last_effort_model);
    // A theme chosen on another device lands here. Safe against a loop:
    // syncSettings already seeded the write tracker from this payload.
    adoptThemeFromSettings(s);
  });
  void refreshRetention();
});

onBus(BUS_TRANSPORT_GAP, (_gap) => {
  // A gap says the replay ring no longer covers what this client missed, so
  // every claim it can no longer support comes down, and the connect replay
  // in the same burst re-establishes whatever is still true. Must NOT assert
  // an outcome: nothing here knows how anything finished.
  //   1. Bump the sync epoch, so loaded windows/rail indices become stale.
  //   2. Per chat: unlatch the claims, then run the shared turn teardown.
  //   3. Reload the header list so name/usage/mode changes reconcile.
  //   4. If a chat is active, reload its messages and rail from scratch.
  //
  // Bump FIRST: the heals below must capture the new epoch to count as
  // fresh. Background chats are not refetched — they heal on next activation.
  bumpSyncEpoch();
  for (const s of getSessions()) {
    // Agent-declared status is as untrustworthy as `thinking` after a gap.
    setAgentStatus(s.id, "", "");
    clearTurnFailed(s.id);
    clearTurnDone(s.id);
    // Unresolved requests are re-pushed by the connect replay (every kind,
    // on every connect), which lands after this handler.
    dropDecisions(s.id);
    // Steers are KAS's state; a gap may have dropped the frames that
    // resolved them. FORGOTTEN, not promoted — asserting "never read" here
    // would be a guess, and `streamInitialState` does not replay the
    // steering buffer, so a gap mid-turn loses the rest of that turn's rows.
    forgetSteers(s.id);
    clearTurnState(s.id);
  }
  // A run's own asks are keyed to `run:<workflowId>`, which is no chat and so has
  // no session row for the loop above to reach. Same reasoning as dropDecisions:
  // the connect replay re-offers whatever is still open, and it does NOT replay
  // the settle, so an ask answered during the outage would otherwise keep its card.
  dropRunDecisions();
  // No tab reconcile here: the tab set is its own server-owned collection,
  // so a gap is answered by re-reading it (app.ts wires `transport:gap` to
  // `listTabs`); a deleted chat's tabs are already closed by the coordinator.
  void loadList();
  // The live-runs inventory is event-fed, so a gap leaves it blind to any
  // run that started or settled during the outage; re-read the server's
  // presence-based projection.
  void rebuildLiveRuns();
  // A run's node state is APPLIED from `run_progress` rather than refetched, so
  // frames lost in the outage leave a stale tree with nothing to notice it. This
  // is the one moment the client knows it missed some.
  invalidateCachedRuns();
  const id = getActiveId();
  if (id !== "") {
    void loadMessages(id);
    void refreshTurnRail(id);
  }
});

// The slash-command catalog is gone, server and client, and should not come
// back as a palette: of 90 commands a session reports, only 13 skills have
// no other door (agent names map to modes, workflows to the config browser,
// steering to attachment) and none is invocable — see vibekit.md "Slash
// commands". Skills are discoverable instead, on the /docs Skills tab.

// compaction_started is advisory only: `thinking` is already true (set by
// the prompt send), and completion persists as a `compacted` event message
// through the normal message_appended path.
onSSE("compaction_started", () => {
  // intentional no-op
});

// Mode switch echo: reflects an agent-initiated mode change so any UI
// reading current_mode_id stays current without waiting for the next
// chat_updated rebuild.
onSSE("mode_changed", (chatID, p) => {
  if (chatID === "") {
    return;
  }
  if (typeof p.mode_id !== "string" || p.mode_id === "") {
    return;
  }
  setCurrentMode(chatID, p.mode_id);
});
