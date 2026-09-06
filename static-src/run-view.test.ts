// ---------------------------------------------------------------------------
// Tests for the run tab: its control gate, its structure, its outputs and its
// empty-step notes.
//
// The FLAVOUR gate these cases used to pin is gone (user decision, 2026-08). It said
// which door you came through decided whether the run was actionable — an owned tab
// from the Workflows launcher carried the live verbs, a History review carried only
// retry — and it existed because the owned tab's × was the stop. The × no longer
// stops anything: the subpage view is universal across a parentless workflow, a
// chat-triggered workflow and a subagent expansion, so its × closes a view. With the
// × disarmed, gating Cancel by door would leave a live run readable from History and
// unstoppable, so the verbs are the STATUS's wherever the run is read from.
//
// ONE gate is left and it now subtracts ONE VERB rather than all four (2026-09). It
// used to withhold everything from an agent-parented run, on the claim that such a
// run has no carrier vibekit can reach; `hostBridge` (run_host.go) resolves one from
// the LAUNCHING CHAT's live bridge, so pause and resume reach it whenever that chat
// is open and cancel reaches it unconditionally. Only RETRY stays withheld, and that
// is a standing user decision rather than a carrier fact: an agent-parented run's
// recovery is the agent's own, and retry is the one verb that RE-HOSTS.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, beforeAll, afterAll } from "vitest";
import type * as RunStore from "./run-store.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

/** The run store's own module type, so the partial mock below keeps every export it
 *  does not override. Spelled as a namespace type import rather than an inline
 *  `import()` annotation, which the lint forbids; `docs.test.ts` is the precedent. */
type RunStoreModule = typeof RunStore;

interface RunInspectReply {
  workflowId: string;
  state?: {
    workflowId: string;
    status?: string;
    capturedOutputs?: Record<string, string>;
    root?: unknown;
    inputs?: Record<string, string>;
    /** KAS's own answer to "was this launched by an agent": an ACP session id,
     *  empty for a manual and a scheduled launch alike. */
    parentSessionId?: string;
  };
  nodePlan?: unknown;
}

/** What a door decided, which is all a door decides now: `owns` (does the ×
 *  stop the run) and `parent` (which chat it nests under). Both are SUBJECT
 *  fields, so they travel to every device rather than living in a local spec. */
interface OpenedTab {
  id: string;
  opts?: { parent?: string; owns?: boolean; activate?: boolean } | undefined;
}

// Hoisted with the vi.mock factories below, which run before ordinary top-level
// initialisers and would otherwise read these in their TDZ.
const m = vi.hoisted(() => ({
  reply: { current: undefined as unknown },
  opened: [] as {
    id: string;
    opts?: { parent?: string; owns?: boolean; activate?: boolean } | undefined;
  }[],
  dispatched: [] as string[],
  /** The launching chat a run tab's `TabSubject.Parent` resolves to. The
   *  post-reload answer for a chat-parented run whose lease has been released, and
   *  the only client-side one. */
  parentChat: { current: "" },
  /** The run store's own record of the launching chat — the LIVE answer, fed by SSE
   *  frames and by `GET /api/runs/live`, and the first of the two sources. */
  runChat: { current: "" },
  /** What `openTab` was asked for. The affordance's whole observable. */
  tabbed: [] as { kind: string; ref?: string }[],
  /** The node paths the chat-step stream was handed a slice for, and the hosts it
   *  was given. Together they are the no-orphan-host rule's observable. */
  projected: [] as string[],
  hosted: [] as HTMLElement[],
  /** Which ROUTE each painted path came from (`RunStepPaint.source.kind`). The
   *  slice-wins rule's whole observable: both routes render identical blocks into
   *  the same host, so the source is the only thing that separates them. */
  sources: [] as string[],
  /** `<kind>:<ref>` for every open tab, as `hasTab` sees it. */
  tabsOpen: new Set<string>(),
  /** The KAS-route read this client holds per node path, and the slice it projects.
   *  Two maps rather than one, because they are two different answers: a `ready`
   *  read with no blocks is a real state (`stepRead` returns it, `stepSliceFor`
   *  withholds it) and it is exactly the case one note arm exists for. */
  reads: new Map<string, { state: string }>(),
  kasSlices: new Map<string, { blocks: unknown[]; toolCalls: unknown[] }>(),
  /** Every path a read was ARMED for, in order. `armStepRead`'s four gates are all
   *  about what must NOT reach here, so the observable is this list staying empty. */
  requested: [] as string[],
  /** How many times the page dropped every read. */
  cleared: { count: 0 },
}));

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve(m.reply.current)),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
}));

vi.mock("./tabs.js", () => ({
  // The spec is the FACTORY's now, so there is no onShow and no onClose to
  // capture: what a door decides is `owns` (does the × stop the run) and
  // `parent` (which chat it nests under), both subject fields.
  openRunTab: vi.fn(
    (id: string, _name: string, opts?: { parent?: string; owns?: boolean; activate?: boolean }) => {
      m.opened.push({ id, opts });
      return Promise.resolve();
    },
  ),
  // Both reached through run-dots.js, which run-view imports to seed a parentless
  // run's tab dot from the fetch. "" keeps the seed inert here: this suite opens
  // no real tabs, so there is no dot to paint.
  tabIdFor: vi.fn(() => ""),
  tabSetVersion: vi.fn(() => 0),
  // A run's tab row is renamed once its state arrives (run-dots.ts). Inert here;
  // a Browser-Mode mock is linked as real ESM, so a name any module in the graph
  // reaches has to exist on it.
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
  // The completion auto-close's two reads, and the names this graph pulls in.
  // Inert here for the same reason `tabIdFor` is: with no tab id to resolve
  // nothing is closable, and the page renders the run CARD, whose markdown
  // bubble reaches the linkifier and through it the editor openers. No case
  // here closes a tab or opens a file; run-subtab.test.ts owns the
  // auto-close's rules.
  closeTab: vi.fn(),
  getActiveTabId: vi.fn(() => ""),
  openEditorView: vi.fn(),
  setTabDirty: vi.fn(),
  toggleGitView: vi.fn(),
  // The launching chat a run tab nests under, read off the persisted subject. The
  // cases below drive it directly, because the alternative is a whole tab
  // projection for one string.
  parentChatRef: vi.fn(() => m.parentChat.current),
  // The eviction exemption's own reader, and the affordance's door. `hasTab`
  // answers off a set the cases drive; `openTab` records so the link's dispatch is
  // observable.
  hasTab: vi.fn((kind: string, ref: string) => m.tabsOpen.has(`${kind}:${ref}`)),
  openTab: vi.fn((args: { kind: string; ref?: string }) => {
    m.tabbed.push({ kind: args.kind, ...(args.ref === undefined ? {} : { ref: args.ref }) });
    return Promise.resolve("opened");
  }),
}));

// `run-store.js` is REAL except for its LAUNCHING-CHAT MEMORY, and that carve-out
// is not convenience. `launchingChatOf` writes a tab-derived answer back through
// `noteRunChat` so every other reader converges on one, which makes `launchedBy`
// module state that leaks between cases — and there is no reset for it alone:
// `forgetRun` also drops the run's state CELL, which this module's one effect is
// subscribed to, so dropping it would leave every later case unpainted. The fetch,
// the cache and the coalescing stay real, because the paint path under test is
// driven by them.
vi.mock("./run-store.js", async (importOriginal) => {
  const actual = await importOriginal<RunStoreModule>();
  return {
    ...actual,
    runChatID: vi.fn(() => m.runChat.current),
    noteRunChat: vi.fn((_workflowID: string, chatID: string) => {
      if (chatID !== "") {
        m.runChat.current = chatID;
      }
    }),
  };
});

vi.mock("./decision-dock.js", () => ({
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  // Reached through run-dots.js, which run-view imports to seed a parentless
  // run's tab dot from the fetch.
  hasPendingDecision: vi.fn(() => false),
  // The card's second input beside `inspect`: which step is blocked on a person,
  // which no node status can say. None is, in every case here.
  runPendingAsks: vi.fn(() => ({ count: 0, nodes: new Set<string>(), label: "" })),
}));

vi.mock("./actions/runs.js", () => {
  const stub = (verb: string) => ({
    dispatch: vi.fn((id: string) => {
      m.dispatched.push(`${verb}:${id}`);
      return Promise.resolve();
    }),
  });
  return {
    cancelRun: stub("cancel"),
    pauseRun: stub("pause"),
    resumeRun: stub("resume"),
    retryRun: stub("retry"),
  };
});

// `run-chat-steps.js` is MOCKED, and for two reasons. Unmocked, the lazy import
// links the real `messages-blocks.ts` graph — the editor openers, the navigator and
// through them `chat.ts`, the whole transcript stack — in a suite that mounts only
// `#run-body` and `#run-dock`. And what run-view OWNS here is the WIRING (resolve
// the chat, slice it, load lazily, hand out the right host per path); the render
// lifecycle is `run-chat-steps.test.ts`'s subject.
vi.mock("./run-chat-steps.js", () => ({
  createRunChatStepStream: vi.fn((hostFor: (path: string) => HTMLElement) => ({
    // One PAINT per path now, not one slice: the stream is fed the blocks AND the
    // route they came from, because a chat step's blocks carry live per-block
    // signals and a delegate deep link while a KAS read's carry neither.
    apply: (
      paints: ReadonlyMap<string, { slice: { blocks: unknown[] }; source: { kind: string } }>,
    ) => {
      for (const [path, paint] of paints) {
        m.projected.push(path);
        m.sources.push(paint.source.kind);
        const host = hostFor(path);
        m.hosted.push(host);
        const marker = document.createElement("p");
        marker.className = "step-marker";
        marker.textContent = `${path}:${String(paint.slice.blocks.length)}`;
        host.appendChild(marker);
      }
    },
    dispose: vi.fn(),
  })),
}));

