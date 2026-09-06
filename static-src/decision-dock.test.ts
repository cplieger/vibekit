// ---------------------------------------------------------------------------
// Tests for decision-dock.ts — the queue, not the cards.
//
// Each case pins something the three modals got wrong or could not do:
//   - the permission modal had NO queue, so a second request overwrote the
//     first and its callback was dropped, leaving KAS waiting on an id nothing
//     would ever answer
//   - the SSE handlers gated on the active chat, so an ask raised on a
//     background chat vanished until a reconnect happened to replay it
//   - SSE reconnect replays every unanswered permission, so a re-delivery must
//     not stack a duplicate
//   - answering twice on one request id is worse than a dropped click
//
// The MOTION half is here too, because the phases are a property of the dock's
// state machine rather than of any card: an answered card stays on screen for
// the length of a phase, so every lookup in this file is scoped to the LIVE card
// (`:scope > .dock-card`) or it would find the answered one first. The phase
// timer is the only clock the dock has — the test page links no app stylesheet,
// so no transition or animation event ever fires here — which is why the timers
// are faked rather than awaited.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

// The store is NOT mocked: the dock's chat-switch trigger is an effect over the
// real `activeSession` computed, and a stubbed signal would test the stub's
// reactivity rather than the wiring that ships. Only the two leaves that reach
// for DOM the dock does not own are mocked.
vi.mock("./editor-openers.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  openFile: undefined,
  openFileGitDiff: vi.fn(),
}));
vi.mock("./actions/permissions.js", () => ({ editNativeRule: { dispatch: vi.fn() } }));
// The attribution toast is the observable half of a card collapsing under the
// reader, so it is mocked to be asserted rather than to be silenced.
const { mockToastInfo } = vi.hoisted(() => ({ mockToastInfo: vi.fn() }));
// The rest of the surface comes from the canonical factory: the dock's graph
// reaches failure-notice.ts through the tab projection now, and that module
// imports `errorWithAction`, so a one-name mock no longer links.
vi.mock("./toast.js", () =>
  import("./__test-helpers__/toast-mock.js").then((m) => ({
    ...m.toastMock(),
    info: mockToastInfo,
  })),
);

import {
  mountDecisionDock,
  mountRunDecisionDock,
  rerenderDocks,
  pushDecision,
  dropDecisions,
  dropRunDecisions,
  collapseSettledDecision,
  collapseSettledRunInput,
  dropRunAsks,
  RUN_INPUT_FALLBACK,
  DOCK_PHASE_MS,
  runPendingAsks,
  _resetForTest,
} from "./decision-dock.js";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
// Not reachable through `loadCSS`: its glob is `../css/*.css`, and the MANIFEST
// carries no extension. Read directly, because the file ORDER it declares is a
// load-bearing cascade fact for the reduced-motion disarm below.
import cssManifest from "./css/MANIFEST?raw";
import { setSessions, setActive } from "./store.js";
import type { PermissionNeededPayload, RunInputNeededPayload, Session } from "./types.js";

function session(id: string): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function host(): HTMLElement {
  return document.getElementById("decision-dock") as HTMLElement;
}

function perm(over: Partial<PermissionNeededPayload> = {}): PermissionNeededPayload {
  return {
    request_id: 1,
    options: [
      { option_id: "allow_once", name: "Allow", kind: "allow_once" },
      { option_id: "reject_once", name: "Reject", kind: "reject_once" },
    ],
    title: "ls -la",
    kind: "execute",
    ...over,
  };
}

function pushPerm(chatID: string, requestID: number, submit = vi.fn()): typeof submit {
  pushDecision({
    kind: "permission",
    chatID,
    requestID,
    payload: perm({ request_id: requestID }),
    submit,
  });
  return submit;
}

/** The LIVE cards, excluding an answered one still on screen for the length of a
 *  phase. `:scope >` excludes it by construction: the outgoing content sits one
 *  level deeper, inside `.dock-outgoing`. */
function liveCards(h: HTMLElement = host()): HTMLElement[] {
  return [...h.querySelectorAll<HTMLElement>(":scope > .dock-card")];
}

function liveCard(h: HTMLElement = host()): HTMLElement | null {
  return h.querySelector<HTMLElement>(":scope > .dock-card");
}

/** The live depth row. Scoped for the same reason: a stale depth row inside
 *  `.dock-outgoing` sits earlier in document order. */
function liveDepth(h: HTMLElement = host()): HTMLElement | null {
  return h.querySelector<HTMLElement>(":scope > .dock-depth");
}

function outgoings(h: HTMLElement = host()): HTMLElement[] {
  return [...h.querySelectorAll<HTMLElement>(".dock-outgoing")];
}

function clickButton(label: string, h: HTMLElement = host()): void {
  const scope: HTMLElement = liveCard(h) ?? h;
  const btn = [...scope.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent === label,
  );
  btn?.click();
}

/** Let every phase's cleanup timer fire. The phase is purely visual — the
 *  response went out synchronously on the click — but `.hidden` and the removal
 *  of the answered card land at the END of it, so a test asserting the settled
 *  DOM has to get there. */
function settleMotion(): void {
  vi.advanceTimersByTime(Math.max(...Object.values(DOCK_PHASE_MS)) + 1);
}

beforeEach(() => {
  // Only the two functions the dock uses. Faking Date and performance as well
  // would reach the reactive graph and the announcer for no benefit; the phase
  // timer is the whole clock under test.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  _resetForTest();
  mockToastInfo.mockClear();
  document.body.innerHTML = `<div id="decision-dock" class="hidden"></div>`;
  setSessions([session("c1"), session("c2")]);
  setActive("c1");
  mountDecisionDock(host());
});

afterEach(() => {
  // `restoreMocks` does not undo fake timers, and a leaked fake clock breaks
  // every later file in this worker.
  vi.useRealTimers();
});

describe("the dock's visibility", () => {
  it("stays hidden and empty until something asks", () => {
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
  });

  it("reveals on a decision and hides again once answered", () => {
    pushPerm("c1", 1);
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();

    clickButton("Allow");
    // `.hidden` lands at the END of the collapse, not on the click: the utility
    // class is `display: none !important` and cannot be animated out, which is
    // why the old collapse rule never rendered a frame.
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
  });
});

