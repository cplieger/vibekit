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
import { dropDecisions } from "../decision-dock.js";
import { loadList, loadMessages } from "../store-load.js";
import { clearTurnState } from "../turn-teardown.js";
import { refreshTurnRail } from "../turn-rail.js";
import { refreshRetention } from "../retention.js";
import { rebuildLiveRuns } from "../run-store.js";

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
    // Null means the refetch failed. Nothing is applied: the local cache is the
    // last thing the server told us, which is a better answer than a value
    // invented here, and the next frame or reload re-syncs.
    if (s === null) {
      return;
    }
    // The `!== undefined` guards these two reads used to carry are gone with the
    // optional fields: an empty last_model is a real value meaning "nothing
    // remembered", and restoreLastModel already treats it that way.
    restoreLastModel(s.last_model);
    restoreLastEffort(s.last_effort);
    // A theme chosen on ANOTHER device lands here, which is the behaviour the
    // retired whole-document arrangement broadcast used to carry. Safe against a
    // loop from both sides: syncSettings has just seeded the write tracker from
    // this very payload, and the adopt path suppresses the write-back anyway.
    adoptThemeFromSettings(s);
  });
  void refreshRetention();
});

onBus(BUS_TRANSPORT_GAP, (_gap) => {
  // Whole-store reconcile, and the shape is UNLATCHING: a gap says the replay ring
  // no longer covers what this client missed, so every claim it can no longer
  // support comes down and the connect replay in the same burst re-establishes
  // whatever is still true (one authoritative `turn_state` per chat with an open
  // turn, the whole pending-ask set, a retained waiting status). What it must NOT
  // do is assert an outcome: nothing here knows how anything finished.
  //   1. Bump the sync epoch, so every loaded window and rail index becomes a
  //      claim this client no longer supports.
  //   2. Per chat: unlatch the claims, then run the shared turn teardown.
  //   3. Reload the header list so name / usage / mode changes reconcile.
  //   4. If a chat is active, reload its messages and rail from scratch so
  //      streaming tails and tool-call updates heal.
  //
  // FIRST, before any heal below goes out: the heals must capture the new
  // epoch to count as fresh, and any fetch already in flight captured the old
  // one, which is what keeps its answer from claiming to have survived the
  // gap. Background chats are NOT refetched here — their loadedEpoch and rail
  // records now read stale, so each heals on its next activation.
  bumpSyncEpoch();
  for (const s of getSessions()) {
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
    // Steers are KAS's state, and a gap means we may have missed the frames that
    // resolved or dropped them. Emptying the dock is the honest default for the
    // same reason `thinking` is cleared above: a row claiming the agent has not
    // read something is a claim this client can no longer support.
    //
    // FORGOTTEN, not promoted. The turn-boundary path leaves a "not delivered"
    // note; doing that here would assert "the agent never read this" on no
    // evidence, when the likeliest truth is that the frame saying it did was
    // among the dropped ones. Notes already established stay.
    //
    // And nothing brings the dock back: `streamInitialState`
    // (`internal/agent/sse.go`) replays pending permissions, `turn_state` and the
    // waiting status, and NOT the steering buffer. So a gap mid-turn loses the
    // rows for the rest of that turn. Recovering them is a server change (a
    // per-busy-bridge `steer_queued` replay) and is recorded as a follow-up.
    forgetSteers(s.id);
    // Everything a gap and a turn ending agree on: thinking, both in-flight
    // markers, the transient banners, a stranded model switch and the dot. The
    // gap door spelled this itself and was short by four effects, which is what
    // left a rate-limit banner over a finished turn and a chunk watermark that
    // dropped the next turn's early deltas.
    clearTurnState(s.id);
  }
  // There is no tab reconcile here any more, and its absence is the point. This
  // loop used to close any chat tab whose session had left GET /api/chats —
  // membership by set difference, over two collections fetched separately, which
  // is the shape that closed tabs nobody closed. The tab set is its own
  // server-owned collection now, so a gap is answered by re-reading THAT
  // collection (app.ts wires `transport:gap` to `listTabs`), and a chat the
  // server deleted has already had its tabs closed by the membership
  // coordinator.
  void loadList();
  // The live-runs inventory is event-fed, and the gap means events were lost:
  // a run that started or settled inside the outage leaves it blind in either
  // direction. Re-read the server's presence-based projection; a failed
  // rebuild keeps the event-fed state (run-store.ts owns that rule).
  void rebuildLiveRuns();
  const id = getActiveId();
  if (id !== "") {
    void loadMessages(id);
    // The rail's half of the same heal: only the ACTIVE chat's index is re-read
    // eagerly (both fetches start after the bump above, so both records claim
    // the new epoch); every other chat's stale record is caught by the gate on
    // its next activation.
    void refreshTurnRail(id);
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
