// ---------------------------------------------------------------------------
// The exec view's TIMELINE: where the time went, and what waited on what.
//
// Every node carries `startedAt` and `endedAt`, so the execution's shape over time
// is already on the wire and no surface has ever drawn it. That is the second thing
// a single column structurally cannot express (nesting is the first): a column shows
// order, and order is not concurrency. Two steps rendered as consecutive rows look
// identical whether they ran one after the other or both at once.
//
// It answers the question a reader has first when a run took 48 minutes — where did
// the time go — and it answers it without reading: one bar per leaf, placed and
// sized in the run's own window, so a long step is a long bar, a stall is a gap, and
// steps that overlap overlap on screen.
//
// LEAVES ONLY, deliberately. A container's span is its children's, so drawing both
// double-counts the time and makes a sequence's bar the width of the whole run,
// which says nothing.
//
// It is a READOUT, not a chart library: no axes, no ticks, no zoom. The scale is one
// number in the corner and the rest is position. A row is selectable, so it is the
// same navigation surface as the tree from a different angle — which is why it takes
// the same `onSelect` and the same selected path.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { formatElapsed } from "../strings.js";
import { elapsed, leaves, window as execWindow, type ExecNode } from "./model.js";
import { STATE_WORD } from "./status.js";

export interface ExecTimelineView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. */
  render(nodes: readonly ExecNode[], selected: string, live: boolean): void;
  /** Re-place the bars of anything still running, so a live run's bar grows and the
   *  window stretches with it. */
  tick(nodes: readonly ExecNode[], selected: string, live: boolean): void;
}

export function buildExecTimeline(onSelect: (path: string) => void): ExecTimelineView {
  const scale = el("span", { className: "ev-tl-scale" });
  const lanes = el("div", { className: "ev-tl-lanes" });
  const root = el(
    "div",
    { className: "ev-tl" },
    el(
      "div",
      { className: "ev-tl-head" },
      el("span", { className: "ev-tl-title" }, "Timeline"),
      scale,
    ),
    lanes,
  );

  /** Rebuilt wholesale on each pass rather than reconciled, and that is a
   *  deliberate difference from the tree beside it. A bar carries no state a reader
   *  can change — no collapse, no focus target of its own — and every bar's geometry
   *  moves whenever the window does, which on a live run is every tick. So there is
   *  nothing for a reconcile to preserve, and keying an index of bars by path would
   *  be bookkeeping for a value that is recomputed regardless. */
  function paint(nodes: readonly ExecNode[], selected: string, live: boolean): void {
    const ls = leaves(nodes).filter((n) => n.start !== undefined);
    const win = execWindow(nodes, live);
    // TWO leaves minimum. A single bar spanning its own window is always full width,
    // so it reports nothing the header's elapsed does not already say — and a region
    // that is only ever one full bar teaches a reader to ignore it. The value here is
    // comparison: which step took the time, and what overlapped.
    if (win === undefined || ls.length < 2) {
      root.hidden = true;
      lanes.replaceChildren();
      return;
    }
    root.hidden = false;
    scale.textContent = formatElapsed(win.span);

    lanes.replaceChildren(
      ...ls.map((n) => {
        const from = Date.parse(n.start ?? "");
        const to = n.end === undefined ? win.to : Date.parse(n.end);
        const left = ((from - win.from) / win.span) * 100;
        // A floor of 0.75%, because a sub-second step in an hour-long run rounds to
        // zero width and a bar you cannot see reports the step as absent rather
        // than as fast.
        const width = Math.max(0.75, ((Math.max(to, from) - from) / win.span) * 100);
        const bar = el("span", { className: "ev-tl-bar" });
        bar.style.insetInlineStart = `${left.toFixed(3)}%`;
        // Clamped so a bar that would end past the window (a clock skew between the
        // server's stamps and this browser) stays inside its track instead of
        // widening the row and scrolling the pane sideways.
        bar.style.inlineSize = `${Math.min(100 - left, width).toFixed(3)}%`;

        const ms = elapsed(n.start, n.end);
        const lane = el(
          "div",
          {
            className: n.path === selected ? "ev-tl-lane ev-selected" : "ev-tl-lane",
            "data-state": n.state,
            "data-path": n.path,
            role: "button",
            tabindex: "0",
            "aria-label": `${n.label}, ${STATE_WORD[n.state]}${ms > 0 ? `, ${formatElapsed(ms)}` : ""}`,
          },
          el("span", { className: "ev-tl-name" }, n.label),
          el("span", { className: "ev-tl-track" }, bar),
          el("span", { className: "ev-tl-dur" }, ms > 0 ? formatElapsed(ms) : ""),
        );
        lane.addEventListener("click", () => {
          onSelect(n.path);
        });
        lane.addEventListener("keydown", (e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onSelect(n.path);
          }
        });
        return lane;
      }),
    );
  }

  return {
    root,
    render: paint,
    tick: paint,
  };
}