describe("the queue", () => {
  it("shows one at a time and advances on answer, instead of losing the first", () => {
    const first = pushPerm("c1", 1);
    const second = pushPerm("c1", 2);

    // Only the head is rendered, and the depth line reports the rest.
    expect(liveCards().length).toBe(1);
    expect(liveDepth()?.textContent).toBe("1 more waiting");

    clickButton("Allow");
    expect(first).toHaveBeenCalledWith("allow_once", undefined);
    expect(second).not.toHaveBeenCalled();

    // The second is now the head, and its depth line is empty. The answered
    // card is still on screen behind it for the length of the advance, which is
    // why these lookups are scoped to the live one.
    expect(liveCards().length).toBe(1);
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
    clickButton("Reject");
    expect(second).toHaveBeenCalledWith("reject_once", undefined);
  });

  it("answers a request at most once", () => {
    const submit = pushPerm("c1", 1);
    const allow = [...host().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === "Allow",
    );
    // The card is detached by the first click; clicking its retained handle
    // again must not produce a second reply on the same request id.
    allow?.click();
    allow?.click();
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("ignores a re-delivered request (SSE reconnect replays unanswered asks)", () => {
    pushPerm("c1", 7);
    pushPerm("c1", 7);
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
  });
});

describe("per-chat routing", () => {
  it("holds a background chat's ask instead of dropping it, and shows it on switch", () => {
    const bg = pushPerm("c2", 1);
    // Nothing on screen: it is not this chat's ask.
    expect(host().classList.contains("hidden")).toBe(true);

    setActive("c2");
    expect(host().classList.contains("hidden")).toBe(false);
    clickButton("Allow");
    expect(bg).toHaveBeenCalledWith("allow_once", undefined);
  });

  it("keeps an unanswered ask across a switch away and back", () => {
    pushPerm("c1", 1);
    setActive("c2");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    setActive("c1");
    expect(liveCard()).not.toBeNull();
  });

  it("dropDecisions clears a chat's asks without answering them", () => {
    const submit = pushPerm("c1", 1);
    dropDecisions("c1");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(submit).not.toHaveBeenCalled();
  });
});

describe("turn approval", () => {
  const files = [
    { path: "a.ts", action_id: "act-1" },
    { path: "b.ts", action_id: "act-2" },
  ];

  it("sends a decision for every offered action, because an omitted id is a rollback", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith("allow_once", { "act-1": true, "act-2": true });
  });

  it("an unchecked row becomes a false decision, not an absent one", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    const boxes = liveCard()?.querySelectorAll<HTMLInputElement>(".dock-file-check") ?? [];
    expect(boxes.length).toBe(2);
    boxes[1]?.click(); // uncheck b.ts
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith("allow_once", { "act-1": true, "act-2": false });
  });

  it("groups files sharing one action id into a single undividable row", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 6,
      payload: perm({
        request_id: 6,
        title: "Review changes",
        // A multi-file semantic rename: KAS keys the decision map by action, so
        // these two paths cannot disagree.
        files: [
          { path: "old.py", action_id: "ren-1" },
          { path: "new.py", action_id: "ren-1" },
        ],
      }),
      submit,
    });
    expect(liveCard()?.querySelectorAll(".dock-file-row").length).toBe(1);
    expect(liveCard()?.querySelectorAll(".dock-file-check").length).toBe(1);
    expect(host().querySelector(".dock-file-atomic")).not.toBeNull();
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith("allow_once", { "ren-1": true });
  });

  it("Roll back all answers with the reject option and no map", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    clickButton("Roll back all");
    expect(submit).toHaveBeenCalledWith("reject_once", undefined);
  });
});

