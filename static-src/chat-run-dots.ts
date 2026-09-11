// ---------------------------------------------------------------------------
// The WORKFLOW mark on a chat's tab row: one effect, over every open chat tab,
// painting whether a run that chat launched is still going.
//
// THE HOLE THIS FILLS. `run-dots.ts` already paints exactly this state, on the
// wrong row: a `run:<workflowId>` TAB, and nothing opens one by itself — every
// door is a reader's. So a run launched from chat A, with no run tab opened, is
// invisible to a reader sitting in chat B, and A's own dot is no help: the
// launching turn ends as soon as `run_workflow` returns, so that dot goes green
// while the run carries on for another forty minutes. `run-bar.ts` covers the
// other half of the gap and only the other half — it renders the ACTIVE chat's
// live runs in the composer, so it can say runs exist and cannot say which chat.
//
// THE INPUT IS GLOBAL, which is the fact that makes this cheap. `run-store.ts`'s
// live-run inventory is keyed by the LAUNCHING chat and rebuilt from
// `GET /api/runs/live` at boot and after a transport gap, so it answers for every
// run in flight on the server whether or not that chat's tab was open when the run
// started. `liveRunsForChat` is the whole join a chat row needs — a chat tab's
// `ref` IS its chat id. No wire change, no endpoint, no Go change.
//
// N RUNS FOLD ONTO ONE MARK, by the dot vocabulary's own precedence minus the
// outcome states: input > waiting > working > nothing. That is the only shape
// consistent with what the strip already does — it has no count-badge idiom
// anywhere, and N marks would be unbounded width — and the cost is stated rather
// than hidden: "three runs, one failing" would read as "one run", so the COUNT and
// its breakdown go into the tooltip and the announced phrase, where they cost no
// second visual channel.
//
// A SETTLED RUN WITHDRAWS THE MARK rather than turning it green or red, and that
// is forced by the input: `noteRunSettled` deletes the inventory row at a terminal
// `run_finished`, so an outcome is simply not available here. `run-bar.ts` reads the
// same inventory and withdraws a settled run too — and it is NOT the precedent for
// the unfetched case, where it does the opposite: it KEEPS such a run and renders it
// in an unknown state, which this mark has no member for. `done` and `failed` are
// therefore refused AT THE FOLD rather than left latent, which makes the ceiling
// explicit instead of something a reader has to infer from a map that never fires.
//
// IT KEEPS NO STATE OF ITS OWN, modelled on `subagent-dots.ts` rather than on
// `run-dots.ts`: the tab set IS the membership, enumerated per pass, so a closed
// tab is simply not visited and there is nothing to sweep. Cost per pass is one
// scan of the live inventory per open chat tab, and that inventory is bounded to a
// handful by the single-run-per-recipe rule (`run-store.ts` `anyRunForChat` records
// why it is a scan rather than a second index). Cheaper than the shape it copies,
// which walks a message array.
// ---------------------------------------------------------------------------

import { effect } from "@cplieger/reactive";
import { hasTab, openChatRefs, setTabRunStatus, tabIdFor } from "./tabs.js";
import type { TabRunDotStatus, TabRunTally } from "./tabs.js";
import { runStatusFor, type RunPauseClass } from "./store.js";
import { runPendingAsks } from "./decision-dock.js";
import { isNeedInputPark, liveRunsForChat, peekLiveRun, runState } from "./run-store.js";

/** The fold's two halves: what the mark paints, and what its phrase says. */
interface RunFold {
  readonly status: TabRunDotStatus | "";
  readonly tally: TabRunTally;
}

/** Fold every live run this chat launched onto one mark.
 *
 *  Per run this is `run-dots.ts`'s own read, verbatim, so a run's mark on a chat
 *  row and the same run's dot on its own tab cannot disagree: the dock's ask joined
 *  by RUN id (a chat-parented run's ask is filed under the launching chat with the
 *  run stamped on the payload, which only that scan finds), the run's own status as
 *  a TRACKED read, and the park classified by the store rather than re-derived
 *  here.
 *
 *  A run whose cell has not resolved yet is answered by the INVENTORY's own
 *  `executing` flag, which is the first answer available: the row lands with the
 *  lifecycle frame or the boot rebuild, a round trip before the `inspect` that
 *  refines it. `executing === false` still contributes nothing — that flag reports
 *  whether THIS PROCESS holds a deadline for the run, so a parked lease and one read
 *  back from disk are indistinguishable there, and claiming a state would be
 *  guessing rather than reporting. */
function foldRuns(chatID: string): RunFold {
  let working = 0;
  let waiting = 0;
  let input = 0;
  for (const row of liveRunsForChat(chatID)) {
    const asking = runPendingAsks(row.id).count > 0;
    const state = runState(row.id);
    const pause: RunPauseClass = isNeedInputPark(state) ? "need_input" : "";
    const rowStatus =
      state === undefined
        ? runStatusFor(row.executing ? "running" : undefined, asking, pause)
        : runStatusFor(state.status, asking, pause);
    switch (rowStatus) {
      case "input":
        input++;
        break;
      case "waiting":
        waiting++;
        break;
      case "working":
        working++;
        break;
      // Unreachable from a LIVE inventory row, and refused here rather than left
      // latent: the mark withdraws when a run ends. `idle` is not this vocabulary's
      // at all — `runStatusFor` never returns it — and is enumerated only because
      // the switch is total over `TabDotState` with no default, so a member added
      // upstream fails the type check here instead of falling into a bucket.
      case "done":
      case "failed":
      case "idle":
      case "":
        break;
    }
  }
  const total = working + waiting + input;
  const status: TabRunDotStatus | "" =
    input > 0 ? "input" : waiting > 0 ? "waiting" : working > 0 ? "working" : "";
  return { status, tally: { total, working, waiting, input } };
}

function repaint(): void {
  for (const ref of openChatRefs()) {
    // Both this and `openChatRefs` above walk the projection's own array in one
    // synchronous pass, so the id resolves for every ref that pass produced.
    // `setTabRunStatus` is a no-op for an unknown id in any case.
    const id = tabIdFor("chat", ref);
    const { status, tally } = foldRuns(ref);
    setTabRunStatus(id, status, tally);
  }
}

/** Wire the effect. Called from the composition root, not at import: an effect
 *  running at module load would paint against a tab strip that has not been
 *  restored yet, and the tab-set dependency is what picks a row up once it exists.
 *
 *  ONE effect, because all three inputs are signal reads inside the pass it makes:
 *  `openChatRefs` subscribes to the tab SET, `liveRunsForChat` touches the
 *  inventory's version, and inside the fold `runState` subscribes to each run's own
 *  cell while `runPendingAsks` subscribes to the dock's queue. No second effect is
 *  needed here — unlike `run-dots.ts`, this module writes no signal, so there is no
 *  self-cycle to split apart. */
export function installChatRunDotSubscriber(): void {
  effect(() => {
    repaint();
  });
}

/** Whether a chat row's fold still needs this run's state cell: the run is live and
 *  the chat that launched it has an open tab, so the effect above reads that cell on
 *  every pass. Registered as a run-state demand from the composition root, because the
 *  store is a leaf and `forgetRun` cannot know who is reading.
 *
 *  The row's OWN `chat` rather than `runChatID`, which deliberately outlives a settle so
 *  a finished run can be re-opened under its parent: reading that instead would demand a
 *  settled run's cell for the life of the page. */
export function chatTabFoldsRun(workflowID: string): boolean {
  const chat = peekLiveRun(workflowID)?.chat ?? "";
  return chat !== "" && hasTab("chat", chat);
}
