// ---------------------------------------------------------------------------
// The exec view PAGE: a full tab for one delegated execution.
//
// Assembles the header, the alert, the timeline, the tree and the detail pane, owns
// the selection between them, and runs one clock for all of them. It names no
// source: a consumer hands it an `ExecRun` and it draws it, which is what makes a
// subagent tab a second adapter rather than a second page.
//
// LAYOUT, and each choice is answering something a transcript column could not:
//
//   header    identity, state, progress, elapsed, controls, and the INPUTS — what
//             the execution was asked to do, which nothing in the app has shown
//   alert     only when it wants a person
//   timeline  where the time went and what overlapped
//   tree      the structure, containers included
//   detail    the selected node at full width
//
// The tree and the detail sit side by side on a wide viewport and stack on a narrow
// one, which is a container query rather than a viewport one because the sidebar
// decides how much width this tab gets — the same reason the timeline rail keys on
// `chat-area`.
//
// ONE CLOCK. A live execution takes minutes and a paused one emits no frames at all,
// so a duration that only moves when a server frame lands reads as frozen. One
// interval drives all three panes and stops when nothing is moving, the same shape
// the transcript uses for its run cards.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN } from "../icons.js";
import { buildAssistantBubble } from "../fundamentals/text-bubble.js";
import { formatElapsed } from "../strings.js";
import { counters, leaves, window as execWindow, type ExecRun } from "./model.js";
import { STATE_WORD } from "./status.js";
import { buildExecTree, nodeAt, attentionRank, type ExecTreeView } from "./tree.js";
import { buildExecTimeline, type ExecTimelineView } from "./timeline.js";
import { buildExecDetail, type ExecDetailView, type EmptyNote } from "./detail.js";

export interface ExecPageOpts {
  /** What a node with a transcript host but no content yet should say. The reasons
   *  are the consumer's: a workflow run has three, a subagent tab will have its own. */
  emptyNote: EmptyNote;
  /** The execution's controls, rebuilt per state. Returns null for a state with no
   *  verbs, so the row disappears rather than rendering empty. */
  controls?: (run: ExecRun) => HTMLElement | null;
  /** The header's identity glyph. Defaults to the workflow one, because that was the
   *  only consumer when this page was written; a subagent page passes the agent
   *  hexagon. Per-page rather than per-render, since a page instance serves one
   *  source. */
  icon?: string;
}

export interface ExecPageView {
  readonly root: HTMLElement;
  /** Draw an execution. Idempotent and safe on every invalidation. */
  render(run: ExecRun | undefined): void;
  /** The host a node's live transcript renders into. */
  bodyFor(path: string): HTMLElement;
  /** Release the clock. Called when the page stops being shown. */
  dispose(): void;
}

