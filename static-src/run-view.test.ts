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
// ONE gate is left and it is about the run rather than the door: an agent-parented
// run is the agent's to drive, on a bridge it holds, so vibekit offers it no verbs.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

interface RunInspectReply {
  workflowId: string;
  state?: {
    workflowId: string;
    status?: string;
    capturedOutputs?: Record<string, string>;
    root?: unknown;
    inputs?: Record<string, string>;
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

/** What `GET /api/runs/{id}/controls` answers. The SERVER decides the row now, so
 *  this is the fixture that used to be a local `parentless` boolean plus a status
 *  table — and the swap is the fix: parentage came from an SSE-fed map that is
 *  empty after any reload, so a reloaded client read every chat-parented run as
 *  parentless. */
interface ControlsReply {
  verbs: string[];
  refused?: Record<string, string>;
  parent_chat_id: string;
}

// Hoisted with the vi.mock factories below, which run before ordinary top-level
// initialisers and would otherwise read these in their TDZ.
const m = vi.hoisted(() => ({
  reply: { current: undefined as unknown },
  controls: { current: undefined as unknown },
  opened: [] as {
    id: string;
    opts?: { parent?: string; owns?: boolean; activate?: boolean } | undefined;
  }[],
  dispatched: [] as string[],
}));

// `mockReset: true` wipes every implementation between tests, so these are ARMED
// in beforeEach rather than only here. The suite used to get away with a
// factory-only implementation because the run store's cache outlived each test and
// answered paints 2..N with the previous test's state — which also meant a case
// could pass on a stale fixture. Each test now genuinely fetches.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
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
}));

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
    // `name` is read by the pending binding on each button, so the stub carries
    // the real action name rather than only a dispatch.
    name: `runs.${verb}`,
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

// `showRun` is what the tab FACTORY calls as the run tab's activation hook
// (registered by the composition root), so it is the seam this suite paints
// through — a door no longer carries an `onShow` of its own.
import { openRunView, showRun } from "./run-view.js";
import { apiGet, apiGetTyped } from "./api-client.js";

/** The row a status used to imply, now stated as a server answer.
 *
 *  Kept as a helper rather than inlined per case so a case reads as "this run
 *  offers these verbs" — but it is a FIXTURE of the server's answer, not a
 *  reimplementation of its table: the table itself is pinned in Go, over all three
 *  of its inputs. */
const CONTROLS_FOR: Record<string, ControlsReply> = {
  running: { verbs: ["pause", "cancel"], parent_chat_id: "" },
  paused: { verbs: ["resume", "cancel"], parent_chat_id: "" },
  completed: { verbs: [], parent_chat_id: "" },
  failed: { verbs: ["retry"], parent_chat_id: "" },
  aborted: { verbs: ["retry"], parent_chat_id: "" },
};

/** Open a run through one of the two doors and let its first paint settle.
 *  Returns the control labels on screen, in order. */
async function paint(
  door: (id: string, name: string) => void,
  status: string,
  capturedOutputs?: Record<string, string>,
  opts: {
    controls?: ControlsReply;
    /** Answer the affordance fetch with nothing, which is what a failed fetch and
     *  the moment before the first one resolves both look like to the store. */
    noControls?: boolean;
    /** The run id to paint. Defaults to `wf_1`; a case that needs a run this
     *  client holds NOTHING for names its own, because the store's caches live for
     *  the module's lifetime and there is no reset that a subscribed view survives
     *  (forgetting a run drops the very signal the view's effect is watching). */
    id?: string;
    root?: unknown;
    inputs?: Record<string, string>;
    nodePlan?: unknown;
  } = {},
): Promise<{ labels: string[]; refusals: string[]; tab: OpenedTab; body: HTMLElement }> {
  const id = opts.id ?? "wf_1";
  const reply: RunInspectReply = {
    workflowId: id,
    state: {
      workflowId: id,
      status,
      ...(capturedOutputs === undefined ? {} : { capturedOutputs }),
      ...(opts.root === undefined ? {} : { root: opts.root }),
      ...(opts.inputs === undefined ? {} : { inputs: opts.inputs }),
    },
    // Beside `state`, not inside it: the plan is its own member of KAS's inspect
    // reply and the store keeps the two apart for that reason.
    ...(opts.nodePlan === undefined ? {} : { nodePlan: opts.nodePlan }),
  };
  m.reply.current = reply;
  m.controls.current =
    opts.noControls === true
      ? undefined
      : (opts.controls ?? CONTROLS_FOR[status] ?? { verbs: [], parent_chat_id: "" });

  document.body.replaceChildren();
  const body = document.createElement("div");
  body.id = "run-body";
  const dock = document.createElement("div");
  dock.id = "run-dock";
  document.body.append(body, dock);

  door(id, "nightly");
  const tab = m.opened.at(-1);
  if (tab === undefined) {
    throw new Error("the opener did not open a tab");
  }
  // The activation hook, driven the way the composition root wires it. ONE argument:
  // it used to take a `parentless` flag the caller derived from an event-fed cache,
  // and the server answers that now.
  showRun(tab.id);
  // Two fetches settle before the row is right — the state and the affordance — so
  // drain enough microtasks for both promise chains without reaching for fake timers.
  for (let i = 0; i < 12; i++) {
    await Promise.resolve();
  }

  const labels = [...body.querySelectorAll(".run-controls button")].map((b) =>
    (b.textContent ?? "").trim(),
  );
  const refusals = [...body.querySelectorAll(".run-control-refusal")].map((n) =>
    (n.textContent ?? "").trim(),
  );
  return { labels, refusals, tab, body };
}