// `run-step-transcript.js` is MOCKED for the same division of labour as the stream
// above: what run-view owns is WHEN a read is armed and WHICH sentence each verdict
// produces, and `run-step-transcript.test.ts` owns the module's own rules (the URL,
// the no-refetch gate, the failure grading, the projection). Driving the verdicts
// directly is also the only way to reach one note arm per case — through the real
// module every arm would have to be produced by a differently-shaped
// `apiGetTypedOrError` envelope.
//
// The version signal has to be a REAL signal: `installViewEffect` reads its `.value`
// as its subscription, so a stub would leave the effect subscribed to nothing.
vi.mock("./run-step-transcript.js", async () => {
  const { signal } = await import("@cplieger/reactive");
  return {
    stepTranscriptVersion: signal(0),
    stepRead: vi.fn((_workflowID: string, nodePath: string) => m.reads.get(nodePath)),
    stepSliceFor: vi.fn((_workflowID: string, nodePath: string) => m.kasSlices.get(nodePath)),
    requestStepTranscript: vi.fn((_workflowID: string, nodePath: string) => {
      m.requested.push(nodePath);
    }),
    clearStepTranscripts: vi.fn(() => {
      m.cleared.count += 1;
    }),
  };
});

// `showRun` is what the tab FACTORY calls as the run tab's activation hook
// (registered by the composition root), so it is the seam this suite paints
// through — a door no longer carries an `onShow` of its own.
import { openRunView, runTabProjectsChat, showRun } from "./run-view.js";
// The MOCK's signal, which is the one the view effect subscribes to. Imported so a
// case can drive the "a read resolved" half of `fetchStep`'s `finally` directly.
import { stepTranscriptVersion } from "./run-step-transcript.js";
// The REAL invalidation (the run-store mock overrides only its launching-chat
// memory), so a case can drive a second fetch of the SAME run: that is how a step
// settling under the reader's cursor reaches the page in production.
import { invalidateRun } from "./run-store.js";
import { appendChunk, setSessions } from "./store.js";
import { clearAllBlockSigs, ensureBlockTextSig } from "./store-signals.js";
// The REAL router: a zero-import leaf, so no mock, and taking the node from the
// PARSER rather than a literal is what makes the fragment case below cover the
// whole URL → focus chain instead of just the opener's fourth argument.
import { parseRoute } from "./router.js";
import type { Message, Session } from "./types.js";

/** Open a run through one of the two doors and let its first paint settle.
 *  Returns the control labels on screen, in order. */
async function paint(
  door: (id: string, name: string) => void,
  status: string,
  capturedOutputs?: Record<string, string>,
  opts: {
    parentless?: boolean;
    root?: unknown;
    inputs?: Record<string, string>;
    nodePlan?: unknown;
    /** The RUN's own answer to "was this launched by an agent". Defaults from
     *  `parentless`, which is now only the pre-fetch hint. */
    parentSessionId?: string;
  } = {},
): Promise<{ labels: string[]; tab: OpenedTab; body: HTMLElement }> {
  const parentless = opts.parentless ?? true;
  const parentSessionId = opts.parentSessionId ?? (parentless ? "" : "acp-sess-1");
  const reply: RunInspectReply = {
    workflowId: "wf_1",
    state: {
      workflowId: "wf_1",
      status,
      ...(capturedOutputs === undefined ? {} : { capturedOutputs }),
      ...(opts.root === undefined ? {} : { root: opts.root }),
      ...(opts.inputs === undefined ? {} : { inputs: opts.inputs }),
      ...(parentSessionId === "" ? {} : { parentSessionId }),
    },
    // Beside `state`, not inside it: the plan is its own member of KAS's inspect
    // reply and the store keeps the two apart for that reason.
    ...(opts.nodePlan === undefined ? {} : { nodePlan: opts.nodePlan }),
  };
  m.reply.current = reply;

  document.body.replaceChildren();
  const body = document.createElement("div");
  body.id = "run-body";
  const dock = document.createElement("div");
  dock.id = "run-dock";
  document.body.append(body, dock);

  door("wf_1", "nightly");
  const tab = m.opened.at(-1);
  if (tab === undefined) {
    throw new Error("the opener did not open a tab");
  }
  // The activation hook, driven the way the composition root wires it. `parentless`
  // is only the PRE-FETCH hint now — the loading row has nothing else to go on —
  // and the first reply replaces it from `state.parentSessionId`.
  showRun(tab.id, parentless);
  // load() awaits one apiGet before painting; drain enough microtasks for the
  // promise chain to settle without reaching for fake timers.
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
  }

  const labels = [...body.querySelectorAll(".run-controls button")].map((b) =>
    (b.textContent ?? "").trim(),
  );
  return { labels, tab, body };
}

/** A resident chat window, so the slice has something to project out of. */
function chatSession(id: string, messages: Message[], thinking = false): Session {
  return {
    id,
    name: "the conversation",
    model: "claude-opus",
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
    message_count: messages.length,
    messages,
    has_more: false,
    thinking,
    working_label: "Thinking",
  };
}

/** One assistant message carrying a step's block, stamped the way the server
 *  stamps it (`wf:<workflowId>:<nodePath>`). */
function stepMessage(id: string, nodePath: string, text: string, workflowID = "wf_1"): Message {
  return {
    id,
    role: "assistant",
    ts: 0,
    content: "",
    blocks: [{ type: "text", text, agent_subtask_id: `wf:${workflowID}:${nodePath}` }],
    tool_calls: [],
  };
}

// The shipped stylesheet, because the instructions clamp DECIDES on a measurement:
// with no `-webkit-line-clamp` in force nothing is ever clipped, so every clamp case
// would report "fits" and pass for the wrong reason. Mounted for the whole file so
// the page renders here the way it renders in production.
let styleEl: HTMLStyleElement;

beforeAll(() => {
  styleEl = mountAppCSS();
});

afterAll(() => {
  styleEl?.remove();
});

beforeEach(() => {
  m.opened.length = 0;
  m.dispatched.length = 0;
  m.tabbed.length = 0;
  m.projected.length = 0;
  m.hosted.length = 0;
  m.sources.length = 0;
  m.parentChat.current = "";
  m.runChat.current = "";
  m.tabsOpen.clear();
  m.reads.clear();
  m.kasSlices.clear();
  m.requested.length = 0;
  m.cleared.count = 0;
  // The REAL chat store is linked here (only api-client, tabs, decision-dock,
  // run-chat-steps and actions/runs are mocked), so the launching chat's window is
  // real state that has to be cleared between cases. The block signals go with it:
  // one left mounted silences `appendChunk`'s signal-absent arm for the next case.
  setSessions([]);
  clearAllBlockSigs();
});

describe("run view controls", () => {
  // The verbs a status accepts are `run-controls.ts`'s pure table; what these pin is
  // that the row is offered at all, from every door, for a run vibekit hosts.
  it("offers the status's verbs on a parentless run, whatever the door", async () => {
    expect((await paint(openRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openRunView, "paused")).labels).toEqual(["Resume", "Cancel"]);
  });

  // The gate subtracts ONE verb, not all four, and that is a behaviour change: the
  // page used to offer nothing at all for an agent-parented run, on the claim that
  // such a run has no carrier vibekit can reach. `hostBridge` (run_host.go) made
  // that false — it resolves the run's carrier from the LAUNCHING CHAT's live bridge
  // by session chain — so pause and resume reach it whenever that chat is open, and
  // cancel reaches it unconditionally through the utility session. Suppressing them
  // left a wedged agent-launched run recoverable only by `curl`.
  it("offers the live verbs on an agent-parented run", async () => {
    expect((await paint(openRunView, "running", undefined, { parentless: false })).labels).toEqual([
      "Pause",
      "Cancel",
    ]);
    expect((await paint(openRunView, "paused", undefined, { parentless: false })).labels).toEqual([
      "Resume",
      "Cancel",
    ]);
  });

  // RETRY is the one that stays withheld, and it is a standing user decision rather
  // than a carrier fact: an agent-parented run's recovery is the agent's own, and
  // retry is also the one verb that RE-HOSTS, so it would put vibekit's bridge under
  // a run the agent still believes it owns.
  it("withholds only retry on an agent-parented run", async () => {
    for (const status of ["failed", "aborted"]) {
      expect((await paint(openRunView, status, undefined, { parentless: false })).labels).toEqual(
        [],
      );
    }
  });

  // The gate reads `state.parentSessionId` — the RUN's own fact — rather than client
  // memory of a launching chat. Retry is what it decides, so this asserts on a
  // FAILED run: a running one is offered the same verbs either way, so it could not
  // tell the gate from its absence.
  it("withholds retry on a run whose parentSessionId is set and whose chat is unknown", async () => {
    const { labels } = await paint(openRunView, "failed", undefined, {
      parentSessionId: "acp-sess-9",
    });
    expect(labels).toEqual([]);
  });

  // ...and the inverse, which is what keeps the gate from withholding retry from a
  // manual run: a scheduled or Workflows-tab launch has NO parent session.
  it("still offers retry when parentSessionId is empty", async () => {
    const { labels } = await paint(openRunView, "failed", undefined, { parentSessionId: "" });
    expect(labels).toEqual(["Retry failed steps"]);
  });

  // A COMPLETED run offers nothing: there is no failed work to reset and nothing to
  // stop. The gate subtracts and must never be the thing that ADDS a verb a terminal
  // status does not accept.
  it("offers no controls on a completed run", async () => {
    expect((await paint(openRunView, "completed")).labels).toEqual([]);
  });

  // Retry acts on a FINISHED run, which is why History carrying it matters: that is
  // where a failed run is found.
  it("offers retry on a failed run", async () => {
    for (const status of ["failed", "aborted"]) {
      expect((await paint(openRunView, status)).labels).toContain("Retry failed steps");
    }
  });

  // The view is shared — one DOM element serves every run tab — so switching from a
  // parentless run to an agent-parented one must repaint the row. A stale row would
  // be the failure mode of the shared-element design.
  //
  // It asserts on a FAILED run because that is where the two authorities differ now:
  // a running run is offered the same verbs either way, so a `running` fixture would
  // pass whether the repaint happened or not.
  it("repaints the row when the shown run changes authority", async () => {
    expect((await paint(openRunView, "failed")).labels).toEqual(["Retry failed steps"]);
    expect((await paint(openRunView, "failed", undefined, { parentless: false })).labels).toEqual(
      [],
    );
  });

  // A run tab is a VIEW: every door opens it with `owns: false`, so its × closes a
  // view and stops nothing. This is the assertion that used to distinguish the two
  // doors, inverted.
  it("opens as a view from every door", async () => {
    const review = await paint(openRunView, "running");
    expect(review.tab.opts?.owns).toBe(false);
  });
});

