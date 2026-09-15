// ---------------------------------------------------------------------------
// System-level handlers: connected, settings_updated, transport:reconcile,
// transport:resumed, the two connect-hook snapshots, compaction_started,
// mode_changed.
//
// SSE events flow through bus (onSSE). `transport:reconcile` is a client-side
// event the SSE adapter emits when every claim this client holds is void (the
// stream bound to a new hub epoch, or the digest answered must_refetch); the
// body re-reads the whole projection. The everyday wake is NOT this path: the
// adapter's digest refetches exactly the subjects that moved.
// ---------------------------------------------------------------------------

import { onSSE, onBus, BUS_RECONCILE, BUS_PAGE_RESUMED, decodeEnvelope, dispatch } from "../bus.js";
import { adoptThemeFromSettings, syncSettings } from "../settings.js";
import { restoreLastModel, restoreLastEffort } from "../session-context.js";
import { setWorkspaceRoot } from "../workspace.js";
import {
  getSessions,
  setAgentStatus,
  setCurrentMode,
  forgetSteers,
  clearTurnFailed,
  clearTurnDone,
} from "../store.js";
import { refreshActiveView } from "../tabs.js";
import { dropDecisions, dropRunDecisions } from "../decision-dock.js";
import { closeNotificationsExcept } from "../notify.js";
import { askTarget, chatTarget, pushTargetTag, runTarget } from "../push-subject.js";
import { loadList, scheduleListRetry } from "../store-load.js";
import { clearTurnState, retractStaleThinking } from "../turn-teardown.js";
import { refreshRetention } from "../retention.js";
import { adoptConnectRuns, invalidateCachedRuns, rebuildLiveRuns } from "../run-store.js";
import { fetchCatalog } from "../session-catalog.js";
import { invalidateTurnRails } from "../turn-rail.js";
import type { SSEPayloads } from "../bus.js";
import type { ServerEvent } from "../types.js";
import type { ConnectedPayload, RunInputNeededPayload } from "../wire/types.gen.js";

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
    restoreLastEffort(s.last_effort_by_model);
    // A theme chosen on another device lands here. Safe against a loop:
    // syncSettings already seeded the write tracker from this payload.
    adoptThemeFromSettings(s);
  });
  void refreshRetention();
});

onBus(BUS_RECONCILE, ({ cause, signal }) => {
  // Every claim this client holds is void; the fresh hello's own hook frames
  // (`pending_snapshot`, `status_snapshot`) re-establish the pending and waiting
  // sets, so nothing here touches the docks. Must NOT assert an outcome: nothing
  // here knows how anything finished.
  const sessions = getSessions();
  for (const s of sessions) {
    clearTurnFailed(s.id);
    clearTurnDone(s.id);
    clearTurnState(s.id);
  }
  console.warn(`[reconcile:${cause}] tore down`, sessions.length, "sessions");
  // No tab reconcile here: the tab set is its own server-owned collection, so a
  // reconcile is answered by re-reading it (app.ts wires this event to `listTabs`);
  // a deleted chat's tabs are already closed by the coordinator.
  // A failed list load here leaves the sidebar holding rows the reconcile already
  // licensed dropping, with nothing else scheduled to re-read it before the next
  // one. `scheduleListRetry` decides whether the failure was the SERVER's — see its
  // reach gate.
  void loadList(signal).then((ok) => {
    if (!ok) {
      scheduleListRetry();
    }
  });
  // ONE token for both run readers below: they act on the same event a network round
  // trip apart, so without it every live run is fetched twice. run-store.ts
  // `answeredCause` owns what the token means.
  const token = `reconcile:${cause}`;
  // The live-runs inventory is event-fed, so a lost stream leaves it blind to any
  // run that started or settled meanwhile; re-read the server's presence-based
  // projection.
  void rebuildLiveRuns(token, signal);
  // A run's node state is APPLIED from `run_progress` rather than refetched, so
  // frames lost leave a stale tree with nothing to notice it. This is the one
  // moment the client knows it may have missed some.
  invalidateCachedRuns(token);
  // The mode/model catalog is a workspace fact the server holds in memory and
  // announces on no frame, so a `config_option_update` during the outage, or a
  // server restart that empties the holder, leaves the picker on whatever boot
  // answered.
  void fetchCatalog({ signal });
  // The rail's `GET /api/chats/{id}/turns` carries no stamp, so its records are
  // invalidated by hand here; each chat re-reads its index at its next activation.
  invalidateTurnRails();
  // LAST: the active view's own gate reads the map the bind just cleared, so the
  // refresh it decides on is a real one.
  refreshActiveView();
});

