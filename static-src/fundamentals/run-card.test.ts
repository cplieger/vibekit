// The workflow run card's state vocabulary.
//
// Two changes are pinned here, and they are one change: the pip row that used to
// sit under the head is gone, so the per-step GLYPH is the only signal left for
// what a step is doing — which means every state a step can be in has to reach it.
// A `paused` step used to render the running spinner, and an unanswered ask
// reached the card at all.
import { vi, describe, it, expect } from "vitest";

// scroll.ts is a self-initialising singleton over a real `#messages`; the canonical
// mock is what every other suite in this graph uses, and its compensation helpers run
// their mutation, so a fold still reaches the DOM. It is also what makes the
// compensation itself observable — see "routes the fold through the scroll
// compensator".
vi.mock("../scroll.js", () =>
  import("../__test-helpers__/scroll-mock.js").then((m) => m.scrollMock),
);

import { buildRunCard, type RunAsks } from "./run-card.js";
import { scrollMock } from "../__test-helpers__/scroll-mock.js";
import { leaves } from "../exec-view/model.js";
import { runToExec } from "../run-exec-source.js";
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

const NO_ASKS: RunAsks = asks(0, []);

function card(): ReturnType<typeof buildRunCard> {
  return buildRunCard("wf_1", "Workflow run", () => {
    /* the footer link is not under test */
  });
}

function rowStates(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step")].map((e) => e.dataset["status"] ?? "");
}

/** Each step's mark, described: `"icon"` for a state that paints a silhouette,
 *  otherwise the character it paints (`""` for a CSS ring). The three settled
 *  outcomes are SVGs now, so reading textContent alone would report every one of
 *  them as empty. */
function glyphs(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step-glyph")].map((e) =>
    e.querySelector("svg") === null ? (e.textContent ?? "") : "icon",
  );
}