// A step only gets a capturedOutputs key when it captured, and the captured value
// is its last assistant text. So an EMPTY value is a fact about the run, not an
// absence — hiding it made a silent step indistinguishable from one that never ran,
// on the surface whose whole job is reading a finished run.
//
// The vocabulary is the EXEC VIEW's (`ev-*`), which is neither this page's old
// hand-rolled one nor the transcript card's: the page renders a generic
// delegated-execution view that a subagent tab will reuse, so nothing on it is
// named for workflows. Results roll up into a foot disclosure; a single step's own
// output lives in the detail pane.
describe("run view captured output", () => {
  it("rolls every capture up into the results region", async () => {
    const { body } = await paint(openRunView, "completed", { build: "ok", review: "done" });
    const keys = [...body.querySelectorAll(".ev-r-item-key")].map((n) => n.textContent);
    expect(keys).toEqual(["build", "review"]);
    expect(body.querySelector(".ev-r-count")?.textContent).toBe("2");
  });

  it("renders an empty capture with its reason instead of dropping it", async () => {
    const { body } = await paint(openRunView, "completed", { review: "   " });
    expect([...body.querySelectorAll(".ev-r-item-key")].map((n) => n.textContent)).toEqual([
      "review",
    ]);
    expect(body.querySelector(".ev-r-item-empty")?.textContent).toContain(
      "without producing any text",
    );
  });

  it("renders a non-empty capture as MARKDOWN, not as preformatted text", async () => {
    // A captured report is a step's last assistant message, so it is markdown and
    // the review page is where it gets read. Rendered through the transcript's own
    // bubble: before this it sat in a `<pre>`, showing its own asterisks.
    const { body } = await paint(openRunView, "completed", { build: "**shipped** ok" });
    expect(body.querySelector(".ev-r-item-body strong")?.textContent).toBe("shipped");
    expect(body.querySelector("pre.run-output-body")).toBeNull();
  });

  // No keys at all means no region: a run whose steps all declared
  // captureOutput:false has nothing to say here, and a visible empty disclosure
  // would claim otherwise.
  it("hides the region when the run captured nothing", async () => {
    const { body } = await paint(openRunView, "completed", {});
    expect(body.querySelector(".ev-results")?.hasAttribute("hidden")).toBe(true);
  });

  // OPEN by default, both levels. A run's product is what a reader opens the page
  // for, and with the page scrolling an open roll-up costs page height rather than
  // pane height — which is the whole reason it used to be shut.
  it("opens the region on first render rather than collapsed", async () => {
    const { body } = await paint(openRunView, "completed", { build: "ok" });
    const region = body.querySelector(".ev-results");
    expect(region?.classList.contains("collapsed")).toBe(false);
    expect(body.querySelector(".ev-r-head")?.getAttribute("aria-expanded")).toBe("true");
  });

  it("gives every capture its own open box", async () => {
    const { body } = await paint(openRunView, "completed", { build: "ok", review: "done" });
    const heads = [...body.querySelectorAll(".ev-r-item > .ev-r-item-head")];
    expect(heads).toHaveLength(2);
    expect(heads.map((h) => h.getAttribute("aria-expanded"))).toEqual(["true", "true"]);
  });

  // A real activation target: `createDisclosure` puts `role="button"` and
  // `tabindex="0"` on a non-`<button>` trigger itself, so the head is reachable by
  // keyboard, and the chevron beside it stays a span (nested-interactive).
  it("makes each box's header a keyboard-reachable button with no nested control", async () => {
    const { body } = await paint(openRunView, "completed", { build: "ok" });
    const head = body.querySelector<HTMLElement>(".ev-r-item-head");
    expect(head?.getAttribute("role")).toBe("button");
    expect(head?.getAttribute("tabindex")).toBe("0");
    expect(head?.querySelectorAll("button, a[href], input")).toHaveLength(0);
  });

  it("collapses only the box whose header was clicked", async () => {
    const { body } = await paint(openRunView, "completed", { build: "ok", review: "done" });
    const heads = [...body.querySelectorAll<HTMLElement>(".ev-r-item-head")];
    heads[0]?.click();
    expect(heads.map((h) => h.getAttribute("aria-expanded"))).toEqual(["false", "true"]);
    // And the outer region is untouched: the two levels are independent.
    expect(body.querySelector(".ev-results")?.classList.contains("collapsed")).toBe(false);
  });
});

// The header's instructions: clamped to three lines with an opener that appears
// ONLY when the content really overflows. Overflow is MEASURED
// (`clamp-text.ts`'s shared ResizeObserver compares scrollHeight against
// clientHeight); the character threshold is the pre-layout guess for the frame the
// element is still detached in, and these cases pin that the measurement is what
// decides — a character-count implementation gets the third one wrong.
describe("run view instructions", () => {
  /** Let the clamp's ResizeObserver deliver its post-layout verdict. A callback is
   *  delivered after layout and before paint, so one frame is the settle. */
  async function settle(): Promise<void> {
    for (let i = 0; i < 2; i++) {
      await new Promise<void>((resolve) => {
        requestAnimationFrame(() => {
          resolve();
        });
      });
    }
  }

  function openers(body: HTMLElement): HTMLButtonElement[] {
    return [...body.querySelectorAll<HTMLButtonElement>(".ev-in-more")];
  }

  it("renders the value in its own clamped box", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      inputs: { repo: "vibekit", target: "static-src" },
    });
    expect([...body.querySelectorAll(".ev-in-k")].map((n) => n.textContent)).toEqual([
      "repo",
      "target",
    ]);
    expect([...body.querySelectorAll(".ev-in-text")].map((n) => n.textContent)).toEqual([
      "vibekit",
      "static-src",
    ]);
  });

  // The case the user called out, and the one a character count also gets right —
  // so it is the floor rather than the proof.
  it("offers NO opener on a short instruction", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      inputs: { repo: "vibekit" },
    });
    expect(openers(body).map((b) => b.hidden)).toEqual([true]);
    await settle();
    expect(openers(body).map((b) => b.hidden)).toEqual([true]);
  });

  it("offers an opener on an instruction that overflows the clamp", async () => {
    // Long enough to overflow three lines at any width this suite can run at.
    // Newlines would NOT do it: the value is not preformatted, so they collapse to
    // spaces — which is exactly why the character/newline guess cannot be trusted
    // and the observer has the last word.
    const { body } = await paint(openRunView, "running", undefined, {
      inputs: { task: "converge the chevrons and then the boxes ".repeat(80).trim() },
    });
    await settle();
    const [more] = openers(body);
    const text = body.querySelector<HTMLElement>(".ev-in-text");
    // The premise: the clamp really is clipping this value.
    expect(text!.scrollHeight - text!.clientHeight).toBeGreaterThan(1);
    expect(more?.hidden).toBe(false);
    expect(more?.tagName).toBe("BUTTON");
    // A real control with a real state, reachable by keyboard because it is a button.
    expect(more?.getAttribute("aria-expanded")).toBe("false");
    expect(more?.textContent).toBe("Show more");
    expect(text?.hasAttribute("data-clamped")).toBe(true);

    more?.click();
    expect(text?.hasAttribute("data-clamped")).toBe(false);
    expect(more?.getAttribute("aria-expanded")).toBe("true");
    expect(more?.textContent).toBe("Show less");
  });

  // THE MEASUREMENT CASE. This value is longer than the character fallback and
  // still fits three lines at this width, so the pre-layout guess says "overflows"
  // and the observer must overrule it. A character-count implementation leaves an
  // opener here that opens nothing.
  it("withdraws the opener when layout says the long value fits", async () => {
    const long = "shipped ".repeat(40).trim();
    expect(long.length).toBeGreaterThan(220);
    const { body } = await paint(openRunView, "running", undefined, { inputs: { task: long } });
    await settle();
    const text = body.querySelector<HTMLElement>(".ev-in-text");
    // The premise, or this case would assert nothing: the text really is not
    // clipped at this width.
    expect(text!.scrollHeight - text!.clientHeight).toBeLessThanOrEqual(1);
    expect(openers(body).map((b) => b.hidden)).toEqual([true]);
  });

  /** Re-render the SAME mounted page from a fresh `inspect` reply.
   *
   *  A fresh `state` object is what makes this a real second render: the store
   *  assigns it to a reactive cell, and an identical reference would notify nobody.
   *  `capturedOutputs` is carried so every case can prove the render actually ran —
   *  the results region is rebuilt whenever its own set changes, so it is the
   *  independent witness the inputs assertion needs. */
  async function rerender(
    tabID: string,
    inputs: Record<string, string>,
    capturedOutputs: Record<string, string>,
  ): Promise<void> {
    m.reply.current = {
      workflowId: "wf_1",
      state: { workflowId: "wf_1", status: "running", inputs, capturedOutputs },
    } satisfies RunInspectReply;
    showRun(tabID, true);
    for (let i = 0; i < 10; i++) {
      await Promise.resolve();
    }
  }

  // `render` runs on every store invalidation — dozens of times a minute on a live
  // run — so an unguarded rebuild would discard the reader's expansion and re-wire
  // every clamp. Node identity is the assertion because that is what survives.
  it("does not rebuild the list when the inputs are unchanged", async () => {
    const { body, tab } = await paint(openRunView, "running", undefined, {
      inputs: { repo: "vibekit" },
    });
    const before = body.querySelector(".ev-in-v");
    await rerender(tab.id, { repo: "vibekit" }, { build: "ok" });

    // The premise, or this case asserts nothing: the page really did re-render.
    expect(body.querySelector(".ev-r-count")?.textContent).toBe("1");
    expect(body.querySelector(".ev-in-v")).toBe(before);
  });

  it("rebuilds the list when the inputs change", async () => {
    const { body, tab } = await paint(openRunView, "running", undefined, {
      inputs: { repo: "vibekit" },
    });
    const before = body.querySelector(".ev-in-v");
    await rerender(tab.id, { repo: "subflux" }, {});

    expect(body.querySelector(".ev-in-v")).not.toBe(before);
    expect(body.querySelector(".ev-in-text")?.textContent).toBe("subflux");
  });
});

