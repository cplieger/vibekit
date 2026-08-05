// @vitest-environment happy-dom
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
  state?: { workflowId: string; status?: string };
}

interface OpenedTab {
  id: string;
  onShow: () => void;
  // Explicitly `| undefined`: a review tab pushes onClose: undefined, and
  // exactOptionalPropertyTypes distinguishes that from an absent property.
  onClose?: (() => void) | undefined;
}

// Hoisted with the vi.mock factories below, which run before ordinary top-level
// initialisers and would otherwise read these in their TDZ.
const m = vi.hoisted(() => ({
  reply: { current: undefined as unknown },
  opened: [] as { id: string; onShow: () => void; onClose?: (() => void) | undefined }[],
  dispatched: [] as string[],
}));

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(() => Promise.resolve(m.reply.current)),
}));

vi.mock("./tabs.js", () => ({
  openRunTab: vi.fn(
    (id: string, _name: string, onShow: () => void, opts?: { onClose?: () => void }) => {
      m.opened.push({ id, onShow, onClose: opts?.onClose });
    },
  ),
}));

vi.mock("./decision-dock.js", () => ({
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
}));

vi.mock("./actions/runs.js", () => {
  const stub = (verb: string) => ({
    dispatch: vi.fn((id: string) => {
      m.dispatched.push(`${verb}:${id}`);
      return Promise.resolve();
    }),
  });
  return { cancelRun: stub("cancel"), pauseRun: stub("pause"), resumeRun: stub("resume") };
});

import { openRunView, openLiveRunView } from "./run-view.js";

/** Open a run through one of the two doors and let its first paint settle.
 *  Returns the control labels on screen, in order. */
async function paint(
  door: (id: string, name: string) => void,
  status: string,
): Promise<{ labels: string[]; tab: OpenedTab }> {
  const reply: RunInspectReply = { workflowId: "wf_1", state: { workflowId: "wf_1", status } };
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
  tab.onShow();
  // load() awaits one apiGet before painting; drain enough microtasks for the
  // promise chain to settle without reaching for fake timers.
  for (let i = 0; i < 5; i++) {
    await Promise.resolve();
  }

  const labels = [...body.querySelectorAll(".run-controls button")].map((b) =>
    (b.textContent ?? "").trim(),
  );
  return { labels, tab };
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
  it("offers no controls on a finished run through either door", async () => {
    for (const status of ["completed", "failed", "aborted"]) {
      expect((await paint(openRunView, status)).labels).toEqual([]);
      expect((await paint(openLiveRunView, status)).labels).toEqual([]);
    }
  });

  // Closing an owned tab cancels; closing a review closes nothing. This is the
  // other half of what the flavour means, and the half with teeth — a review
  // whose × cancelled a live run would destroy work from the read-only surface.
  it("cancels on close only for the owned tab", async () => {
    const review = await paint(openRunView, "running");
    expect(review.tab.onClose).toBeUndefined();

    const live = await paint(openLiveRunView, "running");
    live.tab.onClose?.();
    expect(m.dispatched).toContain("cancel:wf_1");
  });

  // The view is shared — one DOM element serves every run tab — so switching
  // from an owned tab to a review must repaint the row away. A stale row would be
  // the failure mode of the shared-element design.
  it("drops the row when the same view switches from owned to review", async () => {
    expect((await paint(openLiveRunView, "running")).labels).toEqual(["Pause", "Cancel"]);
    expect((await paint(openRunView, "running")).labels).toEqual([]);
  });
});