/** The tag a pending item's banner carries, computed the way the handler that
 *  showed it did: a run ask's is the run's (whichever chat the envelope is keyed
 *  to), a request-shaped ask's is `askTarget`'s (the run it is about when it is a
 *  step's, else its chat), every other item's is its chat's. */
function liveAskTag(evt: ServerEvent): string {
  const chatID = evt.chat_id ?? "";
  switch (evt.type) {
    case "run_input_needed":
      return pushTargetTag(runTarget((evt.payload as RunInputNeededPayload).workflow_id));
    case "permission_needed":
    case "elicitation_needed":
    case "user_input_needed":
      return pushTargetTag(askTarget(chatID, (evt.payload as SSEPayloads[typeof evt.type]).run_id));
    default:
      return pushTargetTag(chatTarget(chatID));
  }
}

// The connect hook's pending set, WHOLE and possibly empty: every unanswered
// permission, run ask and steer across every chat, as the envelopes the live path
// would have published. Replace, never merge — a row resolved on another device
// while this one was away left no frame behind, and only an atomic replacement
// with the current set takes it off the screen. Every item re-dispatches through
// the ordinary envelope door, so its handler is the live one.
onSSE("pending_snapshot", (_chatID, p) => {
  // Decoded first, so the set of chats the snapshot still names is known before
  // anything is dropped; a malformed item is reported and skipped, not fatal to the
  // frame.
  const items: ServerEvent[] = [];
  for (const item of p.items) {
    try {
      items.push(decodeEnvelope(item));
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      console.error("sse: pending_snapshot item rejected:", msg);
    }
  }
  // Every chat or run banner the set does not name comes down, whether or not this
  // tab ever rendered the ask: the tab that slept through it is the one holding it.
  void closeNotificationsExcept(new Set(items.map(liveAskTag)));
  for (const s of getSessions()) {
    dropDecisions(s.id);
    // FORGOTTEN, not promoted — asserting "never read" here would be a guess. The
    // items below re-offer whatever is still in KAS's buffer under its own id; what
    // cannot be recovered is a steer the agent read while this client was away,
    // whose note the mark already carries.
    forgetSteers(s.id);
  }
  // A run's own asks are keyed to `run:<workflowId>`, which is no chat and so has no
  // session row for the loop above to reach.
  dropRunDecisions();
  for (const evt of items) {
    dispatch(evt);
  }
});

// The connect hook's retained waiting-status set, WHOLE and possibly empty: the
// agent status of every chat is REPLACED from it, so a `waiting_on_user` answered
// elsewhere while this client was away clears, and one still owed comes back on a
// second device. A live `chat_status` later on the same connection re-sets a
// running chat's own declaration.
onSSE("status_snapshot", (_chatID, p) => {
  const rows = new Map(p.rows.map((r) => [r.chat_id, r]));
  for (const s of getSessions()) {
    const row = rows.get(s.id);
    if (row === undefined) {
      setAgentStatus(s.id, "");
    } else {
      setAgentStatus(s.id, row.status);
    }
  }
});

// A resume says real time passed unobserved. Every view whose kind has a digest
// subject is answered by the adapter's digest, which runs right after this; the
// active view of any OTHER kind (a run tree, a file listing, the settings page)
// has no subject to be asked about and would stay stale until the reader switched
// tabs, so it refreshes here as it always has.
onBus(BUS_PAGE_RESUMED, () => {
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