// The page's whole reason for existing: an execution is a TREE over time, and the
// two things a single column cannot express are nesting and concurrency. Both were
// being dropped — every surface flattened the state tree to its leaves — so these
// pin that a container is a row of its own carrying the fact that explains it.
describe("run view structure", () => {
  const looping = {
    nodeId: "wf_1",
    type: "sequence",
    status: "running",
    children: [
      {
        nodeId: "fix-loop",
        type: "repeat",
        status: "running",
        children: [
          {
            nodeId: "coder",
            type: "step",
            status: "completed",
            agentName: "wf-coder",
            children: [],
          },
          {
            nodeId: "verify",
            type: "step",
            status: "running",
            agentName: "wf-verify",
            children: [],
          },
        ],
      },
    ],
  };

  it("renders control-flow containers as rows, not as an indent", async () => {
    const { body } = await paint(openRunView, "running", undefined, { root: looping });
    const kinds = [...body.querySelectorAll(".ev-row")].map((r) => r.getAttribute("data-kind"));
    // The repeat is a row of its own, and its two steps are rows beneath it. Before
    // this the leaf flattening left only the steps, so a loop was invisible.
    expect(kinds).toEqual(["repeat", "step", "step"]);
  });

  it("states a repeat's bound and stop condition from the node PLAN", async () => {
    // `nodePlan` had ZERO client readers: it is passed through verbatim by
    // GET /api/runs/{id} and its only content the state tree lacks is exactly this.
    const { body } = await paint(openRunView, "running", undefined, {
      root: looping,
      nodePlan: [
        { nodeId: "fix-loop", maxIterations: 5, stopCondition: "verify.output contains PASS" },
      ],
    });
    const sub = body.querySelector('.ev-row[data-kind="repeat"] .ev-sub')?.textContent ?? "";
    expect(sub).toContain("up to 5 passes");
    expect(sub).toContain("verify.output contains PASS");
  });

  // A container's own status reads `running` for as long as anything inside it is
  // open, which tells a reader nothing. What is worth surfacing is the worst outcome
  // beneath it, so a collapsed group still says a step inside it failed.
  it("rolls a failure up to the container that holds it", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      root: {
        ...looping,
        children: [
          {
            ...looping.children[0],
            children: [{ nodeId: "coder", type: "step", status: "failed", children: [] }],
          },
        ],
      },
    });
    expect(body.querySelector('.ev-row[data-kind="repeat"]')?.getAttribute("data-state")).toBe(
      "fail",
    );
  });

  // Every node carries its own timestamps, so the execution's shape over time is on
  // the wire and no surface had ever drawn it. One lane per leaf.
  it("draws a lane per leaf on the timeline", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "a",
            type: "step",
            status: "completed",
            startedAt: "2026-08-26T18:00:00Z",
            endedAt: "2026-08-26T18:01:00Z",
            children: [],
          },
          {
            nodeId: "b",
            type: "step",
            status: "completed",
            startedAt: "2026-08-26T18:00:30Z",
            endedAt: "2026-08-26T18:02:00Z",
            children: [],
          },
        ],
      },
    });
    const names = [...body.querySelectorAll(".ev-tl-name")].map((n) => n.textContent);
    expect(names).toEqual(["a", "b"]);
    // Overlapping steps overlap on screen, which is the whole point: `b` starts a
    // third of the way into the window rather than after `a`.
    const bar = body.querySelector<HTMLElement>('.ev-tl-lane[data-path$="/b"] .ev-tl-bar');
    expect(parseFloat(bar?.style.insetInlineStart ?? "0")).toBeGreaterThan(0);
    expect(parseFloat(bar?.style.insetInlineStart ?? "100")).toBeLessThan(50);
  });

  // HIERARCHY IS A BOX. A top-level container gets `.ev-group` — bordered, with its
  // own row as the filled header — and a top-level leaf does not. The common flat
  // workflow is all depth-0 leaves, so this item is a no-op for it by design.
  it("boxes a top-level container and leaves a top-level leaf plain", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          looping.children[0],
          { nodeId: "publish", type: "step", status: "pending", children: [] },
        ],
      },
    });
    const top = [...body.querySelectorAll<HTMLElement>(".ev-tree > .ev-row")];
    expect(top.map((r) => r.getAttribute("data-kind"))).toEqual(["repeat", "step"]);
    expect(top.map((r) => r.classList.contains("ev-group"))).toEqual([true, false]);
  });

  // A direct child of a group needs no marker — the box says whose it is. A
  // sub-sub-item gets the `↳` glyph plus one step of indent.
  it("marks a sub-sub-item with the nesting glyph and not a direct child", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "outer",
            type: "parallel",
            status: "running",
            children: [
              {
                nodeId: "inner",
                type: "repeat",
                status: "running",
                children: [{ nodeId: "coder", type: "step", status: "running", children: [] }],
              },
            ],
          },
        ],
      },
    });
    const nestVisible = (id: string): boolean => {
      const row = body.querySelector<HTMLElement>(`.ev-row[data-path$="/${id}"]`);
      return row?.querySelector<HTMLElement>(":scope > .ev-row-main > .ev-nest")?.hidden === false;
    };
    // depth 0 (the group), depth 1 (its direct child), depth 2 (the sub-sub-item).
    expect([nestVisible("outer"), nestVisible("inner"), nestVisible("coder")]).toEqual([
      false,
      false,
      true,
    ]);
    // Depth 0 and 1 both indent by zero; only depth 2 steps in.
    const depthOf = (id: string): string =>
      body
        .querySelector<HTMLElement>(`.ev-row[data-path$="/${id}"]`)
        ?.style.getPropertyValue("--ev-depth") ?? "";
    expect([depthOf("outer"), depthOf("inner"), depthOf("coder")]).toEqual(["0", "0", "1"]);
  });

  // STEPS MUST NOT BECOME COLLAPSIBLE. A childless row has no disclosure at all, and
  // the grouping change must not have given it one.
  it("leaves a step row non-collapsible", async () => {
    const { body } = await paint(openRunView, "running", undefined, { root: looping });
    const steps = [...body.querySelectorAll<HTMLElement>('.ev-row[data-kind="step"]')];
    expect(steps).toHaveLength(2);
    for (const step of steps) {
      expect(step.querySelector<HTMLElement>(":scope > .ev-row-main > .ev-twist")?.hidden).toBe(
        true,
      );
      expect(step.hasAttribute("aria-expanded")).toBe(false);
      expect(step.querySelector(":scope > .ev-kids")).toBeNull();
    }
  });
});

