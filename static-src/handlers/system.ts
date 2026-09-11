// ---------------------------------------------------------------------------
// System-level handlers: settings_updated, transport:gap,
// compaction_started.
//
// SSE events flow through bus (onSSE). `transport:gap` is a client-side event
// emitted by transport.ts on reconnect when the server's replay ring no
// longer covers our last-seen event id; we refetch the active chat and
// refresh the header list so stale usage/names/model counters reconcile.
// ---------------------------------------------------------------------------

import { onSSE, onBus, BUS_TRANSPORT_GAP, BUS_PAGE_RESUMED } from "../bus.js";
import { adoptThemeFromSettings, syncSettings } from "../settings.js";
import { restoreLastModel, restoreLastEffort } from "../session-context.js";
import { setWorkspaceRoot } from "../workspace.js";
import {
  getSessions,
  setAgentStatus,
  setCurrentMode,
  forgetSteers,
  steerCount,
  clearTurnFailed,
  clearTurnDone,
} from "../store.js";
import { bumpSyncEpoch, forgetAllViews } from "../tab-freshness.js";
import { refreshActiveView } from "../tabs.js";
import { dropDecisions, dropRunDecisions } from "../decision-dock.js";
import { loadList, scheduleListRetry } from "../store-load.js";
import { clearTurnState, retractStaleThinking } from "../turn-teardown.js";
import { refreshRetention } from "../retention.js";
import { adoptConnectRuns, invalidateCachedRuns, rebuildLiveRuns } from "../run-store.js";
import { fetchCatalog } from "../session-catalog.js";
import type { ConnectedPayload } from "../wire/types.gen.js";

/** Numbers the gaps, so each one's run readers share a token no other gap can
 *  match. Monotonic per page: a counter rather than a random id because the only
 *  property needed is that two gaps differ. */
let gapSeq = 0;

// The handshake states the workspace root — the only way the client learns
// where the workspace is, needed to make relative agent paths openable.
// Recorded here rather than in transport.ts, whose handshake hook returns
// early on the first connection of a page load and would miss it.
onSSE("connected", (_chatID, p) => {
  if (typeof p.workspace === "string" && p.workspace !== "") {
    setWorkspaceRoot(p.workspace);
  }
  retractUnconfirmedThinking(p);
  // OUTSIDE the retraction's own gate on purpose: the inventory is workspace-global, so
  // it is not scoped by the chat filter and carries its own completeness flag, and an
  // early return inside the retraction must not swallow it.
  adoptConnectRuns(p);
});

/** Stop believing a `thinking` the server does not confirm.
 *
 *  Never writes `setTurnOpen`, which keeps that field at ONE writer (the newest-page window
 *  GET) and would read false for a step turn that is genuinely streaming; the residual, shared
 *  with A2's `run` arm, is a chat whose window was fetched while a step turn was open reading
 *  LIVE until its next fetch. ORDERING: on a RECONNECT this lands before `loadList`, on the
 *  FIRST connect it does not (`transport.ts` holds every frame while `!hydrated`), and
 *  `relatchTurnVerdict`'s header fallback is what buys the order-independence. */
