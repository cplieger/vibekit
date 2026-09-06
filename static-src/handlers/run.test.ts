// ---------------------------------------------------------------------------
// Tests for the run SSE handlers — the routing, not the surfaces.
//
// The contract being pinned is the invalidation model: every event says only
// "refetch", and which surface refetches depends on the event. Getting that
// wrong is what an accumulating client does, and it garbles a run — `run_start`
// re-fires on every resume, and `node_complete` carries neither iteration nor
// branch, so two passes of one loop cannot be told apart from the events alone.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

vi.mock("../run-store.js", () => ({
  invalidateRun: vi.fn(),
  noteRunChat: vi.fn(),
  noteRunLive: vi.fn(),
  noteRunSettled: vi.fn(),
  hasLiveRunForChat: vi.fn(() => false),
}));
// The launching chat's own liveness, for the orphan sweep's two gates. Mocked
// because what this suite pins is WHEN the sweep fires, not how a chat comes to
// be thinking.
vi.mock("../store.js", () => ({ isThinking: vi.fn(() => false) }));
vi.mock("../run-dots.js", () => ({ trackRun: vi.fn() }));
// The app-opened MARKER, the completion auto-close, and the live step transcript.
// Their own rules (record once per client, never a parentless run; close only an
// app-opened tab, only on a clean ending, never the tab on screen) are run-view's;
// what this suite pins is WHICH events reach them and with what.
//
// There is no opener among them any more, and that is the contract these cases
// carry: a starting run's tab is opened server-side, so no run event may open one.
vi.mock("../run-view.js", () => ({
  noteAutoOpenedRun: vi.fn(),
  autoCloseRunSubTab: vi.fn(),
  applyRunStep: vi.fn(),
}));
vi.mock("../toast.js", () => ({ info: vi.fn(), success: vi.fn(), error: vi.fn() }));
// A parked step's question. The DOCK is mocked because its queue, settle-once
// guard and two hosts are `decision-dock.test.ts`'s subject; what this suite pins
// is that the handler enqueues one decision carrying the ENVELOPE's chat id, and
// that its submit reaches the right verb.
vi.mock("../decision-dock.js", () => ({
  pushDecision: vi.fn(),
  collapseSettledRunInput: vi.fn(),
  dropRunAsks: vi.fn(),
  dropTurnDecisions: vi.fn(),
}));
vi.mock("../actions/runs.js", () => ({
  answerRunInput: { dispatch: vi.fn() },
  continueRunStep: { dispatch: vi.fn() },
}));
vi.mock("../notify.js", () => ({ notifyIfHidden: vi.fn(), NOTIFY_TITLE: "Vibekit" }));

import "./run.js";
import { dispatch, onBus, BUS_RUNS_CHANGED } from "../bus.js";
import type { SSEPayloads } from "../bus.js";
import {
  invalidateRun,
  noteRunChat,
  noteRunLive,
  noteRunSettled,
  hasLiveRunForChat,
} from "../run-store.js";
import { isThinking } from "../store.js";
import { trackRun } from "../run-dots.js";
import { noteAutoOpenedRun, autoCloseRunSubTab, applyRunStep } from "../run-view.js";
import { info, success, error } from "../toast.js";
import {
  pushDecision,
  collapseSettledRunInput,
  dropRunAsks,
  dropTurnDecisions,
} from "../decision-dock.js";
import { answerRunInput, continueRunStep } from "../actions/runs.js";
import { notifyIfHidden } from "../notify.js";

const invalidate = vi.mocked(invalidateRun);
const noteChat = vi.mocked(noteRunChat);
const noteLive = vi.mocked(noteRunLive);
const noteSettled = vi.mocked(noteRunSettled);
const track = vi.mocked(trackRun);
const noteAutoOpened = vi.mocked(noteAutoOpenedRun);
const autoClose = vi.mocked(autoCloseRunSubTab);
const stepFrames = vi.mocked(applyRunStep);
const toastInfo = vi.mocked(info);
const toastSuccess = vi.mocked(success);
const toastError = vi.mocked(error);
const enqueue = vi.mocked(pushDecision);
const retireAsk = vi.mocked(collapseSettledRunInput);
const dropAsks = vi.mocked(dropRunAsks);
const sweepOrphans = vi.mocked(dropTurnDecisions);
const chatThinking = vi.mocked(isThinking);
const siblingRunLive = vi.mocked(hasLiveRunForChat);
const answer = vi.mocked(answerRunInput.dispatch);
const waive = vi.mocked(continueRunStep.dispatch);
const notify = vi.mocked(notifyIfHidden);