// ---------------------------------------------------------------------------
// A DOOR THAT NAMES A NODE. The transcript run card's step rows are anchors into
// this page, and a plain click asks for the row's own node to be selected — which
// is what makes a row a door rather than the disclosure it used to be. The request
// travels as `openRunView`'s fourth argument into `ExecRun.focus`, the field the
// subagent adapter has always set and the workflow one never did.
//
// The second case is the one with a mechanism behind it: `page.ts` remembers the
// last focus it honoured, so without a reset the SAME row is a control that works
// once per page instance. An ordinary invalidation carries no focus, and that is
// what clears the watermark.
// ---------------------------------------------------------------------------

describe("run view door focus", () => {
  const stepNode = (nodeId: string, status: string): unknown => ({
    nodeId,
    type: "step",
    status,
    children: [],
  });
  const plan = (...ids: [string, string][]): unknown => ({
    nodeId: "wf_1",
    type: "sequence",
    status: "running",
    children: ids.map(([id, status]) => stepNode(id, status)),
  });

  /** The door the card's step row is: an ordinary open plus the node it names. */
  const rowDoor =
    (path: string) =>
    (id: string, name: string): void => {
      openRunView(id, name, "", path);
    };

  function selectedPath(body: HTMLElement): string {
    return body.querySelector<HTMLElement>(".ev-row.ev-selected")?.dataset["path"] ?? "";
  }

  /** One ordinary store invalidation, the way a `run_progress` frame arrives.
   *
   *  The reply is rebuilt rather than reused: the store's cell is a signal, so an
   *  identical state OBJECT wakes nobody and the page would never repaint. A real
   *  reply is freshly decoded JSON every time. */
  async function invalidated(root: unknown): Promise<void> {
    m.reply.current = {
      workflowId: "wf_1",
      state: { workflowId: "wf_1", status: "running", root },
    } satisfies RunInspectReply;
    invalidateRun("wf_1");
    for (let i = 0; i < 5; i++) {
      await Promise.resolve();
    }
  }

  it("selects the node the door named, not the one the page would follow", async () => {
    // The auto-follow would pick `build` (it is running), so the door has to name
    // the OTHER step for this to say anything.
    const { body } = await paint(rowDoor("wf_1/lint"), "running", undefined, {
      root: plan(["build", "running"], ["lint", "pending"]),
    });
    expect(selectedPath(body)).toBe("wf_1/lint");
    expect(body.querySelector(".ev-d-title")?.textContent).toBe("lint");
  });

  it("re-selects the same node after the reader has moved off it", async () => {
    const two = plan(["build", "running"], ["lint", "pending"]);
    const { body } = await paint(rowDoor("wf_1/build"), "running", undefined, { root: two });
    expect(selectedPath(body)).toBe("wf_1/build");

    // The reader moves in the tree, and a frame lands meanwhile — which is the
    // render that carries no focus.
    body.querySelector<HTMLElement>('.ev-row[data-path="wf_1/lint"] > .ev-row-main')?.click();
    expect(selectedPath(body)).toBe("wf_1/lint");
    await invalidated(two);
    expect(selectedPath(body)).toBe("wf_1/lint");

    // The same row again. Without the watermark reset this stays on `lint`.
    openRunView("wf_1", "nightly", "", "wf_1/build");
    await invalidated(two);
    expect(selectedPath(body)).toBe("wf_1/build");
  });

  it("focuses the node a `#node=` URL named, decoded by the router", async () => {
    // The COLD-DEEP-LINK chain, end to end minus one line: parseRoute reads the
    // fragment, openRunView records it, ExecRun.focus spends it. The node path
    // carries a `/`, which is why the route spells it as a fragment rather than a
    // path segment.
    //
    // What it does NOT cover: app.ts's own `case "run"` branch, which passes
    // `route.node ?? ""` into this same opener. Nothing under static-src/ imports
    // app.js, so no test in this repo can execute that line; the type checker is
    // its only gate.
    const r = parseRoute("/run/wf_1", "#node=wf_1%2Flint");
    const node = r.kind === "run" ? (r.node ?? "") : "";
    // The auto-follow would pick the running `build`, so a lost fragment shows up
    // as that step being selected instead.
    const { body } = await paint(
      (id, name) => openRunView(id, name, "", node),
      "running",
      undefined,
      {
        root: plan(["build", "running"], ["lint", "pending"]),
      },
    );
    expect(selectedPath(body)).toBe("wf_1/lint");
  });

  it("holds a pick the plan does not describe yet rather than losing it", async () => {
    // The click-beats-fetch race: the row was clicked before `inspect` answered, so
    // the first paint has no such node and the request must survive to the next one.
    const { body } = await paint(rowDoor("wf_1/lint"), "running", undefined, {
      root: plan(["build", "running"], ["publish", "pending"]),
    });
    expect(selectedPath(body)).toBe("wf_1/build");

    await invalidated(plan(["build", "running"], ["publish", "pending"], ["lint", "pending"]));
    expect(selectedPath(body)).toBe("wf_1/lint");
  });
});

// ---------------------------------------------------------------------------
// WHAT AN EMPTY STEP PANE SAYS. Two arms are about the step, and the rest are the
// on-demand read's own verdicts — which is the shape of the change rather than a
// count: before it, the note branched on the LAUNCH ROUTE, so a finished
// chat-parented step whose window had paged out was told its transcript was "never
// stored" (false) and a parentless one was told to open a conversation that does not
// exist. After it, the only route-dependent thing left on this pane is the DOOR.
//
// The two route-independent arms lead, because no read can change either: a step
// that NEVER RAN (`pending`/`skipped` are neither in flight nor settled, and reading
// the vocabulary as two buckets is what let a sentence about a step's past execution
// answer for a step with none), and a step still IN FLIGHT (a busy session cannot be
// `session/load`ed, so the read is refused by construction).
//
// TWO sentences were deleted rather than reworded, and each was honest when written:
// "streams into that chat's transcript rather than here" while the sub-tab could not
// READ those blocks, and "Nothing from it is loaded here" while nothing could fetch
// them. Both are false in the common case once the read exists.
// ---------------------------------------------------------------------------