beforeEach(() => {
  m.opened.length = 0;
  m.dispatched.length = 0;
  m.controls.current = undefined;
  vi.mocked(apiGet).mockImplementation(() => Promise.resolve(m.reply.current));
  // The affordance endpoint, the only typed GET this graph makes. Decoded FOR REAL
  // by the caller's own generated decoder, so a fixture with the wrong shape fails
  // here rather than reaching the row. `null` is what a failed fetch produces.
  vi.mocked(apiGetTyped).mockImplementation((_path, decode) =>
    Promise.resolve(m.controls.current === undefined ? null : decode(m.controls.current)),
  );
});

describe("run view controls", () => {
  // The page renders what the server hands it and decides nothing. These cases drive
  // the affordance answer rather than a status, because the status is no longer the
  // input: the exec view's own state word is deliberately not consulted, or the
  // drifting copy of the rule would be back.
  it("renders the verbs the server offers, in the server's order", async () => {
    expect((await paint(openRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openRunView, "paused")).labels).toEqual(["Resume", "Cancel"]);
  });

  // THE DEFECT, at the surface it was reported on. A chat-parented aborted run used
  // to be denied every verb here, on the premise that an agent recovers its own
  // runs — false for exactly this status, so the run had no recovery path and no
  // door in either product. The server now offers retry and the page draws it.
  it("offers retry on an aborted CHAT-PARENTED run", async () => {
    const painted = await paint(openRunView, "aborted", undefined, {
      controls: { verbs: ["retry"], parent_chat_id: "c-launcher" },
    });
    expect(painted.labels).toEqual(["Retry failed steps"]);
  });

  // A COMPLETED run offers nothing and says nothing: its state word in the header
  // already says why, so a sentence there would be noise.
  it("renders no row at all for a run with no verbs and no refusal", async () => {
    const painted = await paint(openRunView, "completed");
    expect(painted.labels).toEqual([]);
    expect(painted.refusals).toEqual([]);
  });

  // The other half of the fix, and it is new behaviour rather than a restored one:
  // the row used to return null whenever the verbs were gated away, so a reader was
  // never told WHY a run offered nothing. A refusal renders in the place they are
  // already looking for the control.
  it("shows the server's sentence where the buttons would have been", async () => {
    const painted = await paint(openRunView, "running", undefined, {
      controls: {
        verbs: ["cancel"],
        refused: { pause: 'This run is driven by an agent in "Findings cleanup"' },
        parent_chat_id: "c-launcher",
      },
    });
    // A partly-refused row keeps its buttons: the sentence is for a row with none.
    expect(painted.labels).toEqual(["Cancel"]);

    const stuck = await paint(openRunView, "running", undefined, {
      controls: {
        verbs: [],
        refused: { pause: "This run has no live engine on this server." },
        parent_chat_id: "",
      },
    });
    expect(stuck.labels).toEqual([]);
    expect(stuck.refusals).toEqual(["This run has no live engine on this server."]);
  });

  // Before the answer lands there is nothing to render. Not an empty row and not a
  // guess: the same degradation the old table gave an unknown status, now covering
  // the moment between the tab opening and the fetch resolving.
  it("renders nothing while the affordance is still in flight", async () => {
    const painted = await paint(openRunView, "running", undefined, {
      noControls: true,
      id: "wf_never_fetched",
    });
    expect(painted.labels).toEqual([]);
    expect(painted.refusals).toEqual([]);
  });

  // The view is shared — one DOM element serves every run tab — so switching to a
  // run with a different answer must repaint the row. A stale row would be the
  // failure mode of the shared-element design.
  it("repaints the row when the shown run's answer differs", async () => {
    expect((await paint(openRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openRunView, "completed")).labels).toEqual([]);
  });

  // A run tab is a VIEW: every door opens it with `owns: false`, so its × closes a
  // view and stops nothing. This is the assertion that used to distinguish the two
  // doors, inverted.
  it("opens as a view from every door", async () => {
    const review = await paint(openRunView, "running");
    expect(review.tab.opts?.owns).toBe(false);
  });

  it("dispatches the verb the button carries", async () => {
    const painted = await paint(openRunView, "aborted");
    const retry = [
      ...painted.body.querySelectorAll<HTMLButtonElement>(".run-controls button"),
    ].find((b) => (b.textContent ?? "").includes("Retry"));
    retry?.click();
    expect(m.dispatched).toEqual(["retry:wf_1"]);
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
    const keys = [...body.querySelectorAll(".ev-r-key")].map((n) => n.textContent);
    expect(keys).toEqual(["build", "review"]);
    expect(body.querySelector(".ev-r-count")?.textContent).toBe("2");
  });

  it("renders an empty capture with its reason instead of dropping it", async () => {
    const { body } = await paint(openRunView, "completed", { review: "   " });
    expect([...body.querySelectorAll(".ev-r-key")].map((n) => n.textContent)).toEqual(["review"]);
    expect(body.querySelector(".ev-r-empty")?.textContent).toContain("without producing any text");
  });

  it("renders a non-empty capture as MARKDOWN, not as preformatted text", async () => {
    // A captured report is a step's last assistant message, so it is markdown and
    // the review page is where it gets read. Rendered through the transcript's own
    // bubble: before this it sat in a `<pre>`, showing its own asterisks.
    const { body } = await paint(openRunView, "completed", { build: "**shipped** ok" });
    expect(body.querySelector(".ev-r-val strong")?.textContent).toBe("shipped");
    expect(body.querySelector("pre.run-output-body")).toBeNull();
  });

  // No keys at all means no region: a run whose steps all declared
  // captureOutput:false has nothing to say here, and a visible empty disclosure
  // would claim otherwise.
  it("hides the region when the run captured nothing", async () => {
    const { body } = await paint(openRunView, "completed", {});
    expect(body.querySelector(".ev-results")?.hasAttribute("hidden")).toBe(true);
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

  // What the run was ASKED to do. `state.inputs` had zero readers on any surface.
  it("states the run's inputs in the header", async () => {
    const { body } = await paint(openRunView, "running", undefined, {
      root: looping,
      inputs: { repo: "vibekit", target: "static-src" },
    });
    expect([...body.querySelectorAll(".ev-in-k")].map((n) => n.textContent)).toEqual([
      "repo",
      "target",
    ]);
    expect([...body.querySelectorAll(".ev-in-v")].map((n) => n.textContent)).toEqual([
      "vibekit",
      "static-src",
    ]);
  });
});

// A node with a transcript host but nothing in it gets a SENTENCE, and which one
// depends on facts a blank region states none of: where the transcript went, and
// whether there ever was one.
describe("run view empty step notes", () => {
  const root = {
    nodeId: "wf_1",
    type: "sequence",
    status: "running",
    children: [{ nodeId: "coder", type: "step", status: "running", children: [] }],
  };

  /** Paint one step and read its note. `parentChat` is the SERVER's answer, which
   *  is the change: the note used to branch on a local flag fed by SSE frames, so
   *  a reloaded reader of a chat-parented run took the parentless arm and was
   *  promised a transcript that could never arrive here. */
  async function note(status: string, parentChat: string): Promise<string> {
    const { body } = await paint(openRunView, status, undefined, {
      root,
      controls: { verbs: [], parent_chat_id: parentChat },
    });
    return body.querySelector(".ev-d-empty")?.textContent ?? "";
  }

  it("points a chat-parented run's reader at the launching chat", async () => {
    expect(await note("running", "c-launcher")).toContain("streams into that chat's transcript");
  });

  it("says a live parentless step has not spoken yet", async () => {
    expect(await note("running", "")).toContain("Waiting for this step");
  });

  // The ordinary case for a finished run, and the one most likely to be read as a
  // bug: the content was live-only, so there is nothing to show and never will be.
  it("says a finished step's transcript is gone rather than pending", async () => {
    const { body } = await paint(openRunView, "completed", undefined, {
      controls: { verbs: [], parent_chat_id: "" },
      root: {
        ...root,
        children: [{ nodeId: "coder", type: "step", status: "completed", children: [] }],
      },
    });
    const text = body.querySelector(".ev-d-empty")?.textContent ?? "";
    expect(text).toContain("never stored");
    expect(text).not.toContain("Waiting");
  });
});
