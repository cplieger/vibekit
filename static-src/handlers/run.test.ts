// @vitest-environment happy-dom
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

vi.mock("../run-view.js", () => ({ refreshRunView: vi.fn() }));

import "./run.js";
import { dispatch, onBus, BUS_RUNS_CHANGED } from "../bus.js";
import type { SSEPayloads } from "../bus.js";
import { refreshRunView } from "../run-view.js";

const runView = vi.mocked(refreshRunView);

// The list side goes over the bus, so the test subscribes exactly as the history
// page does rather than asserting on a mocked import.
let listRefetches = 0;
onBus(BUS_RUNS_CHANGED, () => {
  listRefetches++;
});

beforeEach(() => {
  runView.mockClear();
  listRefetches = 0;
});

type RunEvent = "run_started" | "run_progress" | "run_finished";

function send(type: RunEvent, payload: Record<string, unknown>): void {
  dispatch({ type, chat_id: "c1", payload });
}

// The three run events really are keys of the typed SSE surface, so a rename on
// the Go side that regenerates the wire types breaks this file rather than
// silently unsubscribing the handlers.
const _keys: readonly (keyof SSEPayloads)[] = ["run_started", "run_progress", "run_finished"];
void _keys;

describe("run SSE handlers", () => {
  it("re-reads the run on every one of the three events", () => {
    const order: RunEvent[] = ["run_started", "run_progress", "run_finished"];
    for (const [i, type] of order.entries()) {
      send(type, { workflow_id: "wf_1", kind: "node_start", status: "completed", name: "x" });
      expect(runView).toHaveBeenCalledTimes(i + 1);
      expect(runView).toHaveBeenLastCalledWith("wf_1");
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

  it("passes the workflow id through so a view showing another run ignores it", () => {
    send("run_progress", { workflow_id: "wf_other", kind: "node_start" });
    expect(runView).toHaveBeenCalledWith("wf_other");
  });
});