/** The markup of each step's SVG mark, for the distinctness check below. */
function iconMarkup(root: HTMLElement): string[] {
  return [...root.querySelectorAll<HTMLElement>(".run-step-glyph svg")].map((e) => e.outerHTML);
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

  it("gives every settled state its own mark", () => {
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
    // The three settled OUTCOMES take a silhouette; `skipped` keeps its en dash
    // (nothing happened, and a dash was never one of the marks the ruling removed)
    // and `pending` is a CSS ring.
    expect(glyphs(c.root)).toEqual(["icon", "icon", "icon", "\u2013", ""]);
    // The shape channel at this surface: three states, three different shapes, so
    // hue is never the only thing separating them (WCAG 1.4.1).
    const marks = iconMarkup(c.root);
    expect(marks).toHaveLength(3);
    expect(new Set(marks).size).toBe(3);
  });

  it("renders an unknown run as unknown and live, never as starting", () => {
    const c = card();
    c.render(runOf("unknown", step("a", "unknown")));
    expect(statusWord(c.root)).toBe("unknown");
    expect(rowStates(c.root)).toEqual(["unknown"]);
    expect(c.root.classList.contains("collapsed")).toBe(false);
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
    const iter = (n: string, iteration: number, status: RunNode["status"]): RunNode => ({
      nodeId: n,
      type: "sequence",
      status: "completed",
      iteration,
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
          children: [iter("loop#0", 0, "completed"), iter("loop#1", 1, "running")],
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

// A pause reason is KAS's own prose and reaches the reader verbatim, EXCEPT for
// the two literals that mean a person owes an answer. Those name a tool and a
// mechanism where the reader needs to know somebody is waiting on them, and this
// arm is the case where the question itself never arrived — a restart lost the
// text, or this client has not been handed it yet.
describe("a pause that means a step is waiting on a person", () => {
  function pausedWith(reason: string): string {
    return alertText(
      buildAndRender({ ...runOf("paused", step("a", "paused")), pauseReason: reason }, asks(0, [])),
    );
  }

  it("replaces the send_message literal with a sentence about the reader", () => {
    expect(pausedWith("Step requested user input via send_message.")).toBe(
      "A step is waiting for your answer",
    );
  });

  it("replaces the re-park literal too, whose node id sits in the middle", () => {
    // A plain Resume clears the RUN's pause reason and leaves the step node's
    // signal, so the next step execution parks again under this fallback.
    expect(pausedWith("Step 'review' is waiting for user input.")).toBe(
      "A step is waiting for your answer",
    );
    expect(pausedWith("Step 'review' is waiting for the next user message.")).toBe(
      "A step is waiting for your answer",
    );
  });

  // The branch arm, which no reason can reach: the run keeps only the wrapper KAS
  // composes for a parallel, and that same wrapper covers an interruption and a
  // permanent failure — so the sentence has to come from the node's own signal.
  it("recognises a park inside a parallel branch", () => {
    const run: RunState = {
      workflowId: "wf_1",
      status: "paused",
      pauseReason: "Parallel 'phase1' is waiting on branch 'verify'.",
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "running",
        children: [
          {
            nodeId: "phase1",
            type: "parallel",
            status: "paused",
            children: [
              { nodeId: "verify", type: "step", status: "paused", completionSignal: "need_input" },
            ],
          },
        ],
      },
    };
    expect(alertText(buildAndRender(run, asks(0, [])))).toBe("A step is waiting for your answer");
  });

  it("quotes any other reason verbatim, because it is not about the reader", () => {
    expect(pausedWith("waiting on a watch condition")).toBe(
      "Waiting: waiting on a watch condition",
    );
    expect(pausedWith("")).toBe("Waiting");
  });

  it("keeps the transient-error code beside the sentence", () => {
    const text = alertText(
      buildAndRender(
        {
          ...runOf("paused", step("a", "paused")),
          pauseReason: "Step requested user input via send_message.",
          pauseDetail: { code: "ThrottlingException" },
        },
        asks(0, []),
      ),
    );
    expect(text).toBe(
      "A step is waiting for your answer \u00b7 after a transient error (Throttli" + "ngException)",
    );
  });

  // Upstream 2.21.1 gave `pauseDetail.class` a second member, and until then both
  // render sites hardcoded the transient label. An exhausted continuation budget
  // is not a transient failure, and `pauseReason` already carries upstream's own
  // sentence, so the class states its code and claims nothing.
  it("does not call an exhausted continuation budget a transient error", () => {
    const text = alertText(
      buildAndRender(
        {
          ...runOf("paused", step("a", "paused")),
          pauseReason: "Step could not be continued after 3 consecutive attempts.",
          pauseDetail: { class: "continuation-exhausted", code: "MaxContinuationAttempts" },
        },
        asks(0, []),
      ),
    );
    expect(text).not.toContain("transient");
    expect(text).toBe(
      "Waiting: Step could not be continued after 3 consecutive attempts. \u00b7 " +
        "(MaxContinuationAttempts)",
    );
  });
});

function buildAndRender(state: RunState, a: RunAsks): HTMLElement {
  const c = card();
  c.render(state, a);
  return c.root;
}

// ---------------------------------------------------------------------------
// THE DOOR'S KEY. A step row is a door into `/run/<id>` with that node selected,
// so the path the row hands over has to be the path the exec view addresses that
// node by — otherwise the click lands on nothing and the page silently
// auto-follows. Two producers, two modules: `render` builds a row per state-tree
// leaf through `nodePathOf`, and `run-exec-source.ts` builds an `ExecNode.path`
// through `nodePathSegment`. Nothing pinned that they agree.
//
// It is the same key defect one surface along: KAS spells a repeat's iteration
// container `<repeatId>#<n>` in the state tree and `iter-<n>` in a frame path,
// which is what used to give every loop-body step TWO rows in this card.
// ---------------------------------------------------------------------------
describe("a step row's key is the exec view's own node path", () => {
  // The shape a real completed loop run has: the iteration container carries its
  // own generated id AND its `iteration`, which is what the segment rule reads.
  function loopRun(): RunState {
    return {
      workflowId: "wf_1",
      status: "completed",
      root: {
        nodeId: "wf_1",
        type: "sequence",
        status: "completed",
        children: [
          step("plan", "completed"),
          {
            nodeId: "loop",
            type: "repeat",
            status: "completed",
            children: [
              {
                nodeId: "loop#0",
                type: "sequence",
                status: "completed",
                iteration: 0,
                children: [step("code", "completed"), step("review", "completed")],
              },
            ],
          },
        ],
      },
    };
  }

  const CODE_PATH = "wf_1/loop/iter-0/code";

  function rowPaths(root: HTMLElement): (string | undefined)[] {
    return [...root.querySelectorAll<HTMLElement>(".run-step")].map((e) => e.dataset["node"]);
  }

  it("keys every row by the path the exec view addresses that node by", () => {
    const state = loopRun();
    const c = card();
    c.render(state);
    // The other producer, over the same state. `leaves` is what the run tab
    // navigates and `bodyFor` files a transcript under, so a row that keyed on
    // anything else would open the page on no node at all.
    const execPaths = leaves(runToExec("wf_1", state, undefined, NO_ASKS).nodes).map((n) => n.path);
    expect(execPaths).toEqual(["wf_1/plan", CODE_PATH, "wf_1/loop/iter-0/review"]);
    expect(rowPaths(c.root)).toEqual(execPaths);
  });

  it("names a row after its last path segment, keeping the loop above it", () => {
    const c = card();
    c.render(loopRun());
    const row = c.root.querySelector<HTMLElement>(`.run-step[data-node="${CODE_PATH}"]`);
    expect(row?.querySelector(".run-step-name")?.textContent).toBe("code");
    expect(row?.dataset["status"]).toBe("ok");
    // A settled ok step's mark is the outcome SVG silhouette, not the old ✓ glyph.
    expect(row?.querySelector(".run-step-glyph svg")).not.toBeNull();
  });

  it("keeps two iterations of one loop body in two rows", () => {
    // They share a nodeId, so the node PATH is what separates them.
    const c = card();
    c.render({
      workflowId: "wf_1",
      status: "completed",
      root: {
        nodeId: "wf_1",
        type: "repeat",
        status: "completed",
        children: [
          {
            nodeId: "loop#0",
            type: "sequence",
            status: "completed",
            iteration: 0,
            children: [step("work", "completed")],
          },
          {
            nodeId: "loop#1",
            type: "sequence",
            status: "completed",
            iteration: 1,
            children: [step("work", "completed")],
          },
        ],
      },
    });
    expect(rowPaths(c.root)).toEqual(["wf_1/iter-0/work", "wf_1/iter-1/work"]);
  });
});

// ---------------------------------------------------------------------------
// A ROW IS A DOOR, NOT A DISCLOSURE. It used to open a per-row body hosting that
// step's own live blocks, which made the transcript BUILD every tool card and
// reasoning trace of every step of every run — `content-visibility` skips layout
// and paint on a closed row, not construction. The body is gone; the row opens
// `/run/<id>` at that node instead.
// ---------------------------------------------------------------------------
describe("a step row is a door into the run tab", () => {
  const opened: [string, string, string | undefined][] = [];

  function doorCard(): ReturnType<typeof buildRunCard> {
    opened.length = 0;
    const c = buildRunCard("wf_1", "Workflow run", (id, label, focusNode) => {
      opened.push([id, label, focusNode]);
    });
    c.render(runOf("running", step("build", "running")));
    return c;
  }

  function head(c: ReturnType<typeof buildRunCard>): HTMLAnchorElement {
    return c.root.querySelector<HTMLAnchorElement>(".run-step-head")!;
  }

  it("makes the row head a real anchor at that STEP's route", () => {
    // Real, so middle-click and copy-link work — and the route carries the node as
    // a FRAGMENT, so a copied row link lands on the step rather than on the run's
    // auto-followed default. A fragment rather than a segment because a node path
    // contains `/` and the tab's identity must stay `(run, workflowId)`.
    const h = head(doorCard());
    expect(h.tagName).toBe("A");
    expect(h.getAttribute("href")).toBe("/run/wf_1#node=wf_1%2Fbuild");
  });

  it("keeps the FOOT link on the run itself, with no fragment", () => {
    // "Open run" means the run, not a step, so it must not inherit a node from
    // whichever row happens to be first.
    const c = doorCard();
    expect(c.root.querySelector<HTMLAnchorElement>(".run-open")?.getAttribute("href")).toBe(
      "/run/wf_1",
    );
  });

  it("gives two iterations of one loop body DIFFERENT hrefs", () => {
    // What proves the href is per ROW rather than per card: these two rows share a
    // nodeId, so only the node path separates them.
    //
    // Its own card rather than `doorCard`'s: a row is inserted once and never
    // removed, so re-rendering a different plan into that card would leave its
    // `build` row in the DOM beside these two.
    const c = card();
    c.render({
      workflowId: "wf_1",
      status: "completed",
      root: {
        nodeId: "wf_1",
        type: "repeat",
        status: "completed",
        children: [
          {
            nodeId: "loop#0",
            type: "sequence",
            status: "completed",
            iteration: 0,
            children: [step("work", "completed")],
          },
          {
            nodeId: "loop#1",
            type: "sequence",
            status: "completed",
            iteration: 1,
            children: [step("work", "completed")],
          },
        ],
      },
    });
    expect(
      [...c.root.querySelectorAll<HTMLAnchorElement>(".run-step-head")].map((a) =>
        a.getAttribute("href"),
      ),
    ).toEqual(["/run/wf_1#node=wf_1%2Fiter-0%2Fwork", "/run/wf_1#node=wf_1%2Fiter-1%2Fwork"]);
  });

  it("hosts no step body and carries no disclosure chevron", () => {
    // Not `.hidden` and not `content-visibility`: the body was DELETED, and a
    // region kept in the DOM is a region that still costs its construction.
    const c = doorCard();
    expect(c.root.querySelector(".run-step-body")).toBeNull();
    expect(c.root.querySelector(".run-step-toggle")).toBeNull();
    expect(c.root.querySelector(".run-step.collapsed")).toBeNull();
  });

  it("leaves no interactive element nested inside another", () => {
    // axe's `nested-interactive` (serious): an `<a>` inside a `role="button"` host
    // fires it, and `aria-hidden` + `tabindex="-1"` does not clear it. So the row's
    // anchor carries no role of its own and its children are spans.
    const h = head(doorCard());
    expect(h.getAttribute("role")).toBeNull();
    expect(h.hasAttribute("tabindex")).toBe(false);
    expect([...h.children].map((e) => e.tagName)).toEqual(["SPAN", "SPAN", "SPAN", "SPAN"]);
    expect(h.querySelector("a, button, [tabindex]")).toBeNull();
  });

  it("opens the run at that node on a plain click", () => {
    const c = doorCard();
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
    head(c).dispatchEvent(ev);
    expect(opened).toEqual([["wf_1", "Workflow run", "wf_1/build"]]);
    // The app's own routing took the click, so the browser must not also navigate.
    expect(ev.defaultPrevented).toBe(true);
  });

  it("stands aside for a modified click", () => {
    const c = doorCard();
    const ev = new MouseEvent("click", {
      bubbles: true,
      cancelable: true,
      button: 0,
      ctrlKey: true,
    });
    head(c).dispatchEvent(ev);
    expect(opened).toEqual([]);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("still names the state in the row's accessible label", () => {
    // The ROLE says the row opens something, so the name says what the row IS.
    expect(head(doorCard()).getAttribute("aria-label")).toBe("build, running");
  });
});

// ---------------------------------------------------------------------------
// THE CARD RENDERS EXPANDED WHILE IT IS THE NEWEST TOP-LEVEL ELEMENT and folds when
// the next element is posted after it — the positional rule, at card scope, pushed in
// through `setSuperseded` because only the dispatcher knows where the card sits.
//
// Every fixture here supplies a `disclosure`, because that is the only way a caller
// can state either half (the reader's own recorded state, and this pass's verdict); a
// card built without one keeps the `?? true` floor, which the cases above rely on.
// ---------------------------------------------------------------------------
describe("the newest run card is expanded, and being superseded folds it", () => {
  const clean = (): RunState => runOf("completed", step("build", "completed"));

  /** A card wired to a disclosure. `reader` is what the registry holds (undefined =
   *  the reader has not decided), `defaultOpen` the newest-element verdict, and
   *  `wrote` records every value the card asked the registry to store. */
  function wired(opts: { reader?: boolean; defaultOpen?: boolean } = {}): {
    c: ReturnType<typeof buildRunCard>;
    wrote: boolean[];
  } {
    const wrote: boolean[] = [];
    const c = buildRunCard(
      "wf_1",
      "Workflow run",
      () => {
        /* the footer link is not under test */
      },
      {
        wasOpen: () => opts.reader,
        defaultOpen: opts.defaultOpen ?? true,
        onOpenChange: (open) => wrote.push(open),
      },
    );
    return { c, wrote };
  }

  const collapsed = (c: ReturnType<typeof buildRunCard>): boolean =>
    c.root.classList.contains("collapsed");
  const aria = (c: ReturnType<typeof buildRunCard>): string | null =>
    c.root.querySelector(".run-head")?.getAttribute("aria-expanded") ?? null;

  it("is born from the policy default, and the reader's own state outranks it", () => {
    // The verdict decides when the reader has not, and having decided turns the auto
    // path off for the card's whole life.
    expect(collapsed(wired({ defaultOpen: false }).c)).toBe(true);
    expect(collapsed(wired({ defaultOpen: true }).c)).toBe(false);

    const { c } = wired({ reader: true, defaultOpen: false });
    expect(collapsed(c)).toBe(false);
    c.render(clean());
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
  });

  it("folds a settled clean run the next block follows, and says so in aria", () => {
    const { c } = wired();
    c.render(clean());
    expect(collapsed(c)).toBe(false);
    expect(aria(c)).toBe("true");

    c.setSuperseded(true);
    expect(collapsed(c)).toBe(true);
    expect(aria(c)).toBe("false");
  });

  it("does not fold a run that is still live", () => {
    const { c } = wired();
    c.render(runOf("running", step("build", "running")));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
    // The state settling is what releases the refusal for a verdict already given.
    c.render(clean());
    expect(collapsed(c)).toBe(true);
  });

  it("does not fold a card whose state has not been fetched", () => {
    // Not knowing is not the same as finished: the card is built before its first
    // `inspect` lands, and a fold there would hide a run that may be working.
    const { c } = wired();
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
  });

  it("leaves a run card open while it holds an unanswered ask", () => {
    // The one refusal no status can express: the head reads "needs input" whatever the
    // run reports, so a fold would hide the steps behind a card that says it is
    // waiting on a person. Over a SETTLED CLEAN run deliberately — that is the only
    // shape the other refusals let through, so it is what makes this one falsifiable.
    const { c } = wired();
    c.render(clean(), asks(1, ["build"], "which branch?"));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);

    c.render(clean(), NO_ASKS);
    expect(collapsed(c)).toBe(true);
  });

  it("folds a run the reader stopped, because nobody is waiting on one", () => {
    // The carve-outs are the reference's three plus the ask, and no wider. `stateOf`
    // maps both of these to `warn`, so a "settled clean means only `completed`" reading
    // would exempt a cancelled run from ever folding — an element sitting expanded
    // above every later one for the rest of the session over a run the reader
    // themselves stopped.
    // Over a run whose STEPS all completed, deliberately: an `aborted` STEP counts as
    // failed in `runCounters`, so a stopped step is the failure carve-out and it is the
    // run's own stopped status this admits.
    for (const status of ["cancelled", "aborted"] as const) {
      const { c } = wired();
      c.render(runOf(status, step("build", "completed")));
      expect(collapsed(c)).toBe(false);
      c.setSuperseded(true);
      expect(collapsed(c)).toBe(true);
    }
  });

  it("does not fold a clean run that holds a failed STEP", () => {
    // The run's own status says `completed`, so only the per-step count sees the
    // failure — and a failure is not noise, at either granularity.
    const { c } = wired();
    c.render(runOf("completed", step("build", "completed"), step("test", "failed")));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);
  });

  it("does not fold a failed run, and re-opens one that fails after folding", () => {
    const { c } = wired();
    c.render(runOf("failed", step("build", "failed")));
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(false);

    const later = wired();
    later.c.render(clean());
    later.c.setSuperseded(true);
    expect(collapsed(later.c)).toBe(true);
    later.c.render(runOf("failed", step("build", "failed")));
    expect(collapsed(later.c)).toBe(false);
  });

  it("routes the fold through the scroll compensator", () => {
    // `scroll.ts` calls `preserveReadingPosition` THE ONE ENTRY POINT for a transcript
    // height change, and an auto fold removes height ABOVE the reader — this body is one
    // row per leaf plus a capture preview, taller than the tool-group case the helper
    // was made mandatory for. `autoCollapseGroup` wraps its own fold for exactly this.
    //
    // Withholding the wrapped mutation is what makes the wrapping falsifiable: a
    // `ctl.close()` sitting OUTSIDE the wrapper folds the card regardless, so the last
    // two assertions go red the moment the compensation is dropped.
    const { c } = wired();
    c.render(clean());
    scrollMock.preserveReadingPosition.mockImplementation(() => undefined);

    c.setSuperseded(true);

    expect(scrollMock.preserveReadingPosition).toHaveBeenCalledTimes(1);
    expect(scrollMock.preserveReadingPosition.mock.calls[0]?.[1]).toBe("content-growth");
    expect(collapsed(c)).toBe(false);
    expect(aria(c)).toBe("true");
  });

  it("routes the failure re-open through it as well", () => {
    // The other direction, which `maybeCollapseGroup` also wraps: this ADDS the step
    // rows' height back above the reader.
    const { c } = wired();
    c.render(clean());
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(true);
    // Cleared, because the fold above went through the compensator too.
    scrollMock.preserveReadingPosition.mockClear();
    scrollMock.preserveReadingPosition.mockImplementation(() => undefined);

    c.render(runOf("failed", step("build", "failed")));

    expect(scrollMock.preserveReadingPosition).toHaveBeenCalledTimes(1);
    expect(scrollMock.preserveReadingPosition.mock.calls[0]?.[1]).toBe("content-growth");
    expect(collapsed(c)).toBe(true);
  });

  it("re-opens a folded card when the LAUNCH turns out to have failed", () => {
    // A failed launch created no run, so `inspect` never reports it and the tool call
    // is the only witness — which can arrive after the card has already folded.
    const { c } = wired();
    c.render(clean());
    c.setSuperseded(true);
    expect(collapsed(c)).toBe(true);
    c.setLaunch("failed", "no such recipe");
    expect(collapsed(c)).toBe(false);
  });

  it("writes the reader's choice and never its own", () => {
    // The registry is the reader's latch, so an auto fold and the failure re-open must
    // leave no entry — otherwise every fold would come back as "the reader closed it".
    const { c, wrote } = wired();
    c.render(clean());
    c.setSuperseded(true);
    c.render(runOf("failed", step("build", "failed")));
    expect(wrote).toEqual([]);

    c.root.querySelector<HTMLElement>(".run-head")?.click();
    expect(wrote).toEqual([false]);
  });
});