/** Every toast raised, in order, whatever its level. */
function toasts(): string[] {
  return [...toastInfo.mock.calls, ...toastSuccess.mock.calls, ...toastError.mock.calls].map(
    (c) => c[0],
  );
}

// The list side goes over the bus, so the test subscribes exactly as the history
// page does rather than asserting on a mocked import.
let listRefetches = 0;
onBus(BUS_RUNS_CHANGED, () => {
  listRefetches++;
});

beforeEach(() => {
  invalidate.mockClear();
  noteChat.mockClear();
  noteLive.mockClear();
  noteSettled.mockClear();
  track.mockClear();
  noteAutoOpened.mockClear();
  autoClose.mockClear();
  stepFrames.mockClear();
  toastInfo.mockClear();
  toastSuccess.mockClear();
  toastError.mockClear();
  enqueue.mockClear();
  retireAsk.mockClear();
  dropAsks.mockClear();
  sweepOrphans.mockClear();
  chatThinking.mockReset();
  chatThinking.mockReturnValue(false);
  siblingRunLive.mockReset();
  siblingRunLive.mockReturnValue(false);
  answer.mockClear();
  waive.mockClear();
  notify.mockClear();
  listRefetches = 0;
});

type RunEvent =
  | "run_started"
  | "run_progress"
  | "run_finished"
  | "run_step"
  | "run_input_needed"
  | "run_input_settled";

function send(type: RunEvent, payload: Record<string, unknown>, chatID = "c1"): void {
  dispatch({ type, chat_id: chatID, payload });
}

// The six run events really are keys of the typed SSE surface, so a rename on
// the Go side that regenerates the wire types breaks this file rather than
// silently unsubscribing the handlers.
const _keys: readonly (keyof SSEPayloads)[] = [
  "run_started",
  "run_progress",
  "run_finished",
  "run_step",
  "run_input_needed",
  "run_input_settled",
];
void _keys;