export function buildExecPage(opts: ExecPageOpts): ExecPageView {
  const name = el("span", { className: "ev-name" });
  const stateEl = el("span", { className: "ev-h-state" });
  const progress = el("span", { className: "ev-h-progress" });
  const clock = el("span", { className: "ev-h-clock" });
  const controlsHost = el("div", { className: "ev-h-actions" });
  const inputs = el("dl", { className: "ev-inputs" });
  const alert = el("div", {
    className: "ev-alert",
    role: "status",
    "aria-live": "polite",
    hidden: true,
  });
  const head = el(
    "header",
    { className: "ev-head" },
    el(
      "div",
      { className: "ev-h-main" },
      el(
        "span",
        { className: "ev-h-icon", "aria-hidden": "true" },
        iconEl(opts.icon ?? ICON_TAB_RUN),
      ),
      name,
      el("span", { className: "ev-h-meta" }, stateEl, progress, clock),
      controlsHost,
    ),
    inputs,
  );

  let selected = "";
  /** Whether the reader has chosen a node. Until they have, the page follows the
   *  work: a run being watched should show the step that is running without a click,
   *  and one that failed should open on the failure. After a click it stops moving,
   *  because a pane that re-targets itself under a reader is the defect the
   *  auto-follow exists to avoid at the other extreme. */
  let userPicked = false;
  /** The last `ExecRun.focus` this page honoured.
   *
   *  Recorded rather than consumed once, because ONE page instance serves every tab
   *  of its kind: a subagent tab switching to a different delegate arrives as a
   *  render whose `focus` differs, and that has to re-focus. Comparing against the
   *  last honoured value is what separates it from the dozens of invalidations
   *  carrying the same one, which must not fight a click. */
  let focused = "";

  const tree: ExecTreeView = buildExecTree(select);
  const timeline: ExecTimelineView = buildExecTimeline(select);
  const detail: ExecDetailView = buildExecDetail(opts.emptyNote);

  const treePane = el("div", { className: "ev-pane ev-pane-tree" }, tree.root);
  const panes = el(
    "div",
    { className: "ev-panes" },
    treePane,
    el("div", { className: "ev-pane ev-pane-detail" }, detail.root),
  );

  // --- results ---------------------------------------------------------------
  // The execution's PRODUCT, as a roll-up. The detail pane already shows a node's
  // own output, and this is deliberately a second view of the same text rather than
  // a replacement: they answer different questions — "what did this step do" against
  // "what did the run produce" — and a review of a finished execution is exactly the
  // case where a reader wants the second without clicking through every step to
  // assemble it. The transcript's run card carries the same pair (`.run-outputs`
  // beside each step's own capture), so this is the app's existing shape rather than
  // a new one.
  //
  // COLLAPSED by default, and it costs one row shut: a report can be thousands of
  // characters and the panes above are what a live run is watched through.
  const resultsCount = el("span", { className: "ev-r-count" });
  const resultsBody = el("div", { className: "ev-r-body" });
  const resultsHead = el(
    "div",
    { className: "ev-r-head", role: "button", tabindex: "0" },
    el("span", { className: "ev-r-title" }, "Results"),
    resultsCount,
  );
  const results = el(
    "div",
    { className: "ev-results collapsed", hidden: true },
    resultsHead,
    resultsBody,
  );
  createDisclosure(resultsHead, resultsBody, {
    open: false,
    onToggle: (open) => {
      results.classList.toggle("collapsed", !open);
    },
  });

  const root = el("div", { className: "ev-page" }, head, alert, timeline.root, panes, results);

  let current: ExecRun | undefined;
  let timer: ReturnType<typeof setInterval> | undefined;

  function select(path: string): void {
    userPicked = true;
    selected = path;
    repaint();
  }

  /** The node the page opens on: the one that wants attention, else the first leaf.
   *  Re-derived on every render while the reader has not chosen, which is what makes
   *  a run followable without clicking. */
  function autoSelect(run: ExecRun): string {
    const ls = leaves(run.nodes);
    if (ls.length === 0) {
      return "";
    }
    let best = ls[0];
    if (best === undefined) {
      return "";
    }
    for (const n of ls) {
      if (attentionRank(n.state) < attentionRank(best.state)) {
        best = n;
      }
    }
    return best.path;
  }

  function repaint(): void {
    const run = current;
    if (run === undefined) {
      return;
    }
    // IT DEGRADES BY CONTENT, which is what makes one page serve a twenty-node
    // workflow and a single delegate.
    //
    // A tree pane holding one row is not navigation, it is a row you cannot avoid
    // clicking, and a timeline of one bar says nothing the header's elapsed does not.
    // So the structural regions appear when there is structure: more than one root,
    // or any node with children. A single subagent therefore renders as header +
    // detail + results with the full width on the thing being read, and the same
    // page renders a pipeline's driver-and-stages as a tree — no consumer has to
    // choose a layout, and no consumer gets a pane of one.
    const structural = run.nodes.length > 1 || run.nodes.some((n) => n.children.length > 0);
    panes.classList.toggle("ev-panes-flat", !structural);
    treePane.hidden = !structural;
    if (structural) {
      tree.render(run.nodes, selected);
    }
    timeline.render(run.nodes, selected, run.live);
    detail.render(nodeAt(run.nodes, selected));
  }

  /** The results roll-up, rebuilt only when the SET changed.
   *
   *  Guarded on a signature because `render` runs on every invalidation, dozens of
   *  times over a live run, and re-parsing a settled report each time would throw
   *  away the reader's place in it and reset their scroll. */
  function renderResults(run: ExecRun): void {
    const entries = Object.entries(run.outputs ?? {});
    if (entries.length === 0) {
      results.hidden = true;
      delete resultsBody.dataset["sig"];
      resultsBody.replaceChildren();
      return;
    }
    results.hidden = false;
    resultsCount.textContent = String(entries.length);
    const sig = entries.map(([k, v]) => `${k}\u0001${v}`).join("\u0002");
    if (resultsBody.dataset["sig"] === sig) {
      return;
    }
    resultsBody.dataset["sig"] = sig;
    resultsBody.replaceChildren(
      ...entries.flatMap(([key, value]) => {
        const rows = [el("div", { className: "ev-r-key" }, key)];
        // An EMPTY value is a fact rather than an absence: a source writes a key
        // only for a node that captured, so empty says the node finished without
        // saying anything — indistinguishable from "never ran" if the row is
        // dropped.
        rows.push(
          value.trim() === ""
            ? el(
                "div",
                { className: "ev-r-empty" },
                "This step finished without producing any text.",
              )
            : el("div", { className: "ev-r-val" }, buildAssistantBubble(value, false).root),
        );
        return rows;
      }),
    );
  }

  function setClock(live: boolean): void {
    if (live && timer === undefined) {
      timer = setInterval(() => {
        const run = current;
        if (run === undefined) {
          return;
        }
        tree.tick();
        detail.tick();
        timeline.tick(run.nodes, selected, run.live);
        const win = execWindow(run.nodes, run.live);
        clock.textContent = win === undefined ? "" : formatElapsed(win.span);
      }, 1000);
      return;
    }
    if (!live && timer !== undefined) {
      clearInterval(timer);
      timer = undefined;
    }
  }

  return {
    root,
    render(run) {
      current = run;
      if (run === undefined) {
        return;
      }
      root.dataset["state"] = run.state;
      name.textContent = run.label;
      // The ASK outranks the status word, because the execution genuinely still
      // reads running while a node's ask blocks it and the status would report
      // nothing wrong.
      stateEl.textContent = run.alert?.kind === "input" ? "needs input" : STATE_WORD[run.state];

      const c = counters(run.nodes);
      // Withheld for a ONE-NODE execution. "step 1 of 1" is a progress readout that
      // cannot progress, and the state word beside it already says how the one node
      // ended — measured on a single delegate, where it was the only noise in the
      // header. A workflow run of one step gets the same silence for the same reason.
      progress.textContent =
        c.total <= 1
          ? ""
          : c.current > 0
            ? `step ${String(c.current)} of ${String(c.total)}`
            : `${String(c.done)} of ${String(c.total)}`;
      const win = execWindow(run.nodes, run.live);
      clock.textContent = win === undefined ? "" : formatElapsed(win.span);

      const entries = Object.entries(run.inputs ?? {});
      inputs.hidden = entries.length === 0;
      inputs.replaceChildren(
        ...entries.flatMap(([k, v]) => [
          el("dt", { className: "ev-in-k" }, k),
          el("dd", { className: "ev-in-v" }, v),
        ]),
      );

      alert.hidden = run.alert === undefined;
      if (run.alert !== undefined) {
        alert.dataset["kind"] = run.alert.kind;
        alert.textContent = run.alert.text;
      }

      const row = opts.controls?.(run) ?? null;
      controlsHost.replaceChildren(...(row === null ? [] : [row]));

      renderResults(run);

      // A door that NAMES a node is a choice, so it is honoured like a click and it
      // also stops the auto-follow. Guarded on the value having CHANGED, because
      // `render` runs on every invalidation and re-asserting the same focus would
      // fight a reader who has since clicked elsewhere. A focus naming a node this
      // execution does not have falls through to the auto-follow rather than
      // selecting nothing.
      if (
        run.focus !== undefined &&
        run.focus !== focused &&
        nodeAt(run.nodes, run.focus) !== undefined
      ) {
        focused = run.focus;
        selected = run.focus;
        userPicked = true;
      }
      if (!userPicked || nodeAt(run.nodes, selected) === undefined) {
        selected = autoSelect(run);
      }
      repaint();
      setClock(run.live);
    },
    bodyFor(path) {
      return detail.bodyFor(path);
    },
    dispose() {
      setClock(false);
    },
  };
}
