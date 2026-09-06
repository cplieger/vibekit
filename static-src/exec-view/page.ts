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
//
// TWO REGIONS ARE SIGNATURE-GUARDED, the inputs and the results, and for the same
// reason `detail.ts`'s `renderOutput` is: `render` runs on every store
// invalidation — dozens of times a minute on a live execution — so rebuilding
// either would discard the reader's expansion and their scroll position.

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { chevronEl } from "../chevron.js";
import { attachClamp } from "../clamp-text.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN } from "../icons.js";
import { buildAssistantBubble } from "../fundamentals/text-bubble.js";
import { formatElapsed } from "../strings.js";
import { counters, leaves, window as execWindow, type ExecNode, type ExecRun } from "./model.js";
import { STATE_WORD } from "./status.js";
import { buildExecTree, nodeAt, attentionRank, type ExecTreeView } from "./tree.js";
import { buildExecTimeline, type ExecTimelineView } from "./timeline.js";
import {
  buildExecDetail,
  type ExecDetailView,
  type EmptyAction,
  type EmptyNote,
} from "./detail.js";

export interface ExecPageOpts {
  /** What a node with a transcript host but no content yet should say; the
   *  consumer's call (a workflow run has three reasons, a subagent tab its own). */
  emptyNote: EmptyNote;
  /** An affordance to render beside that note. Optional: a consumer with nowhere
   *  to send anyone passes none and the slot stays empty. */
  emptyAction?: EmptyAction;
  /** The execution's controls, rebuilt per state. Returns null for a state with no
   *  verbs, so the row disappears rather than rendering empty. */
  controls?: (run: ExecRun) => HTMLElement | null;
  /** The header's identity glyph. Defaults to the workflow one; a subagent page
   *  passes the agent hexagon. */
  icon?: string;
  /** Fired when the node the DETAIL PANE is showing changes, OR when that node's
   *  state moves, so a consumer can act on the reader's attention — arming an
   *  on-demand fetch for the step now on screen is the case it exists for.
   *
   *  It is the only way a consumer can learn which node is shown: selection lives
   *  here, and `emptyNote`/`emptyAction` must NOT be used to discover it. Both are
   *  documented pure and cheap and are called on EVERY render, dozens of times a
   *  minute on a live execution, so side-effecting one would fire a fetch per
   *  repaint.
   *
   *  The STATE is part of that guard because a consumer's answer can depend on it:
   *  a step's session cannot be read while it is in flight, and `select()` PINS the
   *  selection, so a path-only guard never fires again for a step the reader clicked
   *  while it was running — leaving the one node they are looking at as the one node
   *  never acted on. A state is a small closed vocabulary and a node walks it a
   *  handful of times, so this stays per-attention rather than per-repaint.
   *
   *  Additive and optional, exactly like `emptyAction`, so a consumer that needs
   *  none passes none. */
  onShowNode?: (node: ExecNode | undefined) => void;
}

/** The instructions clamp, in the shape `fundamentals/turn-header.ts` states it:
 *  the line count the STYLESHEET clamps to, plus the character threshold used only
 *  while the element is detached and cannot be measured. Both numbers in one place,
 *  so the CSS and the fallback cannot drift apart. */