describe("the run tab's dock", () => {
  function mountRunHost(run: () => string): HTMLElement {
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, run);
    return el;
  }

  it("renders a MANUAL run's ask — keyed to the synthetic run chat", () => {
    const host = mountRunHost(() => "wf_1");
    pushDecision({
      kind: "permission",
      chatID: "run:wf_1",
      requestID: 1,
      payload: perm({ request_id: 1 }),
      submit: vi.fn(),
    });
    expect(host.classList.contains("hidden")).toBe(false);
    expect(liveCard(host)).not.toBeNull();
  });

  it("renders an AGENT-LAUNCHED run's ask — keyed to the launching chat — in sync with the chat's dock", () => {
    // The done-when's "banner in both, in sync": one decision object, two
    // hosts rendering it, one answer clearing both.
    setSessions([session("c1")]);
    setActive("c1");
    const runHost = mountRunHost(() => "wf_2");

    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "wf_2",
      requestID: 5,
      payload: perm({ request_id: 5 }),
      submit,
    });

    // Both surfaces show it.
    expect(liveCard()).not.toBeNull();
    expect(liveCard(runHost)).not.toBeNull();

    // Answer from the CHAT's dock; the run tab's rendering clears too. Each host
    // owns its own phase, so both have to be let through it.
    liveCard()?.querySelector<HTMLButtonElement>("button")?.click();
    expect(submit).toHaveBeenCalledTimes(1);
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(runHost.classList.contains("hidden")).toBe(true);
  });

  it("shows nothing for another run's ask", () => {
    const runHost = mountRunHost(() => "wf_3");
    pushDecision({
      kind: "permission",
      chatID: "run:wf_OTHER",
      requestID: 9,
      payload: perm({ request_id: 9 }),
      submit: vi.fn(),
    });
    expect(runHost.classList.contains("hidden")).toBe(true);
  });

  it("re-keys when the shared view shows a different run", () => {
    // One #run-dock element serves every run tab; the run id is a getter and a
    // tab switch re-renders through rerenderDocks.
    let shown = "wf_a";
    const runHost = mountRunHost(() => shown);
    pushDecision({
      kind: "permission",
      chatID: "run:wf_b",
      requestID: 11,
      payload: perm({ request_id: 11 }),
      submit: vi.fn(),
    });
    expect(runHost.classList.contains("hidden")).toBe(true);
    shown = "wf_b";
    rerenderDocks();
    expect(runHost.classList.contains("hidden")).toBe(false);
  });

  // The transcript's run card reads this instead of mounting a dock: it has no
  // surface to answer on, and what it needs is which STEP is blocked.
  describe("runPendingAsks", () => {
    it("joins both keyings and names the steps", () => {
      pushDecision({
        kind: "permission",
        chatID: "run:wf_1",
        requestID: 1,
        payload: perm({ request_id: 1, node_id: "build", title: "Run git push" }),
        submit: vi.fn(),
      });
      pushDecision({
        kind: "permission",
        chatID: "c1",
        runID: "wf_1",
        requestID: 2,
        payload: perm({ request_id: 2, node_id: "test" }),
        submit: vi.fn(),
      });
      const a = runPendingAsks("wf_1");
      expect(a.count).toBe(2);
      expect([...a.nodes].sort()).toEqual(["build", "test"]);
      expect(a.label).toBe("Run git push");
    });

    it("counts an ask the wire could not attribute to a step", () => {
      pushDecision({
        kind: "permission",
        chatID: "run:wf_1",
        requestID: 3,
        payload: perm({ request_id: 3 }),
        submit: vi.fn(),
      });
      const a = runPendingAsks("wf_1");
      expect(a.count).toBe(1);
      expect(a.nodes.size).toBe(0);
    });

    it("ignores a chat's own ask and another run's", () => {
      pushDecision({
        kind: "permission",
        chatID: "c1",
        requestID: 4,
        payload: perm({ request_id: 4, node_id: "build" }),
        submit: vi.fn(),
      });
      pushDecision({
        kind: "permission",
        chatID: "run:wf_OTHER",
        requestID: 5,
        payload: perm({ request_id: 5 }),
        submit: vi.fn(),
      });
      expect(runPendingAsks("wf_1").count).toBe(0);
      expect(runPendingAsks("").count).toBe(0);
    });

    it("clears once the ask is answered", () => {
      const host = mountRunHost(() => "wf_1");
      pushDecision({
        kind: "permission",
        chatID: "run:wf_1",
        requestID: 6,
        payload: perm({ request_id: 6, node_id: "build" }),
        submit: vi.fn(),
      });
      expect(runPendingAsks("wf_1").count).toBe(1);
      host.querySelector<HTMLButtonElement>("button")?.click();
      expect(runPendingAsks("wf_1").count).toBe(0);
    });
  });

  it("lets the run tab answer an ask sitting BEHIND the chat's own head", () => {
    // Settle guards on membership, not head position: the chat's queue can hold
    // its own ask first, and the run tab renders (and answers) the step's ask
    // behind it. Answering out of queue order is protocol-correct — each
    // request id is its own JSON-RPC exchange — and refusing it would leave a
    // dead button in the run tab.
    setSessions([session("c1")]);
    setActive("c1");
    const runHost = mountRunHost(() => "wf_4");

    const chatSubmit = pushPerm("c1", 20);
    const stepSubmit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "wf_4",
      requestID: 21,
      payload: perm({ request_id: 21 }),
      submit: stepSubmit,
    });

    // The chat's dock shows its head (20); the run tab shows the step's (21).
    runHost.querySelector<HTMLButtonElement>("button")?.click();
    expect(stepSubmit).toHaveBeenCalledTimes(1);
    expect(chatSubmit).not.toHaveBeenCalled();
    // The chat's own ask is still on screen, unharmed.
    expect(liveCard()).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The FOURTH kind: a workflow step's question.
//
// It is the one decision that is not request-shaped — no int64 request id, a
// string ask id instead — which is why the dock's internal key had to become a
// per-kind composition. Every case here pins something that composition or the
// second settle entry point could get wrong, and the whole point of the feature
// is the first one: the prompt has to reach the PARENT TAB, the chat that
// launched the run.
// ---------------------------------------------------------------------------