describe("run SSE handlers", () => {
  it("invalidates the run store on every one of the three events", () => {
    const order: RunEvent[] = ["run_started", "run_progress", "run_finished"];
    for (const [i, type] of order.entries()) {
      send(type, { workflow_id: "wf_1", kind: "node_start", status: "completed", name: "x" });
      expect(invalidate).toHaveBeenCalledTimes(i + 1);
      expect(invalidate).toHaveBeenLastCalledWith("wf_1");
    }
  });

  // Every run's tab carries a dot now, agent-launched included, so every event
  // tracks the run. The origin no longer decides — a chat's own dot cannot cover a
  // run that outlives its turn.
  it("tracks the run for its dot on every event", () => {
    const order: RunEvent[] = ["run_started", "run_progress", "run_finished"];
    for (const [i, type] of order.entries()) {
      send(type, { workflow_id: "wf_1", kind: "node_start", status: "completed" });
      expect(track).toHaveBeenCalledTimes(i + 1);
      expect(track).toHaveBeenLastCalledWith("wf_1");
    }
  });

  // A run's tab is the SERVER's to open, so what a start frame does here is record
  // that the tab is the app's doing — the one fact the server cannot answer,
  // because it knows it offered the tab and not whether this reader has since
  // claimed it. The launching chat id comes off the envelope and is what keeps a
  // parentless run out of the claim.
  it("records the run's tab as app-opened, naming the chat that launched it", () => {
    send("run_started", { workflow_id: "wf_1", name: "publish" });
    expect(noteAutoOpened).toHaveBeenCalledWith("wf_1", "c1");
  });

  // A reader whose first sight of a run is a progress frame got the tab from the
  // server's own `node_start` retry, and may equally have opened it themselves —
  // the run events are not in the SSE replay ring, so this frame cannot tell the
  // two apart. Claiming it would let the completion auto-close take the reader's.
  it("claims nothing from a progress frame", () => {
    send("run_progress", { workflow_id: "wf_2", kind: "node_start" });
    expect(noteAutoOpened).not.toHaveBeenCalled();
  });

  // A tab appearing at the moment work ENDS is noise: there is nothing live to
  // watch, and History is the door to a finished run.
  it("claims nothing on the finish frame either", () => {
    send("run_finished", { workflow_id: "wf_3", status: "completed" });
    expect(noteAutoOpened).not.toHaveBeenCalled();
  });

  // Recorded on EVERY event including the finish, because it is what a later
  // re-open nests under: a reader who closed the automatic tab and then clicks the
  // run in its transcript should land beside the same conversation.
  it("records the launching chat on every event, the finish included", () => {
    const order: RunEvent[] = ["run_started", "run_progress", "run_finished"];
    for (const [i, type] of order.entries()) {
      send(type, { workflow_id: "wf_4", kind: "node_start", status: "completed" });
      expect(noteChat).toHaveBeenCalledTimes(i + 1);
      expect(noteChat).toHaveBeenLastCalledWith("wf_4", "c1");
    }
  });

  it("refetches the history list only at the two ENDS of a run", () => {
    // A start adds a row and a finish settles its outcome; the seven kinds in
    // between change the run, not the list, and a busy run emits many of them.
    send("run_started", { workflow_id: "wf_1", name: "publish" });
    expect(listRefetches).toBe(1);
    send("run_finished", { workflow_id: "wf_1", status: "completed" });
    expect(listRefetches).toBe(2);

    listRefetches = 0;
    for (const kind of ["node_start", "node_complete", "watch_poll", "loop_iteration"]) {
      send("run_progress", { workflow_id: "wf_1", kind });
    }
    expect(listRefetches).toBe(0);
  });

  it("passes the workflow id through so a surface showing another run ignores it", () => {
    send("run_progress", { workflow_id: "wf_other", kind: "node_start" });
    expect(invalidate).toHaveBeenCalledWith("wf_other");
  });

  // The live-runs inventory feeds the eviction sweep's exemption: a chat with a
  // run in flight must not lose its transcript window. Both live-marking events
  // carry the chat — run_progress too, because run_started is not replayed to a
  // client that connects mid-run.
  //
  // Both say EXECUTING, and each knows it for its own reason: a start frame fires on
  // the launch and on every resume, and a node-level progress frame is a step moving
  // inside a run that is still going.
  it("records the run LIVE and EXECUTING with its chat, on start and on progress", () => {
    send("run_started", { workflow_id: "wf_live", name: "publish" });
    expect(noteLive).toHaveBeenCalledWith("wf_live", "c1", true);
    send("run_progress", { workflow_id: "wf_live", kind: "node_start" });
    expect(noteLive).toHaveBeenLastCalledWith("wf_live", "c1", true);
    expect(noteLive).toHaveBeenCalledTimes(2);
    expect(noteSettled).not.toHaveBeenCalled();
  });

  // The RUN-LEVEL pause folds into run_progress, so a progress frame is proof of
  // life but not of execution. Reading every progress frame as executing would let a
  // parked run keep its chat's whole message window resident on the strength of the
  // very frame that parked it — a needInput park can sit for hours.
  it("records a run-level pause as live but NOT executing", () => {
    send("run_progress", { workflow_id: "wf_live", kind: "paused" });
    expect(noteLive).toHaveBeenCalledWith("wf_live", "c1", false);
    expect(noteSettled).not.toHaveBeenCalled();
  });

  // Its counterpart, and the reason the gate is the run-level kind rather than any
  // pause: a node-level pause is one step waiting inside a run that is still going.
  it("records a NODE-level pause as still executing", () => {
    send("run_progress", { workflow_id: "wf_live", kind: "node_paused" });
    expect(noteLive).toHaveBeenCalledWith("wf_live", "c1", true);
  });

  it("settles the run on every terminal finish, recognised or not", () => {
    for (const status of ["completed", "failed", "aborted", "cancelled", "exploded"]) {
      noteSettled.mockClear();
      send("run_finished", { workflow_id: "wf_end", status });
      expect(noteSettled, status).toHaveBeenCalledWith("wf_end");
    }
  });

  it("keeps a PAUSED run live but stops calling it executing", () => {
    // The server's lease survives a pause the same way — presence means
    // non-terminal, so the row stays and the dot painter keeps its run. What lapses
    // is the eviction exemption: a policy stop (`onMaxIterations`) reports through
    // this frame and writes nothing into the chat afterwards.
    send("run_finished", { workflow_id: "wf_pause", status: "paused" });
    expect(noteSettled).not.toHaveBeenCalled();
    expect(noteLive).toHaveBeenCalledWith("wf_pause", "c1", false);
  });

  // The dock's queue is the third thing a terminal finish has to release. A run's
  // ask is filed under the LAUNCHING chat's key, and `dropTurnDecisions` exempts a
  // run-scoped ask on purpose (the run outlives the turn that launched it), so
  // nothing else can reach it once the run is over — and `input` is the top rung of
  // the tab dot, which left the launching chat amber for the life of the page.
  it("drops the run's unanswered asks on every terminal finish", () => {
    for (const status of ["completed", "failed", "aborted", "cancelled", "exploded"]) {
      dropAsks.mockClear();
      send("run_finished", { workflow_id: "wf_ask", status });
      expect(dropAsks, status).toHaveBeenCalledWith("wf_ask");
    }
  });

  it("leaves a PAUSED run's asks queued: that pause is what wants an answer", () => {
    send("run_finished", { workflow_id: "wf_pause", status: "paused" });
    expect(dropAsks).not.toHaveBeenCalled();
  });

  it("drops nothing on the frames that are not an ending", () => {
    send("run_started", { workflow_id: "wf_ask", name: "publish" });
    send("run_progress", { workflow_id: "wf_ask", kind: "node_complete" });
    expect(dropAsks).not.toHaveBeenCalled();
  });

  // ---------------------------------------------------------------------------
  // The ORPHAN backstop, and the reason `dropRunAsks` above cannot be it.
  //
  // Every remover in the dock is keyed on `runID`, and a step's request-shaped ask
  // reaches the client with `run_id` EMPTY whenever the step-session registry has
  // not seen its sub-session. Such an ask is filed under the launching chat's id,
  // so it lights that chat's tab dot, and it is invisible to the run sub-tab's
  // dock, to `runPendingAsks` and to `dropRunAsks` alike — the sub-tab reads as
  // answered while the parent stays amber for the life of the page.
  //
  // `dropTurnDecisions` is the sweep that CAN name it (it keeps only asks with a
  // non-empty `runID`), and it had exactly one production caller: `turn_ended` on
  // the launching chat. A step-driven turn's `turn_end` is dropped by the
  // attribution gate, so no such frame arrives while the run is going. The missing
  // piece was a TRIGGER, not a predicate — which is why every case here is about
  // WHEN the sweep fires and none is about what it removes.
  // ---------------------------------------------------------------------------
  describe("a run's terminal frame sweeps the launching chat's orphaned asks", () => {
    it("sweeps when the launching chat is idle and no sibling run is live", () => {
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "c-parent");
      expect(sweepOrphans).toHaveBeenCalledWith("c-parent");
    });

    it("leaves the chat's OWN live turn alone", () => {
      // The user prompted the launching chat while its run was going, so that
      // turn's permission ask is live and answerable — sweeping it would strand a
      // JSON-RPC request nothing can answer.
      chatThinking.mockReturnValue(true);
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "c-parent");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });

    it("leaves a SIBLING run's orphan alone while that run is still executing", () => {
      // Two runs launched from one chat share its queue key, and a sibling's
      // orphaned step ask carries an empty runID too — indistinguishable from this
      // run's. `noteRunSettled` has already run for THIS run by then, so the
      // predicate answers about siblings only.
      siblingRunLive.mockReturnValue(true);
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "c-parent");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });

    it("does not sweep on a PAUSE: the run is still going and its step is waiting", () => {
      send("run_finished", { workflow_id: "wf_orphan", status: "paused" }, "c-parent");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });

    it("sweeps nothing for a PARENTLESS run, which has no chat to sweep", () => {
      // A manual or scheduled run's lifecycle frames are workspace-global (empty
      // envelope chat id) and its asks are keyed to the synthetic `run:<id>`, which
      // `dropRunAsks` already reaches.
      send("run_finished", { workflow_id: "wf_orphan", status: "completed" }, "");
      expect(sweepOrphans).not.toHaveBeenCalled();
    });
  });

  // The finish frame's other half: the tab the run opened for itself goes away with
  // it. The STATUS travels because the decision needs it — a failed run keeps its
  // tab — and this suite only pins that the frame reaches the rule with the run's
  // own verdict rather than a boolean this handler derived.
  it("hands the finish frame's status to the auto-close", () => {
    send("run_finished", { workflow_id: "wf_5", status: "completed" });
    expect(autoClose).toHaveBeenCalledWith("wf_5", "completed");
  });

  it("routes a bad ending to it too, verdict intact, and decides nothing itself", () => {
    send("run_finished", { workflow_id: "wf_6", status: "failed" });
    expect(autoClose).toHaveBeenCalledWith("wf_6", "failed");
  });

  it("leaves the auto-close out of the frames that are not an ending", () => {
    send("run_started", { workflow_id: "wf_7", name: "publish" });
    send("run_progress", { workflow_id: "wf_7", kind: "node_complete" });
    expect(autoClose).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The signal: who gets told, and when.
//
// The asymmetry is the contract. A start is announced only for a run nobody
// launched by hand; a completion is announced for every run, because nothing else
// in the app says a run ended (no push kind, and one shared run-dock element means
// a background run tab has no surface).
// ---------------------------------------------------------------------------
describe("run toasts", () => {
  // Each case uses its own workflow ids. The start guard is module state that
  // outlives a test (it is keyed on the run, and a run does not restart because a
  // test ended), so sharing an id between cases would have one suppress another.
  it("announces a SCHEDULED run's start and names it", () => {
    send("run_started", { workflow_id: "wf_ann_1", name: "nightly-publish", scheduled: true });
    expect(toastInfo).toHaveBeenCalledTimes(1);
    expect(toastInfo.mock.calls[0]?.[0]).toBe("Scheduled run started: nightly-publish");
  });

  // A manual launch already has the user's attention: they pressed Run, and a run
  // tab opened in front of them. The flag is absent on the wire for such a run
  // rather than false, so an older server reads as manual too.
  it("says nothing when a run started manually", () => {
    send("run_started", { workflow_id: "wf_man_1", name: "publish" });
    send("run_started", { workflow_id: "wf_man_2", name: "publish", scheduled: false });
    expect(toasts()).toEqual([]);
  });

  // `run_start` re-fires on every resume — three frames were measured for one run
  // — and toast.ts coalesces nothing, so the guard is what stops one scheduled run
  // from producing a stack of identical toasts.
  it("announces a scheduled start once however often the frame re-fires", () => {
    for (let i = 0; i < 3; i++) {
      send("run_started", { workflow_id: "wf_resume", name: "nightly", scheduled: true });
    }
    expect(toastInfo).toHaveBeenCalledTimes(1);

    // A different run is a different announcement.
    send("run_started", { workflow_id: "wf_resume_other", name: "other", scheduled: true });
    expect(toastInfo).toHaveBeenCalledTimes(2);
  });

  it("announces a completion for a scheduled AND a manual run", () => {
    send("run_started", { workflow_id: "wf_sched", name: "nightly", scheduled: true });
    send("run_started", { workflow_id: "wf_manual", name: "by-hand" });
    toastInfo.mockClear();

    send("run_finished", { workflow_id: "wf_sched", status: "completed", name: "nightly" });
    send("run_finished", { workflow_id: "wf_manual", status: "completed", name: "by-hand" });
    expect(toastSuccess.mock.calls.map((c) => c[0])).toEqual([
      "nightly finished",
      "by-hand finished",
    ]);
  });

  // The level follows the OUTCOME, not the event: "finished" is not a verdict.
  it("maps each terminal status to the level it deserves", () => {
    const cases: { status: string; want: string; level: "success" | "error" | "info" }[] = [
      { status: "completed", want: "publish finished", level: "success" },
      { status: "failed", want: "publish failed", level: "error" },
      { status: "aborted", want: "publish was aborted", level: "error" },
      { status: "cancelled", want: "publish was cancelled", level: "info" },
    ];
    for (const c of cases) {
      toastInfo.mockClear();
      toastSuccess.mockClear();
      toastError.mockClear();
      send("run_finished", { workflow_id: "wf_level", status: c.status, name: "publish" });
      const mock = { success: toastSuccess, error: toastError, info: toastInfo }[c.level];
      expect(mock.mock.calls.map((x) => x[0])).toEqual([c.want]);
      expect(toasts()).toHaveLength(1);
    }
  });

  // A paused run is not a completion. KAS reports an onMaxIterations policy stop
  // through this same frame, and the run is still resumable, so calling it
  // finished would be a false statement about work that has not ended.
  it("stays silent on a policy pause", () => {
    send("run_finished", { workflow_id: "wf_pause", status: "paused", name: "publish" });
    expect(toasts()).toEqual([]);
  });

  // A pause is also not the end of the run's start signal: the client stopped
  // believing it was running, so a resumed scheduled run announces itself again.
  it("re-announces a scheduled start after the run stopped", () => {
    send("run_started", { workflow_id: "wf_restart", name: "nightly", scheduled: true });
    send("run_finished", { workflow_id: "wf_restart", status: "paused", name: "nightly" });
    send("run_started", { workflow_id: "wf_restart", name: "nightly", scheduled: true });
    expect(toastInfo).toHaveBeenCalledTimes(2);
  });

  // A page opened mid-run, or a frame KAS sent no state with, has no name to use.
  // A generic label beats a bare workflow uuid and beats saying nothing.
  it("falls back to a generic label when the frame carries no name", () => {
    send("run_finished", { workflow_id: "wf_noname", status: "completed" });
    expect(toastSuccess.mock.calls[0]?.[0]).toBe("Workflow run finished");
    send("run_started", { workflow_id: "wf_noname_2", scheduled: true });
    expect(toastInfo.mock.calls[0]?.[0]).toBe("Scheduled run started: Workflow run");
  });

  // An unrecognised status is still an ending; naming it verbatim beats silence.
  it("passes an unknown status through rather than dropping it", () => {
    send("run_finished", { workflow_id: "wf_odd", status: "exploded", name: "publish" });
    expect(toastInfo.mock.calls[0]?.[0]).toBe("publish finished: exploded");
  });

  // The seven frames between the ends are invalidations, and a busy run emits
  // many: one toast each would bury the two that mean something.
  it("never toasts a progress frame", () => {
    for (const kind of ["node_start", "node_complete", "watch_poll", "loop_iteration", "paused"]) {
      send("run_progress", { workflow_id: "wf_prog", kind });
    }
    expect(toasts()).toEqual([]);
  });

  // `run_step` is the one run frame whose PAYLOAD is read rather than used as a
  // signal to refetch — a step's transcript is not in `inspect` and no endpoint
  // serves it, so there is nothing to invalidate. Both halves of that are asserted
  // here, because either one alone would leave the surface wrong: a refetch
  // instead of a hand-off would show none of the content, and a hand-off plus a
  // refetch would put a request on the wire for every delta of every step.
  it("hands a step frame to the view and refetches nothing", () => {
    send("run_step", {
      workflow_id: "wf_live",
      node_path: "seq/coder",
      kind: "text",
      delta: "working",
    });
    expect(stepFrames).toHaveBeenCalledTimes(1);
    expect(stepFrames.mock.calls[0]?.[0]?.node_path).toBe("seq/coder");
    expect(invalidate).not.toHaveBeenCalled();
    expect(toasts()).toEqual([]);
    expect(listRefetches).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// A parked step's question.
//
// The one run event besides `run_step` that carries a payload rather than being
// an invalidation, and for a sharper reason: KAS parks the run with one fixed
// `pauseReason` literal and an empty `pauseDetail`, so refetching `inspect` can
// say a step wants input and can never say what it asked.
//
// The whole feature is where the card lands, so the chat id is what these cases
// are about: it comes off the ENVELOPE, which is the launching chat for an
// agent-parented run and `run:<workflowId>` for a parentless one.
// ---------------------------------------------------------------------------
describe("a step's question", () => {
  interface AskDecision {
    kind: string;
    chatID: string;
    runID?: string;
    askID: string;
    submit: (text: string | null) => void;
  }

  function ask(over: Record<string, unknown> = {}, chatID = "c1"): AskDecision {
    send(
      "run_input_needed",
      {
        workflow_id: "wf_1",
        ask_id: "notify:7",
        node_id: "review",
        step_session_id: "sess-1",
        agent_name: "reviewer",
        question: "Ship it?",
        asked_at: "2026-09-03T10:00:00Z",
        ...over,
      },
      chatID,
    );
    return enqueue.mock.calls.at(-1)?.[0] as unknown as AskDecision;
  }

  it("enqueues one decision keyed to the LAUNCHING chat", () => {
    const d = ask();
    expect(enqueue).toHaveBeenCalledTimes(1);
    expect(d.kind).toBe("run_input");
    expect(d.chatID).toBe("c1");
    // Both, because they are what the two hosts match on: the composer's dock
    // takes the chat id, a run tab's takes either.
    expect(d.runID).toBe("wf_1");
    expect(d.askID).toBe("notify:7");
  });

  it("keys a PARENTLESS run's ask to the synthetic run chat the server sent", () => {
    // A manual or scheduled run has no launching chat, so the envelope carries
    // `run:<workflowId>` and the run tab's dock is the surface that matches.
    const d = ask({}, "run:wf_1");
    expect(d.chatID).toBe("run:wf_1");
    expect(d.runID).toBe("wf_1");
  });

  it("tracks the run so its tab dot reports the block", () => {
    ask();
    expect(track).toHaveBeenCalledWith("wf_1");
  });

  // On the connect replay this frame can be the FIRST one a client sees for a run,
  // so without it the run card's footer link opens the tab top-level instead of
  // beside the conversation that launched it.
  it("records the launching chat, like the three lifecycle events do", () => {
    ask();
    expect(noteChat).toHaveBeenCalledWith("wf_1", "c1");
  });

  it("hands the synthetic run chat through unchanged and lets the store refuse it", () => {
    // `run:<workflowId>` is not a chat id, and the run store is where that rule
    // lives — a caller-side filter would be a second copy of it. See run-store.ts.
    ask({}, "run:wf_1");
    expect(noteChat).toHaveBeenCalledWith("wf_1", "run:wf_1");
  });

  it("pushes a notification, because this ask blocks a run indefinitely", () => {
    ask();
    expect(notify).toHaveBeenCalledWith("Vibekit", "A workflow step is waiting for your answer");
  });

  it("refetches nothing: the question is on no endpoint", () => {
    ask();
    expect(invalidate).not.toHaveBeenCalled();
    expect(listRefetches).toBe(0);
    expect(toasts()).toEqual([]);
  });

  it("sends text to the ANSWER verb, addressed by ask id", () => {
    ask().submit("yes, ship it");
    expect(answer).toHaveBeenCalledWith({
      workflowID: "wf_1",
      ask_id: "notify:7",
      text: "yes, ship it",
    });
    expect(waive).not.toHaveBeenCalled();
  });

  it("sends null to the CONTINUE verb, addressed by NODE", () => {
    // The step-status verb takes a node, not an ask: it clears the node's
    // need-input signal so the step re-runs with its own default continuation.
    ask().submit(null);
    expect(waive).toHaveBeenCalledWith({ workflowID: "wf_1", nodeID: "review" });
    expect(answer).not.toHaveBeenCalled();
  });

  it("retires the card on the settle frame", () => {
    send("run_input_settled", {
      workflow_id: "wf_1",
      ask_id: "notify:7",
      settled_by: "user",
    });
    expect(retireAsk).toHaveBeenCalledWith("wf_1", "notify:7", "user");
    // Not an invalidation either: the run's own status never described the ask.
    expect(invalidate).not.toHaveBeenCalled();
  });

  it("carries the unattended settler through rather than flattening it", () => {
    send("run_input_settled", {
      workflow_id: "wf_1",
      ask_id: "notify:7",
      settled_by: "unattended",
    });
    expect(retireAsk).toHaveBeenCalledWith("wf_1", "notify:7", "unattended");
  });
});
