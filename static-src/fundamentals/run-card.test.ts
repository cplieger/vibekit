// The workflow run card's state vocabulary.
//
// Two changes are pinned here, and they are one change: the pip row that used to
// sit under the head is gone, so the per-step GLYPH is the only signal left for
// what a step is doing — which means every state a step can be in has to reach it.
// A `paused` step used to render the running spinner, and an unanswered ask
// reached the card at all.
import { describe, it, expect } from "vitest";

import { buildRunCard, type RunAsks } from "./run-card.js";
import type { RunNode, RunState } from "../run-store.js";

function step(nodeId: string, status: RunNode["status"]): RunNode {
  return { nodeId, type: "step", status };
}

function runOf(status: NonNullable<RunState["status"]>, ...children: RunNode[]): RunState {
  return {
    workflowId: "wf_1",
    status,
    root: { nodeId: "wf_1", type: "sequence", status: "running", children },
  };
}

function asks(count: number, nodes: string[], label = ""): RunAsks {
  return { count, nodes: new Set(nodes), label };
}

function card(): ReturnType<typeof buildRunCard> {
  return buildRunCard("wf_1", "Workflow run", () => {
    /* the footer link is not under test */
  });
}

function rowStates(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step")].map((e) => e.dataset["status"] ?? "");
}

function glyphs(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step-glyph")].map((e) => e.textContent ?? "");
}

function statusWord(root: HTMLElement): string {
  return root.querySelector(".run-state")?.textContent ?? "";
}

function alertText(root: HTMLElement): string {
  const a = root.querySelector<HTMLElement>(".run-alert");
  return a === null || a.classList.contains("hidden") ? "" : (a.textContent ?? "");
}

describe("the deleted pip row", () => {
  it("builds no spine region at all", () => {
    const c = card();
    c.render(runOf("running", step("a", "running"), step("b", "pending")));
    // Not `.hidden`: a region kept in the DOM is a region that can come back with
    // one CSS rule, and the pips were deleted rather than suppressed.
    expect(c.root.querySelector(".run-spine")).toBeNull();
    expect(c.root.querySelector(".run-pip")).toBeNull();
  });

  it("leaves the head, alert, body and foot in that order", () => {
    const c = card();
    expect([...c.root.children].map((e) => e.className.split(" ")[0])).toEqual([
      "run-head",
      "run-alert",
      "run-body",
      "run-foot",
    ]);
  });
});

describe("a step's own state", () => {
  it("separates a paused step from a running one", () => {
    const c = card();
    c.render(runOf("paused", step("a", "running"), step("b", "paused")));
    // `paused` used to fold onto `running`, so both rows carried the spinner and the
    // card claimed progress on a step where nothing was moving.
    expect(rowStates(c.root)).toEqual(["running", "waiting"]);
    // Neither carries a badge character: CSS draws the ring for both, and MOTION is
    // what separates them.
    expect(glyphs(c.root)).toEqual(["", ""]);
  });

  it("keeps every settled state's badge character", () => {
    const c = card();
    c.render(
      runOf(
        "completed",
        step("a", "completed"),
        step("b", "failed"),
        step("c", "aborted"),
        step("d", "skipped"),
        step("e", "pending"),
      ),
    );
    expect(rowStates(c.root)).toEqual(["ok", "fail", "warn", "skipped", "pending"]);
    expect(glyphs(c.root)).toEqual(["\u2713", "\u2717", "\u26A0", "\u2013", ""]);
  });

  it("names the state in the row's accessible label", () => {
    const c = card();
    c.render(runOf("paused", step("a", "paused")));
    expect(c.root.querySelector(".run-step-head")?.getAttribute("aria-label")).toBe("a, waiting");
  });
});

describe("an unanswered ask", () => {
  it("marks the step the ask names", () => {
    const c = card();
    c.render(runOf("running", step("a", "running"), step("b", "pending")), asks(1, ["a"]));
    expect(rowStates(c.root)).toEqual(["input", "pending"]);
    expect(glyphs(c.root)[0]).toBe("?");
    expect(c.root.querySelector(".run-step-head")?.getAttribute("aria-label")).toBe(
      "a, waiting for your answer",
    );
  });

  it("leaves a settled step alone, because node_id is not instance-unique", () => {
    const c = card();
    // A repeat's iterations are separate `iter-N` containers holding the SAME step,
    // so the two rows have distinct node paths and a shared node id. An ask naming
    // `a` therefore matches both, and only the one still in flight can be the asker.
    const iter = (n: string, status: RunNode["status"]): RunNode => ({
      nodeId: n,
      type: "sequence",
      status: "completed",
      children: [step("a", status)],
    });
    c.render(
      {
        workflowId: "wf_1",
        status: "running",
        root: {
          nodeId: "wf_1",
          type: "repeat",
          status: "running",
          children: [iter("iter-0", "completed"), iter("iter-1", "running")],
        },
      },
      asks(1, ["a"]),
    );
    expect(rowStates(c.root)).toEqual(["ok", "input"]);
  });

  it("takes the head's status word over the run's own", () => {
    const c = card();
    const state = runOf("running", step("a", "running"));
    c.render(state, asks(1, ["a"]));
    // The run genuinely IS running — KAS blocks the asking step's turn and leaves the
    // run's status alone — so `data-status` must not be overwritten, and the second
    // axis is what the rail and the word read.
    expect(statusWord(c.root)).toBe("needs input");
    expect(c.root.dataset["status"]).toBe("running");
    expect(c.root.dataset["asking"]).toBe("true");

    c.render(state, asks(0, []));
    expect(statusWord(c.root)).toBe("running");
    expect(c.root.dataset["asking"]).toBeUndefined();
  });

  it("reports itself in the alert, ahead of the run's own status", () => {
    const c = card();
    c.render(runOf("running", step("a", "running")), asks(1, ["a"], "Run git push"));
    expect(alertText(c.root)).toBe("Waiting for your answer: Run git push");
    expect(c.root.querySelector<HTMLElement>(".run-alert")?.dataset["kind"]).toBe("input");
  });

  it("still says so when the wire could not name a step", () => {
    const c = card();
    // No sub-session in the step registry means no `node_id`, and the run is blocked
    // either way — only the ROW cannot be marked.
    c.render(runOf("running", step("a", "running")), asks(2, []));
    expect(rowStates(c.root)).toEqual(["running"]);
    expect(statusWord(c.root)).toBe("needs input");
    expect(alertText(c.root)).toBe("Waiting for your answer \u00b7 2 asks waiting");
  });

  it("loses to a failed launch, which is the one state with no run behind it", () => {
    const c = card();
    c.render(runOf("running", step("a", "running")), asks(1, ["a"], "Run git push"));
    c.setLaunch("failed", "recipe not found");
    expect(alertText(c.root)).toBe("recipe not found");
    // setLaunch re-renders from what it was last told, so the ask survives the pass
    // rather than being cleared by the omission.
    expect(c.root.dataset["asking"]).toBe("true");
  });

  it("outranks a pause, which is the state a click cannot resolve", () => {
    const state: RunState = {
      ...runOf("paused", step("a", "paused")),
      pauseReason: "retry budget",
    };
    expect(alertText(buildAndRender(state, asks(0, [])))).toBe("Waiting: retry budget");
    expect(alertText(buildAndRender(state, asks(1, ["a"], "Approve")))).toBe(
      "Waiting for your answer: Approve",
    );
  });
});

function buildAndRender(state: RunState, a: RunAsks): HTMLElement {
  const c = card();
  c.render(state, a);
  return c.root;
}