describe("run view empty step notes", () => {
  const root = {
    nodeId: "wf_1",
    type: "sequence",
    status: "running",
    children: [{ nodeId: "coder", type: "step", status: "running", children: [] }],
  };

  function settled(status: string): unknown {
    return {
      ...root,
      status,
      children: [{ nodeId: "coder", type: "step", status, children: [] }],
    };
  }

  /** A run whose ONE leaf carries `status`. One leaf because the pane picks its own
   *  default: `autoSelect` takes the node wanting attention, and `pending`, `skipped`
   *  and `ok` all rank last, so with two rank-last leaves the case would be asserting
   *  on whichever the walk reached first. */
  function oneLeaf(status: string): unknown {
    return { ...root, children: [{ nodeId: "verify", type: "step", status, children: [] }] };
  }

  async function note(
    status: string,
    opts: { parentless?: boolean; root?: unknown } = {},
  ): Promise<string> {
    const { body } = await paint(openRunView, status, undefined, { root, ...opts });
    return body.querySelector(".ev-d-empty")?.textContent ?? "";
  }

  // `gone` is the verdict for a step whose session KAS no longer holds, and it is
  // the one that REPLACED the route-keyed "never stored" sentence — which was true
  // for a parentless run and false for a chat-parented one. Now the same verdict
  // produces the same sentence on both routes, and it still names what IS durable.
  it("says a gone transcript is no longer stored, on either route", async () => {
    for (const parentless of [true, false]) {
      m.reads.set("wf_1/coder", { state: "gone" });
      const text = await note("completed", { parentless, root: settled("completed") });
      expect(text).toContain("no longer stored");
      expect(text).toContain("captureOutput");
      expect(text).not.toContain("Waiting");
    }
  });

  // `unavailable` is the transient verdict, and it must not read as `gone`: the read
  // could not be COMPLETED, so it is worth asking again, where a gone transcript
  // never will be. The sentence says so without instructing the reader to retry —
  // re-selecting the step is what arms the next read.
  it("says an unavailable transcript could not be read, not that it is gone", async () => {
    m.reads.set("wf_1/coder", { state: "unavailable" });
    const text = await note("completed", { root: settled("completed") });
    expect(text).toContain("could not be read");
    expect(text).not.toContain("no longer stored");
    expect(text).not.toContain("captureOutput");
  });

  // A `ready` read reaches the NOTE only with the slice empty and the read holding no
  // blocks, which is its own fact rather than a failure: the step ran and wrote
  // nothing. Deliberately not "captured nothing" — the capture is a different thing
  // this pane already names two regions above.
  it("says a ready read with no blocks means the step wrote nothing", async () => {
    m.reads.set("wf_1/coder", { state: "ready" });
    const text = await note("completed", { root: settled("completed") });
    expect(text).toContain("without producing a transcript");
    expect(text).not.toContain("captureOutput");
    expect(text).not.toContain("could not be read");
  });

  // While the read is in flight the pane says so rather than showing one of the
  // settled answers, which would be a verdict nothing has reached yet.
  it("says the transcript is loading while the read is in flight", async () => {
    m.reads.set("wf_1/coder", { state: "loading" });
    expect(await note("completed", { root: settled("completed") })).toContain("Loading this step");
  });

  // The DEFAULT arm, reached for a SETTLED step in the instant before its request
  // exists: `repaint` builds this note and only then fires `onShowNode`, which is what
  // arms the read. So loading is what is true there, and a verdict would report an
  // answer nothing has reached yet.
  it("says loading for a settled step whose read this repaint is arming", async () => {
    const text = await note("completed", { root: settled("completed") });
    expect(text).toContain("Loading this step");
    expect(text).not.toContain("could not be read");
    expect(text).not.toContain("no longer stored");
  });

  // The FOURTH arm, and the only one that is not the endpoint's own verdict: a 4xx
  // means the server refused the ADDRESS itself. Its sentence must not read as the
  // transient one a line above it, because `settled()` never re-asks — a retry-shaped
  // sentence would offer the reader something that cannot happen.
  it("says an unaddressable transcript is not named by the plan, not that it failed", async () => {
    m.reads.set("wf_1/coder", { state: "unaddressable" });
    const text = await note("completed", { root: settled("completed") });
    expect(text).toContain("the run's plan does not name it");
    expect(text).not.toContain("could not be read");
    expect(text).not.toContain("no longer stored");
  });

  // The WAITING string is now shared by both routes, which it was not before.
  it("says a live parentless step has not spoken yet", async () => {
    expect(await note("running")).toContain("Waiting for this step");
  });

  it("shares the waiting string with a live chat-parented step whose chat is here", async () => {
    setSessions([chatSession("c-1", [])]);
    m.parentChat.current = "c-1";
    expect(await note("running", { parentless: false })).toContain("Waiting for this step");
  });

  // ...and withholds it when this client holds nothing for that chat, because there it
  // would promise output nothing can deliver. What it must NOT fall through to is the
  // loading sentence: `armStepRead`'s third gate refuses a live step, so no read
  // exists and none is armed until the step settles, and claiming a load would be the
  // same lie the residency gate was added for. The arm states that bound instead, and
  // the door beside it offers the conversation.
  it("names the bound, not a load, for a live chat-parented step with no resident chat", async () => {
    const text = await note("running", { parentless: false });
    expect(text).not.toContain("Waiting");
    expect(text).not.toContain("Loading");
    expect(text).toContain("cannot be read here until it finishes");
  });

  // The same arm for the other IN-FLIGHT state, because `inFlight` spans three: a
  // PAUSED step is not producing output either, and the answer may not read as a
  // verdict about its transcript.
  it("names the bound for a paused chat-parented step with no resident chat", async () => {
    const text = await note("running", { parentless: false, root: oneLeaf("paused") });
    expect(text).toContain("cannot be read here until it finishes");
    expect(text).not.toContain("Loading");
  });

  // THE FOURTH ARM, on the route that made the missing one visible: a `pending` leaf
  // of a chat-parented run reached state 3's sentence, so the pane asserted the step
  // ran and that its transcript is in the conversation, two rows under its own state
  // word reading "not started". The launching chat is resident here, so the waiting
  // string is available and must not be taken either: this step is not working, it
  // has not begun.
  it("says a pending chat-parented step has not started, never that it ran", async () => {
    setSessions([chatSession("c-1", [])]);
    m.parentChat.current = "c-1";
    const text = await note("running", { parentless: false, root: oneLeaf("pending") });
    expect(text).toContain("has not started");
    expect(text).not.toContain("ran inside");
    expect(text).not.toContain("Nothing from it is loaded here");
    expect(text).not.toContain("Waiting");
  });

  // The other not-run state, and the one no wording can promise a future for: a
  // branch that did not run never will.
  it("says a skipped chat-parented step produced nothing, never that it ran", async () => {
    setSessions([chatSession("c-1", [])]);
    m.parentChat.current = "c-1";
    const text = await note("completed", { parentless: false, root: oneLeaf("skipped") });
    expect(text).toContain("was skipped");
    expect(text).not.toContain("ran inside");
    expect(text).not.toContain("yet");
  });

  // The arm is ROUTE-INDEPENDENT, which is the shape of the fix rather than a bonus.
  // State 2's sentence is about a step whose output was streamed and dropped — "once
  // the step has finished", and a capture it would have declared — so it answers for
  // a parentless leaf that has not run no better than state 3 did.
  it("answers a parentless step that has not run without the capture hint", async () => {
    const text = await note("running", { root: oneLeaf("pending") });
    expect(text).toContain("has not started");
    expect(text).not.toContain("captureOutput");
    expect(text).not.toContain("never stored");
  });
});

// STATE 1, and the wiring behind it: resolve the launching chat, slice its messages
// by this run's step ids, and hand each step's slice the host the detail pane minted
// for that node path.
describe("run view chat-route steps", () => {
  const twoSteps = {
    nodeId: "wf_1",
    type: "sequence",
    status: "running",
    children: [
      { nodeId: "coder", type: "step", status: "running", children: [] },
      { nodeId: "verify", type: "step", status: "pending", children: [] },
    ],
  };

  /** Let the lazy `import("./run-chat-steps.js")` resolve and its follow-up paint
   *  run. A module load is not a microtask chain, so this POLLS rather than draining
   *  a fixed count — a fixed count is what makes such a case pass on one machine and
   *  hang on another. */
  async function loaded(): Promise<void> {
    for (let i = 0; i < 200 && m.projected.length === 0; i++) {
      await new Promise<void>((resolve) => {
        setTimeout(resolve, 1);
      });
    }
  }

  it("renders the launching chat's blocks in the step's own body", async () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compiling")])]);
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "running", undefined, {
      parentless: false,
      root: twoSteps,
    });
    await loaded();

    const host = body.querySelector<HTMLElement>('.ev-d-body[data-path="wf_1/coder"]');
    expect(host?.childElementCount).toBeGreaterThan(0);
    expect(host?.querySelector(".step-marker")?.textContent).toBe("wf_1/coder:1");
    // And the note is retired for the node on screen, because the host has content:
    // no copy string can claim anything about a transcript that is rendered.
    expect(body.querySelector<HTMLElement>(".ev-d-empty")?.hidden).toBe(true);
  });

  // The no-orphan-host rule: a step with no slice gets no host minted for it, or the
  // pane would carry an empty region per unstarted step.
  it("mints a host only for a step the slice has content for", async () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compiling")])]);
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "running", undefined, {
      parentless: false,
      root: twoSteps,
    });
    await loaded();

    expect(m.projected).toEqual(["wf_1/coder"]);
    expect(body.querySelector('.ev-d-body[data-path="wf_1/verify"]')).toBeNull();
  });

  // Another run's step at the same node path must not reach this pane. Two runs of
  // one recipe share every path, so this is the ordinary case rather than an edge.
  it("ignores another run's step blocks", async () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "theirs", "wf_2")])]);
    m.parentChat.current = "c-1";
    await paint(openRunView, "running", undefined, { parentless: false, root: twoSteps });
    // A fixed short settle rather than the poll above: nothing will ever arrive here,
    // so a poll would spend its whole budget proving it.
    for (let i = 0; i < 20; i++) {
      await Promise.resolve();
    }
    expect(m.projected).toEqual([]);
  });

  // The RUN store knows nothing about a finished run's chat (its lease is released),
  // so the tab's own persisted parent is the only answer left — and this is the
  // post-reload population the whole item exists for.
  it("finds the launching chat through the run tab's own parent", async () => {
    setSessions([chatSession("c-9", [stepMessage("m1", "wf_1/coder", "compiling")])]);
    m.parentChat.current = "c-9";
    await paint(openRunView, "completed", undefined, {
      parentless: false,
      root: {
        ...twoSteps,
        status: "completed",
        children: [{ nodeId: "coder", type: "step", status: "completed", children: [] }],
      },
    });
    await loaded();
    expect(m.projected).toEqual(["wf_1/coder"]);
  });

  /** Poll until the pane has re-projected. A store delta reaches this effect through
   *  a `queueMicrotask` coalescer, so the wait is for a real flush rather than a
   *  fixed number of drained microtasks. */
  async function reprojected(was: number): Promise<void> {
    for (let i = 0; i < 200 && m.projected.length === was; i++) {
      await new Promise<void>((resolve) => {
        setTimeout(resolve, 1);
      });
    }
  }

  // A step's prose GROWS after the first paint, and the pane has to follow it or
  // state 1 is only correct for content that arrived before the tab was opened.
  // Nothing here mounts a block signal, so this is the version-bump route on its
  // own: `appendChunk`'s signal-absent arm schedules the launching chat's version,
  // and `installViewEffect`'s read of it is what re-runs the projection.
  it("re-projects a step when a delta grows its block", async () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compil")])]);
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "running", undefined, {
      parentless: false,
      root: twoSteps,
    });
    await loaded();
    const first = m.projected.length;

    appendChunk("c-1", "m1", "ing", false, 0, "wf:wf_1:wf_1/coder");
    await reprojected(first);

    expect(m.projected.length).toBeGreaterThan(first);
    const host = body.querySelector<HTMLElement>('.ev-d-body[data-path="wf_1/coder"]');
    expect(host?.querySelector(".step-marker")?.textContent).toBe("wf_1/coder:1");
  });

  // The same delta with the TRANSCRIPT also holding that block, which is the
  // ordinary case: `appendChunk` takes its mounted-block arm instead, writing the
  // per-block signal `subscribeToDeltas` reads and coalescing a version bump behind
  // it. Either route has to reach the pane, so the outcome is the same assertion.
  it("re-projects a step whose block the transcript has mounted too", async () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compil")])]);
    m.parentChat.current = "c-1";
    ensureBlockTextSig("m1", 0, "compil");
    const { body } = await paint(openRunView, "running", undefined, {
      parentless: false,
      root: twoSteps,
    });
    await loaded();
    const first = m.projected.length;

    appendChunk("c-1", "m1", "ing", false, 0, "wf:wf_1:wf_1/coder");
    await reprojected(first);

    expect(m.projected.length).toBeGreaterThan(first);
    expect(
      body.querySelector<HTMLElement>('.ev-d-body[data-path="wf_1/coder"] .step-marker')
        ?.textContent,
    ).toBe("wf_1/coder:1");
  });
});