const CLAMP = { lines: 3, fallbackChars: 220 } as const;

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
  /** The path the detail pane is currently showing, so `onShowNode` fires on a
   *  change of ATTENTION rather than on every repaint. "" means none. */
  let shownPath = "";
  /** That node's state at the last notification, the second half of the same guard:
   *  a node settling under the reader's cursor is a change of attention too, and it
   *  is the only one a pinned selection can produce. "" means none. */
  let shownState = "";
  /** The last `ExecRun.focus` honoured. Recorded rather than consumed once,
   *  because one page instance serves every tab of its kind: a subagent tab
   *  switching delegates arrives as a render with a different `focus`, which
   *  must re-focus without fighting a later click. */
  let focused = "";

  const tree: ExecTreeView = buildExecTree(select);
  const timeline: ExecTimelineView = buildExecTimeline(select);
  const detail: ExecDetailView = buildExecDetail(opts.emptyNote, opts.emptyAction);

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
  // OPEN by default: a run's product is what a reader opens the page for, and with
  // the page scrolling (18-pages.css) an open roll-up costs page height rather than
  // pane height — which is the whole reason it used to be shut.
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
  const results = el("div", { className: "ev-results", hidden: true }, resultsHead, resultsBody);
  createDisclosure(resultsHead, resultsBody, {
    open: true,
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
    const node = nodeAt(run.nodes, selected);
    detail.render(node);
    // AFTER the pane has been told, so a consumer reacting synchronously finds the
    // host it is about to write into already created. Guarded on the shown node's
    // PATH and STATE: `repaint` runs on every store invalidation and on every
    // selection, so notifying unconditionally would fire per repaint rather than per
    // attention — while a path-only guard misses a node that settles where it
    // stands, which `select()`'s pin makes the common shape rather than an edge.
    const path = node?.path ?? "";
    const state = node?.state ?? "";
    if (path !== shownPath || state !== shownState) {
      shownPath = path;
      shownState = state;
      opts.onShowNode?.(node);
    }
  }

  /** The header's instructions list — what this execution was ASKED to do — rebuilt
   *  only when the SET changed.
   *
   *  The signature guard is REQUIRED, not an optimisation: `render` runs on every
   *  store invalidation, so an unguarded `replaceChildren` would discard the
   *  reader's show-more expansion and rebuild every clamp dozens of times a minute
   *  on a live run. Same idiom and same reason as `renderResults` below and
   *  `detail.ts`'s `renderOutput`. */
  function renderInputs(run: ExecRun): void {
    const entries = Object.entries(run.inputs ?? {});
    if (entries.length === 0) {
      inputs.hidden = true;
      delete inputs.dataset["sig"];
      inputs.replaceChildren();
      return;
    }
    inputs.hidden = false;
    const sig = entries.map(([k, v]) => `${k}\u0001${v}`).join("\u0002");
    if (inputs.dataset["sig"] === sig) {
      return;
    }
    inputs.dataset["sig"] = sig;
    inputs.replaceChildren(
      ...entries.flatMap(([k, v]) => {
        const text = el("div", { className: "ev-in-text" }, v);
        const more = el("button", {
          type: "button",
          className: "ev-in-more",
        }) as HTMLButtonElement;
        // Overflow is MEASURED (`clamp-text.ts` runs one shared `ResizeObserver`
        // over every clamped element and compares `scrollHeight` against
        // `clientHeight`), so a short instruction is never offered an opener that
        // opens nothing. The character threshold is only the pre-layout guess for
        // the frame in which the element is still detached, corrected before paint.
        attachClamp(text, more, CLAMP);
        return [
          el("dt", { className: "ev-in-k" }, k),
          el("dd", { className: "ev-in-v" }, text, more),
        ];
      }),
    );
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
    resultsBody.replaceChildren(...entries.map(([key, value]) => resultItem(key, value)));
  }

  /** One capture as its own collapsible box, at full natural height.
   *
   *  OPEN by default, matching the region around it and the run card's rule for the
   *  thing a reader just asked to see. The head is the trigger and the chevron is a
   *  SPAN: `createDisclosure` writes `role="button"` and `tabindex="0"` on a
   *  non-`<button>` trigger itself, and a real control nested inside that role is
   *  axe's `nested-interactive`, which `aria-hidden` does not clear. */
  function resultItem(key: string, value: string): HTMLElement {
    const head = el(
      "div",
      { className: "ev-r-item-head" },
      el("span", { className: "ev-r-item-twist", "aria-hidden": "true" }, chevronEl()),
      el("span", { className: "ev-r-item-key" }, key === "" ? "Output" : key),
    );
    // An empty value is a fact, not an absence: a source writes a key only for a
    // node that captured, so empty distinguishes "finished silently" from "never
    // ran" — and it still earns a box, because that is the fact.
    const body = el(
      "div",
      { className: "ev-r-item-body" },
      value.trim() === ""
        ? el(
            "div",
            { className: "ev-r-item-empty" },
            "This step finished without producing any text.",
          )
        : buildAssistantBubble(value, false).root,
    );
    createDisclosure(head, body, { open: true });
    return el("div", { className: "ev-r-item" }, head, body);
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

      renderInputs(run);

      alert.hidden = run.alert === undefined;
      if (run.alert !== undefined) {
        alert.dataset["kind"] = run.alert.kind;
        alert.textContent = run.alert.text;
      }

      const row = opts.controls?.(run) ?? null;
      controlsHost.replaceChildren(...(row === null ? [] : [row]));

      renderResults(run);

      // A render carrying NO focus clears the watermark, so the next assertion of
      // the same path is honoured. Without it a door could name a node only once
      // per page instance: the reader moves in the tree, clicks the same door
      // again, and it is a control that does nothing. The clear is safe because a
      // focus-free render is also one that asserts nothing — `userPicked` still
      // holds whatever the reader last chose.
      if (run.focus === undefined) {
        focused = "";
      }
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