describe("a workflow step's question", () => {
  function runInput(over: Partial<RunInputNeededPayload> = {}): RunInputNeededPayload {
    return {
      workflow_id: "wf_1",
      ask_id: "notify:7",
      node_id: "review",
      step_session_id: "sess-1",
      agent_name: "reviewer",
      question: "Ship it?",
      asked_at: "2026-09-03T10:00:00Z",
      ...over,
    };
  }

  function pushAsk(
    chatID: string,
    over: Partial<RunInputNeededPayload> = {},
    submit: (text: string | null) => void = vi.fn(),
  ): typeof submit {
    const payload = runInput(over);
    pushDecision({
      kind: "run_input",
      chatID,
      runID: payload.workflow_id,
      askID: payload.ask_id,
      payload,
      submit,
    });
    return submit;
  }

  function mountRunHost(run: () => string): HTMLElement {
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, run);
    return el;
  }

  function textarea(h: HTMLElement = host()): HTMLTextAreaElement | null {
    return liveCard(h)?.querySelector<HTMLTextAreaElement>(".run-input-text") ?? null;
  }

  it("renders in the PARENT TAB, keyed to the launching chat", () => {
    // The bug: a step paused to ask and no prompt appeared anywhere. The
    // envelope's chat id is the launching chat for an agent-parented run, so
    // the composer dock's own matcher is what puts the card in the parent tab.
    pushAsk("c1");
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()?.classList.contains("dock-run-input")).toBe(true);
    expect(liveCard()?.textContent).toContain("Ship it?");
    expect(liveCard()?.textContent).toContain("reviewer \u00b7 step review");
  });

  it("renders a PARENTLESS run's ask too, keyed to the synthetic run chat", () => {
    // A manual or scheduled run has no launching chat, so the server keys its
    // ask to `run:<workflowId>`. That must not regress: the run tab's own dock
    // is the only surface such a run has.
    const runHost = mountRunHost(() => "wf_1");
    pushAsk("run:wf_1");
    // Not the composer's — `run:wf_1` is not a chat and has no tab.
    expect(host().classList.contains("hidden")).toBe(true);
    expect(liveCard(runHost)).not.toBeNull();
  });

  it("shows one ask in BOTH hosts and one answer clears both", () => {
    const runHost = mountRunHost(() => "wf_1");
    const submit = pushAsk("c1");
    expect(liveCard()).not.toBeNull();
    expect(liveCard(runHost)).not.toBeNull();

    const box = textarea();
    if (box !== null) {
      box.value = "  yes, ship it  ";
    }
    clickButton("Send answer");
    // Trimmed, because leading and trailing whitespace is not part of an answer.
    expect(submit).toHaveBeenCalledWith("yes, ship it");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(runHost.classList.contains("hidden")).toBe(true);
  });

  it("sends null for continue-without-answering", () => {
    // The post-restart door: the ask registry is in memory, so a restart leaves
    // the run parked with the question gone and a reader cannot answer what they
    // cannot read. `null` re-drives the step with KAS's own continuation.
    const submit = pushAsk("c1", { question: "" });
    expect(liveCard()?.textContent).toContain(RUN_INPUT_FALLBACK);
    expect(liveCard()?.textContent).toContain("lost when the server restarted");
    clickButton("Continue without answering");
    expect(submit).toHaveBeenCalledWith(null);
  });

  it("refuses an empty answer instead of sending one", () => {
    const submit = pushAsk("c1");
    clickButton("Send answer");
    expect(submit).not.toHaveBeenCalled();
    // Still on screen: the box is the instruction, and waiving is its own button.
    expect(liveCard()).not.toBeNull();
  });

  it("answers an ask at most once", () => {
    const submit = pushAsk("c1");
    const box = textarea();
    if (box !== null) {
      box.value = "ok";
    }
    const send = [...(liveCard()?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
      (b) => b.textContent === "Send answer",
    );
    send?.click();
    send?.click();
    expect(submit).toHaveBeenCalledTimes(1);
  });

  // -------------------------------------------------------------------------
  // The held answer. `settle` splices the entry BEFORE the answer goes out, so
  // the card carrying the reader's text is already gone when the server refuses
  // it — and the one refusal that is RETRYABLE re-offers the SAME ask on a fresh
  // `run_input_needed`. Invariant 2 grants the optimistic dock its carve-out on a
  // refusal returning the TEXT as well as the row, so these pin both directions.
  // -------------------------------------------------------------------------
  describe("the words a refused send is holding", () => {
    function sendAnswer(text: string): void {
      const box = textarea();
      if (box !== null) {
        box.value = text;
      }
      clickButton("Send answer");
    }

    it("comes back with the ask the server re-offered", () => {
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      // What the server does on a between-steps refusal: restoreAsk re-broadcasts
      // the same ask, so the client pushes it again against a fresh card.
      pushAsk("c1");
      expect(textarea()?.value).toBe("the release branch");
    });

    it("is dropped once the ask is genuinely settled", () => {
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      collapseSettledRunInput("wf_1", "notify:7", "user");
      // A second ask carrying that id is a NEW question — a run can park on the
      // same node again — so seeding it with words answering the old one would put
      // a stale sentence in front of the reader as though they had typed it.
      pushAsk("c1");
      expect(textarea()?.value).toBe("");
    });

    it("is dropped when the run itself ends", () => {
      // The path the per-ask settle cannot cover: a run's terminal sweep drops
      // cards without a settle frame per ask, so nothing else frees the words.
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      dropRunAsks("wf_1");
      pushAsk("c1");
      expect(textarea()?.value).toBe("");
    });

    it("is dropped by continue-without-answering, which retires the question", () => {
      pushAsk("c1");
      sendAnswer("the release branch");
      settleMotion();
      pushAsk("c1");
      clickButton("Continue without answering");
      settleMotion();
      pushAsk("c1");
      expect(textarea()?.value).toBe("");
    });

    it("is held per ASK, so a second parked step cannot evict the first's", () => {
      // One slot would lose the older answer here, which is why the store is keyed
      // by ask rather than holding the last send.
      pushAsk("c1", { ask_id: "notify:1" });
      sendAnswer("the release branch");
      settleMotion();
      pushAsk("c1", { ask_id: "notify:2" });
      sendAnswer("main");
      settleMotion();

      pushAsk("c1", { ask_id: "notify:1" });
      expect(textarea()?.value).toBe("the release branch");
    });
  });

  it("ignores a re-delivered ask (the connect replay re-offers every parked one)", () => {
    // The server replays every parked step's question on connect, exactly as it
    // replays every unanswered permission, so a reconnect must not stack a
    // second copy. Identity is the ASK ID, not a request number.
    pushAsk("c1");
    pushAsk("c1");
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
  });

  it("keys separately from a permission carrying the same number", () => {
    // A run ask id is arbitrary server-composed text. Pushing a permission with
    // request id 1 beside an ask id of "1" must leave two decisions, or the
    // per-kind identity has collapsed into one id space.
    pushPerm("c1", 1);
    pushAsk("c1", { ask_id: "1" });
    expect(liveDepth()?.textContent).toBe("1 more waiting");
  });

  it("survives a switch away from the parent tab and back", () => {
    pushAsk("c1");
    setActive("c2");
    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    setActive("c1");
    expect(liveCard()?.classList.contains("dock-run-input")).toBe(true);
  });

  describe("runPendingAsks", () => {
    it("counts an ask under either keying and names its step", () => {
      pushAsk("c1", { ask_id: "notify:1", node_id: "review" });
      pushAsk("run:wf_1", { ask_id: "notify:2", node_id: "build" });
      const a = runPendingAsks("wf_1");
      expect(a.count).toBe(2);
      expect([...a.nodes].sort()).toEqual(["build", "review"]);
      expect(a.label).toBe("Ship it?");
    });

    it("labels a question-less ask with the shared fallback", () => {
      // The card's heading and the run card's alert have to read the same
      // sentence, so the fallback is one exported constant rather than two.
      pushAsk("run:wf_1", { question: "" });
      expect(runPendingAsks("wf_1").label).toBe(RUN_INPUT_FALLBACK);
    });

    it("clears once the ask is answered", () => {
      const runHost = mountRunHost(() => "wf_1");
      pushAsk("run:wf_1");
      expect(runPendingAsks("wf_1").count).toBe(1);
      const box = textarea(runHost);
      if (box !== null) {
        box.value = "done";
      }
      clickButton("Send answer", runHost);
      expect(runPendingAsks("wf_1").count).toBe(0);
    });
  });

  describe("dropRunDecisions", () => {
    it("clears a run-keyed ask, which the per-chat sweep cannot reach", () => {
      // A transport gap drops every claim the client can no longer support and lets
      // the connect replay re-offer what is still open. That sweep walks the chat
      // store, and `run:<workflowId>` is no chat — so an ask ANSWERED during the
      // outage kept its card (the settle is not replayed), the click answered 409,
      // and the dock spliced a question that had already closed.
      const submit = pushAsk("run:wf_1");
      dropRunDecisions();
      settleMotion();
      expect(runPendingAsks("wf_1").count).toBe(0);
      expect(submit).not.toHaveBeenCalled();
    });

    it("leaves an ask keyed to a launching CHAT alone", () => {
      // That queue is the chat's, so `dropDecisions` already owns it in the same
      // handler; dropping it here would take it down twice and, keyed on runID
      // instead of the prefix, would reach every chat-keyed run ask as well.
      pushAsk("c1");
      dropRunDecisions();
      expect(runPendingAsks("wf_1").count).toBe(1);
      expect(liveCard()?.classList.contains("dock-run-input")).toBe(true);
    });
  });

  describe("collapseSettledRunInput", () => {
    it("retires the card and says a person answered elsewhere", () => {
      const submit = pushAsk("c1");
      collapseSettledRunInput("wf_1", "notify:7", "user");
      settleMotion();

      expect(host().classList.contains("hidden")).toBe(true);
      // Never answered: the step's session already took someone else's words.
      expect(submit).not.toHaveBeenCalled();
      expect(mockToastInfo).toHaveBeenCalledWith(
        "The workflow step's question was answered in another window.",
      );
    });

    it("says a machine answered when the unattended floor did", () => {
      pushAsk("c1");
      collapseSettledRunInput("wf_1", "notify:7", "unattended");
      expect(mockToastInfo).toHaveBeenCalledWith(
        "The workflow step's question was answered automatically because nobody was watching.",
      );
    });

    it("claims NO answer when the question merely stopped being answerable", () => {
      // The reason a run ask needs a third settler the other three kinds do not: the
      // step's node can move on and the run can end while it is still parked, and
      // both retire the card without anybody replying. Saying "answered in another
      // window" there is a sentence the reader can disprove.
      pushAsk("c1");
      collapseSettledRunInput("wf_1", "notify:7", "moot");
      expect(mockToastInfo).toHaveBeenCalledWith(
        "The workflow step's question is no longer waiting for an answer.",
      );
    });

    it("finds a PARENTLESS run's ask, which no chat id names", () => {
      // The settle event carries only the run, and a parentless ask is keyed to
      // `run:<id>`, so the lookup has to scan every queue rather than one.
      const runHost = mountRunHost(() => "wf_1");
      pushAsk("run:wf_1");
      collapseSettledRunInput("wf_1", "notify:7", "user");
      settleMotion();
      expect(runHost.classList.contains("hidden")).toBe(true);
      expect(runPendingAsks("wf_1").count).toBe(0);
    });

    it("leaves an ask belonging to another run alone", () => {
      pushAsk("c1");
      collapseSettledRunInput("wf_OTHER", "notify:7", "user");
      collapseSettledRunInput("wf_1", "notify:OTHER", "user");
      collapseSettledRunInput("", "notify:7", "user");
      collapseSettledRunInput("wf_1", "", "user");

      expect(liveCard()).not.toBeNull();
      expect(mockToastInfo).not.toHaveBeenCalled();
    });

    it("does not retire a permission with the same number as the ask id", () => {
      const submit = pushPerm("c1", 7);
      collapseSettledRunInput("wf_1", "7", "user");
      expect(liveCard()).not.toBeNull();
      clickButton("Allow");
      expect(submit).toHaveBeenCalledTimes(1);
    });
  });
});