// ---------------------------------------------------------------------------
// THE KAS ROUTE: `GET /api/runs/{id}/steps/{path...}`, the third and last way a step's
// transcript reaches this pane, and the one that works whatever happened before — it
// does not care which route launched the run or what window this client holds.
//
// Two halves, and they are separate subjects: WHEN a read is armed (four gates, all
// about what must not reach the endpoint) and WHICH route paints when both have
// content (the chat slice wins).
// ---------------------------------------------------------------------------

describe("run view KAS-route step reads", () => {
  const oneStep = (status: string): unknown => ({
    nodeId: "wf_1",
    type: "sequence",
    status,
    children: [{ nodeId: "coder", type: "step", status, children: [] }],
  });

  /** A two-leaf plan: `first` is the step the reader watches, `second` the one that
   *  picks up after it. TWO leaves because the page hides the tree pane for a single
   *  row (`structural` in `exec-view/page.ts`), so a one-leaf plan has no row to click
   *  — and because `autoSelect` then has somewhere else to go once the first settles,
   *  which is what makes the reader's pin load-bearing rather than incidental. */
  const twoLeaves = (first: string, second: string): unknown => ({
    nodeId: "wf_1",
    type: "sequence",
    status: "running",
    children: [
      { nodeId: "coder", type: "step", status: first, children: [] },
      { nodeId: "verify", type: "step", status: second, children: [] },
    ],
  });

  /** Let the lazy `import("./run-chat-steps.js")` resolve. Same poll as the chat
   *  route's, because the KAS route feeds the same stream. */
  async function loaded(): Promise<void> {
    for (let i = 0; i < 200 && m.projected.length === 0; i++) {
      await new Promise<void>((resolve) => {
        setTimeout(resolve, 1);
      });
    }
  }

  /** A KAS read holding one block, as `stepSliceFor` projects it. */
  function kasSlice(): { blocks: unknown[]; toolCalls: unknown[] } {
    return { blocks: [{ type: "text", text: "from the endpoint" }], toolCalls: [] };
  }

  // The ARM, and the case every gate below is measured against: a settled leaf whose
  // slice is empty is exactly what the read exists for.
  it("arms a read for a settled step with no chat slice", async () => {
    await paint(openRunView, "completed", undefined, { root: oneStep("completed") });
    expect(m.requested).toEqual(["wf_1/coder"]);
  });

  // GATE 2. A step with no session has nothing to load, so the round trip would be
  // spent being told what the note already answers locally.
  it("arms no read for a step that never ran", async () => {
    for (const status of ["pending", "skipped"]) {
      m.requested.length = 0;
      await paint(openRunView, "running", undefined, { root: oneStep(status) });
      expect(m.requested).toEqual([]);
    }
  });

  // GATE 3. A busy session cannot be `session/load`ed, so a live step's read is
  // refused by construction — and its content reaches the pane by its own route
  // meanwhile.
  it("arms no read for a step still in flight", async () => {
    for (const status of ["running", "paused"]) {
      m.requested.length = 0;
      await paint(openRunView, "running", undefined, { root: oneStep(status) });
      expect(m.requested).toEqual([]);
    }
  });

  // ...and gate 3 is a DEFERRAL, not a refusal, which is the half a path-keyed guard
  // loses. A reader who clicks a running step pins the selection (`select()` sets
  // `userPicked`), so the shown path never moves again for the rest of that run's
  // viewing — and the step they are looking at is then the one node never read. The
  // step SETTLING is what arms it.
  it("arms the read when the step the reader picked settles where it stands", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      root: twoLeaves("running", "pending"),
    });
    expect(m.requested).toEqual([]);

    // The reader's own gesture. A row is an ordinary `treeitem` whose click selects it
    // (`exec-view/tree.ts`), so this is the real pin rather than a reach into state.
    body.querySelector<HTMLElement>('.ev-row[data-path="wf_1/coder"] > .ev-row-main')?.click();
    await Promise.resolve();
    expect(m.requested).toEqual([]);

    // One ordinary refetch: the first step finished and the second picked up. Same run,
    // same page, same selection — and `autoSelect` would have MOVED to the now-running
    // `verify`, so the pin is what keeps `coder` on screen.
    m.reply.current = {
      workflowId: "wf_1",
      state: { workflowId: "wf_1", status: "running", root: twoLeaves("completed", "running") },
    } satisfies RunInspectReply;
    invalidateRun("wf_1");
    for (let i = 0; i < 5; i++) {
      await Promise.resolve();
    }

    expect(body.querySelector<HTMLElement>(".ev-d-title")?.textContent).toBe("coder");
    // EXACTLY one, which is the other half: arming per repaint would re-ask on every
    // frame of a live run, and `unavailable` is a verdict a repeat ask retries.
    expect(m.requested).toEqual(["wf_1/coder"]);
  });

  // GATE 4, and the one that makes "preferred when the slice is empty" a gate rather
  // than a preference: the chat route's blocks are the same content and are already
  // rendered, so asking would fetch a second copy of what is on screen.
  it("arms no read when the launching chat already holds the step's blocks", async () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compiled")])]);
    m.parentChat.current = "c-1";
    await paint(openRunView, "completed", undefined, {
      parentless: false,
      root: oneStep("completed"),
    });
    expect(m.requested).toEqual([]);
  });

  // ...and the inverse, which is the population the read was built for: the chat is
  // resident but its window has paged the run's turn out, so the slice is present and
  // EMPTY. A gate keyed on residency rather than on content would refuse here.
  it("arms a read when the launching chat is resident but holds no blocks", async () => {
    setSessions([chatSession("c-1", [])]);
    m.parentChat.current = "c-1";
    await paint(openRunView, "completed", undefined, {
      parentless: false,
      root: oneStep("completed"),
    });
    expect(m.requested).toEqual(["wf_1/coder"]);
  });

  // GATE 1. A container hosts no transcript, so there is nothing to read for it —
  // and it must not be asked about, because the endpoint answers 404 for a path that
  // names no STEP.
  //
  // It has to SELECT the container to reach the gate at all: `onShowNode` fires for
  // the shown node, and the pane auto-selects a LEAF, so a case that only paints
  // passes with the gate deleted. A container row is an ordinary `treeitem` whose
  // click selects it (`exec-view/tree.ts`), so this is a real gesture rather than a
  // reach into internals.
  it("arms no read for a container the reader selects", async () => {
    const { body } = await paint(openRunView, "completed", undefined, {
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "completed",
        children: [
          {
            nodeId: "loop",
            type: "repeat",
            status: "completed",
            children: [{ nodeId: "coder", type: "step", status: "completed", children: [] }],
          },
        ],
      },
    });
    m.requested.length = 0;

    // The root sequence is the run's own header rather than a row, so `wf_1/loop` is
    // the container on screen.
    const head = body.querySelector<HTMLElement>('.ev-row[data-path="wf_1/loop"] > .ev-row-main');
    expect(head).not.toBeNull();
    head?.click();
    await Promise.resolve();
    expect(m.requested).toEqual([]);
  });

  // The read's blocks RENDER, through the same stream and into the same host the chat
  // route uses — which is the point of the whole item: the pane stops caring which
  // route a step's transcript came from.
  it("renders a KAS read's blocks in the step's own body", async () => {
    m.reads.set("wf_1/coder", { state: "ready" });
    m.kasSlices.set("wf_1/coder", kasSlice());
    const { body } = await paint(openRunView, "completed", undefined, {
      root: oneStep("completed"),
    });
    await loaded();

    // On the SET of paths and the routes, not on the call count: a repaint applies
    // again with the same content by design, so a count would pin the number of
    // effect runs rather than what reached the pane.
    expect(new Set(m.projected)).toEqual(new Set(["wf_1/coder"]));
    expect(new Set(m.sources)).toEqual(new Set(["kas"]));
    expect(
      body.querySelector<HTMLElement>('.ev-d-body[data-path="wf_1/coder"] .step-marker')
        ?.textContent,
    ).toBe("wf_1/coder:1");
    // And no note stands beside rendered content.
    expect(body.querySelector<HTMLElement>(".ev-d-empty")?.hidden).toBe(true);
  });

  // THE SLICE WINS when it has blocks, and the SOURCE is the only observable that
  // separates the two routes: both render identical blocks into the same host. It
  // matters because the chat route's blocks are live and carry per-block signals plus
  // a delegate deep link, where the read's are a settled snapshot with neither.
  it("paints the chat slice rather than the read when both have content", async () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "from the chat")])]);
    m.parentChat.current = "c-1";
    m.reads.set("wf_1/coder", { state: "ready" });
    m.kasSlices.set("wf_1/coder", kasSlice());
    await paint(openRunView, "completed", undefined, {
      parentless: false,
      root: oneStep("completed"),
    });
    await loaded();

    expect(new Set(m.projected)).toEqual(new Set(["wf_1/coder"]));
    // The whole assertion: the read had content too, and no paint took it.
    expect(m.sources).not.toContain("kas");
    expect(new Set(m.sources)).toEqual(new Set(["chat"]));
  });

  // A RESOLVED read has to repaint the pane, and nothing else can carry it: the run
  // store's own cell has not changed, the launching chat's version has not moved, and
  // on a parentless run there is no chat to move. So the read's version signal is the
  // page's only subscription to its own answer arriving — without it a step's
  // transcript lands in the cache and stays off screen until some unrelated frame
  // happens to repaint.
  it("repaints when a read resolves after the first paint", async () => {
    const { body } = await paint(openRunView, "completed", undefined, {
      root: oneStep("completed"),
    });
    await loaded();
    expect(body.querySelector('.ev-d-body[data-path="wf_1/coder"]')).toBeNull();

    // The answer lands in the cache, then the module bumps its version — the two
    // halves `fetchStep` performs in its `finally`.
    m.reads.set("wf_1/coder", { state: "ready" });
    m.kasSlices.set("wf_1/coder", kasSlice());
    stepTranscriptVersion.value = stepTranscriptVersion.peek() + 1;

    for (let i = 0; i < 200 && m.projected.length === 0; i++) {
      await new Promise<void>((resolve) => {
        setTimeout(resolve, 1);
      });
    }
    expect(new Set(m.sources)).toEqual(new Set(["kas"]));
    expect(
      body.querySelector<HTMLElement>('.ev-d-body[data-path="wf_1/coder"] .step-marker')
        ?.textContent,
    ).toBe("wf_1/coder:1");
  });

  // Every read is dropped when the page mounts. The cache is keyed by (run, path) and
  // its whole bound is this call: a run tab retargeting or closing is the one moment
  // an entry stops being wanted.
  it("drops every read on mount", async () => {
    await paint(openRunView, "completed", undefined, { root: oneStep("completed") });
    expect(m.cleared.count).toBeGreaterThan(0);
  });
});

