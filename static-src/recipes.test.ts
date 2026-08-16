// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for the Workflows sub-tab — the Run ⇄ Cancel row logic, not the DOM
// chrome. Each case pins a piece of the single-run contract:
//   - the button names the recipe's ONE possible live run (Run ⇄ Cancel flips
//     on the run list, not on who launched it)
//   - paused is NOT terminal: a paused run still blocks a relaunch, so its row
//     must offer Cancel or the recipe wedges with no way out
//   - launching with declared inputs collects them inline (no modal)
//   - a launch opens the run tab that OWNS the run
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

const dispatched: { name: string; args: unknown }[] = [];

vi.mock("./actions/runs.js", () => ({
  loadRecipes: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "recipes", args });
      return Promise.resolve(recipesReply);
    }),
  },
  loadRuns: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "runs", args });
      return Promise.resolve(runsReply);
    }),
  },
  launchRun: {
    dispatch: vi.fn(
      (
        args: unknown,
        opts?: { onSuccess?: (d: { workflow_id: string; name: string }) => void },
      ) => {
        dispatched.push({ name: "launch", args });
        opts?.onSuccess?.({ workflow_id: "wf_new", name: "goal" });
        return Promise.resolve({ workflow_id: "wf_new", name: "goal" });
      },
    ),
  },
  cancelRun: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "cancel", args });
      return Promise.resolve({ ok: true });
    }),
  },
}));
vi.mock("./run-view.js", () => ({ openLiveRunView: vi.fn() }));
// The unattended note's auto-approve read-out. Unmocked, refreshAutoApprove
// reaches /api/settings through the actions transport (which the api-client mock
// does not cover), fire-and-forget, so the request was still open when the window
// tore down and printed an unhandled AbortError.
vi.mock("./persist.js", () => ({ loadSettings: vi.fn(async () => ({})) }));

// The Schedule button's actions: unmocked they reach the network, and a row's
// summary line is decoration this suite does not assert on.
vi.mock("./actions/schedules.js", () => ({
  loadSchedules: { dispatch: vi.fn(async () => ({ schedules: [] })) },
  saveSchedule: { dispatch: vi.fn(async () => null) },
  deleteSchedule: { dispatch: vi.fn(async () => null) },
}));

import { renderRecipesPanel } from "./recipes.js";
import { openLiveRunView } from "./run-view.js";
import { launchRun, cancelRun } from "./actions/runs.js";
import type { RecipesResponse, WorkflowRunRow, ResumableSessionRow } from "./types.js";

let recipesReply: RecipesResponse = { recipes: [] };
let runsReply: { sessions: ResumableSessionRow[]; runs: WorkflowRunRow[] } = {
  sessions: [],
  runs: [],
};

function recipe(name: string, inputs?: Record<string, string>): RecipesResponse["recipes"][0] {
  const base = { name, source: `bundled://${name}`, description: `${name} desc` };
  return inputs === undefined ? base : { ...base, inputs };
}

function run(name: string, id: string, status: string): WorkflowRunRow {
  return { workflow_id: id, name, status, updated_at: 0 };
}

async function render(): Promise<HTMLElement> {
  const panel = document.createElement("div");
  document.body.appendChild(panel);
  renderRecipesPanel(panel);
  // renderRecipesPanel awaits its two fetches before painting.
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  return panel;
}

function buttonFor(panel: HTMLElement, source: string): HTMLButtonElement | null {
  return panel.querySelector<HTMLButtonElement>(`[data-recipe="${source}"] .recipe-run-btn`);
}

beforeEach(() => {
  document.body.replaceChildren();
  dispatched.length = 0;
  vi.mocked(openLiveRunView).mockClear();
  vi.mocked(launchRun.dispatch).mockClear();
  vi.mocked(cancelRun.dispatch).mockClear();
  recipesReply = { recipes: [] };
  runsReply = { sessions: [], runs: [] };
});

describe("the Run ⇄ Cancel row", () => {
  it("labels an idle recipe Run and a live one Cancel", async () => {
    recipesReply = { recipes: [recipe("goal"), recipe("investigate")] };
    runsReply.runs = [run("investigate", "wf_9", "running")];
    const panel = await render();

    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
    expect(buttonFor(panel, "bundled://investigate")?.textContent).toBe("Cancel");
  });

  it("treats a PAUSED run as live — Cancel, or the recipe wedges", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_1", "paused")];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Cancel");
  });

  it("returns a TERMINAL run's row to Run — a fresh run, never a retry", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_1", "failed")];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
  });

  it("launches an input-less recipe on click and opens the OWNED run tab", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    const panel = await render();

    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(launchRun.dispatch).mock.calls[0]?.[0]).toEqual({
      source: "bundled://goal",
      inputs: {},
    });
    expect(vi.mocked(openLiveRunView)).toHaveBeenCalledWith("wf_new", "goal");
  });

  it("cancels the LIVE run on click, whoever launched it", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_7", "running")];
    const panel = await render();

    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(cancelRun.dispatch).mock.calls[0]?.[0]).toBe("wf_7");
    expect(vi.mocked(launchRun.dispatch)).not.toHaveBeenCalled();
  });

  it("collects declared inputs inline before launching — no modal", async () => {
    recipesReply = { recipes: [recipe("goal", { prompt: "prompt", max_iterations: "string" })] };
    const panel = await render();

    // First click expands the form instead of launching.
    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(launchRun.dispatch)).not.toHaveBeenCalled();
    const form = panel.querySelector<HTMLFormElement>(".recipe-input-form");
    expect(form).not.toBeNull();

    // Fill one field, leave the other empty (allowed), submit.
    const field = form?.querySelector<HTMLInputElement>("input");
    if (field !== null && field !== undefined) {
      field.value = "make the tests pass";
    }
    form?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    const args = vi.mocked(launchRun.dispatch).mock.calls[0]?.[0] as {
      source: string;
      inputs: Record<string, string>;
    };
    expect(args.source).toBe("bundled://goal");
    expect(args.inputs).toEqual({ prompt: "make the tests pass" });
  });
});