describe("a decision another surface answered", () => {
  // Every surface is offered the same ask and only the first answer is
  // accepted, so on every other surface the card outlived the question: it sat
  // there looking live, and clicking it achieved nothing.

  function mountRunHost(run: () => string): HTMLElement {
    const el = document.createElement("div");
    el.id = "run-dock";
    el.className = "hidden";
    document.body.appendChild(el);
    mountRunDecisionDock(el, run);
    return el;
  }

  it("collapses the card and says a person answered elsewhere", () => {
    const submit = pushPerm("c1", 1);
    expect(liveCard()).not.toBeNull();

    collapseSettledDecision("c1", "permission", 1, "user");
    settleMotion();

    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    // Never answered: a second answer on one request id is what this prevents.
    expect(submit).not.toHaveBeenCalled();
    expect(mockToastInfo).toHaveBeenCalledWith(
      "The permission request was answered in another window.",
    );
  });

  it("says a machine answered when the unattended floor did", () => {
    // An operator reading a card that collapses under them has to learn that a
    // deadline decided it, not a colleague.
    pushDecision({
      kind: "user_input",
      chatID: "c1",
      requestID: 3,
      payload: { request_id: 3, question: "Which region?" },
      submit: vi.fn(),
    });
    collapseSettledDecision("c1", "user_input", 3, "unattended");

    expect(mockToastInfo).toHaveBeenCalledWith(
      "The agent's question was answered automatically because nobody was watching.",
    );
  });

  it("stays quiet about a card the reader never saw", () => {
    const head = pushPerm("c1", 1);
    pushPerm("c1", 2);

    collapseSettledDecision("c1", "permission", 2, "user");

    // The queued one leaves without a word; the head is untouched and its depth
    // line drops back to nothing.
    expect(mockToastInfo).not.toHaveBeenCalled();
    expect(liveCards().length).toBe(1);
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
    clickButton("Allow");
    expect(head).toHaveBeenCalledTimes(1);
  });

  it("ignores a request it is not holding", () => {
    // The surface that DID answer arrives here with nothing to remove, because
    // answering splices the entry before the answer goes out. It must not
    // announce at itself, and must not disturb an unrelated ask.
    pushPerm("c1", 1);
    collapseSettledDecision("c1", "permission", 999, "user");
    collapseSettledDecision("c-unknown", "permission", 1, "user");

    expect(mockToastInfo).not.toHaveBeenCalled();
    expect(liveCard()).not.toBeNull();
  });

  it("matches on kind as well as request id", () => {
    // Request ids are per-bridge JSON-RPC ids, so one id can name a permission
    // and an elicitation. Retiring the wrong card would drop an ask nobody
    // answered.
    const submit = pushPerm("c1", 1);
    collapseSettledDecision("c1", "elicitation", 1, "user");

    expect(liveCard()).not.toBeNull();
    expect(mockToastInfo).not.toHaveBeenCalled();
    clickButton("Allow");
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("retires the ask on the run tab too, from one event", () => {
    // One decision, two renderings: the chat's dock and the run tab watching the
    // same step. One answer has to clear both.
    setSessions([session("c1")]);
    setActive("c1");
    const runHost = mountRunHost(() => "wf_9");
    pushDecision({
      kind: "permission",
      chatID: "c1",
      runID: "wf_9",
      requestID: 30,
      payload: perm({ request_id: 30 }),
      submit: vi.fn(),
    });
    expect(runHost.classList.contains("hidden")).toBe(false);

    collapseSettledDecision("c1", "permission", 30, "user");
    settleMotion();

    expect(runHost.classList.contains("hidden")).toBe(true);
    expect(host().classList.contains("hidden")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Motion. The three phases, the dispatch order, and the one thing requirement 3
// is: an advance must never take the tray through zero.
//
// Nothing here asserts wall-clock timing. The phase window is a number the dock
// and `26-dock.css` agree on (pinned at the bottom of this file); what these
// cases pin is the STATE MACHINE — which phase is entered, what coexists during
// it, and what is left behind after it.
// ---------------------------------------------------------------------------

describe("the enter phase", () => {
  it("grows from collapsed with the content in place, then cleans up after itself", () => {
    pushPerm("c1", 1);

    expect(host().dataset["dockPhase"]).toBe("entering");
    // Un-hidden for the whole phase: the box has to be laid out to animate.
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();
    // Nothing was on screen, so there is nothing to fade out alongside it.
    expect(outgoings().length).toBe(0);

    settleMotion();
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();
  });
});

describe("the exit phase", () => {
  it("keeps the answered card on screen while the tray shrinks, and hides only at the end", () => {
    pushPerm("c1", 1);
    settleMotion();

    clickButton("Allow");

    expect(host().dataset["dockPhase"]).toBe("leaving");
    // The card is what fades out, so it has to still be there — and the host
    // must NOT be `.hidden` yet, or `display: none` would end the animation on
    // the frame it started.
    expect(host().classList.contains("hidden")).toBe(false);
    expect(outgoings().length).toBe(1);
    expect(outgoings()[0]?.querySelector(".dock-card")).not.toBeNull();
    // No live card: this is the last decision.
    expect(liveCard()).toBeNull();

    settleMotion();
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    expect(host().dataset["dockPhase"]).toBeUndefined();
  });
});

describe("the advance phase", () => {
  it("morphs between two cards instead of collapsing and re-growing", () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    settleMotion();

    clickButton("Allow");

    expect(host().dataset["dockPhase"]).toBe("advancing");
    // Exactly one of each, coexisting: the outgoing card is what cross-fades
    // out while the incoming one fades in, so a frame with neither is
    // unreachable.
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(1);
    expect(host().classList.contains("hidden")).toBe(false);

    settleMotion();
    expect(outgoings().length).toBe(0);
    expect(liveCards().length).toBe(1);
  });

  it("never adds .hidden at any point — requirement 3, watched rather than sampled", async () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    settleMotion();

    // Records are collected raw and analysed at the end rather than judged in
    // the callback. `oldValue` is the only trustworthy reading: `r.target`'s
    // className at CALLBACK time is the current one, so an add-and-remove inside
    // a single synchronous block would look like nothing happened. The sequence
    // of oldValues plus the final value IS every value the attribute held.
    const records: MutationRecord[] = [];
    const obs = new MutationObserver((batch) => {
      records.push(...batch);
    });
    obs.observe(host(), {
      attributes: true,
      attributeFilter: ["class"],
      attributeOldValue: true,
    });

    clickButton("Allow");
    expect(host().classList.contains("hidden")).toBe(false);
    await Promise.resolve();
    settleMotion();
    await Promise.resolve();
    records.push(...obs.takeRecords());
    obs.disconnect();

    const held = [...records.map((r) => r.oldValue ?? ""), host().className];
    expect(held.filter((cls) => cls.split(/\s+/).includes("hidden"))).toEqual([]);
    expect(liveCard()).not.toBeNull();
  });

  it("updates the LIVE depth row mid-advance, not the answered card's stale one", () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    pushPerm("c1", 3);
    settleMotion();
    expect(liveDepth()?.textContent).toBe("2 more waiting");

    clickButton("Allow");
    expect(liveDepth()?.textContent).toBe("1 more waiting");

    // A queue-depth change for the SAME head takes the update-in-place branch,
    // which must not rebuild the card (that would discard the user's typing).
    // The answered card is prepended, so it and its stale depth row come FIRST
    // in document order: an unscoped lookup writes into the card on its way out
    // and the live row keeps a number that is no longer true.
    collapseSettledDecision("c1", "permission", 3, "user");

    expect(liveDepth()?.textContent).toBe("");
    expect(liveDepth()?.classList.contains("hidden")).toBe(true);
    expect(liveCards().length).toBe(1);
  });

  it("does not drop the next decision, and it is answerable", () => {
    const first = pushPerm("c1", 1);
    const second = pushPerm("c1", 2);
    settleMotion();

    clickButton("Allow");
    expect(first).toHaveBeenCalledTimes(1);
    // Mid-advance, with the answered card still on screen: the incoming card is
    // live, not a placeholder.
    clickButton("Reject");
    expect(second).toHaveBeenCalledWith("reject_once", undefined);
  });
});

describe("the dispatch is never gated on the animation", () => {
  it("submits in the same tick as the click, before any timer runs", () => {
    const submit = pushPerm("c1", 1);
    settleMotion();

    clickButton("Allow");
    // No timer advanced, no frame waited: `settle` splices, dispatches and
    // bumps synchronously inside the click handler, and the render effect that
    // starts the animation runs after the dispatch has returned.
    expect(submit).toHaveBeenCalledTimes(1);
    expect(submit).toHaveBeenCalledWith("allow_once", undefined);
    // And the phase is live at that same moment, so the two genuinely overlap.
    expect(host().dataset["dockPhase"]).toBe("leaving");
  });
});

describe("interruption and cleanup", () => {
  it("survives rapid Allow-Allow-Allow with nothing half-faded left over", () => {
    const subs = [pushPerm("c1", 1), pushPerm("c1", 2), pushPerm("c1", 3)];
    settleMotion();

    // No timer advance between clicks: every phase interrupts the previous one.
    clickButton("Allow");
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(1);

    clickButton("Allow");
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(1);

    clickButton("Allow");
    expect(outgoings().length).toBe(1);
    expect(liveCards().length).toBe(0);

    for (const s of subs) {
      expect(s).toHaveBeenCalledTimes(1);
    }

    settleMotion();
    // The final state, with nothing orphaned anywhere in the document and no
    // inline geometry left pinned on the box.
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    expect(document.querySelectorAll(".dock-outgoing").length).toBe(0);
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(host().style.height).toBe("");
    expect(host().style.marginBlockEnd).toBe("");
    expect(host().style.transition).toBe("");
  });

  it("re-enters when a decision arrives mid-exit instead of completing the exit", () => {
    pushPerm("c1", 1);
    settleMotion();
    clickButton("Allow");
    expect(host().dataset["dockPhase"]).toBe("leaving");

    // The exit's own timer must not survive to hide a dock that has something
    // in it again.
    pushPerm("c1", 2);
    expect(host().dataset["dockPhase"]).toBe("entering");
    expect(liveCard()).not.toBeNull();

    settleMotion();
    expect(host().classList.contains("hidden")).toBe(false);
    expect(liveCard()).not.toBeNull();
    expect(outgoings().length).toBe(0);
  });

  it("neutralises the answered card so it is neither read again nor focusable", () => {
    pushPerm("c1", 1);
    pushPerm("c1", 2);
    settleMotion();
    liveCard()?.querySelector<HTMLButtonElement>("button")?.focus();

    clickButton("Allow");

    const [out] = outgoings();
    expect(out?.getAttribute("aria-hidden")).toBe("true");
    expect(out?.hasAttribute("inert")).toBe(true);
    expect(out?.contains(document.activeElement)).toBe(false);
  });

  it("takes no second answer from a handle retained on the answered card", () => {
    const first = pushPerm("c1", 1);
    const second = pushPerm("c1", 2);
    settleMotion();

    const allow = liveCard()?.querySelector<HTMLButtonElement>("button");
    allow?.click();
    // The card is inside `.dock-outgoing` now. A scripted click still dispatches
    // through `inert`, so the authoritative guard is `settle`'s membership check
    // — and the incoming decision must not be answered by the outgoing card's
    // button either, which is why `user-input.ts` stopped keeping its reporter
    // in module state.
    allow?.click();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).not.toHaveBeenCalled();
  });

  it("_resetForTest leaves no timer that can fire into a later test", () => {
    const dock = host();
    pushPerm("c1", 1);
    settleMotion();
    clickButton("Allow");
    expect(dock.dataset["dockPhase"]).toBe("leaving");

    _resetForTest();
    // endPhase ran: the outgoing card is gone and the phase attribute with it,
    // but the host was deliberately NOT hidden, so a surviving timer would be
    // visible as a `.hidden` appearing out of nowhere.
    expect(dock.querySelectorAll(".dock-outgoing").length).toBe(0);
    expect(dock.dataset["dockPhase"]).toBeUndefined();

    settleMotion();
    expect(dock.classList.contains("hidden")).toBe(false);
  });
});

describe("motion off: reduced motion and a background tab", () => {
  function reduceMotion(): void {
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q.includes("prefers-reduced-motion"),
      media: q,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
  }

  it("swaps instantly on an exit, with the same DOM and the same response", () => {
    const submit = pushPerm("c1", 1);
    reduceMotion();

    clickButton("Allow");

    // Same tick, no phase, nothing to clean up.
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(outgoings().length).toBe(0);
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("swaps instantly on an advance too", () => {
    pushPerm("c1", 1);
    const second = pushPerm("c1", 2);
    reduceMotion();

    clickButton("Allow");

    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(outgoings().length).toBe(0);
    expect(liveCards().length).toBe(1);
    clickButton("Reject");
    expect(second).toHaveBeenCalledWith("reject_once", undefined);
  });

  it("takes no phase in a background tab, where setTimeout runs but animations do not", () => {
    // An accessor on the prototype, so spying on the instance does not work.
    const submit = pushPerm("c1", 1);
    vi.spyOn(Document.prototype, "hidden", "get").mockReturnValue(true);

    clickButton("Allow");

    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().dataset["dockPhase"]).toBeUndefined();
    expect(outgoings().length).toBe(0);
    expect(submit).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// The stylesheet, read as SOURCE. The test page links no app stylesheet, so
// there is no cascade for `getComputedStyle` to report on; these assert what is
// authored. See __test-helpers__/css-rules.ts.
// ---------------------------------------------------------------------------

describe("26-dock.css carries the phases the module names", () => {
  const dock = loadCSS("26-dock.css");

  it("declares the enter, exit and advance rules", () => {
    expect(
      /height var\(--dur-standard\)/.test(ruleContaining(dock, ".decision-dock", "top").body),
    ).toBe(true);
    expect(
      /animation: vk-slide-up/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="entering"] > .dock-card').body,
      ),
    ).toBe(true);
    expect(
      /height var\(--dock-exit-dur\)/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="leaving"]').body,
      ),
    ).toBe(true);
    expect(
      /height var\(--dur-exit\)/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="advancing"]').body,
      ),
    ).toBe(true);
  });

  it("does not make the exit a display toggle", () => {
    // The whole point of the phase attribute: `.hidden` is `display: none
    // !important` from an unlayered utility rule and beats any component rule
    // that tries to animate it, which is why the old `.decision-dock.hidden`
    // collapse never rendered. The class lands after the collapse instead.
    const leaving = ruleContaining(dock, '.decision-dock[data-dock-phase="leaving"]');
    expect(/display:/.test(leaving.body)).toBe(false);
  });

  it("takes the outgoing card out of flow only for the advance", () => {
    // In flow for the exit, so a shrinking box CLIPS it; out of flow for the
    // advance, so the box's height is the incoming card's and the two overlap.
    expect(/position:/.test(ruleContaining(dock, ".dock-outgoing").body)).toBe(false);
    expect(
      /position: absolute/.test(
        ruleContaining(dock, '.decision-dock[data-dock-phase="advancing"] > .dock-outgoing').body,
      ),
    ).toBe(true);
  });

  it("offsets the two advance halves in opposite directions", () => {
    expect(/translate: var\(--dock-shift\) 0/.test(dock)).toBe(true);
    expect(/translate: calc\(-1 \* var\(--dock-shift\)\) 0/.test(dock)).toBe(true);
  });

  it("is disarmed under reduced motion, which the global duration sweep cannot do", () => {
    // A zeroed animation RUNS to completion rather than being suppressed, and the
    // sweep says nothing about the inline pixel height the module pins.
    const a11y = loadCSS("40-a11y.css");
    expect(
      /transition: none/.test(
        ruleContaining(a11y, ".decision-dock[data-dock-phase]", "prefers-reduced-motion").body,
      ),
    ).toBe(true);
    expect(
      /display: none/.test(ruleContaining(a11y, ".dock-outgoing", "prefers-reduced-motion").body),
    ).toBe(true);
  });

  // The two guards below are why the rule above is spelled with an attribute
  // selector. Asserting its BODY says `transition: none` proves the declaration
  // was authored, not that it ever applies — and the first spelling of this rule
  // was a bare `.decision-dock`, which scores (0,1,0) and loses to every
  // `.decision-dock[data-dock-phase="…"]` (0,2,0) phase rule in 26-dock.css. It
  // passed a body assertion while being dead in the only case it exists for: a
  // live phase. A body-only test cannot see that, so the cascade facts it
  // depends on are pinned directly.
  it("spells the reduced-motion disarm specifically enough to beat a live phase", () => {
    const a11y = loadCSS("40-a11y.css");
    const disarm = ruleContaining(
      a11y,
      ".decision-dock[data-dock-phase]",
      "prefers-reduced-motion",
    ).selector;
    // Every phase rule qualifies `.decision-dock` with one attribute, so the
    // disarm needs one too. A bare class can never win, whatever the file order.
    expect(disarm).toMatch(/\.decision-dock\[data-dock-phase/u);
  });

  it("keeps 40-a11y.css after 26-dock.css, which is what breaks the specificity tie", () => {
    // Both files are unlayered and the disarm ties the phase rules at (0,2,0),
    // so it wins on source order alone. That makes the MANIFEST's order a
    // load-bearing part of the reduced-motion behaviour rather than a listing
    // convention: swapping these two lines silently re-arms the animation.
    const order = cssManifest.split("\n").map((l) => l.trim());
    expect(order.indexOf("40-a11y.css")).toBeGreaterThan(order.indexOf("26-dock.css"));
  });
});

describe("the cleanup timer and the stylesheet agree on every duration", () => {
  // There is no transitionend or animationend listener in the module: one timer
  // per host is the sole cleanup authority. The cost of that is a duplicated
  // number, so a retune of one side without the other must fail here rather than
  // leave an outgoing card on screen or remove it mid-animation.
  const dock = loadCSS("26-dock.css");

  function tokenMs(name: string): number {
    for (const file of ["26-dock.css", "01-tokens.css"]) {
      const hit = new RegExp(`\\${name}:\\s*([0-9.]+)s`).exec(loadCSS(file));
      if (hit?.[1] !== undefined) {
        return Number(hit[1]) * 1000;
      }
    }
    throw new Error(`no time token ${name}`);
  }

  function heightToken(body: string): string {
    const hit = /height (var\((--[a-z-]+)\))/.exec(body);
    expect(hit?.[2], `no height transition token in ${body}`).toBeDefined();
    return hit?.[2] ?? "";
  }

  it("matches DOCK_PHASE_MS to the transition each phase actually runs", () => {
    const pairs: [keyof typeof DOCK_PHASE_MS, string][] = [
      ["entering", heightToken(ruleContaining(dock, ".decision-dock", "top").body)],
      [
        "leaving",
        heightToken(ruleContaining(dock, '.decision-dock[data-dock-phase="leaving"]').body),
      ],
      [
        "advancing",
        heightToken(ruleContaining(dock, '.decision-dock[data-dock-phase="advancing"]').body),
      ],
    ];
    for (const [phase, token] of pairs) {
      expect(DOCK_PHASE_MS[phase], `${phase} vs ${token}`).toBe(tokenMs(token));
    }
  });

  it("keeps the enter and exit inside the bands the requirement names", () => {
    expect(DOCK_PHASE_MS.entering).toBeGreaterThanOrEqual(180);
    expect(DOCK_PHASE_MS.entering).toBeLessThanOrEqual(220);
    expect(DOCK_PHASE_MS.leaving).toBeGreaterThanOrEqual(110);
    expect(DOCK_PHASE_MS.leaving).toBeLessThanOrEqual(140);
    // The exit is the fast one: the tray has to be gone before the reader
    // reaches for the box underneath it.
    expect(DOCK_PHASE_MS.leaving).toBeLessThan(DOCK_PHASE_MS.entering);
  });
});