// The affordance: a real anchor into the launching conversation, rendered only where
// there is a conversation to name.
describe("run view empty step action", () => {
  const root = {
    nodeId: "wf_1",
    type: "sequence",
    status: "completed",
    children: [{ nodeId: "coder", type: "step", status: "completed", children: [] }],
  };

  it("offers a link to the launching chat when one is known", async () => {
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "completed", undefined, {
      parentless: false,
      root,
    });
    const link = body.querySelector<HTMLAnchorElement>(".ev-d-link");
    expect(link?.getAttribute("href")).toBe("/chat/c-1");
    expect(link?.textContent).toContain("Open the conversation");
    expect(body.querySelector<HTMLElement>(".ev-d-empty-action")?.hidden).toBe(false);
  });

  // THE CASE A COPY STRING MUST NOT OVER-CLAIM ON. A cold client reaching a finished
  // chat-parented run through a bare `/run/{id}` link can prove the run WAS
  // chat-parented and cannot name the chat, so it gets the sentence and no door.
  it("offers no link when the launching chat is unknown", async () => {
    const { body } = await paint(openRunView, "completed", undefined, {
      parentless: false,
      root,
    });
    expect(body.querySelector(".ev-d-link")).toBeNull();
    expect(body.querySelector<HTMLElement>(".ev-d-empty-action")?.hidden).toBe(true);
    // ...and the note still stands on its own. It is the READ's verdict now, which is
    // what makes the note and the door independent: the door needs a launching chat,
    // the sentence does not.
    expect(body.querySelector(".ev-d-empty")?.textContent).toContain("Loading this step");
  });

  it("offers no link on a parentless run", async () => {
    m.parentChat.current = "";
    const { body } = await paint(openRunView, "completed", undefined, { root });
    expect(body.querySelector(".ev-d-link")).toBeNull();
  });

  // THE PAIR HAS TO AGREE. The note no longer claims a step that never ran has a
  // transcript in the launching chat, and this link's only subject is that
  // transcript — so a door to it beside "produced no output" is the same over-claim
  // one element along. The chat is KNOWN here, so the step's state is the only thing
  // withholding the link.
  it("offers no link for a step that never ran, with the chat known", async () => {
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "running", undefined, {
      parentless: false,
      root: {
        ...root,
        status: "running",
        children: [{ nodeId: "verify", type: "step", status: "skipped", children: [] }],
      },
    });
    expect(body.querySelector(".ev-d-link")).toBeNull();
    expect(body.querySelector<HTMLElement>(".ev-d-empty-action")?.hidden).toBe(true);
    expect(body.querySelector(".ev-d-empty")?.textContent).toContain("was skipped");
  });

  /** Click the link and report whether the APP's own handler cancelled the event,
   *  without letting the browser act on it.
   *
   *  The guard is not optional: this is a real anchor in a real browser, so an
   *  uncancelled click FOLLOWS the href, which navigates the test iframe and takes
   *  the whole file down with "Cannot connect to the iframe". A document-level
   *  listener runs after the anchor's own, so it reads the app's verdict first and
   *  then cancels on the test's behalf. */
  function clickLink(link: HTMLElement | null, init: MouseEventInit = {}): boolean {
    let prevented = false;
    const guard = (e: Event): void => {
      prevented = e.defaultPrevented;
      e.preventDefault();
    };
    document.addEventListener("click", guard);
    try {
      link?.dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, ...init }),
      );
    } finally {
      document.removeEventListener("click", guard);
    }
    return prevented;
  }

  it("opens the conversation on a plain click", async () => {
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "completed", undefined, {
      parentless: false,
      root,
    });
    expect(clickLink(body.querySelector(".ev-d-link"))).toBe(true);
    expect(m.tabbed).toEqual([{ kind: "chat", ref: "c-1" }]);
  });

  // The middle-click / modified-click escape: a reader asking the BROWSER to open it
  // gets the browser's behaviour, which is what the real `href` is for.
  it("stands aside for a modified click", async () => {
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "completed", undefined, {
      parentless: false,
      root,
    });
    expect(clickLink(body.querySelector(".ev-d-link"), { metaKey: true })).toBe(false);
    expect(m.tabbed).toEqual([]);
  });

  it("stands aside for a middle click", async () => {
    m.parentChat.current = "c-1";
    const { body } = await paint(openRunView, "completed", undefined, {
      parentless: false,
      root,
    });
    expect(clickLink(body.querySelector(".ev-d-link"), { button: 1 })).toBe(false);
    expect(m.tabbed).toEqual([]);
  });

  // `detail.ts` guards its slot on element IDENTITY, and re-seating a node BLURS it,
  // so a fresh element per render would drop focus out of the link several times a
  // minute on a live run.
  it("returns the same element across repaints", async () => {
    m.parentChat.current = "c-1";
    const { body, tab } = await paint(openRunView, "running", undefined, {
      parentless: false,
      root: {
        ...root,
        status: "running",
        children: [{ nodeId: "coder", type: "step", status: "running", children: [] }],
      },
    });
    const before = body.querySelector(".ev-d-link");
    expect(before).not.toBeNull();
    showRun(tab.id, false);
    for (let i = 0; i < 10; i++) {
      await Promise.resolve();
    }
    expect(body.querySelector(".ev-d-link")).toBe(before);
  });
});

// The eviction sweep's third exemption. It exists because `hasExecutingRunForChat`
// covers a run that is still EXECUTING only, so a reader who opens a finished (or
// parked) chat-parented run's sub-tab and then works elsewhere would have the
// chat's window swept out from under the slice — state 1 degrading to state 3 while
// they watch.
describe("run tab eviction exemption", () => {
  it("exempts a chat whose resident blocks belong to an OPEN run tab", () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compiling")])]);
    m.tabsOpen.add("run:wf_1");
    expect(runTabProjectsChat("c-1")).toBe(true);
  });

  it("exempts nothing when that run's tab is closed", () => {
    setSessions([chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compiling")])]);
    expect(runTabProjectsChat("c-1")).toBe(false);
  });

  // A different chat's window holds nothing this run tab is projecting, so it stays
  // evictable — the exemption has to be per chat or it would pin the whole store.
  it("exempts only the chat whose blocks the run tab reads", () => {
    setSessions([
      chatSession("c-1", [stepMessage("m1", "wf_1/coder", "compiling")]),
      chatSession("c-2", []),
    ]);
    m.tabsOpen.add("run:wf_1");
    expect(runTabProjectsChat("c-2")).toBe(false);
  });

  it("exempts nothing for a chat with no resident window", () => {
    m.tabsOpen.add("run:wf_1");
    expect(runTabProjectsChat("c-absent")).toBe(false);
  });

  // A SUBAGENT's blocks carry a bare uuid, not a `wf:` id, so they never name a run
  // tab. The two exemptions are separate predicates for that reason.
  it("ignores a delegate's blocks", () => {
    setSessions([
      chatSession("c-1", [
        {
          id: "m1",
          role: "assistant",
          ts: 0,
          content: "",
          blocks: [
            {
              type: "text",
              text: "delegate work",
              agent_subtask_id: "8f2c1f2e-0000-4000-8000-000000000000",
            },
          ],
          tool_calls: [],
        },
      ]),
    ]);
    m.tabsOpen.add("run:wf_1");
    expect(runTabProjectsChat("c-1")).toBe(false);
  });
});
