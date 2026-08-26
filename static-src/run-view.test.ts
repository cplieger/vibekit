// ---------------------------------------------------------------------------
// Tests for the shared run view's FLAVOUR gate — which door you came through
// decides whether the run is actionable, and status alone never does.
//
// This is the one rule the view has that cannot be read off a pure table:
// run-controls.ts pins WHICH verbs a status accepts, and these cases pin WHETHER
// the row is offered at all. The pair matters because the same `running` run is
// reachable from both doors: the Workflows tab (owned, actionable) and the
// History list (a review). GET /api/sessions does not filter by status, so the
// History case is real traffic and not a hypothetical.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

interface RunInspectReply {
  workflowId: string;
  state?: { workflowId: string; status?: string; capturedOutputs?: Record<string, string> };
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
  setTabStatus: vi.fn(),
}));

vi.mock("./decision-dock.js", () => ({
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  // Reached through run-dots.js, which run-view imports to seed a parentless
  // run's tab dot from the fetch.
  hasPendingDecision: vi.fn(() => false),
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
import { openRunView, openLiveRunView, showRun } from "./run-view.js";

/** Open a run through one of the two doors and let its first paint settle.
 *  Returns the control labels on screen, in order. */
async function paint(
  door: (id: string, name: string) => void,
  status: string,
  capturedOutputs?: Record<string, string>,
): Promise<{ labels: string[]; tab: OpenedTab; body: HTMLElement }> {
  const reply: RunInspectReply = {
    workflowId: "wf_1",
    state: {
      workflowId: "wf_1",
      status,
      ...(capturedOutputs === undefined ? {} : { capturedOutputs }),
    },
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
  // The activation hook, driven the way the composition root wires it: `owns` is
  // the subject fact the door set, and `parentless` is the RUN's own fact — no
  // chat launched this one, so it is true through either door.
  showRun(tab.id, tab.opts?.owns === true, true);
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

describe("run view flavour gate", () => {
  // The user decision this pins: History must not hand out live tabs. A run that
  // is still moving is still only a record when it is reached from the list of
  // finished work.
  it("offers no controls on a review, even while the run is live", async () => {
    for (const status of ["running", "paused"]) {
      expect((await paint(openRunView, status)).labels).toEqual([]);
    }
  });

  // The same two statuses through the owned door are the live surface, so the
  // verbs appear. If this and the case above ever agree, the gate is gone.
  it("offers the status's verbs on an owned tab", async () => {
    expect((await paint(openLiveRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openLiveRunView, "paused")).labels).toEqual(["Resume", "Cancel"]);
  });

  // A finished run is read-only through BOTH doors: the gate subtracts, and must
  // never be the thing that ADDS a verb a terminal status does not accept.
  // A COMPLETED run offers nothing through either door: there is no failed work
  // to reset and nothing to stop.
  it("offers no controls on a completed run through either door", async () => {
    expect((await paint(openRunView, "completed")).labels).toEqual([]);
    expect((await paint(openLiveRunView, "completed")).labels).toEqual([]);
  });

  // A FAILED run is the one case a review door DOES carry a control: retry acts
  // on a finished run, and History is where a failed one is found. The launcher
  // door carries it too — both are gated on the run being parentless, which
  // openLiveRunView always is and openRunView is told.
  it("offers retry on a failed run through both doors", async () => {
    for (const status of ["failed", "aborted"]) {
      expect((await paint(openLiveRunView, status)).labels).toContain("Retry failed steps");
    }
  });

  // Closing an owned tab cancels; closing a review closes nothing. This is the
  // other half of what the flavour means, and the half with teeth — a review
  // whose × cancelled a live run would destroy work from the read-only surface.
  //
  // The door no longer carries the teardown: `owns` is a SUBJECT field, and the
  // factory turns it into the × behaviour (tab-materialize pins that half). So
  // what a door owes is the flag, and this asserts each door sets the one that
  // makes the two coexist for one `(kind, ref)` on the wire.
  it("marks only the owned tab as owning the run", async () => {
    const review = await paint(openRunView, "running");
    expect(review.tab.opts?.owns).toBe(false);

    const live = await paint(openLiveRunView, "running");
    expect(live.tab.opts?.owns).toBe(true);
  });

  // The view is shared — one DOM element serves every run tab — so switching
  // from an owned tab to a review must repaint the row away. A stale row would be
  // the failure mode of the shared-element design.
  it("drops the row when the same view switches from owned to review", async () => {
    expect((await paint(openLiveRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openRunView, "running")).labels).toEqual([]);
  });
});

// A step only gets a capturedOutputs key when it captured, and the captured
// value is its last assistant text. So an EMPTY value is a fact about the run,
// not an absence — hiding it made a silent step indistinguishable from one that
// never ran, on the surface whose whole job is reading a finished run.
describe("run view captured output", () => {
  it("renders an empty capture with its reason instead of dropping it", async () => {
    const { body } = await paint(openRunView, "completed", { review: "   " });
    const nodes = [...body.querySelectorAll(".run-output-node")].map((n) => n.textContent);
    expect(nodes).toEqual(["review"]);
    const empty = body.querySelector(".run-output-empty");
    expect(empty?.textContent).toContain("last assistant message carried no text");
    // The empty row replaces the body, never sits beside a blank one.
    expect(body.querySelectorAll(".run-output-body")).toHaveLength(0);
  });

  it("still renders a non-empty capture verbatim", async () => {
    const { body } = await paint(openRunView, "completed", { build: "ok\nshipped" });
    expect(body.querySelector(".run-output-body")?.textContent).toBe("ok\nshipped");
    expect(body.querySelector(".run-output-empty")).toBeNull();
  });

  it("shows an empty step beside a speaking one", async () => {
    const { body } = await paint(openRunView, "completed", { build: "ok", review: "" });
    expect(body.querySelectorAll(".run-output")).toHaveLength(2);
    expect(body.querySelectorAll(".run-output-empty")).toHaveLength(1);
  });

  // No keys at all means no section: a run whose steps all declared
  // captureOutput:false has nothing to say here, and an empty heading would
  // claim otherwise.
  it("omits the section when the run captured nothing", async () => {
    const { body } = await paint(openRunView, "completed", {});
    expect([...body.querySelectorAll(".section-title")].map((h) => h.textContent)).not.toContain(
      "Captured output",
    );
  });
});