function retractUnconfirmedThinking(p: ConnectedPayload): void {
  // A scoped or over-cap list states NOTHING about the chats it omits, so the flag is what
  // bounds the blast radius rather than making a clear over a live turn merely unlikely.
  if (!p.busy_stated) {
    return;
  }
  const busy = new Set(p.busy_chats ?? []);
  let cleared = 0;
  for (const s of getSessions()) {
    if (!s.thinking || busy.has(s.id)) {
      continue;
    }
    // The NARROW retraction, never `healSettledChat`: `busy_chats` deliberately omits a chat
    // whose only open turn is a workflow STEP, so a chat reached here may still have one
    // streaming — and a busy-but-UNDECLARED chat gets the bare busy signal with no `message`,
    // so nothing would re-note the live-turn marker a full teardown drops.
    retractStaleThinking(s.id);
    cleared++;
  }
  if (cleared > 0) {
    console.warn(
      `[connect] cleared ${String(cleared)} stale thinking latches the server does not confirm`,
    );
  }
}

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
  // A gap drops every claim this client can no longer support; the connect replay
  // in the same burst re-establishes whatever is still true. Must NOT assert an
  // outcome: nothing here knows how anything finished.
  //
  // Bump the epoch FIRST, or a heal below captures the old one and stamps its
  // answer as fresh. Only the active VIEW is refreshed; every other view heals at
  // its next activation.
  bumpSyncEpoch();
  const sessions = getSessions();
  let forgotten = 0;
  for (const s of sessions) {
    // Agent-declared status is as untrustworthy as `thinking` after a gap.
    setAgentStatus(s.id, "", "");
    clearTurnFailed(s.id);
    clearTurnDone(s.id);
    // Unresolved requests are re-pushed by the connect replay (every kind,
    // on every connect), which lands after this handler.
    dropDecisions(s.id);
    // Steers are KAS's state; a gap may have dropped the frames that
    // resolved them. FORGOTTEN, not promoted — asserting "never read" here
    // would be a guess. The connect replay DOES re-offer whatever is still in
    // KAS's buffer (`replayPendingSteers`), so a row still waiting comes back
    // under its own id; what a gap cannot recover is a steer the agent read
    // during the outage, whose note the mark already carries.
    forgotten += steerCount(s.id);
    forgetSteers(s.id);
    clearTurnState(s.id);
  }
  // The one line that separates the two triggers behind "my steer vanished while
  // I was away": a gap here means the dock was force-emptied and the connect
  // replay is what refills it, and its ABSENCE with a dock that emptied anyway
  // means the turn simply ended and the steer was never read. Nothing else on
  // either path is observable from the outside.
  console.warn("[gap] tore down", sessions.length, "sessions, forgot", forgotten, "waiting steers");
  // A run's own asks are keyed to `run:<workflowId>`, which is no chat and so has
  // no session row for the loop above to reach. Same reasoning as dropDecisions:
  // the connect replay re-offers whatever is still open, and it does NOT replay
  // the settle, so an ask answered during the outage would otherwise keep its card.
  dropRunDecisions();
  // No tab reconcile here: the tab set is its own server-owned collection,
  // so a gap is answered by re-reading it (app.ts wires `transport:gap` to
  // `listTabs`); a deleted chat's tabs are already closed by the coordinator.
  // A failed list load here leaves the sidebar holding rows the gap already licensed
  // dropping, with nothing else scheduled to re-read it before the next reconnect.
  // `scheduleListRetry` decides whether the failure was the SERVER's — see its reach gate.
  void loadList().then((ok) => {
    if (!ok) {
      scheduleListRetry();
    }
  });
  // ONE token for both run readers below: they act on the same event a network round
  // trip apart, so without it every live run is fetched twice. Minted here because the
  // gap is the cause; run-store.ts `answeredCause` owns what the token means.
  const cause = `gap:${String(++gapSeq)}`;
  // The live-runs inventory is event-fed, so a gap leaves it blind to any
  // run that started or settled during the outage; re-read the server's
  // presence-based projection.
  void rebuildLiveRuns(cause);
  // A run's node state is APPLIED from `run_progress` rather than refetched, so
  // frames lost in the outage leave a stale tree with nothing to notice it. This
  // is the one moment the client knows it missed some.
  invalidateCachedRuns(cause);
  // The mode/model catalog is a workspace fact the server holds in memory and announces on
  // no frame, so a `config_option_update` during the outage, or a server restart that
  // empties the holder, leaves the picker on whatever boot answered. A gap is the one
  // signal this client gets, and this handler is the ONE reader of that endpoint.
  void fetchCatalog();
  // LAST, so it stays after the bump above: a refresh that captured the old epoch
  // would stamp its answer as fresh.
  refreshActiveView();
});

// A resume says real time passed unobserved, which undermines every view rather than
// only the one on screen — a chat marked `loaded` at the current epoch reads FRESH for
// the life of the document, so an in-app switch back to it costs zero fetches and shows
// the pre-suspension window. So the freshness RECORDS go and the refresh follows, in that
// order, or the active view's own gate reads the record the drop is about to remove.
//
// The records rather than the epoch, which is `forgetAllViews`' own subject: a resume
// dropped no frames, so a fetch that spanned the suspension is answered from the server's
// current state and stranding it would buy a second fetch for nothing. Every other view
// pays its one refresh when the reader activates it.
onBus(BUS_PAGE_RESUMED, () => {
  forgetAllViews();
  refreshActiveView();
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
