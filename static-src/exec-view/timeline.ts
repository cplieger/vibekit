// The exec view's timeline: where the time went, and what waited on what.
//
// Every node carries `startedAt`/`endedAt`, so an execution's shape over time was
// already on the wire and nothing had drawn it — a column shows order, not
// concurrency, so two steps rendered as consecutive rows look identical whether
// they ran sequentially or at once.
//
// LEAVES ONLY: a container's span is its children's, so drawing both
// double-counts the time.
//
// A READOUT, not a chart library: no axes, no ticks, no zoom — one number for
// scale, the rest is position. Selectable with the same `onSelect` and selected
// path as the tree, so it is the same navigation surface from a different angle.

import { el } from "@cplieger/reactive";
import { formatElapsed } from "../strings.js";
import { elapsed, leaves, window as execWindow, type ExecNode } from "./model.js";
import { STATE_WORD } from "./status.js";

export interface ExecTimelineView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. */
  render(nodes: readonly ExecNode[], selected: string, live: boolean): void;
  /** Re-place the bars of anything still running: a live run's bar grows and the
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

  /** Rebuilt wholesale on each pass rather than reconciled: a bar carries no state
   *  a reader can change, and every bar's geometry moves whenever the window does
   *  (every tick on a live run), so keying an index of bars by path would be
   *  bookkeeping for a value recomputed regardless. */
  function paint(nodes: readonly ExecNode[], selected: string, live: boolean): void {
    const ls = leaves(nodes).filter((n) => n.start !== undefined);
    const win = execWindow(nodes, live);
    // TWO leaves minimum: a single bar spanning its own window is always full
    // width and reports nothing the header's elapsed does not already say.
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
        // Floor of 0.75%: a sub-second step in an hour-long run rounds to zero
        // width otherwise, reporting the step as absent rather than fast.
        const width = Math.max(0.75, ((Math.max(to, from) - from) / win.span) * 100);
        const bar = el("span", { className: "ev-tl-bar" });
        bar.style.insetInlineStart = `${left.toFixed(3)}%`;
        // Clamped so clock skew between the server's stamps and this browser
        // cannot widen the row past the track.
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
