// ---------------------------------------------------------------------------
// The control row's IN-FLIGHT state, and what happens to it when the row is
// rebuilt.
//
// Two properties, and neither had a test anywhere in this suite. A retry starts a
// process and can legitimately take tens of seconds, so an unbound button looks
// dead for the whole handshake and can be clicked again meanwhile — that is what
// the pending binding is for. And the row is rebuilt on every render, one per
// `run_progress` frame, so a binding that outlives its button accumulates one live
// effect per frame for the tab's lifetime.
//
// Its own file because it needs REAL actions: pending state lives in the actions
// registry, so a plain `vi.fn()` dispatch cannot produce it, and run-view.test.ts's
// cases want a dispatch that resolves the moment it is called.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

const m = vi.hoisted(() => ({
  reply: { current: undefined as unknown },
  controls: { current: undefined as unknown },
  opened: [] as string[],
  /** One resolver per dispatch still in flight, so a test decides when a verb
   *  settles and can hold one open across a repaint. */
  settle: [] as (() => void)[],
}));

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

vi.mock("./tabs.js", () => ({
  openRunTab: vi.fn((id: string) => {
    m.opened.push(id);
    return Promise.resolve();
  }),
  tabIdFor: vi.fn(() => ""),
  tabSetVersion: vi.fn(() => 0),
  setTabStatus: vi.fn(),
  closeTab: vi.fn(),
  getActiveTabId: vi.fn(() => ""),
  openEditorView: vi.fn(),
  setTabDirty: vi.fn(),
  toggleGitView: vi.fn(),
}));

vi.mock("./decision-dock.js", () => ({
  mountRunDecisionDock: vi.fn(),
  rerenderDocks: vi.fn(),
  hasPendingDecision: vi.fn(() => false),
  runPendingAsks: vi.fn(() => ({ count: 0, nodes: new Set<string>(), label: "" })),
}));

// REAL actions, deliberately: `bindLoadingState` reads the registry's pending
// signal for the action NAME, so only a genuine dispatch flips the button. Each
// one parks until the test resolves it, which is what a slow engine handshake
// looks like from the button's seat.
vi.mock("./actions/runs.js", async () => {
  const { defineAction } = await import("@cplieger/actions");
  const verb = (name: string) =>
    defineAction<string, null>({
      name: `runs.${name}`,
      run: () =>
        new Promise<null>((resolve) => {
          m.settle.push(() => {
            resolve(null);
          });
        }),
      error: false,
    });
  return {
    cancelRun: verb("cancel"),
    pauseRun: verb("pause"),
    resumeRun: verb("resume"),
    retryRun: verb("retry"),
  };
});

import { openRunView, showRun } from "./run-view.js";
import { apiGet, apiGetTyped } from "./api-client.js";
import { invalidateRun } from "./run-store.js";

/** Drain enough microtasks for the store's two fetches and the render they wake. */
async function drain(): Promise<void> {
  for (let i = 0; i < 12; i++) {
    await Promise.resolve();
  }
}

/** ONE run for the whole file, run-view.test.ts's convention and for its reason:
 *  the view installs a single effect that subscribes to the cell of whatever run it
 *  first painted, so a case naming a second run would render nothing at all. */
const RUN = "wf_1";

/** The state fixture, a NEW object each time: the store's cell only wakes its
 *  readers when the value actually changes, so re-using one would paint once. */
function abortedState(): unknown {
  return { workflowId: RUN, state: { workflowId: RUN, status: "aborted" } };
}

const RETRY_ONLY = { verbs: ["retry"], refused: {}, parent_chat_id: "" };

/** Open the run, whose only verb is retry, and let its first paint settle. */
async function paintRetryRow(): Promise<HTMLElement> {
  m.reply.current = abortedState();
  m.controls.current = RETRY_ONLY;

  document.body.replaceChildren();
  const body = document.createElement("div");
  body.id = "run-body";
  const dock = document.createElement("div");
  dock.id = "run-dock";
  document.body.append(body, dock);

  openRunView(RUN, "nightly");
  showRun(RUN);
  await drain();
  return body;
}

/** Repaint the run the way a `run_progress` frame does: a fresh state lands in the
 *  store and the view's one effect renders it, rebuilding the control row. */
async function repaint(): Promise<void> {
  m.reply.current = abortedState();
  invalidateRun(RUN);
  await drain();
}

function retryButton(body: HTMLElement): HTMLButtonElement {
  const btn = body.querySelector<HTMLButtonElement>(".run-controls button");
  if (btn === null) {
    throw new Error("the control row has no button");
  }
  return btn;
}

beforeEach(() => {
  m.opened.length = 0;
  m.settle.length = 0;
  vi.mocked(apiGet).mockImplementation(() => Promise.resolve(m.reply.current));
  vi.mocked(apiGetTyped).mockImplementation((_path, decode) =>
    Promise.resolve(m.controls.current === undefined ? null : decode(m.controls.current)),
  );
});

afterEach(async () => {
  // Leave nothing pending: the registry is module state for the whole file, so a
  // dispatch still in flight would arrive in the next test's button.
  for (const resolve of m.settle.splice(0)) {
    resolve();
  }
  await drain();
});

describe("the control row's in-flight state", () => {
  // The button is unbound no more. Without this a retry looked dead for the whole
  // handshake — no disabled state, no busy state — and every further click sent
  // another request for work already being started.
  it("disables the button and marks it busy while the verb is in flight", async () => {
    const body = await paintRetryRow();
    const btn = retryButton(body);
    expect(btn.disabled).toBe(false);

    btn.click();
    await drain();
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
  });

  it("hands the button back when the verb settles", async () => {
    const body = await paintRetryRow();
    const btn = retryButton(body);

    btn.click();
    await drain();
    // The premise, stated: without it the release below is satisfied by a button
    // nothing ever disabled, which is the same green for a binding that is missing
    // entirely.
    expect(btn.disabled).toBe(true);

    for (const resolve of m.settle.splice(0)) {
      resolve();
    }
    await drain();

    expect(btn.disabled).toBe(false);
    expect(btn.hasAttribute("aria-busy")).toBe(false);
  });

  // THE LEAK. `bindLoadingState` disposes itself only for an element that was
  // ATTACHED the last time its effect ran, and the row is built before the page
  // appends it — so a button replaced before its first pending flip never armed
  // that path and stayed subscribed. A detached button still following the action
  // is the observable half of one live effect per repaint, for the tab's lifetime.
  it("stops following the verb once the row that carried the button is replaced", async () => {
    const body = await paintRetryRow();
    const stale = retryButton(body);

    await repaint();
    const live = retryButton(body);
    expect(stale.isConnected).toBe(false);
    expect(live).not.toBe(stale);

    live.click();
    await drain();

    expect(live.disabled).toBe(true);
    expect(stale.disabled).toBe(false);
    expect(stale.hasAttribute("aria-busy")).toBe(false);
  });

  // The other direction, and the reason the drain cannot simply dispose every
  // binding it finds: a verb in flight across a repaint must keep the NEW button
  // disabled, because the work is still running and the row is the same row.
  it("keeps the replacement button disabled while the verb it carries is still in flight", async () => {
    const body = await paintRetryRow();
    retryButton(body).click();
    await drain();

    await repaint();
    const live = retryButton(body);

    expect(live.disabled).toBe(true);
    expect(live.getAttribute("aria-busy")).toBe("true");
  });
});
