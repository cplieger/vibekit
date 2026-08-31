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

// Hoisted with the vi.mock factories below, which run before ordinary top-level
// initialisers and would otherwise read these in their TDZ.
const m = vi.hoisted(() => ({
  reply: { current: undefined as unknown },
  opened: [] as {
    id: string;
    opts?: { parent?: string; owns?: boolean; activate?: boolean } | undefined;
  }[],
  dispatched: [] as string[],
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
  } = {},
): Promise<{ labels: string[]; tab: OpenedTab; body: HTMLElement }> {
  const reply: RunInspectReply = {
    workflowId: "wf_1",
    state: {
      workflowId: "wf_1",
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
  // is the RUN's own fact and the only authority input left.
  showRun(tab.id, opts.parentless ?? true);
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

beforeEach(() => {
  m.opened.length = 0;
  m.dispatched.length = 0;
});

describe("run view controls", () => {
  // The verbs a status accepts are `run-controls.ts`'s pure table; what these pin is
  // that the row is offered at all, from every door, for a run vibekit hosts.
  it("offers the status's verbs on a parentless run, whatever the door", async () => {
    expect((await paint(openRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openRunView, "paused")).labels).toEqual(["Resume", "Cancel"]);
  });

  // The one gate left. An agent-parented run is the agent's to drive on a bridge it
  // holds, so the page offers nothing rather than a button that fights it.
  it("offers nothing on an agent-parented run", async () => {
    for (const status of ["running", "paused", "failed"]) {
      expect((await paint(openRunView, status, undefined, { parentless: false })).labels).toEqual(
        [],
      );
    }
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
  // parentless run to an agent-parented one must repaint the row away. A stale row
  // would be the failure mode of the shared-element design.
  it("repaints the row away when the shown run changes authority", async () => {
    expect((await paint(openRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openRunView, "running", undefined, { parentless: false })).labels).toEqual(
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

  async function note(status: string, parentless: boolean): Promise<string> {
    const { body } = await paint(openRunView, status, undefined, { parentless, root });
    return body.querySelector(".ev-d-empty")?.textContent ?? "";
  }

  it("points a chat-parented run's reader at the launching chat", async () => {
    expect(await note("running", false)).toContain("streams into that chat's transcript");
  });

  it("says a live parentless step has not spoken yet", async () => {
    expect(await note("running", true)).toContain("Waiting for this step");
  });

  // The ordinary case for a finished run, and the one most likely to be read as a
  // bug: the content was live-only, so there is nothing to show and never will be.
  it("says a finished step's transcript is gone rather than pending", async () => {
    const { body } = await paint(openRunView, "completed", undefined, {
      parentless: true,
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
