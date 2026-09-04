// The exec view page: a full tab for one delegated execution.
//
// Assembles the header, the alert, the timeline, the tree and the detail pane,
// owns the selection between them, and runs one clock for all of them. Names no
// source: a consumer hands it an `ExecRun` and it draws it.
//
// LAYOUT: header (identity/state/progress/elapsed/controls/inputs), alert (only
// when it wants a person), timeline (where the time went), tree (structure,
// containers included), detail (the selected node at full width). Tree and
// detail sit side by side on a wide viewport and stack on a narrow one, via a
// container query on the tab rather than the viewport (the sidebar decides how
// much width the tab gets).
//
// ONE CLOCK: a live execution takes minutes and a paused one emits no frames at
// all, so one interval drives all three panes and stops when nothing moves.

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { chevronEl } from "../chevron.js";
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
  /** What a node with a transcript host but no content yet should say; the
   *  consumer's call (a workflow run has three reasons, a subagent tab its own). */
  emptyNote: EmptyNote;
  /** The execution's controls. Returns null for a state with no verbs, so the row
   *  disappears rather than rendering empty, and may return the SAME element it
   *  returned last time to say nothing changed — the page then leaves it in place
   *  rather than re-inserting it. */
  controls?: (run: ExecRun) => HTMLElement | null;
  /** The header's identity glyph. Defaults to the workflow one; a subagent page
   *  passes the agent hexagon. */
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
   *  work (a running or failed node opens without a click); after a click it
   *  stops moving. */
  let userPicked = false;
  /** The last `ExecRun.focus` honoured. Recorded rather than consumed once,
   *  because one page instance serves every tab of its kind: a subagent tab
   *  switching delegates arrives as a render with a different `focus`, which
   *  must re-focus without fighting a later click. */
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

  // Results: the execution's PRODUCT, as a roll-up, deliberately distinct from
  // the detail pane's per-node output ("what did this step do" vs "what did the
  // run produce"). Mirrors the transcript run card's `.run-outputs` pattern.
  //
  // Collapsed by default: a report can be thousands of characters.
  const resultsCount = el("span", { className: "ev-r-count" });
  const resultsBody = el("div", { className: "ev-r-body" });
  // The disclosure glyph LEADS, as the app's other section headers do. A span
  // and not a button: `.ev-r-head` is `role="button"`, so a nested control is
  // axe's `nested-interactive`, which `aria-hidden` + `tabindex="-1"` does not
  // clear. `chevronEl()` already returns an `aria-hidden` span.
  const resultsHead = el(
    "div",
    { className: "ev-r-head", role: "button", tabindex: "0" },
    el("span", { className: "ev-r-twist", "aria-hidden": "true" }, chevronEl()),
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

  /** The node the page opens on: the one that wants attention, else the first
   *  leaf. Re-derived on every render while the reader has not chosen. */
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
    // Degrades by content: a tree of one row is not navigation, and a timeline
    // of one bar says nothing the header's elapsed does not. Structural regions
    // appear only when there is structure (more than one root, or any node with
    // children).
    const structural = run.nodes.length > 1 || run.nodes.some((n) => n.children.length > 0);
    panes.classList.toggle("ev-panes-flat", !structural);
    treePane.hidden = !structural;
    if (structural) {
      tree.render(run.nodes, selected);
    }
    timeline.render(run.nodes, selected, run.live);
    detail.render(nodeAt(run.nodes, selected));
  }

  /** The results roll-up, rebuilt only when the SET changed (guarded by
   *  signature, since `render` runs on every invalidation over a live run and
   *  re-parsing would reset the reader's scroll). */
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
        // An empty value is a fact, not an absence: a source writes a key only
        // for a node that captured, so empty distinguishes "finished silently"
        // from "never ran".
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
      // An unanswered ask outranks the status word: the run genuinely still
      // reads `running` while it blocks on a node's ask.
      stateEl.textContent = run.alert?.kind === "input" ? "needs input" : STATE_WORD[run.state];

      const c = counters(run.nodes);
      // Withheld for a one-node execution: "step 1 of 1" cannot progress, and the
      // state word beside it already says how the node ended.
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

      // Re-inserted only when it is a DIFFERENT row. `replaceChildren` with a node
      // that is already the host's only child still removes and re-adds it, which
      // blurs whatever is focused inside — and this runs once per progress frame.
      const row = opts.controls?.(run) ?? null;
      if (row === null) {
        controlsHost.replaceChildren();
      } else if (controlsHost.firstChild !== row || controlsHost.childNodes.length !== 1) {
        controlsHost.replaceChildren(row);
      }

      renderResults(run);

      // A door that NAMES a node is a choice: honoured like a click, and it stops
      // the auto-follow. Guarded on the value having changed, or re-asserting the
      // same focus on every invalidation would fight a reader who clicked
      // elsewhere. A focus naming an absent node falls through to auto-follow.
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
